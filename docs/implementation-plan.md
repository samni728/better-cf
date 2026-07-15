# 实施路线图

## 阶段 0：保护现状

目标：不破坏当前可运行 CLI。

任务：

- 保留当前 `main.go` 能继续编译运行。
- 把当前远端运行目录和源码目录分清楚。
- 确认 `ips-v4.txt`、`ips-v6.txt`、`locations.json`、`url.txt` 数据加载路径。

验收：

```bash
go run main.go
```

仍然可以打开原 CLI 菜单并完成一次 IPv4 TLS 测试。

## 阶段 1：拆出 scanner 核心包

目标：把 CLI 交互和测速逻辑分离。

任务：

- 新建 `internal/scanner`。
- 定义 `ScanRequest`、`IPTestResult`。
- 拆出：
  - 数据下载和加载
  - IPv4 / IPv6 随机生成
  - RTT 测试
  - 速度测试
  - 数据中心解析
  - Top N 选择
- 把 CLI 改成调用 scanner 包。

验收：

- CLI 行为不变。
- 新 scanner 可以被测试调用。
- 能返回多个结构化结果，而不是只返回一个字符串。

## 阶段 2：加入 SQLite 存储

目标：保存运行历史和 IP 测试结果。

任务：

- 新建 `internal/storage`。
- 初始化 SQLite。
- 创建基础表：
  - `scan_runs`
  - `ip_test_results`
  - `ip_profiles`
  - `dns_targets`
  - `dns_change_logs`
- 扫描完成后写入数据库。
- 更新 IP 长期画像。

验收：

- 运行一次扫描后，可以在 SQLite 中查到 run 和 IP 测试结果。
- 设置带宽、实测带宽、峰值速度、RTT、数据中心都被保存。

## 阶段 3：Cloudflare DNS 同步

目标：安全替换指定域名 A / AAAA，并反查确认。

任务：

- 新建 `internal/dns`。
- 实现 Cloudflare Client：
  - List records
  - Delete record
  - Create record
  - Verify records
- 实现 `ReplaceDNSRecords`。
- 删除逻辑限制在：
  - 指定 Zone
  - 指定完整 record name
  - A / AAAA 类型
- 每一步写入 `dns_change_logs`。

验收：

- 可以把测试 IP 写入指定域名。
- 不会触碰其他域名或其他记录类型。
- 同步后反查 Cloudflare，结果一致才标记 confirmed。

## 阶段 4：Web 后端与基础 WebUI

目标：通过浏览器完成配置、扫描、同步、查看结果。

任务：

- 新建 `cmd/cf-betterip-web`。
- 新建 HTTP server。
- 实现页面：
  - Dashboard
  - Run Now
  - Cloudflare Settings
  - History
- 实现任务控制台：
  - 立即执行按钮
  - 最近运行记录
  - 可展开运行日志
  - 定时任务状态
- 实现 API：
  - 保存配置
  - 手动强制扫描
  - 查询运行状态
  - 查询历史详情
- 实现日志接口：
  - 查看最近任务日志
  - 运行中自动刷新日志
- 扫描任务后台执行，页面显示状态。

验收：

- 浏览器打开 WebUI。
- 可以配置 Cloudflare。
- 可以手动运行 IPv4 / IPv6 TLS。
- 可以看到每个 IP 的带宽、峰值速度、RTT、数据中心。
- 可以看到 DNS 同步是否 confirmed。

## 阶段 5：定时任务

目标：每天自动扫描并同步。

任务：

- 新建 `internal/scheduler`。
- 支持每日固定时间运行。
- 支持启用/停用。
- 定时任务使用与 Run Now 相同的扫描参数。
- 失败写日志。

验收：

- WebUI 配置每天几点运行。
- 到点自动创建 run。
- 完成后自动同步 DNS。

## 阶段 6：Docker 部署

目标：一键部署。

任务：

- 编写 `Dockerfile`。
- 编写 `docker-compose.yml`。
- `/data` 挂载 SQLite 和数据文件。
- README 增加部署说明。

验收：

```bash
docker compose up -d
```

可以访问 WebUI，数据重启后不丢失。

## 推荐开发顺序

不要先做漂亮 UI。建议顺序：

```text
scanner 结构化结果
-> SQLite 保存
-> Cloudflare 安全同步
-> 简单 WebUI
-> 定时任务
-> Docker
```

这样每一步都有可验证成果，也能避免在核心逻辑没稳之前陷入界面细节。

## 风险点

### DNS 删除风险

必须通过代码限制只删除指定域名 A / AAAA。

### Token 泄露风险

API Token 只在本地保存，WebUI 不明文回显。后续可以做加密存储。

### 扫描耗时风险

Top N 扫描可能比当前 CLI 更久，需要后台任务和进度状态。

### 数据源依赖

当前数据源来自远程 URL，后续要支持手动更新、失败兜底和本地缓存。

### 结果不稳定

Cloudflare Anycast 结果受网络环境影响，需要保存历史，让用户看到趋势和使用时长。
