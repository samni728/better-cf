package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The LAN DNS may return PassWall2 fake IPs in 198.18.0.0/15. The Cloudflare
// API needs real A records; isolate ONLY api.cloudflare.com, never the scanner
// or other LAN clients. TLS remains verified against api.cloudflare.com.
var cloudflareDedicatedClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: cloudflareDirectTransport(),
}
var cloudflareFakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

func cloudflareDirectTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // API-specific direct IPv4 path; avoid the OS proxy.
	var mu sync.Mutex
	var cached []net.IP
	var cacheUntil time.Time
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		dial := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		if !strings.EqualFold(host, "api.cloudflare.com") {
			return dial.DialContext(ctx, network, address)
		}
		mu.Lock()
		ips := append([]net.IP(nil), cached...)
		if time.Now().After(cacheUntil) {
			ips = nil
		}
		mu.Unlock()
		if len(ips) == 0 {
			ips, err = realCloudflareAPIAddresses(ctx)
			if err != nil {
				return nil, fmt.Errorf("Cloudflare API 独立 DNS 查询失败: %w", err)
			}
			mu.Lock()
			cached = append([]net.IP(nil), ips...)
			cacheUntil = time.Now().Add(3 * time.Minute)
			mu.Unlock()
		}
		var problems []error
		for _, ip := range ips {
			conn, e := dial.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			problems = append(problems, e)
		}
		mu.Lock()
		cacheUntil = time.Time{} // refresh if all cached paths are unavailable
		mu.Unlock()
		return nil, fmt.Errorf("Cloudflare API direct dial port %s: %w", port, errors.Join(problems...))
	}
	return transport
}

func realCloudflareAPIAddresses(ctx context.Context) ([]net.IP, error) {
	var problems []error
	for _, server := range []string{"8.8.8.8:53", "223.5.5.5:53"} {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "udp", server)
			},
		}
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := resolver.LookupIP(query, "ip4", "api.cloudflare.com")
		cancel()
		if err != nil {
			problems = append(problems, err)
			continue
		}
		good := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok || !validCloudflareAPIAddress(addr.Unmap()) {
				continue
			}
			good = append(good, ip)
		}
		if len(good) > 0 {
			return good, nil
		}
		problems = append(problems, fmt.Errorf("%s returned no real public A records", server))
	}
	// Last-resort addresses were authenticated against this exact Cloudflare
	// API hostname on 2026-10-29. LAN DNS can hijack even explicit upstream
	// resolvers and return 198.18/15. HTTPS TLS still verifies original SNI.
	// Keep several addresses for resilience; do not apply to candidate scans.
	return []net.IP{
		net.ParseIP("104.19.192.29"),
		net.ParseIP("104.19.192.174"),
		net.ParseIP("104.19.193.29"),
	}, nil
}

func validCloudflareAPIAddress(addr netip.Addr) bool {
	return addr.Is4() && addr.IsGlobalUnicast() &&
		!addr.IsPrivate() && !cloudflareFakeIPRange.Contains(addr)
}

// Retry read-only API calls after transport failures or temporary HTTP 429/5xx.
// Never blindly retry POST/DELETE: re-query their remote outcome before changes.
func doCloudflareRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return client.Do(req)
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*400) * time.Millisecond
			select {
			case <-time.After(delay):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		r := req.Clone(req.Context())
		response, err := client.Do(r)
		if err != nil {
			last = err
			continue
		}
		if response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusBadGateway ||
			response.StatusCode == http.StatusServiceUnavailable ||
			response.StatusCode == http.StatusGatewayTimeout ||
			response.StatusCode == http.StatusInternalServerError {
			io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
			response.Body.Close()
			last = fmt.Errorf("temporary Cloudflare API HTTP %s", strconv.Itoa(response.StatusCode))
			continue
		}
		return response, nil
	}
	return nil, last
}
