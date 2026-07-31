package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 命令行版本的入口
func main() {
	dataDir = strings.TrimSpace(os.Getenv("BETTER_CF_DATA_DIR"))
	initLocations()
	showMenu()
}

// 显示主菜单
func showMenu() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println("----------------------------------------")
		fmt.Println("1. IPV4 优选 (TLS)")
		fmt.Println("2. IPV4 优选 (非 TLS)")
		fmt.Println("3. IPV6 优选 (TLS)")
		fmt.Println("4. IPV6 优选 (非 TLS)")
		fmt.Println("5. 单 IP 测速 (TLS)")
		fmt.Println("6. 单 IP 测速 (非 TLS)")
		fmt.Println("7. 清空缓存")
		fmt.Println("8. 更新数据")
		fmt.Println("0. 退出")
		fmt.Print("请选择菜单 (默认 0): ")

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			input = "0"
		}

		switch input {
		case "0":
			fmt.Println("退出成功")
			return
		case "1":
			runIPSelector(4, true)
		case "2":
			runIPSelector(4, false)
		case "3":
			runIPSelector(6, true)
		case "4":
			runIPSelector(6, false)
		case "5":
			runSingleSpeedTest(true)
		case "6":
			runSingleSpeedTest(false)
		case "7":
			clearCache()
		case "8":
			updateData()
		default:
			fmt.Println("无效输入，请重新选择")
		}
	}
}

// runIPSelector 运行 IP 优选流程
func runIPSelector(ipType int, useTLS bool) {
	var bandwidth int
	var taskNum int

	fmt.Print("请设置期望的带宽大小 (默认最小 1，单位 Mbps): ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			bandwidth = 1
		} else {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Println("输入无效，已使用默认值 1 Mbps")
				bandwidth = 1
			} else {
				bandwidth = val
			}
		}
	}

	fmt.Print("请设置 RTT 测试进程数 (默认 50，最大 100): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			taskNum = 50
		} else {
			val, err := strconv.Atoi(input)
			if err != nil {
				fmt.Println("输入无效，已使用默认值 50")
				taskNum = 50
			} else if val <= 0 {
				fmt.Println("进程数不能为 0，自动设置为默认值")
				taskNum = 50
			} else {
				taskNum = val
			}
			if taskNum > 100 {
				fmt.Println("超过最大进程限制，自动设置为最大值")
				taskNum = 100
			}
		}
	}

	maxRTTMs := maxRTTFromEnv()
	if maxRTTMs == 0 {
		fmt.Print("请设置最大 RTT (默认 200，单位毫秒): ")
		if scanner.Scan() {
			maxRTTMs = normalizeMaxRTT(parseIntWithDefault(scanner.Text(), 200))
		}
	}
	if maxRTTMs == 0 {
		maxRTTMs = 200
	}

	speed := bandwidth * 128
	startTime := time.Now()
	filter := locationFilterFromEnv()
	if filter.Enabled() {
		fmt.Println("地区筛选:", filter.Summary())
	}

	// 执行 Cloudflare 测试
	anycast, max, avgms, dataCenterCode := cloudflareTest(ipType, useTLS, taskNum, speed, maxRTTMs, filter)

	realBandwidth := max / 128
	endTime := time.Now()
	dataCenter := lookupLocation(dataCenterCode)

	fmt.Println()
	fmt.Println("优选 IP:", anycast)
	fmt.Println("设置带宽:", bandwidth, "Mbps")
	fmt.Println("最大 RTT:", maxRTTMs, "毫秒")
	fmt.Println("实测带宽:", realBandwidth, "Mbps")
	fmt.Println("峰值速度:", max, "kB/s")
	fmt.Println("往返延迟:", avgms, "毫秒")
	fmt.Println("数据中心:", locationDisplayName(dataCenterCode, dataCenter))
	fmt.Println("数据中心代码:", dataCenterCode)
	fmt.Println("数据中心国家:", dataCenter.Cca2)
	fmt.Println("数据中心区域:", dataCenter.Region)
	fmt.Println("数据中心城市:", dataCenter.City)
	fmt.Println("总计用时:", int(endTime.Sub(startTime).Seconds()), "秒")
}

