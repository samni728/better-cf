# WebUI 工作流

WebUI 要让用户一眼确认：

```text
这批 IP 是怎么来的？
这些 IP 质量如何？
本轮要分发到哪些 DNS 目标？
每个目标的 Cloudflare DNS 是否完整同步？
```

## 页面结构

### First Setup

首次访问系统且尚未创建管理员时，进入初始化页面：

```text
管理员用户名
管理员密码
确认密码
```

规则：

- 只允许在没有管理员时创建。
- 一旦管理员存在，`/setup` 不再允许重新初始化。
- 密码必须做哈希存储，不能明文保存。

### Login

管理员登录成功后创建会话 Cookie，再进入 Dashboard。未登录时不能访问 Dashboard、Run Now、Cloudflare Settings、Schedule 和 History。

### Dashboard

首页优先展示执行结果，不把配置清单当作主视图。

汇总示例：

```text
最近任务：强制新扫描
任务状态：已完成
DNS 同步：已确认
入选优选 IP：IPv4 10 / 10，IPv6 10 / 10
已确认 DNS 记录：50 / 50
已确认 DNS 目标：3 / 3
最近同步时间：2026-07-15 10:52
```

DNS 记录数是 fan-out 后的累计数，不是唯一 IP 数。例如同一 IP 已在 3 个目标反查确认，记为 3 条 DNS 记录。

首页同时提供：

```text
立即执行
当前定时策略与下次运行时间
今日入选 IP 数
今日已确认 DNS 记录数
今日任务数
当前阶段与进度条
最近运行记录与可展开日志
```

目标概览按 DNS 目标展示：

```text
目标          域名                 记录族  凭据        本轮状态  已确认
主站优选      speed.example.com      A+AAAA    生产 Token   已确认      20 / 20
备用 IPv4     v4.example.net          A         备用账号     已确认      10 / 10
测试 IPv6     v6.lab.example.org      AAAA      Lab Key      失败         0 / 10
```

IP 列表继续展示 IP、记录类型、协议、实测带宽、峰值速度、RTT、数据中心、使用时长和连续通过天数。多目标状态不用单个“Cloudflare 已同步”布尔值代替，应显示 `已确认目标数 / 计划目标数`。

### Run Now

手动运行页面字段：

```text
运行模式：
- 强制新扫描
- 重测当前 DNS IP

IP 类型：
- IPv4
- IPv6
- IPv4 + IPv6

协议：
- TLS
- 非 TLS

目标结果数量：
- IPv4 数量
- IPv6 数量

设置带宽：默认 100 Mbps
RTT 并发数：默认 50，最大 100
是否同步 Cloudflare：开 / 关
```

打开 DNS 同步时，页面在开始前展示本轮执行计划：

```text
已启用 DNS 目标：3
预计 DNS 记录：50
结果分发：一次扫描 -> 3 个目标
```

点击“开始运行”后对当前已启用目标、凭据引用和应写数量取运行快照。任务执行中编辑配置不改变这次计划。

运行阶段：

```text
正在生成候选 IP
RTT 测试中
速度测试中
正在构建 DNS fan-out 计划
正在为目标创建缺失记录
正在删除目标过期记录
正在按目标反查确认
```

日志必须包含目标名称或 ID，并区分手动立即执行、定时自动执行和系统后台日志。

### Cloudflare Settings

页面分为“Cloudflare 凭据库”和“DNS 目标”两个独立区域。

#### Cloudflare 凭据库

列表项展示：

```text
凭据名称
认证类型：API Token / Email + Global API Key
密钥状态：已配置（脱敏）
引用该凭据的 DNS 目标数
编辑 / 删除
```

新增或编辑表单：

```text
名称
认证方式：
- API Token
- Email + Global API Key

API Token 模式：API Token
Global API Key 模式：Cloudflare Email + Global API Key
Account ID（可选）
```

编辑时密钥留空表示保留已保存值，不得在 HTML 中回填完整密钥。被 DNS 目标引用的凭据不能直接删除，页面应列出阻止删除的目标。

#### DNS 目标

列表项展示：

```text
名称
根域名
Zone ID（脱敏或截断显示）
完整域名
记录族：IPv4 / IPv6 / IPv4 + IPv6
引用凭据
启用状态
测试 / 编辑 / 停用 / 删除
```

新增或编辑表单：

```text
目标名称
根域名，例如 example.com
Zone ID
完整域名，例如 speed.example.com
记录族：ipv4 / ipv6 / both
Cloudflare 凭据：从凭据库选择
是否启用
```

保存前必须校验完整域名属于根域名，引用凭据存在，且已启用目标不会与另一目标重叠管理同一 `Zone ID + 完整域名 + A/AAAA`。

