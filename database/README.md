# Cloudflare IP 地区网段快照

- 文件：`local-ip-ranges.csv`
- 上游：`https://api.cloudflare.com/local-ip-ranges.csv`
- 更新日期：2026-07-31
- 记录数：138,258
- SHA-256：`a99759cdfc2b61d86e6c68d36e183c1eb69d2b9898fc1521e28e607e97fde261`

每行的前四个字段为：

```text
CIDR,国家代码,区域/数据中心代码,城市
```

这份快照作为 Cloudflare 网段地理标签参考与数据快照保留。它不是 better-cloudflare-ip 的可测 CDN 候选池，不再直接用于生成扫描 IP。实际候选池为 `ips-v4.txt / ips-v6.txt`，地区筛选以当次请求返回的 `CF-RAY` 机房代码为准。