// cloudflareTest 核心测试逻辑
func cloudflareTest(ipType int, useTLS bool, taskNum int, speed int, maxRTTMs int, filter locationFilter) (string, int, int, string) {
	downloadAllData()
	filename := dataPath("ips-v4.txt")
	if ipType == 6 {
		filename = dataPath("ips-v6.txt")
	}
	content, err := getFileContent(filename)
	if err != nil {
		fmt.Println("读取 IP 列表失败:", err)
		return "", 0, 0, ""
	}
	ipList := parseIPList(content)
	if len(ipList) == 0 {
		fmt.Printf("原版 IPv%d 地址池为空。\n", ipType)
		return "", 0, 0, ""
	}
	fmt.Printf("已加载原版 IPv%d Cloudflare Anycast 地址池，共 %d 个子网。\n", ipType, len(ipList))
	if filter.Enabled() {
		fmt.Printf("候选 IP 仍从原版地址池生成；实际响应机房将根据 CF-RAY 按 %s 筛选。\n", filter.Summary())
	}

	sampleSize := 100
	if len(ipList) < sampleSize {
		sampleSize = len(ipList)
	}

	filterStartedAt := time.Now()
	fallbackLogged := false
	for {
		activeFilter := filter.Active(time.Since(filterStartedAt))
		if filter.Enabled() && !activeFilter.Enabled() && !fallbackLogged {
			fmt.Printf("地区优先等待已达 %s，回退到全局随机模式。\n", filter.PreferDuration)
			fallbackLogged = true
		}
		var rttResults []RTTResult
		for {
			sampled := randomSample(ipList, sampleSize)
			var testIPs []string
			if ipType == 6 {
				testIPs = getRandomIPv6s(sampled)
			} else {
				testIPs = getRandomIPv4s(sampled)
			}

			fmt.Printf("已从原版地址池生成 %d 个候选 IP，先进行快速响应和机房检测...\n", len(testIPs))

			rttResults = runRTTTest(testIPs, taskNum, useTLS, maxRTTMs, activeFilter)
			if len(rttResults) > 0 {
				break
			}
			if activeFilter.Enabled() {
				fmt.Printf("本轮没有符合 %s 且 RTT 达标的响应 IP，继续从原版地址池换一批候选...\n", activeFilter.Summary())
			} else {
				fmt.Println("本轮没有 RTT 达标的 Cloudflare 响应 IP，继续换一批候选...")
			}
			activeFilter = filter.Active(time.Since(filterStartedAt))
		}

		fmt.Println("待测速的 IP 地址")
		for _, r := range rttResults {
			fmt.Printf("%s 往返延迟 %d 毫秒\n", r.IP, r.LatencyMs)
		}

		// 速度测试
		for _, r := range rttResults {
			fmt.Println("正在测试", r.IP)
			speedPort := 80
			if useTLS {
				speedPort = 443
			}
			maxSpeed, _, dc := runSpeedTestSimple(r.IP, speedPort, useTLS)
			fmt.Printf("%s 峰值速度 %d kB/s", r.IP, maxSpeed)
			if dc != "" {
				fmt.Printf(", 数据中心 %s", lookupDataCenter(dc))
			}
			fmt.Println()
			if maxSpeed < speed {
				continue
			}
			if r.DataCenterCode != "" && dc != "" && !strings.EqualFold(r.DataCenterCode, dc) {
				fmt.Printf("跳过 %s：初筛机房 %s 与下载机房 %s 不一致，路由不稳定。\n", r.IP, r.DataCenterCode, dc)
				continue
			}

			// 下载可能改变队列和路由状态，达标后重新测量一次。
			// 只有初筛和复检都稳定，才把 IP 作为最终结果。
			time.Sleep(500 * time.Millisecond)
			postAvgMs, postMaxMs, postDC := testRTT(r.IP, useTLS)
			if postAvgMs <= 0 {
				fmt.Printf("跳过 %s：下载后 RTT 复检失败。\n", r.IP)
				continue
			}
			if dc != "" && postDC != "" && !strings.EqualFold(dc, postDC) {
				fmt.Printf("跳过 %s：下载机房 %s 与复检机房 %s 不一致，路由不稳定。\n", r.IP, dc, postDC)
				continue
			}
			if r.DataCenterCode != "" && postDC != "" && !strings.EqualFold(r.DataCenterCode, postDC) {
				fmt.Printf("跳过 %s：初筛机房 %s 与复检机房 %s 不一致，路由不稳定。\n", r.IP, r.DataCenterCode, postDC)
				continue
			}
			if activeFilter.Enabled() && !activeFilter.MatchesDataCenter(postDC) {
				fmt.Printf("跳过 %s：下载后实测机房 %s 已不符合 %s。\n", r.IP, postDC, activeFilter.Summary())
				continue
			}
			stableAvgMs := maxInt(r.LatencyMs, postAvgMs)
			stableMaxMs := maxInt(r.MaxLatencyMs, postMaxMs)
			fmt.Printf("%s RTT 复检：初筛平均 %d ms / 最大 %d ms，下载后平均 %d ms / 最大 %d ms。\n",
				r.IP, r.LatencyMs, r.MaxLatencyMs, postAvgMs, postMaxMs)
			if maxRTTMs > 0 && stableMaxMs > maxRTTMs {
				fmt.Printf("跳过 %s：稳定性 RTT %d ms 超过上限 %d ms。\n", r.IP, stableMaxMs, maxRTTMs)
				continue
			}
			if dc == "" {
				dc = postDC
			}
			return r.IP, maxSpeed, stableAvgMs, dc
		}
		fmt.Println("当前所有 IP 都未达到期望带宽，重新开始新一轮测试...")
	}
}