“测试”必须执行真实 DNS 写入：

```text
解析目标凭据
-> 确认 Zone 可访问
-> 创建 _cf-betterip-test.<完整域名> 临时 TXT
-> 按创建返回的 record ID 删除该 TXT
```

测试不触碰正式 A / AAAA，失败时明确指出是凭据、Zone 访问、TXT 创建还是 TXT 删除失败。

#### 手动 DNS 目标

页面下方提供独立的“手动 DNS 目标”区域。这里的目标永远不参加自动扫描、定时任务和扫描完成后的 DNS 写入。

添加时填写：

```text
目标名称
根域名
Zone ID
目标完整域名（必须是具体子域名）
Cloudflare 凭据
```

保存后可混合粘贴合计最多 500 个 `vmess://` / `vless://` 分享链接并点击“手动更新”。系统只解析 vmess JSON 的 `add` 或 vless URL 的服务器 IP，按 IPv4 / IPv6 去重后完整替换该域名的 A / AAAA；SNI、Host 和备注里的地址必须忽略。输入框旁实时显示识别到的唯一 IPv4 / IPv6 和无效链接数量。页面必须明确提示：没有出现在本次导入中的地址会被删除；某一地址族为空时会清空该族的旧记录。

每张手动目标卡片提供：

- 手动更新：解析并写入本次 IP 集合。
- 清空全部 A/AAAA：只清理远端记录，保留目标配置。
- 清空并删除目标：远端清理成功后再删除本地配置。

所有查询和删除都必须同时精确匹配 Zone ID、完整域名及 A/AAAA 类型，不得影响同 Zone 的其他名称或其他记录类型。

#### 旧配置迁移提示

首次读取旧 single / split 配置时，系统自动生成凭据库项和 DNS 目标。页面可显示一次性成功提示：

```text
已将旧版单域名 / 分离域名配置迁移为 2 组凭据、3 个 DNS 目标。
请测试各目标后再执行正式同步。
```

迁移后 UI 不再显示 single / split 模式选择器。

### Schedule

MVP 先支持一个定时任务：

```text
启用定时任务
定时类型：每小时 / 每天固定时间 / 每 N 天固定时间
每天运行时间
间隔天数
运行模式
IPv4 数量
IPv6 数量
设置带宽
RTT 并发数
同步 Cloudflare
```

定时执行与手动执行一样，在运行开始时取所有已启用 DNS 目标的快照并 fan-out。不需要为每个 DNS 目标创建一个重复扫描计划。

### History

历史运行列表：

```text
时间
模式与扫描参数
IPv4 / IPv6 入选数
DNS 目标：已确认 / 计划
DNS 记录：已确认 / 应写
DNS 同步状态
耗时
详情
```

运行详情页：

```text
本次任务参数和 DNS 计划快照
所有测试 IP
入选 DNS 的 IP
失败 IP 和失败原因
逐 DNS 目标同步状态
创建日志
删除日志
反查确认结果
```

日志顺序要反映安全替换：`query -> create -> delete -> verify`。不能继续展示“先删除后创建”的阶段文案。

### IP Detail

单个 IP 历史页保留首次发现、最近测试、连续通过天数、通过 / 失败次数、最佳 / 最近带宽、RTT 和数据中心。

对 DNS 同步使用多目标语义：

```text
最近一次计划目标：3
已确认目标：2
未确认：测试 IPv6 (Zone 访问失败)
```

## 状态文案

运行模式：

```text
强制新扫描：本次从 IP 池重新生成候选 IP 并测速。
重测当前 IP：本次收集已管理 DNS 目标中的现有 IP，去重后测试。
补位扫描：保留仍然达标的旧 IP，并扫描新 IP 补足数量。
```

DNS 同步状态：

```text
已确认：所有计划 DNS 目标都与本轮应写集合一致。
部分同步：部分目标已确认，但仍有目标失败或不一致。
失败：本轮未完成计划中的 DNS 结果。
未执行：本次没有执行 DNS 同步。
```

## 交互原则

- 任何 DNS 操作前都显示目标名称、Zone 根域名、完整域名和记录族。
- 页面明确提示“先创建缺失记录，成功后再删除过期记录”。
- 创建失败时明确提示“未删除旧记录”。
- 运行结束后同时展示目标确认数和 DNS 记录确认数。
- 对失败原因使用可操作文案，不只显示原始 API 错误。

## MVP 页面优先级

第一批页面：

1. Dashboard
2. Run Now
3. Cloudflare Settings（凭据库 + DNS 目标）
4. History

第二批页面：

1. Schedule
2. IP Detail
3. DNS Snapshot / Rollback
