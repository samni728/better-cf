# 数据模型

当前单机版的产品配置、任务和最终结果保存在 `data/app_state.json`；高频搜寻经验保存在 `data/search_memory.sqlite`。SQLite 当前只负责候选记忆，不替代 JSON 的配置主数据。

`search_memory.sqlite` 当前包含：

- `search_profiles`：按地区、节点指纹和测试参数隔离的搜寻空间。
- `ip_observations`：IP、窄/宽网段、结果、错误类别、RTT、带宽、机房和时间。
- `port_observations`：每次真连接尝试的协议、端口、延迟、成功状态和错误类别。
- `manual_prefixes`：用户为单个搜索 profile 手动指定的 IPv4 `/16` 或 IPv6 `/32` 父网段。
- `schema_meta`：数据库 schema 版本。

搜索记忆 schema 当前为 `2`。`ip_observations.candidate_source` 保存 `exact / narrow / wide / global`，用于计算四类候选的动态预算。服务会把 schema `1` 原地迁移到 `2`，不删除已有观察或端口结果。

该数据库使用 WAL，部署和备份时应将 `.sqlite`、`.sqlite-wal`、`.sqlite-shm` 视作同一组；正常停服备份可只复制 checkpoint 后的主文件。

本轮 DNS 配置拆成两类可独立管理的资源：

```text
Cloudflare 凭据库
        ^ credential_id
        |
DNS 目标列表
```

不再把凭据、Zone 和 IPv4 / IPv6 目标写死在“single / split”两个固定槽位中。

## settings

保存扫描、定时、地区筛选等全局配置。DNS 模型使用版本号支持一次性迁移。

