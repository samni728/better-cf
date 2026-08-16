# 同步与部署策略

## 唯一源码方向

本项目以 Mac 本地目录为唯一开发源：

```text
/Users/samni/Desktop/开发项目/cf-betterip-ser
```

213 VPS 只是运行与测速环境：

```text
/root/cf-betterip/source
```

正常方向永远是：

```text
Mac 本地源码 -> GitHub（需要发布时）-> 213 VPS
```

不得在普通开发或部署中从 VPS 反向覆盖 Mac 代码。VPS 上的源码差异只用于只读排查和紧急取证。

## 远端目录边界

- `/root/cf-betterip/source`：已部署源码。
- `/root/cf-betterip/source/bin`：Linux 可执行文件。
- `/root/cf-betterip/source/data`：WebUI 账号、Cloudflare 凭据、任务与历史数据；`search_memory.sqlite` 及其 WAL/SHM 保存搜寻经验。
- `/root/cf-betterip/source/logs` 和 `web.log`：运行日志。
- `/root/cf-betterip/backups`：部署前备份。

从 Mac 上传源码时必须排除远端 `.git/`、`data/`、`logs/`、`bin/` 和 `web.log`，再单独上传已验证的 Linux 二进制。不得使用未排除运行数据的 `rsync --delete`。

## 标准部署顺序

1. Mac 本地执行 `go test ./...`、`go vet ./...` 和必要的 race 测试。
2. 构建 `linux/amd64` 静态二进制。
3. 只读确认 213 没有正在运行的任务。
4. 备份当前二进制、`data/app_state.json` 和远端源码。
5. 单向上传 Mac 源码，不覆盖运行数据。
6. 新二进制先使用临时端口和临时数据目录通过 `/healthz`。
7. 原子替换正式二进制并重启。
8. 验证 VPS 本机和 Mac 到 `:18080/healthz`，并检查进程、端口、数据版本与日志。

部署前除 `app_state.json` 外，还要备份 `search_memory.sqlite*`。若 SQLite 已存在，最好先正常停止 Web 进程再做最终备份，确保 WAL 已完整落盘。
9. 健康检查失败时，恢复部署前二进制与数据备份。

## 反向拉取限制

`scripts/pull-from-vps.sh` 只保留为灾难恢复取证工具，不属于日常开发流程。必须由用户明确要求“从 VPS 恢复源码”后，才能带显式解锁参数执行。

## 安全要求

- 不把 Cloudflare Token、Global API Key 或 VPS root 密码写入仓库、日志或部署脚本。
- `data/app_state.json` 及其备份必须保持 `0600`。
- 部署前不停止正在执行的优选任务。
- 不从 VPS 将 `data/`、日志或密钥同步回 Mac 源码库。
