# Cloudflare DNS 多目标同步设计

## 目标

把一次测速入选的 IPv4 / IPv6 结果，安全分发到任意数量的 Cloudflare DNS 目标。

一次扫描只生成一份结果：

```text
selected IPv4 -> 所有 ipv4 / both 目标的 A 记录
selected IPv6 -> 所有 ipv6 / both 目标的 AAAA 记录
```

目标数量增加只会增加 DNS API 操作，不会重复扫描或重复消耗测速带宽。

## Cloudflare 凭据库

系统管理可复用的 Cloudflare 凭据，每个 DNS 目标通过 `credential_id` 引用其中一组。

支持两种认证方式：

```text
API Token:
  Authorization: Bearer <token>

Global API Key:
  X-Auth-Email: <email>
  X-Auth-Key: <global-api-key>
```

推荐优先使用 API Token，并限定到需要管理的 Zone 和 `DNS Edit` 权限。Email + Global API Key 仅用于兼容现有 Cloudflare 账号配置，其权限更大，更需要安全保管。

凭据不包含 Zone ID；Zone ID 属于 DNS 目标。因此同一凭据可以被不同 Zone 的多个目标复用。

## DNS 目标

目标可以新增、编辑、停用和删除，其配置为：

```text
id
name             显示名称
root_domain      Cloudflare Zone 根域名
zone_id          Cloudflare Zone ID
record_name      完整域名
record_family    ipv4 | ipv6 | both
credential_id    凭据库引用
enabled
```

示例：

```text
目标“主域名”
root_domain = example.com
record_name = speed.example.com
record_family = both

目标“备用 IPv4”
root_domain = example.net
record_name = v4.example.net
record_family = ipv4
```

一轮入选 2 个 IPv4 和 2 个 IPv6 时，上述两个目标应有 6 条 DNS 记录：主域名 4 条，备用 IPv4 2 条。

## 独立手动 DNS 目标

手动 DNS 目标与上述自动目标分开保存和执行，不进入立即扫描、定时任务、运行快照、扫描数量或自动 DNS fan-out 计划。它只在管理员主动点击按钮时访问 Cloudflare。

手动更新流程：

```text
粘贴一个或多个 vmess:// / vless:// 分享链接
-> vmess Base64 解码后只读取 JSON add 字段
-> vless 只读取 URL authority 中的服务器地址
-> 只接受 IPv4 / IPv6 字面量并规范化、去重
-> IPv4 完整替换目标域名的 A 集合
-> IPv6 完整替换目标域名的 AAAA 集合
```

单次合计最多处理 500 个分享链接，页面在提交前实时显示唯一 IPv4 / IPv6 识别数量。节点名称、Host、SNI、备注、端口、UUID 和其他字段都不会写入 DNS；vmess `add` 或 vless 服务器地址为域名而非 IP 时直接拒绝整次导入。

手动目标有两个清理动作：

- “清空全部 A/AAAA”：删除该精确完整域名下的全部 A 和 AAAA，保留本地目标配置，方便以后再次导入。
- “清空并删除目标”：必须先确认 Cloudflare 记录清空成功，再移除本地目标配置；远端失败时保留配置，避免留下无法追踪的记录。

导入采用完整替换语义：如果本次只导入 IPv4，那么该目标现有 AAAA 也会被清空。手动目标不得与任何已启用自动目标或其他手动目标重复管理同一 `Zone ID + 完整域名 + A/AAAA`。

## 核心原则

每个目标独立执行差异替换：

```text
查询精确匹配的旧记录
-> 计算 keep / create / delete
-> 先创建缺失记录
-> 创建全部成功后再删除过期记录
-> 反查确认最终集合
```

不做无限追加，也不再“先清空后创建”。先创建后删除可以避免新记录创建失败时把旧的可用解析一并清空。

## 安全边界

查询、删除和反查都必须同时满足：

```text
request.zone_id == target.zone_id
record.name == target.record_name
record.type == 当前 fan-out 记录类型
```

记录类型映射：

```text
record_family = ipv4 -> 只管理 A
record_family = ipv6 -> 只管理 AAAA
record_family = both -> 分别管理 A 和 AAAA
```

不得删除：

- 同一 Zone 下的其他完整域名。
- 当前目标未管理的 A 或 AAAA 类型。
- CNAME、TXT、MX、NS 或其他类型记录。
- 仅因为与 `root_domain` 同 Zone 就被模糊匹配到的记录。

`record_name` 必须等于 `root_domain` 或以 `.<root_domain>` 结尾。即使凭据权限很大，程序也必须在代码层保持以上边界。

## 同步请求结构

```go
type DNSSyncTarget struct {
    TargetID   string
    Name       string
    RootDomain string
    ZoneID     string
    RecordName string
    RecordType string
    Credential CloudflareCredential
    IPs        []string
}
```

`record_family = both` 在执行层展开成 A 和 AAAA 两个 `DNSSyncTarget`，但在 UI 和运行汇总中仍属于同一个用户 DNS 目标。

默认建议：

