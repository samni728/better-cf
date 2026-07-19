# Better CF

Better CF 是一个基于 `better-cloudflare-ip` 的 Cloudflare 优选 IP 自动化项目。

它的目标是：在当前 VPS / 本地网络环境中定期扫描速度更好的 Cloudflare IPv4 / IPv6 IP，保存测速结果，再把最终选出的 IP 批量同步到你自己的 Cloudflare 域名解析中。这样客户端只需要使用你的自定义优选域名，就能使用最新一轮筛选出来的 Cloudflare 优选 IP。

## 项目来源与引用

本项目引用并封装了以下开源项目的核心测速能力：

- 上游项目：[`badafans/better-cloudflare-ip`](https://github.com/badafans/better-cloudflare-ip)
- 上游能力：Cloudflare IPv4 / IPv6 地址池生成、RTT 测试、TLS / 非 TLS 测速、CF-RAY 数据中心识别、优选 IP 输出。

本项目不是替代上游项目，而是在其命令行测速逻辑之上增加了：

- WebUI 配置与运行看板
- 管理员账号登录
- 定时任务与立即执行
- 断点续接
- Cloudflare DNS 批量替换
- 测速结果持久化
- IP 结果看板与运行日志

如果只需要手动命令行测速，可以直接使用上游项目；如果需要自动更新自己的 Cloudflare 域名解析，可以使用本项目。

## 核心功能

### 1. Cloudflare 优选 IP 扫描

支持扫描：

- IPv4 TLS
- IPv4 非 TLS
- IPv6 TLS
- IPv6 非 TLS

可以在 WebUI 中配置：

- 是否启用 IPv4 扫描与 A 记录同步
- 是否启用 IPv6 扫描与 AAAA 记录同步
- 全局随机、地区优先或严格地区筛选
- 按 Cloudflare IP 网段数据库选择国家、区域/数据中心代码和城市
- 国家、区域、城市三级联动，例如 `CN → CN-GD → Guangzhou`
- IPv4 写入数量
- IPv6 写入数量
- 期望带宽 Mbps
- RTT 测试进程数
- 是否使用 TLS

如果某个协议族没有勾选启用，或者写入数量为 0，系统会跳过该协议族的扫描和 DNS 同步。例如 VPS 没有 IPv6 时，可以取消 IPv6 勾选，或把 IPv6 数量设为 0。

当前执行逻辑会按任务串行收集结果，避免多个测速任务并发影响真实带宽表现。

### 2. Cloudflare DNS 自动同步

扫描达到目标数量后，系统会一次性同步到 Cloudflare：

- IPv4 写入 A 记录
- IPv6 写入 AAAA 记录
- 支持单域名模式：IPv4 和 IPv6 都写到同一个域名
- 支持分离域名模式：IPv4 和 IPv6 分别写到不同域名
- 支持统一 Cloudflare Token / Zone ID
- 支持 IPv4 / IPv6 使用独立 Token / Zone ID

同步时只会操作配置中指定域名下对应类型的 DNS 记录，不会删除其他域名或其他记录类型。

### 3. 先清空后更新

每次同步时，系统会：

1. 查询指定域名已有的 A / AAAA 记录。
2. 删除旧记录。
3. 创建本轮扫描得到的新记录。
4. 再次从 Cloudflare 查询确认写入结果。

这样可以保证域名解析中的 IP 始终是最新一轮的优选结果，而不是不断追加旧 IP。

### 4. WebUI 看板

WebUI 当前包含：

- 当前同步状态
- 今日更新 IP 数量
- 今日写入 DNS 数量
- 今日任务数量
- DNS 同步状态
- 最近一次执行进度
- 最近任务日志
- IP 结果看板

IP 结果看板会展示：

- IPv4 / IPv6
- IP 地址
- 协议与记录类型
- 实测带宽
- 峰值速度
- RTT
- Cloudflare 数据中心
- 测试耗时
- DNS 是否已同步
- 测试时间

### 5. 定时任务与立即执行

支持：

- 立即执行
- 每小时执行
- 每天固定时间执行
- 每 N 天固定时间执行

任务执行过程中可以在 WebUI 中查看实时日志。

### 6. 断点续接与防卡死

如果服务重启或任务中断，已经保存的 IP 结果不会丢失。

系统会提供“继续执行”入口，续接已保存的结果并继续补齐剩余数量。

执行过程中：

- 只要脚本持续有输出，就认为任务正常。
- 如果连续一段时间没有任何输出，才会判定当前尝试卡住。
- 卡住后会终止当前尝试并进入下一轮，避免任务永久挂死。

### 7. 停止、删除与超时保护

WebUI 支持：

- 停止正在运行的任务。
- 删除历史任务记录。
- 删除任务时同步删除该任务产生的 IP 测试结果。
- 已有任务运行时，不会再启动第二个测速任务，避免并发影响真实带宽。

系统还有两层自动保护：

- 整体任务超时：默认 3 小时。
- 单协议族无新增结果超时：默认 30 分钟。

例如 VPS 没有 IPv6，但配置了扫描 10 个 IPv6，任务会在 IPv6 阶段持续尝试；如果 30 分钟都没有新增有效 IPv6，就会自动失败并停止，不会永远跑下去。此时可以把 IPv6 数量改成 0，再重新执行。

## 当前架构

```text
cmd/cf-betterip-web/
  WebUI / 登录 / 配置 / 任务执行 / DNS 同步

main.go
  上游 better-cloudflare-ip CLI 测速逻辑

docs/
  产品设计、架构、DNS 同步、数据模型、WebUI 工作流等文档

scripts/
  本地运行与从 VPS 同步源码的辅助脚本
```

当前数据默认保存在：

```text
data/app_state.json
```

仓库内置一份最新 Cloudflare GeoFeed 快照：

```text
database/local-ip-ranges.csv
```

文件每行字段为 `CIDR, 国家, 区域/数据中心代码, 城市` 。首次启动会复制到 `data/local-ip-ranges.csv`；之后可在 WebUI 的“地区筛选”中点击“更新地区 IP 数据库”，从 Cloudflare GeoFeed 重新下载、校验并原子替换运行时数据。

该目录包含管理员账号哈希、Cloudflare Token、任务历史和测速结果，不应该提交到 Git。

## Docker Compose 启动

推荐使用 Docker Compose 部署。镜像会同时编译：

- `better-cloudflare-ip` 原始测速 CLI
- `cf-betterip-web` WebUI 服务

推荐运行：

```bash
./scripts/docker-up.sh
```

也可以手动选择 Compose 命令。

Docker Compose v2：

```bash
docker compose up -d --build
```

Docker Compose v1：

```bash
docker-compose up -d --build
```

访问：

```text
http://服务器IP:18080
```

如果需要改端口：

```bash
BETTER_CF_PORT=8080 docker compose up -d --build
```

或复制一份环境变量示例：

```bash
cp .env.example .env
```

然后编辑 `.env`：

```text
BETTER_CF_PORT=18080
TZ=Asia/Shanghai
BETTER_CF_RUN_TIMEOUT_HOURS=3
BETTER_CF_FAMILY_TIMEOUT_MINUTES=30
BETTER_CF_LOCATION_PREFER_MINUTES=10
```

Compose 会把运行数据挂载到本地：

```text
./data:/app/data
```

这里会保存：

- WebUI 管理员账号哈希
- Cloudflare 配置
- 任务日志
- 测速结果
- `better-cloudflare-ip` 下载的 IP 池和数据中心缓存
- 运行时地区 IP 网段数据库

`data/` 不应该提交到 Git。

常用命令：

```bash
./scripts/compose.sh logs -f
./scripts/compose.sh restart
./scripts/compose.sh down
```

`scripts/compose.sh` 会优先使用 `docker compose`，如果系统没有 v2 插件，则自动回退到 `docker-compose` v1。

注意：终端里的 `root@host:/path#` 是命令提示符，`Docker Compose version ...` 是命令输出，都不要复制进去执行。真正需要执行的只有 `docker compose ...`、`docker-compose ...` 或本项目的 `./scripts/compose.sh ...`。

## 源码启动

### 1. 准备原始测速二进制

Web 服务会调用 `better-cloudflare-ip` 二进制进行真实测速。

默认查找路径包括：

```text
/root/cf-betterip/better-cloudflare-ip
../better-cloudflare-ip
./better-cloudflare-ip
```

也可以通过环境变量指定：

```bash
export SCANNER_BIN=/path/to/better-cloudflare-ip
```

如果希望 CLI 下载的 IP 池和位置缓存写入指定目录，可以配置：

```bash
export BETTER_CF_DATA_DIR=./data
```

### 2. 启动 WebUI

```bash
go run ./cmd/cf-betterip-web --listen 0.0.0.0:18080 --data-dir ./data
```

或使用脚本：

```bash
./scripts/run-web.sh
```

访问：

```text
http://服务器IP:18080
```

首次访问需要创建管理员账号。

### 3. 配置 Cloudflare

在 WebUI 中填写：

- Cloudflare API Token
- Zone ID
- 目标域名
- IPv4 / IPv6 数量
- 地区筛选模式、国家、区域/数据中心代码和城市
- 带宽目标
- RTT 测试进程数
- 定时策略

保存后可以先点击“测试 Cloudflare 写入”，系统会创建并删除一个临时 TXT 记录，用于确认配置是否可用。

## 安全说明

请不要提交以下内容：

- `memo.md`
- `data/`
- `logs/`
- `bin/`
- `.env`
- Cloudflare Token
- VPS 密码
- 管理员账号数据

本仓库只应该保存源码、文档和可复用脚本。

## 开发文档

详细设计请查看：

- [`docs/product-design.md`](docs/product-design.md)
- [`docs/architecture.md`](docs/architecture.md)
- [`docs/dns-sync.md`](docs/dns-sync.md)
- [`docs/data-model.md`](docs/data-model.md)
- [`docs/webui-workflows.md`](docs/webui-workflows.md)
- [`docs/sync-policy.md`](docs/sync-policy.md)

## 免责声明

本项目用于学习、网络质量测试和个人基础设施自动化管理。使用者需要自行确认使用场景符合 Cloudflare 服务条款、当地法律法规以及相关网络服务规则。