// randomSample 从列表中随机抽取 n 个元素
func randomSample(list []string, n int) []string {
	shuffled := make([]string, len(list))
	copy(shuffled, list)
	randomMu.Lock()
	randomGenerator.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	randomMu.Unlock()
	if n > len(shuffled) {
		n = len(shuffled)
	}
	return shuffled[:n]
}

// RTTResult RTT 测试结果
type RTTResult struct {
	IP             string
	LatencyMs      int
	MaxLatencyMs   int
	DataCenterCode string
}

// runRTTTest 运行 RTT 测试（并发，带进度显示）
func runRTTTest(ipList []string, taskNum int, useTLS bool, maxRTTMs int, filter locationFilter) []RTTResult {
	if len(ipList) == 0 {
		return nil
	}
	if len(ipList) < taskNum {
		taskNum = len(ipList)
	}

	// 第一阶段只请求一次 /cdn-cgi/trace。无响应、非目标机房和 RTT 超限的
	// 候选不会再进行 3 次稳定性采样，这与原版“快速找到有响应 IP 再精测”的顺序一致。
	probeResults, responding, wrongLocation, highRTT := runResponseProbe(ipList, taskNum, useTLS, maxRTTMs, filter)
	if filter.Enabled() {
		fmt.Printf("快速检测完成：%d/%d 个 IP 响应 Cloudflare，%d 个实际机房不符合所选地区，%d 个首次 RTT 超限，%d 个进入稳定性测试。\n",
			responding, len(ipList), wrongLocation, highRTT, len(probeResults))
	} else {
		fmt.Printf("快速检测完成：%d/%d 个 IP 响应 Cloudflare，%d 个首次 RTT 超限，%d 个进入稳定性测试。\n",
			responding, len(ipList), highRTT, len(probeResults))
	}
	if len(probeResults) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	resultChan := make(chan RTTResult, len(probeResults))
	thread := make(chan struct{}, taskNum)
	var count int
	var mu sync.Mutex
	total := len(probeResults)

	for _, probe := range probeResults {
		ip := probe.IP
		wg.Add(1)
		thread <- struct{}{}
		go func(ip string) {
			defer func() {
				<-thread
				wg.Done()
				mu.Lock()
				count++
				current := count
				mu.Unlock()
				if current%10 == 0 || current == total {
					fmt.Printf("RTT 稳定性测试进度: %d/%d\n", current, total)
				}
			}()

			avgMs, maxMs, dc := testRTT(ip, useTLS)
			if avgMs > 0 && (!filter.Enabled() || filter.MatchesDataCenter(dc)) {
				resultChan <- RTTResult{IP: ip, LatencyMs: avgMs, MaxLatencyMs: maxMs, DataCenterCode: dc}
			}
		}(ip)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var results []RTTResult
	skippedHighRTT := 0
	for r := range resultChan {
		if maxRTTMs > 0 && r.MaxLatencyMs > maxRTTMs {
			skippedHighRTT++
			continue
		}
		results = append(results, r)
	}
	if skippedHighRTT > 0 {
		fmt.Printf("RTT 稳定性测跳过 %d 个超过 %d ms 上限的 IP。\n", skippedHighRTT, maxRTTMs)
	}

	// 与原 Shell 版一致：不只截取前 10 个，所有稳定候选按延迟排序后逐个测速。
	sort.Slice(results, func(i, j int) bool {
		if results[i].LatencyMs == results[j].LatencyMs {
			return results[i].MaxLatencyMs < results[j].MaxLatencyMs
		}
		return results[i].LatencyMs < results[j].LatencyMs
	})
	fmt.Printf("RTT 稳定性测试完成，%d/%d 个 IP 有效，将按延迟从低到高逐个测速。\n", len(results), total)
	return results
}

func runResponseProbe(ipList []string, taskNum int, useTLS bool, maxRTTMs int, filter locationFilter) ([]RTTResult, int, int, int) {
	var wg sync.WaitGroup
	type probeResult struct {
		RTTResult
		responded     bool
		wrongLocation bool
		highRTT       bool
	}
	resultChan := make(chan probeResult, len(ipList))
	thread := make(chan struct{}, taskNum)
	for _, ip := range ipList {
		wg.Add(1)
		thread <- struct{}{}
		go func(ip string) {
			defer func() { <-thread; wg.Done() }()
			latencyMs, dc := testRTTSample(ip, useTLS)
			if latencyMs <= 0 {
				resultChan <- probeResult{}
				return
			}
			result := probeResult{RTTResult: RTTResult{IP: ip, LatencyMs: latencyMs, MaxLatencyMs: latencyMs, DataCenterCode: dc}, responded: true}
			if filter.Enabled() && !filter.MatchesDataCenter(dc) {
				result.wrongLocation = true
			} else if maxRTTMs > 0 && latencyMs > maxRTTMs {
				result.highRTT = true
			}
			resultChan <- result
		}(ip)
	}
	go func() { wg.Wait(); close(resultChan) }()

	var candidates []RTTResult
	responding, wrongLocation, highRTT := 0, 0, 0
	for result := range resultChan {
		if !result.responded {
			continue
		}
		responding++
		if result.wrongLocation {
			wrongLocation++
			continue
		}
		if result.highRTT {
			highRTT++
			continue
		}
		candidates = append(candidates, result.RTTResult)
	}
	return candidates, responding, wrongLocation, highRTT
}

// testRTT 测试单个 IP 的 RTT（TCP 连接 + 验证 CF-RAY）。
// 与原 Shell 版保持一致：使用实际测速域名的 /cdn-cgi/trace，
// 连续 3 次取 TCP 建连时间，任意一次失败或机房漂移就丢弃。
func testRTT(ip string, useTLS bool) (int, int, string) {
	var totalMs int
	maxMs := 0
	dataCenterCode := ""
	for range 3 {
		sampleMs, colo := testRTTSample(ip, useTLS)
		if sampleMs <= 0 || colo == "" {
			return 0, 0, ""
		}
		if dataCenterCode == "" {
			dataCenterCode = colo
		} else if !strings.EqualFold(dataCenterCode, colo) {
			return 0, 0, ""
		}

		totalMs += sampleMs
		if sampleMs > maxMs {
			maxMs = sampleMs
		}
	}

	return totalMs / 3, maxMs, dataCenterCode
}

// testRTTSample 对实际测速域名发起一次请求，返回 TCP 建连时间和 CF-RAY 机房码。
func testRTTSample(ip string, useTLS bool) (int, string) {
	port := 80
	if useTLS {
		port = 443
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), time.Second)
	if err != nil {
		return 0, ""
	}
	tcpDuration := time.Since(start)
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	var rwc net.Conn = conn
	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: speedTestDomain})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return 0, ""
		}
		rwc = tlsConn
	}
	requestTarget := "/cdn-cgi/trace"
	if !useTLS {
		requestTarget = "http://" + speedTestDomain + requestTarget
	}
	reqStr := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\nConnection: close\r\n\r\n", requestTarget, speedTestDomain)
	if _, err = rwc.Write([]byte(reqStr)); err != nil {
		_ = rwc.Close()
		return 0, ""
	}
	resp, err := http.ReadResponse(bufio.NewReader(rwc), nil)
	_ = rwc.Close()
	if err != nil {
		return 0, ""
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, ""
	}
	colo := extractDataCenter(resp.Header.Get("CF-RAY"))
	if colo == "" {
		return 0, ""
	}
	sampleMs := int(tcpDuration.Milliseconds())
	if sampleMs < 1 {
		sampleMs = 1
	}
	return sampleMs, colo
}

