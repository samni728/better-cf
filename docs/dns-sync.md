# Cloudflare DNS 同步设计

## 目标

把本次测速选出的 IPv4 / IPv6 批量写入指定 Cloudflare 域名。目标域名支持两种模式：

```text
单域名模式：IPv4 A 和 IPv6 AAAA 都写入同一个域名。
分离域名模式：IPv4 A 写入一个域名，IPv6 AAAA 写入另一个域名。
```

示例：

```text
speed.123go.eu.org
```

最终 DNS 记录应类似：

```text
A     speed.123go.eu.org    104.24.242.5
A     speed.123go.eu.org    104.17.78.34
AAAA  speed.123go.eu.org    2606:4700:xxxx::1
AAAA  speed.123go.eu.org    2606:4700:xxxx::2
```

分离域名示例：

```text
A     ipv4-speed.123go.eu.org    104.24.242.5
AAAA  ipv6-speed.123go.eu.org    2606:4700:xxxx::1
```

## Cloudflare 凭据策略

支持统一凭据和独立凭据：

```text
统一凭据：IPv4 / IPv6 目标共用同一个 API Token、Account ID、Zone ID。
独立凭据：IPv4 和 IPv6 目标各自配置 API Token、Account ID、Zone ID。
```

MVP 推荐默认使用统一凭据，降低配置复杂度。只有当 IPv4 和 IPv6 目标处在不同 Cloudflare 账号、不同 Zone，或需要不同 Token 权限时，才启用独立凭据。

## 核心原则

每次同步采用替换模式：

```text
按目标查询指定域名的旧 A 或 AAAA
-> 删除该目标域名旧 A 或 AAAA
-> 创建本次最新 A 或 AAAA
-> 反查 Cloudflare
-> 确认实际 DNS 记录与目标列表一致
```

不做无限追加。

## 安全边界

删除操作必须同时满足以下条件：

```text
zone_id == 用户配置的 zone_id
record.name == 用户配置的完整域名
record.type in ["A", "AAAA"]
```

如果启用分离域名模式，则 IPv4 同步任务只能操作 IPv4 目标域名的 A 记录，IPv6 同步任务只能操作 IPv6 目标域名的 AAAA 记录。

绝对不能删除：

- 同一个 Zone 下的其他子域名。
- 根域名。
- CNAME。
- TXT。
- MX。
- NS。
- 其他类型记录。

即使 Cloudflare Token 权限很大，程序也必须在代码层限制操作范围。

## API Token 建议

Cloudflare API Token 建议使用最小权限：

```text
Zone: 指定 zone
Permission: DNS Edit
```

程序应支持 API Token，不优先使用 Global API Key。

## 同步请求结构

```go
type ReplaceDNSRecordsRequest struct {
    ZoneID     string
    RecordName string
    IPv4       []string
    IPv6       []string
    TTL        int
    Proxied    bool
    RunID      int64
}
```

默认建议：

```text
TTL: 1，表示 Auto
Proxied: false
```

因为这里需要把优选 IP 直接暴露给客户端使用，不应该套 Cloudflare 橙云代理。

## 同步流程

### 1. 预检查

删除前先校验：

- Zone ID 不为空。
- RecordName 是完整域名。
- IPv4 都是合法 IPv4。
- IPv6 都是合法 IPv6。
- A / AAAA 数量符合任务配置。
- Cloudflare Token 可用。

### 2. 查询旧记录

分别查询：

```text
name = record_name, type = A
name = record_name, type = AAAA
```

保存旧记录到日志，第二阶段可升级为 DNS 快照。

### 3. 删除旧记录

只删除上一步查到的目标域名 A / AAAA。

每次删除都记录：

```text
action = delete_old
record_type
ip
cloudflare_record_id
status
error_message
```

### 4. 创建新记录

对本次选择的 IP 创建记录：

```text
type = A or AAAA
name = record_name
content = ip
ttl = configured ttl
proxied = false
```

每次创建都记录 Cloudflare 返回的 record id。

### 5. 反查确认

创建完成后再次查询：

```text
name = record_name, type = A
name = record_name, type = AAAA
```

对比：

```text
Cloudflare 实际 A 集合 == 本次目标 IPv4 集合
Cloudflare 实际 AAAA 集合 == 本次目标 IPv6 集合
```

完全一致才标记为：

```text
dns_sync_status = confirmed
```

否则标记为：

```text
partial
failed
```

## WebUI 状态

同步状态建议展示：

```text
已确认：Cloudflare 上的记录与本次目标 IP 完全一致
部分同步：有部分记录创建或删除失败
失败：Cloudflare 上的记录与目标结果不一致
未执行：本次只测速，没有同步 DNS
```

## 失败处理

MVP 可以先采用简单策略：

- 删除前做完整预检查。
- 删除和创建每一步都记录日志。
- 创建失败时 WebUI 标红提示。
- 不自动删除其他记录来“修复”未知状态。

第二阶段再做：

- DNS 快照。
- 一键回滚。
- 两阶段替换策略。

## 配置测试

Cloudflare 配置页需要提供“测试配置”按钮。这个测试必须是真实写入测试，而不是只检查字段是否填写。

测试流程：

```text
1. 读取当前保存的 Cloudflare Token、Zone ID、目标域名。
2. 查询 Zone，确认 Token 和 Zone ID 可访问。
3. 创建临时 TXT 记录：
   _cf-betterip-test.<目标域名>
4. 创建成功后立刻删除该 TXT 记录。
5. 记录测试结果并展示给用户。
```

安全边界：

- 测试只创建和删除 `_cf-betterip-test.<目标域名>` 的 TXT 记录。
- 测试不触碰正式 A / AAAA 记录。
- 测试不删除其他域名、其他记录类型。
- 分离域名模式下，IPv4 和 IPv6 目标分别测试自己的有效凭据和目标域名。

测试成功说明：

```text
Cloudflare Token 有效
Zone ID 可访问
目标域名格式可用
DNS 写入权限可用
临时记录可删除
```

测试失败时，页面需要明确告诉用户是哪一步失败，方便修正 Token、Zone ID 或域名。

## 为什么不直接 update 单条记录

本项目需要一个域名对应多条 A / AAAA 记录，用于 DNS 轮询访问多个优选 IP。因此更适合：

```text
删除旧集合
创建新集合
反查确认
```

而不是维护一条记录做 update。
