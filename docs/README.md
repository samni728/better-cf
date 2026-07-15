# Cloudflare BetterIP DNS Sync 文档中心

本目录用于记录 `cf-betterip` 后续产品化改造的整体设计。项目目标不是简单给现有命令行工具套一层页面，而是把“当前网络下的 Cloudflare 优选 IP 测速”升级为一个可视化、可追溯、可定时、可安全同步 DNS 的小型运维工具。

## 产品一句话

自动扫描当前 VPS 网络环境下最快、最稳定的 Cloudflare IPv4 / IPv6 TLS IP，并安全替换指定 Cloudflare 域名的 A / AAAA 记录，同时在 WebUI 中展示测速数据、DNS 同步结果和历史记录。

## 文档导航

- [产品设计](product-design.md)：用户、场景、MVP 边界、Now / Next / Later。
- [系统架构](architecture.md)：模块拆分、数据流、部署方式。
- [测速与选择策略](scan-strategy.md)：强制新扫描、复测、补位、排序逻辑。
- [DNS 同步设计](dns-sync.md)：Cloudflare 批量替换、安全删除边界、同步确认。
- [管理员登录与初始化](auth-and-setup.md)：首次创建管理员、登录保护、配置写入流程。
- [数据模型](data-model.md)：SQLite 表结构草案、历史追踪、IP 长期画像。
- [WebUI 工作流](webui-workflows.md)：页面、配置项、用户操作路径。
- [实施路线图](implementation-plan.md)：从 CLI 重构到 WebUI、Docker、定时任务的阶段计划。
- [同步策略](sync-policy.md)：以 VPS 项目为主，本地目录作为同步副本的工作方式。

## 当前源码状态

远端源码位于：

```bash
/root/cf-betterip/source
```

当前本地目录作为远端源码的同步副本。默认协作规则是：先以 VPS 上的 `/root/cf-betterip/source` 为主，再从 VPS 同步到本地当前目录。

当前上游仓库：

```bash
https://github.com/badafans/better-cloudflare-ip.git
```

当前源码基本是单文件 Go CLI：

```text
source/main.go
```

现有能力：

- 从 IPv4 / IPv6 地址池随机生成测试 IP。
- 对候选 IP 做 RTT 测试。
- 使用 HTTP / HTTPS 对候选 IP 做下载测速。
- 读取 CF-RAY 并解析 Cloudflare 数据中心。
- 输出优选 IP、设置带宽、实测带宽、峰值速度、往返延迟、数据中心、总耗时。

主要缺口：

- CLI 交互和核心测速逻辑强耦合。
- 当前只返回第一个达标 IP，不支持 Top N 结果。
- 没有 WebUI。
- 没有数据库和历史记录。
- 没有 Cloudflare DNS 更新逻辑。
- 没有定时任务、运行状态和同步确认。

## MVP 方向

第一版优先完成一条闭环：

```text
首次访问 WebUI 创建管理员账号
-> 登录后台
-> 配置参数
-> 强制扫描 IPv4 / IPv6 TLS 优选 IP
-> 保存完整测速数据
-> 只替换指定域名的 A / AAAA 记录
-> 写入后从 Cloudflare 反查确认
-> WebUI 展示本次任务、IP 质量和 DNS 同步状态
```

这个闭环跑通后，再扩展复测、补位、历史趋势、回滚和多目标域名。