// runSpeedTestSimple 简单速度测试，返回 (峰值速度 kB/s, 单次 TCP 建连耗时 ms, 数据中心代码)。
// 单次建连耗时只供“单 IP 测速”显示，优选结果的 RTT 使用 testRTT 的 3 次稳定性采样。
func runSpeedTestSimple(ip string, port int, useTLS bool) (int, int, string) {
	var tcpMs int
	dialer := &net.Dialer{Timeout: 1 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			start := time.Now()
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err == nil {
				tcpMs = int(time.Since(start).Milliseconds())
			}
			return conn, err
		},
	}
	if useTLS {
		transport.TLSClientConfig = &tls.Config{ServerName: speedTestDomain}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	testURL := fmt.Sprintf("%s://%s/%s", scheme, speedTestDomain, speedTestFile)

	resp, err := client.Get(testURL)
	if err != nil {
		return 0, 0, ""
	}
	defer resp.Body.Close()

	cfRay := resp.Header.Get("CF-RAY")
	dataCenter := extractDataCenter(cfRay)

	buf := make([]byte, 32*1024)
	var totalBytes int64
	var windowBytes int64
	windowStart := time.Now()
	maxSpeed := 0

	for {
		n, err := resp.Body.Read(buf)
		totalBytes += int64(n)
		windowBytes += int64(n)
		if err != nil {
			break
		}

		elapsed := time.Since(windowStart).Seconds()
		if elapsed >= 1.0 {
			speedKB := int(float64(windowBytes) / 1024 / elapsed)
			if speedKB > maxSpeed {
				maxSpeed = speedKB
			}
			windowBytes = 0
			windowStart = time.Now()
		}
	}

	// 最后一个不满 1 秒的窗口不参与峰值计算，避免时间过短导致速度虚高

	return maxSpeed, tcpMs, dataCenter
}