```sql
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

关键版本项：

```text
dns_config_version = 2
```

## cloudflare_credentials

凭据库保存可被多个 DNS 目标复用的 Cloudflare 身份。

```sql
CREATE TABLE cloudflare_credentials (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  auth_type TEXT NOT NULL,
  api_token TEXT,
  email TEXT,
  global_api_key TEXT,
  account_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

`auth_type` 只能是：

```text
api_token
global_api_key
```

字段约束：

- `api_token` 模式必须有 API Token，`email` 和 `global_api_key` 不参与认证。
- `global_api_key` 模式必须同时有 Email 和 Global API Key。
- `account_id` 可选，DNS 记录操作以 Zone ID 为边界。
- 页面只显示凭据名称、认证类型和脱敏状态，不回显完整密钥。
- 凭据被 DNS 目标引用时不允许直接删除；必须先更换或删除引用它的目标。

本地持久化的 API Token 和 Global API Key 都是敏感数据，`data/` 不得提交 Git。后续可增加静态加密或操作系统密钥链封装。

## dns_targets

每个 DNS 目标都是一个可独立增删改停用的 fan-out 终点。

```sql
CREATE TABLE dns_targets (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  root_domain TEXT NOT NULL,
  zone_id TEXT NOT NULL,
  record_name TEXT NOT NULL,
  record_family TEXT NOT NULL,
  credential_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(credential_id) REFERENCES cloudflare_credentials(id)
);
```

字段含义：

- `name`：给用户看的目标名称，例如“主站优选”。
- `root_domain`：Cloudflare Zone 的根域名，例如 `example.com`。
- `zone_id`：该根域名对应的 Cloudflare Zone ID。
- `record_name`：要同步的完整域名，例如 `speed.example.com`。
- `record_family`：`ipv4`、`ipv6` 或 `both`。
- `credential_id`：引用 `cloudflare_credentials.id`，不在目标内复制密钥。
- `enabled`：停用后不参与同步，但保留配置和历史引用。

校验规则：

- `record_name` 必须等于 `root_domain` 或是其子域名，禁止跨 Zone 写入。
- `zone_id`、`record_name`、`credential_id` 不得为空。
- 即使目标已停用，其非空 `credential_id` 也必须仍然存在；删除被停用目标引用的凭据前，必须先重新绑定或删除该目标。
- `record_family = ipv4` 只操作 A，`ipv6` 只操作 AAAA，`both` 操作两种记录。
- 同一 Zone ID + 完整域名 + 记录类型不应被两个已启用目标重复管理，避免互相覆盖。

## 一次扫描与多目标 fan-out

扫描参数和入选 IP 属于一次 `scan_run`，不属于某个 DNS 目标。运行流程是：

```text
扫描一次 IPv4 / IPv6
  -> 得到一份入选 IPv4 / IPv6 集合
  -> 枚举已启用 DNS 目标
  -> 按 record_family 取需要的集合
  -> 对每个目标独立同步和确认
```

新增目标只增加 DNS API 写入量，不会重复执行带宽测试。

DNS 应写记录数按“IP × 目标”累计：

```text
required_dns_record_count =
  ∑(目标包含 ipv4 ? selected_ipv4_count : 0)
  + ∑(目标包含 ipv6 ? selected_ipv6_count : 0)
```

例如入选 10 个 IPv4 和 10 个 IPv6，两个 `both` 目标的应写数是 40，而不是 20。`synced_ip_count` / “已写入 DNS”也必须按各目标反查确认的记录数累计，不对相同 IP 去重。

## 旧 single / split 配置自动迁移

读取 `dns_config_version < 2` 的旧 `app_state.json` 时执行一次性迁移：

1. 将旧的统一 API Token 转成一条凭据；IPv4 / IPv6 独立 Token 分别转成独立凭据。
2. 旧 `single` 模式转成一个 `record_family = both` 的 DNS 目标。
3. 旧 `split` 模式转成 IPv4 和 IPv6 两个目标，并按原“继承 / 独立凭据”关系填写 `credential_id`。
4. 根域名从旧配置或完整域名推导；Zone ID、完整域名和启用状态原样保留。
5. 改写前保留一份旧状态备份，然后写入 `dns_config_version = 2`。只有新凭据库和目标列表为空时才从旧字段生成，确保重复启动不会创建重复项。

旧字段仅用于单向兼容读取；新 UI 不再继续编辑 single / split 槽位。

## scan_runs

保存每次扫描和 fan-out 汇总。

```sql
CREATE TABLE scan_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  use_tls INTEGER NOT NULL,
  requested_ipv4_count INTEGER NOT NULL,
  requested_ipv6_count INTEGER NOT NULL,
  configured_bandwidth_mbps INTEGER NOT NULL,
  rtt_concurrency INTEGER NOT NULL,
  update_dns INTEGER NOT NULL DEFAULT 0,
  planned_dns_target_count INTEGER NOT NULL DEFAULT 0,
  confirmed_dns_target_count INTEGER NOT NULL DEFAULT 0,
  required_dns_record_count INTEGER NOT NULL DEFAULT 0,
  confirmed_dns_record_count INTEGER NOT NULL DEFAULT 0,
  dns_sync_status TEXT NOT NULL DEFAULT 'not_run',
  dns_sync_summary TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error_message TEXT
);
```

`status` 可选 `queued`、`running`、`succeeded`、`failed`、`cancelled`。

`dns_sync_status` 可选：

```text
not_run    未执行
confirmed  所有计划目标都反查一致
partial    部分目标成功，部分失败
failed     没有完成计划中的 DNS 结果
```

## dns_target_syncs

每个运行对每个目标保留独立结果，避免一个目标的错误覆盖整轮细节。

```sql
CREATE TABLE dns_target_syncs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  target_id TEXT NOT NULL,
  status TEXT NOT NULL,
  required_record_count INTEGER NOT NULL,
  confirmed_record_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  FOREIGN KEY(run_id) REFERENCES scan_runs(id),
  FOREIGN KEY(target_id) REFERENCES dns_targets(id)
);
```

## ip_test_results

保存每个 IP 在每次扫描中的原始结果。IP 测试结果不复制为每个 DNS 目标一份。

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
  created_at TEXT NOT NULL,
  FOREIGN KEY(run_id) REFERENCES scan_runs(id)
);
```

`source` 可选 `new_scan`、`retest`、`previous_dns`。某个 IP 在哪些目标完成同步，应通过 `dns_target_syncs` 和 DNS 操作日志追踪，不用单个全局布尔值表达多目标状态。

## dns_change_logs

保存每个目标的 Cloudflare DNS 操作。

```sql
CREATE TABLE dns_change_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  target_id TEXT NOT NULL,
  action TEXT NOT NULL,
  record_name TEXT NOT NULL,
  record_type TEXT NOT NULL,
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
create_new
delete_old
verify
rollback
```

正常替换的操作顺序必须是 `list_old -> create_new -> delete_old -> verify`。如果 `create_new` 任一步失败，该目标的 `delete_old` 不得开始。

## ip_profiles 与 dns_snapshots

`ip_profiles` 继续保存 IP 的长期通过率、最佳 / 最近速度、RTT 和数据中心。

`dns_snapshots` 是后续回滚能力，快照必须同时引用 `run_id` 和 `target_id`，不能只保存一份全局记录集合。

## 数据保留策略

MVP 先全部保留。后续可以：

- 只保留最近 180 天详细测速和 DNS 操作日志。
- 长期保留 `ip_profiles`、`scan_runs` 和同步汇总。
- 支持导出 CSV / JSON。
- 备份 `data/app_state.json` 或 SQLite 文件后再执行格式升级。
