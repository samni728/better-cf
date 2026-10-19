package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const directIngressTraceLimit = 16 * 1024

// Each port must independently prove the same mainland, non-WARP IPv4 egress.
func verifyIPv4DirectIngress(ctx context.Context, candidateIP string) error {
	dialer := &net.Dialer{Timeout: 6 * time.Second}
	return verifyIPv4DirectIngressWithTransport(ctx, candidateIP, func(ip string, port int) http.RoundTripper {
		return newIPv4DirectIngressTransport(ip, port, dialer.DialContext)
	})
}

func newIPv4DirectIngressTransport(candidateIP string, port int, dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, "tcp4", net.JoinHostPort(candidateIP, strconv.Itoa(port)))
		},
		TLSClientConfig:       &tls.Config{ServerName: "www.cloudflare.com", MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   6 * time.Second,
		ResponseHeaderTimeout: 6 * time.Second,
		DisableKeepAlives:     true,
	}
}

func verifyIPv4DirectIngressWithTransport(ctx context.Context, candidateIP string, transportFor func(string, int) http.RoundTripper) error {
	if !isDirectIngressPublicIPv4(candidateIP) {
		return fmt.Errorf("直连准入失败：候选地址必须是公网 IPv4")
	}
	var egress string
	for _, port := range []int{443, 2053, 2083, 2087, 2096, 8443} {
		ip, err := verifyIPv4DirectIngressPort(ctx, port, transportFor(candidateIP, port))
		if err != nil {
			return fmt.Errorf("HTTPS:%d 直连准入失败：%s", port, err)
		}
		if egress != "" && ip != egress {
			return fmt.Errorf("HTTPS:%d 直连准入失败：各端口出口 IPv4 不一致", port)
		}
		egress = ip
	}
	return nil
}

func verifyIPv4DirectIngressPort(ctx context.Context, port int, transport http.RoundTripper) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	client := &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "https://www.cloudflare.com:"+strconv.Itoa(port)+"/cdn-cgi/trace", nil)
	if err != nil {
		return "", fmt.Errorf("无法建立校验请求")
	}
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("校验已取消")
		}
		return "", fmt.Errorf("HTTPS 校验请求失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("校验响应状态必须为 200")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, directIngressTraceLimit+1))
	if err != nil {
		return "", fmt.Errorf("读取校验响应失败")
	}
	if len(body) > directIngressTraceLimit {
		return "", fmt.Errorf("校验响应过大")
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "ip" && key != "loc" && key != "warp") {
			continue
		}
		if _, exists := fields[key]; exists {
			return "", fmt.Errorf("校验响应含重复字段")
		}
		fields[key] = value
	}
	if fields["warp"] != "off" {
		return "", fmt.Errorf("WARP 必须为关闭状态")
	}
	if fields["loc"] != "CN" {
		return "", fmt.Errorf("出口必须位于中国大陆（CN）")
	}
	if !isDirectIngressPublicIPv4(fields["ip"]) {
		return "", fmt.Errorf("出口必须是合法公网 IPv4")
	}
	return fields["ip"], nil
}

func isDirectIngressPublicIPv4(value string) bool {
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	// IsGlobalUnicast also accepts special-use ranges that cannot prove public egress.
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		if netip.MustParsePrefix(prefix).Contains(addr) {
			return false
		}
	}
	return true
}