// extractDataCenter 从 CF-RAY 头提取三字码头
func extractDataCenter(cfRay string) string {
	if cfRay == "" {
		return ""
	}
	parts := strings.Split(cfRay, "-")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// lookupDataCenter 查找数据中心名称
func lookupDataCenter(colo string) string {
	loc := lookupLocation(colo)

	if loc.City != "" {
		return loc.City
	}
	return colo
}

func lookupLocation(colo string) location {
	locationMu.RLock()
	loc := locationMap[strings.ToUpper(strings.TrimSpace(colo))]
	locationMu.RUnlock()
	return loc
}

func locationDisplayName(colo string, loc location) string {
	if loc.City == "" {
		return strings.ToUpper(strings.TrimSpace(colo))
	}
	if loc.Cca2 == "" {
		return loc.City
	}
	return fmt.Sprintf("%s / %s", loc.City, loc.Cca2)
}

// runSingleSpeedTest 单 IP 测速
func runSingleSpeedTest(useTLS bool) {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("请输入需要测速的 IP: ")
	if !scanner.Scan() {
		return
	}
	ip := strings.TrimSpace(scanner.Text())

	defaultPort := 80
	if useTLS {
		defaultPort = 443
	}

	fmt.Printf("请输入需要测速的端口 (默认%d): ", defaultPort)
	var port int
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			port = defaultPort
		} else {
			val, err := strconv.Atoi(input)
			if err != nil || val <= 0 {
				fmt.Printf("输入无效，已使用默认端口 %d\n", defaultPort)
				port = defaultPort
			} else {
				port = val
			}
		}
	} else {
		port = defaultPort
	}

	fmt.Printf("正在测速 %s 端口 %d\n", ip, port)

	speedKB, tcpMs, dc := runSpeedTestSimple(ip, port, useTLS)
	if dc != "" {
		fmt.Printf("%s 平均速度 %d kB/s, TCP延迟 %dms, 数据中心=%s\n", ip, speedKB, tcpMs, lookupDataCenter(dc))
	} else {
		fmt.Printf("%s 平均速度 %d kB/s, TCP延迟 %dms\n", ip, speedKB, tcpMs)
	}
}

