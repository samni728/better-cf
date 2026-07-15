# 系统架构

## 总体形态

建议第一版继续使用 Go 技术栈，因为现有核心测速逻辑已经是 Go。目标是从单文件 CLI 改造成一个可编译、可 Docker 化、可长期运行的 Web 服务。

推荐形态：

```text
Go Web 服务
-> 内置 WebUI
-> SQLite 本地数据库
-> Cloudflare API Client
-> Cron Scheduler
-> 本地 data 目录保存 IP 池和测速 URL
```

## 推荐目录结构

```text
source/
  cmd/
    cf-betterip-web/
      main.go
    cf-betterip-cli/
      main.go
  internal/
    scanner/
      types.go
      data.go
      rtt.go
      speed.go
      selector.go
    dns/
      cloudflare.go
      sync.go
      types.go
    scheduler/
      scheduler.go
      jobs.go
    storage/
      db.go
      migrations.go
      runs.go
      results.go
      dns_logs.go
    web/
      server.go
      handlers.go
      templates.go
  web/
    templates/
    static/
  data/
    ips-v4.txt
    ips-v6.txt
    locations.json
    url.txt
  docs/
  Dockerfile
  docker-compose.yml
  README.md
```

## 模块边界

### UI / 前端交互层

负责：

- 展示 Dashboard。
- 配置 Cloudflare Token / Zone / 目标域名。
- 配置扫描参数和定时任务。
- 触发手动扫描、复测、DNS 同步。
- 展示运行日志、IP 结果、同步确认状态。

不负责：

- 不直接写 DNS。
- 不直接拼 Cloudflare API。
- 不承载测速业务逻辑。

### API / 后端入口层

负责：

- 路由。
- 请求校验。
- 调用服务层。
- 返回统一响应。

典型接口：

```text
GET  /api/settings
POST /api/settings/cloudflare
POST /api/runs/force-refresh
POST /api/runs/retest-current
GET  /api/runs
GET  /api/runs/{id}
GET  /api/dns/current
POST /api/dns/sync
```

### Domain / 业务层

负责定义核心业务概念：

- ScanRequest
- ScanRun
- IPTestResult
- IPProfile
- DNSTarget
- DNSChangeLog
- DNSSyncStatus

业务层需要明确规则：

- 什么叫通过测试。
- 什么 IP 可以进入 DNS。
- 什么记录可以删除。
- 什么状态算同步成功。

### Services / 应用服务层

负责流程编排：

```text
ForceRefreshService
-> 调用 scanner 扫描
-> 保存结果
-> 选择 Top N
-> 调用 dns sync
-> 保存同步日志
-> 更新 run 状态
```

```text
RetestCurrentService
-> 从 Cloudflare 或数据库读取当前 DNS IP
-> 重测速度
-> 保存结果
-> 可选更新 DNS
```

### Data / 数据层

第一版使用 SQLite：

- 部署简单。
- 足够记录历史。
- 方便备份。
- 后续可迁移到 PostgreSQL。

数据库文件建议放在：

```text
/data/cf-betterip.db
```

### Integrations / 外部集成层

第一版只有 Cloudflare API：

- List DNS Records
- Delete DNS Record
- Create DNS Record
- Verify Token / Zone

Cloudflare API Token 建议只给指定 Zone 的 DNS Edit 权限。

### Agents / 自动化层

第一版的自动化就是定时任务：

- 每天强制扫描。
- 每周强制更新数据源。
- 失败重试。

暂时不需要复杂 agent。

### Observability / 运维层

必须记录：

- 每次扫描参数。
- 每个 IP 的测试结果。
- 每次 DNS 删除和创建。
- Cloudflare 反查确认结果。
- 错误原因。

WebUI 里要能看到最近一次运行是否真正成功。

## 数据流

强制新扫描：

```text
用户点击 Run
-> 后端创建 run
-> scanner 下载/读取数据源
-> 随机生成候选 IP
-> RTT 测试
-> 速度测试
-> 保存 ip_test_results
-> 选择 Top N
-> dns sync 删除指定域名旧 A/AAAA
-> 创建新 A/AAAA
-> 反查 Cloudflare DNS
-> 对比目标 IP 列表
-> 保存 dns_change_logs
-> 更新 run 状态
-> WebUI 展示结果
```

## 部署方式

第一版支持两种：

### 原生二进制

```bash
go build -o cf-betterip-web ./cmd/cf-betterip-web
./cf-betterip-web --data-dir ./data --listen :8080
```

### Docker

```bash
docker compose up -d
```

建议挂载：

```text
./data:/data
```

用于保存 SQLite、IP 数据源和日志。
