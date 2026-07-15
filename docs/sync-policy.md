# 同步策略

## 主从关系

本项目以 VPS 上的源码目录为主：

```bash
/root/cf-betterip/source
```

当前本地目录作为同步副本：

```bash
/Users/samni/Desktop/开发项目/cf-betterip-ser
```

默认工作逻辑：

```text
VPS source 是主项目
-> 本地目录从 VPS 拉取同步
-> 本地用于阅读、备份、文档查看和必要时辅助编辑
```

## 为什么这样做

当前项目真正运行、测速和后续部署都发生在 VPS 上。把 VPS 作为主项目可以避免：

- 本地代码和远端运行代码不一致。
- 本地改了但忘记部署。
- 文档、源码、运行数据混在不同位置。

## 默认同步方向

默认方向是：

```text
VPS -> 本地
```

也就是从：

```bash
root@your-vps-ip:/root/cf-betterip/source/
```

同步到：

```bash
/Users/samni/Desktop/开发项目/cf-betterip-ser/
```

## 本地同步命令

在本地当前目录执行：

```bash
VPS_HOST='your-vps-ip' ./scripts/pull-from-vps.sh
```

该脚本默认：

- 使用 `rsync`。
- 排除远端 `.git/`。
- 排除远端运行产物 `bin/`、`data/`、`logs/`。
- 不使用 `--delete`，避免删除本地额外文件，例如 `memo.md`。
- 不在脚本中保存明文密码。

如果没有配置 SSH key，可以临时使用：

```bash
VPS_HOST='your-vps-ip' VPS_PASSWORD='your-password' ./scripts/pull-from-vps.sh
```

## 什么时候允许本地推回 VPS

默认不主动从本地推回 VPS。只有在以下情况才考虑反向同步：

- 明确确认本地改动是要进入远端主项目。
- 先检查本地和远端差异。
- 避免覆盖 VPS 上的新改动。

后续如果需要，可以再增加 `scripts/push-to-vps.sh`，但第一阶段先保持单向同步，降低误覆盖风险。

## 开发协作规则

后续开发时优先遵循：

1. 先查看 VPS 当前源码状态。
2. 在 VPS 项目或从 VPS 拉下来的同步副本中规划修改。
3. 修改完成后确保 VPS 上的源码是最新主版本。
4. 再执行 `./scripts/pull-from-vps.sh` 更新本地副本。

## 注意事项

- `/root/cf-betterip` 是运行目录，包含二进制和数据文件。
- `/root/cf-betterip/source` 是源码目录。
- `/root/cf-betterip/source/data` 后续会保存 WebUI 配置和敏感 Token，不要同步到本地文档副本。
- 不要把运行数据文件误当作源码提交。
- 不要把 Cloudflare API Token、root 密码等敏感信息写进文档或脚本。