// clearCache 清空缓存，删除所有数据文件，下次运行重新下载
func clearCache() {
	for _, f := range []string{"locations.json", "local-ip-ranges.csv", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	fmt.Println("缓存已清空，下次操作会自动重新下载数据")
}

// updateData 重新下载所有数据
func updateData() {
	fmt.Println("正在重新下载数据...")
	for _, f := range []string{"locations.json", "local-ip-ranges.csv", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	initLocations()
}

// ----------------------- 工具函数 -----------------------

var (
	dataDir         string
	randomMu        sync.Mutex
	randomGenerator = rand.New(rand.NewSource(time.Now().UnixNano()))
	locationMap     map[string]location
	locationMu      sync.RWMutex
	speedTestDomain string
	speedTestFile   string
)

type location struct {
	Iata   string  `json:"iata"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Cca2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
}

type locationFilter struct {
	Mode           string
	Country        string
	Region         string
	City           string
	PreferDuration time.Duration
}

func maxRTTFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("BETTER_CF_MAX_RTT_MS"))
	if raw == "" {
		return 0
	}
	return normalizeMaxRTT(parseIntWithDefault(raw, 200))
}

func parseIntWithDefault(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func normalizeMaxRTT(value int) int {
	if value < 10 {
		return 10
	}
	if value > 2000 {
		return 2000
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func locationFilterFromEnv() locationFilter {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("BETTER_CF_LOCATION_MODE")))
	if mode != "strict" && mode != "prefer" {
		mode = "any"
	}
	preferMinutes, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BETTER_CF_LOCATION_PREFER_MINUTES")))
	if err != nil || preferMinutes < 1 {
		preferMinutes = 10
	}
	return locationFilter{
		Mode:           mode,
		Country:        strings.ToUpper(strings.TrimSpace(os.Getenv("BETTER_CF_LOCATION_COUNTRY"))),
		Region:         strings.TrimSpace(os.Getenv("BETTER_CF_LOCATION_REGION")),
		City:           strings.TrimSpace(os.Getenv("BETTER_CF_LOCATION_CITY")),
		PreferDuration: time.Duration(preferMinutes) * time.Minute,
	}
}

func (f locationFilter) Enabled() bool {
	return f.Mode != "any" && (f.Country != "" || f.Region != "" || f.City != "")
}

func (f locationFilter) Active(elapsed time.Duration) locationFilter {
	if !f.Enabled() {
		return locationFilter{Mode: "any"}
	}
	if f.Mode == "prefer" && elapsed >= f.PreferDuration {
		return locationFilter{Mode: "any"}
	}
	return f
}

func (f locationFilter) Summary() string {
	parts := make([]string, 0, 4)
	if f.Mode == "strict" {
		parts = append(parts, "严格地区")
	} else if f.Mode == "prefer" {
		parts = append(parts, fmt.Sprintf("地区优先（%s 后回退）", f.PreferDuration))
	}
	if f.Country != "" {
		parts = append(parts, "国家="+f.Country)
	}
	if f.Region != "" {
		parts = append(parts, "区域="+f.Region)
	}
	if f.City != "" {
		parts = append(parts, "城市="+f.City)
	}
	if len(parts) == 0 {
		return "全局随机"
	}
	return strings.Join(parts, " / ")
}

func (f locationFilter) MatchesDataCenter(code string) bool {
	if !f.Enabled() {
		return true
	}
	loc := lookupLocation(code)
	if loc.Iata == "" {
		return false
	}
	if f.Country != "" && !strings.EqualFold(f.Country, loc.Cca2) {
		return false
	}
	if f.Region != "" && !strings.EqualFold(f.Region, loc.Region) {
		return false
	}
	if f.City != "" && !strings.EqualFold(f.City, loc.City) {
		return false
	}
	return true
}

func dataPath(name string) string {
	if dataDir == "" {
		return name
	}
	return filepath.Join(dataDir, name)
}

var downloadClient = &http.Client{Timeout: 30 * time.Second}

func getURLContent(targetURL string) (string, error) {
	resp, err := downloadClient.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func getFileContent(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func saveToFile(filename, content string) error {
	dir := filepath.Dir(filename)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return os.WriteFile(filename, []byte(content), 0644)
}

func parseIPList(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var ipList []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ipList = append(ipList, line)
		}
	}
	return ipList
}

func nextRandomIntn(n int) int {
	randomMu.Lock()
	defer randomMu.Unlock()
	return randomGenerator.Intn(n)
}

func getRandomIPv4s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		if idx := strings.Index(subnet, "/"); idx >= 0 {
			subnet = subnet[:idx]
		}
		octets := strings.Split(subnet, ".")
		if len(octets) == 4 {
			octets[3] = fmt.Sprintf("%d", nextRandomIntn(256))
			randomIPs = append(randomIPs, strings.Join(octets, "."))
		}
	}
	return randomIPs
}

func getRandomIPv6s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		if idx := strings.Index(subnet, "/"); idx >= 0 {
			subnet = subnet[:idx]
		}
		// 展开 :: 压缩，确保有 8 段
		if strings.Contains(subnet, "::") {
			parts := strings.Split(subnet, "::")
			left := strings.Split(parts[0], ":")
			var right []string
			if len(parts) > 1 && parts[1] != "" {
				right = strings.Split(parts[1], ":")
			}
			missing := 8 - len(left) - len(right)
			sections := left
			for range missing {
				sections = append(sections, "0")
			}
			sections = append(sections, right...)
			subnet = strings.Join(sections, ":")
		}
		sections := strings.Split(subnet, ":")
		if len(sections) >= 3 {
			sections = sections[:3]
			for i := 3; i < 8; i++ {
				sections = append(sections, fmt.Sprintf("%x", nextRandomIntn(65536)))
			}
			randomIPs = append(randomIPs, strings.Join(sections, ":"))
		}
	}
	return randomIPs
}

// downloadAllData 确保所有数据文件存在，缺失则自动下载
func downloadAllData() {
	urlFilename := dataPath("url.txt")
	if _, err := os.Stat(urlFilename); os.IsNotExist(err) {
		fmt.Println("本地", urlFilename, "不存在，正在下载...")
		content, err := getURLContent("https://www.baipiao.eu.org/cloudflare/url")
		if err != nil {
			fmt.Println("下载测速 URL 失败:", err)
			return
		}
		if err := saveToFile(urlFilename, content); err != nil {
			fmt.Println("保存测速 URL 失败:", err)
			return
		}
	}

	content, err := getFileContent(urlFilename)
	if err != nil {
		fmt.Println("读取测速 URL 失败:", err)
		return
	}
	content = strings.TrimSpace(content)
	parts := strings.SplitN(content, "/", 2)
	if len(parts) == 2 {
		speedTestDomain = parts[0]
		speedTestFile = parts[1]
	} else {
		fmt.Println("测速 URL 格式异常")
	}

	for _, item := range []struct{ file, url string }{
		{"ips-v4.txt", "https://www.baipiao.eu.org/cloudflare/ips-v4"},
		{"ips-v6.txt", "https://www.baipiao.eu.org/cloudflare/ips-v6"},
	} {
		fp := dataPath(item.file)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			fmt.Println("本地", fp, "不存在，正在下载...")
			c, err := getURLContent(item.url)
			if err != nil {
				fmt.Println("下载 IP 列表失败:", err)
				return
			}
			if err := saveToFile(fp, c); err != nil {
				fmt.Println("保存 IP 列表失败:", err)
				return
			}
		}
	}

	fp := dataPath("locations.json")
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		fmt.Println("本地", fp, "不存在，正在下载...")
		resp, err := downloadClient.Get("https://www.baipiao.eu.org/cloudflare/locations")
		if err != nil {
			fmt.Println("获取位置信息失败:", err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
		resp.Body.Close()
		if err != nil {
			fmt.Println("读取响应内容失败:", err)
			return
		}
		if err := saveToFile(fp, string(body)); err != nil {
			fmt.Println("保存位置信息失败:", err)
			return
		}
	}
}

// initLocations 初始化数据中心位置信息
func initLocations() {
	downloadAllData()

	fp := dataPath("locations.json")
	body, err := os.ReadFile(fp)
	if err != nil {
		fmt.Println("读取位置文件失败:", err)
		return
	}

	var locations []location
	if err := json.Unmarshal(body, &locations); err != nil {
		fmt.Println("解析位置信息 JSON 失败:", err)
		return
	}

	loadedMap := make(map[string]location)
	for _, loc := range locations {
		loadedMap[loc.Iata] = loc
	}

	locationMu.Lock()
	locationMap = loadedMap
	locationMu.Unlock()

	fmt.Printf("已加载 %d 个数据中心位置信息\n", len(loadedMap))
}
