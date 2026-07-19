# Cloudflare IP 地区网段快照

- 文件：`local-ip-ranges.csv`
- 上游：`https://api.cloudflare.com/local-ip-ranges.csv`
- 更新日期：2026-07-19
- 记录数：137,994
- SHA-256：`fa8388cc954a7110e44e8be15a7c3357c1a4c4683fd1330833c10e46e935208d`

每行的前四个字段为：

```text
CIDR,国家代码,区域/数据中心代码,城市
```

这份快照让新部署在无需等待首次下载的情况下直接使用国家、区域和城市筛选。运行后可在 WebUI 中点击“更新地区 IP 数据库”获取最新数据。