```text
TTL: 1 (Auto)
Proxied: false
```

优选 IP 需要直接暴露给客户端使用，因此不应启用 Cloudflare 橙云代理。

## fan-out 流程

### 1. 构建执行计划

在 DNS 操作前固定本轮快照：

- 本轮入选 IPv4 / IPv6 集合。
- 开始同步时的已启用目标列表。
- 每个目标引用的凭据 ID 和记录族；任务快照不复制 Token / Global API Key。
- 计划目标数与应写 DNS 记录总数。

执行中的 UI 配置修改不得改变已开始运行的计划。

### 2. 逐目标预检查

预检查包括：

- 目标已启用。
- Zone ID、根域名、完整域名和 `credential_id` 完整。
- 完整域名位于根域名内。
- 凭据引用存在，且符合对应认证类型的字段要求。
- IPv4 / IPv6 都是对应协议族的合法、去重 IP。
- 记录数与本轮扫描要求一致。
- Cloudflare Zone 可以被当前凭据访问。

预检查与收敛以“一个 DNS 目标”为安全边界：同一目标的 A / AAAA 全部通过预检查后才开始写入。某个目标的凭据、Zone 或域名校验失败时，该目标不会被修改，但会继续处理其他独立目标。这避免一个过期 Token 阻断其他账号的有效更新。

DNS 列表查询必须按 `result_info.total_pages` 读取全部分页，再做完整域名和记录类型的二次过滤，避免记录超过单页时重复创建或漏删。

### 3. 查询与差集

对当前目标和记录类型精确查询旧集合，然后计算：

```text
keep     = old ∩ desired
toCreate = desired - old
toDelete = old - desired
```

所有集合比较都以规范化后的 IP 内容为准。

### 4. 先创建缺失记录

对 `toCreate` 中的 IP 创建记录：

```text
type = A or AAAA
name = target.record_name
content = ip
ttl = configured ttl
proxied = false
```

每次创建都记录 Cloudflare 返回的 record ID。只有 `toCreate` 全部成功，才可进入删除阶段。

任何创建失败时：

- 不删除 `toDelete`。
- 保留旧可用解析。
- 目标标记失败或部分同步，并显示已创建的额外记录。
- 不用删除其他未知记录的方式“修复”未知状态。

### 5. 后删除过期记录

新记录全部创建成功后，只删除本次精确查询得到且属于 `toDelete` 的 record ID。删除前再校验目标 Zone ID、完整域名和类型。

### 6. 反查确认

对每个目标的每种管理类型重新查询：

```text
Cloudflare 实际记录集合 == 本轮应写 IP 集合
```

目标的全部类型都一致才标记该目标 `confirmed`。所有计划目标都确认后，整轮才标记 `dns_sync_status = confirmed`。

## 计数语义

多目标场景下，不能再把唯一 IP 数当作 DNS 写入数。

```text
required_dns_record_count
  = 每个已启用目标应有的 A / AAAA 记录数之和

confirmed_dns_record_count
  = 每个目标经反查确认的 A / AAAA 记录数之和
```

同一 IP 写入 3 个 DNS 目标，记为 3 条已确认 DNS 记录。Dashboard 同时展示：

```text
已确认 DNS 记录：confirmed_dns_record_count / required_dns_record_count
已确认 DNS 目标：confirmed_target_count / planned_target_count
```

这两个数字与“入选优选 IP 数”分开展示。

## 失败与部分成功

同步状态：

```text
已确认：所有计划目标的实际集合都与应写集合一致
部分同步：至少一个目标已确认，但仍有目标失败或不一致
失败：未完成计划中的 DNS 结果
未执行：本轮只测速，没有同步 DNS
```

失败时页面必须按目标展示阶段、凭据名称、Zone、完整域名、记录类型和可操作错误，不只显示一条全局失败文案。

## 配置测试

每个 DNS 目标都提供真实写入测试：

```text
1. 解析 target.credential_id 引用的凭据。
2. 查询 target.zone_id，确认凭据可访问。
3. 在该 Zone 中创建唯一临时 TXT 记录。
4. 只使用 Cloudflare 返回的 record ID 删除刚创建的 TXT。
5. 记录创建和删除结果。
```

临时名称使用专用前缀，例如：

```text
_cf-betterip-test.<record_name>
```

测试不触碰正式 A / AAAA，不按名称批量删除 TXT，也不测试该凭据库中未被当前目标引用的其他凭据。

## 旧配置兼容

旧 single / split 配置仅是新模型的迁移输入：

- single 自动变成一个 `both` 目标。
- split 自动变成 IPv4 和 IPv6 两个目标。
- 原统一 / 独立 Token 自动变成凭据库项并由目标引用。

迁移完成后，同步引擎只消费新的凭据和目标列表，不再分支执行 single / split 两套流程。

## 为什么不直接 update 单条记录

一个完整域名需要对应多条 A / AAAA，用于 DNS 轮询多个优选 IP。因此更适合对集合计算差异、先创建后删除并反查确认，而不是维护一条记录做 update。
