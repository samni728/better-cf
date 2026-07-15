# 数据模型

第一版建议使用 SQLite。它足够轻量，适合单 VPS 部署，也方便备份和迁移。

## settings

保存全局配置。

```sql
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

敏感信息如 Cloudflare API Token 可以先本地保存，后续再加密存储。

## dns_targets

保存要同步的目标域名。

```sql
CREATE TABLE dns_targets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  zone_id TEXT NOT NULL,
  account_id TEXT,
  credential_mode TEXT NOT NULL DEFAULT 'shared',
  api_token_ref TEXT,
  record_name TEXT NOT NULL,
  record_family TEXT NOT NULL,
  ttl INTEGER NOT NULL DEFAULT 1,
  proxied INTEGER NOT NULL DEFAULT 0,
  ipv4_count INTEGER NOT NULL DEFAULT 10,
  ipv6_count INTEGER NOT NULL DEFAULT 10,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

`record_name` 必须是完整域名，例如：

```text
speed.123go.eu.org
```

`record_family` 可选：

```text
both
ipv4
ipv6
```

第一版 UI 可以用更简单的配置表达：

```text
单域名模式：一个 target，record_family = both
分离域名模式：两个 target，record_family 分别为 ipv4 / ipv6
```

## scan_runs

保存每次运行。

```sql
CREATE TABLE scan_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  target_id INTEGER,
  target_domain TEXT,
  ip_version INTEGER,
  use_tls INTEGER NOT NULL,
  requested_count INTEGER NOT NULL,
  configured_bandwidth_mbps INTEGER NOT NULL,
  rtt_concurrency INTEGER NOT NULL,
  candidate_sample_size INTEGER NOT NULL,
  update_dns INTEGER NOT NULL DEFAULT 0,
  dns_sync_status TEXT NOT NULL DEFAULT 'not_run',
  dns_sync_summary TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error_message TEXT,
  FOREIGN KEY(target_id) REFERENCES dns_targets(id)
);
```

`mode` 可选：

```text
force_refresh
retest_current
refill
```

`status` 可选：

```text
queued
running
succeeded
failed
cancelled
```

`dns_sync_status` 可选：

```text
not_run
confirmed
partial
failed
```

## ip_test_results

保存每个 IP 每次测试的原始结果。

```sql
CREATE TABLE ip_test_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  ip TEXT NOT NULL,
  ip_version INTEGER NOT NULL,
  record_type TEXT NOT NULL,
  protocol TEXT NOT NULL,
  configured_bandwidth_mbps INTEGER NOT NULL,
  measured_bandwidth_mbps INTEGER NOT NULL,
  peak_speed_kbps INTEGER NOT NULL,
  rtt_ms INTEGER NOT NULL,
  data_center_code TEXT,
  data_center_name TEXT,
  test_duration_seconds INTEGER,
  passed INTEGER NOT NULL,
  failure_reason TEXT,
  rank INTEGER,
  selected_for_dns INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL,
  cloudflare_synced INTEGER NOT NULL DEFAULT 0,
  cloudflare_record_id TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(run_id) REFERENCES scan_runs(id)
);
```

`source` 可选：

```text
new_scan
retest
previous_dns
```

## ip_profiles

保存 IP 长期画像。

```sql
CREATE TABLE ip_profiles (
  ip TEXT PRIMARY KEY,
  ip_version INTEGER NOT NULL,
  first_seen_at TEXT NOT NULL,
  first_selected_at TEXT,
  last_tested_at TEXT,
  last_selected_at TEXT,
  total_test_count INTEGER NOT NULL DEFAULT 0,
  pass_count INTEGER NOT NULL DEFAULT 0,
  fail_count INTEGER NOT NULL DEFAULT 0,
  selected_count INTEGER NOT NULL DEFAULT 0,
  pass_streak_days INTEGER NOT NULL DEFAULT 0,
  best_measured_bandwidth_mbps INTEGER,
  best_peak_speed_kbps INTEGER,
  best_rtt_ms INTEGER,
  last_measured_bandwidth_mbps INTEGER,
  last_peak_speed_kbps INTEGER,
  last_rtt_ms INTEGER,
  last_data_center_code TEXT,
  last_data_center_name TEXT,
  updated_at TEXT NOT NULL
);
```

用途：

- 展示 IP 已经使用多久。
- 展示连续通过天数。
- 展示历史最佳速度和最近速度。
- 用于后续排序和稳定性判断。

## dns_change_logs

保存 Cloudflare DNS 操作日志。

```sql
CREATE TABLE dns_change_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  target_id INTEGER,
  action TEXT NOT NULL,
  record_name TEXT NOT NULL,
  record_type TEXT,
  ip TEXT,
  cloudflare_record_id TEXT,
  status TEXT NOT NULL,
  error_message TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(run_id) REFERENCES scan_runs(id),
  FOREIGN KEY(target_id) REFERENCES dns_targets(id)
);
```

`action` 可选：

```text
list_old
delete_old
create_new
verify
rollback
```

## dns_snapshots

第二阶段使用，更新前保存旧记录，支持回滚。

```sql
CREATE TABLE dns_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  target_id INTEGER NOT NULL,
  record_name TEXT NOT NULL,
  records_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(run_id) REFERENCES scan_runs(id),
  FOREIGN KEY(target_id) REFERENCES dns_targets(id)
);
```

## 数据保留策略

MVP 先全部保留。

后续可以增加：

- 只保留最近 180 天详细测速数据。
- 长期保留 `ip_profiles` 和 `scan_runs` 汇总。
- 支持导出 CSV / JSON。
