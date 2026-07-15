# 测速与选择策略

## 现有 CLI 流程

当前 `main.go` 的核心流程是：

```text
读取 IP 池
-> 随机生成 100 个测试 IP
-> RTT 并发测试
-> 保留 RTT 最低的前 10 个
-> 串行速度测试
-> 找到第一个达到设置带宽的 IP
-> 输出优选结果
```

CLI 当前输出包括：

```text
优选 IP
设置带宽
实测带宽
峰值速度
往返延迟
数据中心
总计用时
```

这些字段在 Web 产品中都应该作为结构化数据保存。

## Web 版需要改变的地方

Web 版不能只返回第一个达标 IP，而应该返回 Top N：

```go
func ScanTopIPs(req ScanRequest) ([]IPTestResult, error)
```

核心请求：

```text
ip_version: 4 / 6
use_tls: true / false
result_count: 10
configured_bandwidth_mbps: 100
rtt_concurrency: 100
candidate_sample_size: 100
```

核心结果：

```text
ip
record_type
protocol
configured_bandwidth_mbps
measured_bandwidth_mbps
peak_speed_kbps
rtt_ms
data_center_code
data_center_name
test_duration_seconds
passed
rank
selected_for_dns
```

## 运行模式

### 强制新扫描

默认模式。

每次都从 IP 池随机生成新候选 IP，重新 RTT 和速度测试，再选择 Top N 写入 DNS。

适合：

- 每日自动更新。
- 用户手动要求换一批新 IP。
- 当前 DNS IP 质量不稳定。

### 重测当前 IP

辅助模式。

从当前 Cloudflare DNS 读取指定域名下的 A / AAAA 记录，只重测这些 IP 的 RTT、速度和数据中心。

适合回答：

```text
当前正在使用的 IP 现在还快不快？
这个 IP 已经连续通过几天？
是否需要强制换新？
```

### 补位扫描

第二阶段再做。

先重测当前 IP，如果通过数量不足，再扫描新 IP 补足缺口。

## 通过标准

一个 IP 通过测试的最低标准：

- RTT 测试成功。
- 能通过 HTTP / HTTPS 请求验证 Cloudflare 响应。
- `peak_speed_kbps > 0`。
- `measured_bandwidth_mbps >= configured_bandwidth_mbps`。
- 能解析出数据中心更好，但不作为硬性条件。

## 排序策略

MVP 先使用透明排序，不引入复杂黑盒评分。

排序优先级：

1. 通过测试的 IP 优先。
2. 实测带宽越高越好。
3. 峰值速度越高越好。
4. RTT 越低越好。
5. 历史连续通过天数越多越好。
6. 最近被成功同步过的 IP 可作为稳定性参考，但强制新扫描模式下不强行偏向旧 IP。

## 指标解释

### 设置带宽

用户期望的最低带宽，例如 100 Mbps。

当前 CLI 中换算为：

```go
speedKB := bandwidthMbps * 128
```

### 实测带宽

根据峰值速度换算：

```go
measuredBandwidthMbps := peakSpeedKBps / 128
```

### 峰值速度

下载测速过程中按 1 秒窗口统计的最大速度，单位 kB/s。

### RTT 延迟

TCP 连接耗时，当前 CLI 连续 3 次取平均。

### 数据中心

从 `CF-RAY` 响应头中提取三字码，再通过 `locations.json` 转换为城市名。

## 历史画像

每个 IP 需要积累长期画像：

```text
首次发现时间
首次写入 DNS 时间
最近测试时间
最近写入 DNS 时间
测试次数
通过次数
失败次数
被选中次数
连续通过天数
历史最佳带宽
历史最佳峰值速度
历史最佳 RTT
最近数据中心
```

这样 WebUI 可以展示：

```text
这个 IP 已经用了 7 天
连续 7 天测试通过
历史最佳 124 Mbps
最近一次 104 Mbps
数据中心 Los Angeles
```
