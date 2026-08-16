# Better CF

[![Version](https://img.shields.io/badge/version-v1.1.0-2563eb)](VERSION)
[![GitHub](https://img.shields.io/badge/GitHub-samni728%2Fbetter--cf-111827?logo=github)](https://github.com/samni728/better-cf)
[![Star](https://img.shields.io/github/stars/samni728/better-cf?style=social)](https://github.com/samni728/better-cf)

Better CF 是一个基于 `better-cloudflare-ip` 的 Cloudflare 优选 IP 自动化项目。

当前 `v1.1.0` 已把 WebUI 拆成执行中心、任务历史、IP 结果和项目配置四个工作区，并新增可管理的 SQLite 搜寻记忆：真连接成功会从具体 IP 逐级扩展到 `/24`、父 `/16` 和全局池；近期失败 IP/网段会跨任务冷却，候选预算会按历史成功率自动调整。

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
- 按 `CF-RAY` 实测响应机房选择国家、Cloudflare 大区和城市
- 国家、大区、城市三级联动，例如 `JP → Asia Pacific → Tokyo`
- 实时显示当前地区匹配的 Cloudflare 机房数量与 IATA 代码
- IPv4 写入数量
- IPv6 写入数量
- 期望带宽 Mbps
- RTT 测试进程数
- 最大 TCP RTT（默认 200ms）
- 是否使用 TLS

如果某个协议族没有勾选启用，或者写入数量为 0，系统会跳过该协议族的扫描和 DNS 同步。例如 VPS 没有 IPv6 时，可以取消 IPv6 勾选，或把 IPv6 数量设为 0。

当前执行逻辑会按任务串行收集结果，避免多个测速任务并发影响真实带宽表现。候选 IP 先进行一次快速 HTTPS 响应和 `CF-RAY` 机房检测；通过后在下载测速前后各执行 3 次 TCP RTT 采样。任意一次超过最大 RTT 都会被淘汰，最终页面显示两轮中较差的平均 RTT。

可选的“真连接测试”位于原有 RTT / 带宽筛选之后。它不是再次测速，而是把候选 IP 写入用户提供的 `vmess://` 或 `vless://` 节点模板，通过临时 Xray HTTP 代理访问指定测试 URL：

- IPv4 与 IPv6 可以分别启用。
- HTTP / 非 TLS 与 HTTPS / TLS 可以分别启用，并分别要求一条匹配的节点模板。
- HTTP 完整检查 `80, 8080, 8880, 2052, 2082, 2086, 2095`。
- HTTPS 完整检查 `443, 2053, 2083, 2087, 2096, 8443`。
- 一个候选只要有一个所选端口真正返回 HTTP `2xx/3xx` 就能入选；系统仍会完成整个端口组并记录所有可用端口及各自响应延迟。
- 节点分享链接含 UUID 等敏感信息，只保存在当前 Settings，不复制到任务日志或任务配置快照。

启用此功能时，VPS 需要安装官方 Xray-core，并可通过 `XRAY_BIN` 指定路径；默认也会查找 `/root/cf-betterip/xray`。

地区模式的含义：

- 全局随机：下方保留的地区值不参与筛选，从全球 IP 池随机抽取。
- 所选地区优先：候选仍来自原版全局 Anycast 地址池，先只接受 `CF-RAY` 实测机房匹配的结果，连续 10 分钟无结果才回退全球。
- 仅接受所选地区：只接受 `CF-RAY` 实测机房匹配的结果，不回退全球；单个协议族连续 30 分钟没有新增结果时任务失败。

可选机房来自 `locations.json`。Cloudflare 使用 Anycast，IP 本身没有固定落地国家；所以地区条件以当前 VPS 对测速域名发起请求时返回的 `CF-RAY` 为准。Cloudflare GeoFeed 快照仍保留作为地理数据参考，但不再用来生成测速候选 IP。

地区扫描先按当前条件重测历史成功 IP，仍达标则直接复用；否则优先从历史及当前命中的 IPv4 `/24` 或 IPv6 `/48` 子网扩展新 IP，并在全局地址池遍历完前不重复抽取子网。IPv4 或 IPv6 某一方连续 30 分钟无新增结果时，只结束该协议族并继续另一方；数量未全部满足时不执行部分 DNS 替换。

### 2. Cloudflare DNS 自动同步

扫描达到目标数量后，系统会把同一轮入选结果分发到所有已启用 DNS 目标：

- 凭据库可以新增、编辑和删除多组 Cloudflare 凭据。
- 每组凭据可选 `API Token` 或 `Email + Global API Key`。
- DNS 目标可以新增、编辑、停用和删除，包含显示名称、根域名、Zone ID、完整域名、`ipv4` / `ipv6` / `both` 以及引用的凭据 ID。
- `ipv4` 目标只写 A，`ipv6` 目标只写 AAAA，`both` 目标同时写 A 和 AAAA。
- 一次扫描只测一份 IPv4 / IPv6 结果，不会因目标数量增加而重复测速；DNS 阶段再 fan-out 到多个目标。

同步时只会操作目标所引用 Zone ID 内、完整域名与记录类型都精确匹配的 DNS 记录，不会删除其他域名或其他记录类型。

旧版“单域名 / IPv4 与 IPv6 分离”配置会在首次读取时自动迁移成凭据库和 DNS 目标列表，无需手工重建配置。

### 2.1 独立手动 DNS 目标

配置页可以另外添加“手动 DNS 目标”。它们不参与自动扫描、定时任务或 auto update，只在管理员手动操作时更改 Cloudflare DNS。

- 可混合粘贴合计最多 500 个 `vmess://` / `vless://` 分享链接，输入框会实时显示已识别的唯一 IPv4 / IPv6 数量。
- vmess 只读取 JSON 的 `add`，vless 只读取 URL 的服务器地址；两者都只接受真实 IPv4 / IPv6 字面量，不使用节点名称、Host、SNI、备注、端口或 UUID。
- IP 规范化并去重后，完整替换该完整域名的 A / AAAA 集合。
- “清空”只删除该完整域名的 A / AAAA，保留手动目标配置；“清空并删除目标”会在 Cloudflare 清理成功后再移除本地配置。
- 手动目标不允许与自动 DNS 目标重复，防止定时任务覆盖手动结果。

### 3. 安全替换：先创建、后删除

每次同步时，系统会：

1. 按目标查询已有的 A / AAAA 记录。
2. 计算需要保留、创建和删除的差集。
3. 先创建缺失的新记录；任何创建失败时，该目标不删除旧记录。
4. 新记录创建成功后，再删除不在本轮目标集合中的过期记录。
5. 再次从 Cloudflare 查询，确认每个目标的实际记录与应写集合一致。

这样既不会无限追加旧 IP，也不会在新记录尚未创建成功时先清空可用解析。

### 4. WebUI 看板

WebUI 当前包含：

- 当前同步状态
- 今日更新 IP 数量
- 今日写入 DNS 记录数量（按目标 fan-out 后累计）
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

文件每行字段为 `CIDR, 国家, 区域代码, 城市`。首次启动会复制到 `data/local-ip-ranges.csv`；之后可在 WebUI 中更新。注意：这份 GeoFeed 快照是地理数据参考，不是 better-cloudflare-ip 的可测 CDN 地址池；实际扫描候选来自 `ips-v4.txt / ips-v6.txt`，地区判定来自 `CF-RAY + locations.json`。

该目录包含管理员账号哈希、Cloudflare API Token / Global API Key、任务历史和测速结果，不应该提交到 Git。

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

- 一组或多组 Cloudflare 凭据：API Token，或 Email + Global API Key
- 一个或多个 DNS 目标：名称、根域名、Zone ID、完整域名、记录族、凭据
- IPv4 / IPv6 数量
- 地区筛选模式、国家、区域/数据中心代码和城市
- 带宽目标
- RTT 测试进程数
- 定时策略

保存后可以按 DNS 目标测试 Cloudflare 写入，系统会使用该目标引用的凭据创建并删除一个临时 TXT 记录，用于确认凭据、Zone 和 DNS 写入权限是否可用。

## 安全说明

请不要提交以下内容：

- `memo.md`
- `data/`
- `logs/`
- `bin/`
- `.env`
- Cloudflare API Token / Global API Key
- VPS 密码
- 管理员账号数据

本仓库只公开源码和可复用脚本。项目内部设计、部署及运维文档保存在本地 `docs/`，不会同步到 GitHub。

## 免责声明

本项目用于学习、网络质量测试和个人基础设施自动化管理。使用者需要自行确认使用场景符合 Cloudflare 服务条款、当地法律法规以及相关网络服务规则。
