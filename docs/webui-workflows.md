# WebUI 工作流

WebUI 目标是让用户一眼确认：

```text
这批 IP 是怎么来的？
这些 IP 质量如何？
它们用了多久？
Cloudflare DNS 是否已经完整同步？
```

## 页面结构

### First Setup

首次访问系统且尚未创建管理员时，进入初始化页面：

```text
管理员用户名
管理员密码
确认密码
```

提交成功后写入本地配置，跳转登录页。

规则：

- 只允许在没有管理员时创建。
- 一旦管理员存在，`/setup` 不再允许重新初始化。
- 密码必须做哈希存储，不能明文保存。

### Login

管理员登录页面。

登录成功后创建会话 Cookie，进入 Dashboard。

未登录时不能访问：

```text
Dashboard
Run Now
Cloudflare Settings
Schedule
History
```

### Dashboard

首页展示当前目标域名状态：

```text
目标域名：speed.123go.eu.org
最近任务：强制新扫描
任务状态：已完成
DNS 同步：已确认
最近同步时间：2026-07-15 10:52
IPv4 A：10 / 10 已同步
IPv6 AAAA：10 / 10 已同步
```

首页同时提供：

```text
立即执行按钮
当前定时策略
下一次计划运行时间
最近运行记录
可展开运行日志
```

首页不以“配置了什么”为主，而以“执行结果怎么样”为主：

```text
今日更新 IP
今日写入 DNS
今日任务数
DNS 同步状态
当前执行阶段
执行进度条
```

IP 列表展示：

```text
IP              类型  协议  实测带宽  峰值速度    RTT   数据中心       使用时长  连续通过  Cloudflare
104.24.242.5    A     TLS   104Mbps   13343KB/s  1ms   Los Angeles   1天      1天      已同步
```

### Run Now

手动运行页面。

字段：

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

设置带宽：
- 默认 100 Mbps

RTT 并发数：
- 默认 50
- 最大 100

是否同步 Cloudflare：
- 开 / 关
```

按钮：

```text
开始运行
```

运行中显示：

```text
正在生成候选 IP
RTT 测试中
速度测试中
正在替换 Cloudflare DNS
正在反查确认
```

每次运行都要有独立日志，可以在页面中展开查看。日志需要区分：

```text
手动立即执行
定时自动执行
系统后台日志
```

### Cloudflare Settings

配置 Cloudflare：

```text
DNS 目标模式：单域名 / IPv4 与 IPv6 分离
统一 API Token
统一 Account ID
统一 Zone ID
单域名目标完整域名
IPv4 目标完整域名
IPv6 目标完整域名
IPv4 是否继承统一 Cloudflare 凭据
IPv6 是否继承统一 Cloudflare 凭据
IPv4 独立 API Token / Account ID / Zone ID
IPv6 独立 API Token / Account ID / Zone ID
TTL
是否代理 proxied
```

默认：

```text
TTL = Auto
proxied = false
```

操作：

```text
测试连接
保存配置
读取当前 DNS 记录
```

“测试连接”必须执行真实 DNS 写入测试：

```text
创建临时 TXT -> 删除临时 TXT
```

这样用户可以在正式运行前确认 Cloudflare 配置确实可写。

### Schedule

定时任务页面。

MVP 可以先只支持一个定时任务，但定时方式要灵活：

```text
启用定时任务
定时类型：
- 每小时
- 每天固定时间
- 每 N 天固定时间

每天运行时间
间隔天数
运行模式：强制新扫描
IPv4 数量
IPv6 数量
设置带宽
RTT 并发数
同步 Cloudflare
```

第二阶段支持多个任务。

### History

历史运行记录：

```text
时间
模式
参数
成功数量
DNS 同步状态
耗时
详情
```

运行详情页：

```text
本次任务参数
所有测试 IP
入选 DNS 的 IP
失败 IP 和失败原因
DNS 删除日志
DNS 创建日志
DNS 反查确认结果
```

日志展示规则：

- 最近日志在 Dashboard 直接展示摘要。
- 每条运行记录可展开查看完整日志。
- 正在运行的任务页面自动刷新日志。
- 日志保留任务阶段、时间、触发方式和错误信息。

### IP Detail

单个 IP 历史页：

```text
IP：104.24.242.5
首次发现：2026-07-15
首次写入 DNS：2026-07-15
最近测试：2026-07-15
最近同步：2026-07-15
连续通过：1 天
测试次数：3
通过次数：2
失败次数：1
历史最佳带宽：124 Mbps
最近带宽：104 Mbps
历史最佳 RTT：1 ms
最近数据中心：Los Angeles
```

## 状态文案

运行模式：

```text
强制新扫描：本次从 IP 池重新生成候选 IP 并测速。
重测当前 IP：本次只测试当前 Cloudflare DNS 中已有的 IP。
补位扫描：保留仍然达标的旧 IP，并扫描新 IP 补足数量。
```

DNS 同步状态：

```text
已确认：Cloudflare 上的记录与本次目标 IP 完全一致。
部分同步：部分记录未能成功写入或删除。
失败：Cloudflare 上的最终记录与目标 IP 不一致。
未执行：本次没有执行 DNS 同步。
```

## 交互原则

- 删除 DNS 前必须显示目标域名。
- 页面上明确提示只会操作该域名的 A / AAAA 记录。
- 运行过程中给出阶段状态。
- 运行结束后必须展示 Cloudflare 反查确认结果。
- 对失败原因使用清晰文案，不只显示原始错误。

## MVP 页面优先级

第一批页面：

1. Dashboard
2. Run Now
3. Cloudflare Settings
4. History

第二批页面：

1. Schedule
2. IP Detail
3. DNS Snapshot / Rollback
