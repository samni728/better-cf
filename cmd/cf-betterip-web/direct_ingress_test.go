package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type directIngressRoundTripFunc func(*http.Request) (*http.Response, error)

func (f directIngressRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIPv4DirectIngressTracePolicy(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"ok", "ip=1.1.1.1\nloc=CN\nwarp=off\n", "", 200},
		{"exact-limit", "ip=1.1.1.1\nloc=CN\nwarp=off\n" + strings.Repeat("x", directIngressTraceLimit-len("ip=1.1.1.1\nloc=CN\nwarp=off\n")), "", 200},
		{"warp", "ip=1.1.1.1\nloc=CN\nwarp=on\n", "WARP", 200},
		{"foreign", "ip=1.1.1.1\nloc=US\nwarp=off\n", "中国大陆", 200},
		{"ipv6", "ip=2606:4700::1111\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"private", "ip=192.168.1.1\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"loopback", "ip=127.0.0.1\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"cgnat", "ip=100.64.0.1\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"documentation", "ip=192.0.2.1\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"malformed", "ip=secret-value\nloc=CN\nwarp=off\n", "公网 IPv4", 200},
		{"missing", "ip=1.1.1.1\nloc=CN\n", "WARP", 200},
		{"duplicate", "ip=1.1.1.1\nip=8.8.8.8\nloc=CN\nwarp=off\n", "重复", 200},
		{"status", "secret-value", "200", 403},
		{"redirect", "secret-value", "200", 302},
		{"oversized", strings.Repeat("x", 16*1024+1), "过大", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := verifyIPv4DirectIngressWithTransport(context.Background(), "1.0.0.1", func(ip string, port int) http.RoundTripper {
				return directIngressRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if port != []int{443, 2053, 2083, 2087, 2096, 8443}[calls-1] || r.URL.Port() != strconv.Itoa(port) {
						t.Fatal("incorrect direct ingress port sequence")
					}
					if ip != "1.0.0.1" || r.URL.Scheme != "https" || r.URL.Hostname() != "www.cloudflare.com" || r.URL.Path != "/cdn-cgi/trace" || !r.Close {
						t.Fatalf("unexpected request: %v", r)
					}
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) > 6*time.Second {
						t.Fatal("missing per request deadline")
					}
					return &http.Response{StatusCode: tt.status, Header: http.Header{"Location": []string{"https://example.invalid"}}, Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
				})
			})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if calls != 6 {
					t.Fatalf("calls=%d", calls)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatal("sensitive trace leaked")
			}
		})
	}
}

func TestIPv4DirectIngressDifferentEgress(t *testing.T) {
	calls := 0
	err := verifyIPv4DirectIngressWithTransport(context.Background(), "1.0.0.1", func(_ string, _ int) http.RoundTripper {
		return directIngressRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			ip := "1.1.1.1"
			if calls == 2 {
				ip = "8.8.8.8"
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ip=" + ip + "\nloc=CN\nwarp=off\n")), Request: r}, nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("err=%v", err)
	}
}

func TestIPv4DirectIngressTransportAndErrors(t *testing.T) {
	var target string
	transport := newIPv4DirectIngressTransport("1.0.0.1", 2053, func(ctx context.Context, network, address string) (net.Conn, error) {
		target = address
		return nil, errors.New("private-network-detail")
	})
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.TLSClientConfig.ServerName != "www.cloudflare.com" || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe transport")
	}
	_, _ = transport.DialContext(context.Background(), "tcp", "untrusted.example:443")
	if target != "1.0.0.1:2053" {
		t.Fatalf("target=%s", target)
	}
	err := verifyIPv4DirectIngressWithTransport(context.Background(), "1.0.0.1", func(_ string, _ int) http.RoundTripper { return transport })
	if err == nil || strings.Contains(err.Error(), "private-network-detail") {
		t.Fatalf("err=%v", err)
	}
	for _, ip := range []string{"garbage", "::1", "127.0.0.1"} {
		err = verifyIPv4DirectIngressWithTransport(context.Background(), ip, func(_ string, _ int) http.RoundTripper { t.Fatal("invalid candidate dialed"); return nil })
		if err == nil {
			t.Fatalf("accepted %s", ip)
		}
	}
}
