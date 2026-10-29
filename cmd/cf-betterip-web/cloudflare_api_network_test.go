package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudflareAPIRejectsFakeIP(t *testing.T) {
	for _, raw := range []string{"198.18.225.145", "198.19.0.1", "192.168.10.1", "127.0.0.1"} {
		if validCloudflareAPIAddress(netip.MustParseAddr(raw)) {
			t.Errorf("accepted fake/private address %s", raw)
		}
	}
	if !validCloudflareAPIAddress(netip.MustParseAddr("104.19.192.29")) {
		t.Fatal("rejected public API address")
	}
}

func TestCloudflareAPIReadRetriesAndDoesNotRetryWrites(t *testing.T) {
	var getCount atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		getCount.Add(1)
		if getCount.Load() == 1 {
			return nil, errors.New("EOF")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"success\":true,\"result\":{}}"))}, nil
	})}
	cred := CloudflareCredentialConfig{AuthType: "api_token", APIToken: "example"}
	if err := cloudflareRequest(client, http.MethodGet, "https://example.invalid/zone", cred, nil, nil); err != nil {
		t.Fatal(err)
	}
	if getCount.Load() != 2 {
		t.Fatalf("expected 2 GET attempts, got %d", getCount.Load())
	}
	var postCount atomic.Int32
	writeClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		postCount.Add(1)
		return nil, errors.New("connection reset")
	})}
	if err := cloudflareRequest(writeClient, http.MethodPost, "https://example.invalid/zone", cred, map[string]string{"name": "test"}, nil); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if postCount.Load() != 1 {
		t.Fatalf("write retried %d times", postCount.Load())
	}
}

func TestCloudflareAPIDirectLive(t *testing.T) {
	path := os.Getenv("CF_BETTERIP_API_LIVE_SETTINGS")
	if path == "" {
		t.Skip("live settings not supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state AppState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	for _, target := range state.Settings.DNSTargets {
		if !target.Enabled || target.ZoneID == "" {
			continue
		}
		for _, cred := range state.Settings.CloudflareCredentials {
			if cred.ID != target.CredentialID {
				continue
			}
			name, err := getCloudflareZoneName(&http.Client{Timeout: 30 * time.Second}, target.ZoneID, cred)
			if err != nil {
				t.Fatal(err)
			}
			if name != target.RootDomain {
				t.Fatalf("wrong zone name %q", name)
			}
			t.Log("API_ZONE_AUTH_OK", name)
			return
		}
	}
	t.Fatal("no configured target and credential found")
}
