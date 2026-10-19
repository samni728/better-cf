package main

import (
	"encoding/json"
	"testing"
)

func gateSettings(t *testing.T, raw string) Settings {
	t.Helper()
	var settings Settings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestRequiredHTTPSSettings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    string
		wantErr bool
	}{
		{"legacy disabled", `{}`, false},
		{"ipv4 requirement with family disabled", `{"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, true},
		{"ipv6 requirement with family disabled", `{"true_connection_https":true,"true_connection_required_https_ports_ipv6":[2087]}`, true},
		{"https disabled", `{"true_connection_ipv4":true,"true_connection_required_https_ports_ipv4":[8443]}`, true},
		{"not selected", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_https_ports":[443],"true_connection_required_https_ports_ipv4":[8443]}`, true},
		{"none selected", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_https_ports":[],"true_connection_required_https_ports_ipv4":[8443]}`, true},
		{"unsupported", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[12345]}`, true},
		{"duplicate", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443,8443]}`, true},
		{"default selection", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, false},
		{"independent requirements", `{"true_connection_ipv4":true,"true_connection_ipv6":true,"true_connection_https":true,"true_connection_https_ports":[8443,2087],"true_connection_required_https_ports_ipv4":[8443],"true_connection_required_https_ports_ipv6":[2087]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequiredHTTPSSettings(gateSettings(t, tc.json))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestResultConnectionGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings string
		result   IPTestResult
		wantErr  bool
	}{
		{"legacy disabled", `{}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4}, false},
		{"required despite disabled", `{"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4}, true},
		{"not tested", `{"true_connection_ipv4":true,"true_connection_https":true}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4}, true},
		{"tested but empty", `{"true_connection_ipv4":true,"true_connection_https":true}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true}, true},
		{"legacy any success", `{"true_connection_ipv4":true,"true_connection_http":true}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "http", Port: 80}}}, false},
		{"disabled scheme cannot pass", `{"true_connection_ipv4":true,"true_connection_https":true}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "http", Port: 80}}}, true},
		{"unselected port cannot pass", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_https_ports":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 443}}}, true},
		{"wrong https port", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 443}}}, true},
		{"one of two is insufficient", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[443,8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 443}}}, true},
		{"all required succeed", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[443,8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 443}, {Scheme: "https", Port: 8443}}}, false},
		{"production uppercase scheme", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTPS", Port: 8443}}}, false},
		{"http cannot satisfy required https", `{"true_connection_ipv4":true,"true_connection_http":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTP", Port: 80}}}, true},
		{"ipv6 independent", `{"true_connection_ipv4":true,"true_connection_ipv6":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443],"true_connection_required_https_ports_ipv6":[2087]}`, IPTestResult{IP: "2606:4700::1", IPVersion: 6, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 2087}}}, false},
		{"family spoof", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "104.16.1.1", IPVersion: 6}, true},
		{"mapped ipv4 cannot spoof ipv6", `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`, IPTestResult{IP: "::ffff:104.16.1.1", IPVersion: 6}, true},
		{"unknown family", `{}`, IPTestResult{IP: "104.16.1.1", IPVersion: 0}, true},
		{"invalid address", `{}`, IPTestResult{IP: "not-an-ip", IPVersion: 4}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResultTrueConnection(gateSettings(t, tc.settings), tc.result)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestDNSConnectionGateChecksEveryAddress(t *testing.T) {
	settings := gateSettings(t, `{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_required_https_ports_ipv4":[8443]}`)
	good := IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 8443}}}
	bad := IPTestResult{IP: "104.16.1.2", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "https", Port: 443}}}
	if err := validateDNSConnectionResults(settings, []IPTestResult{good}); err != nil {
		t.Fatal(err)
	}
	if err := validateDNSConnectionResults(settings, []IPTestResult{good, bad}); err == nil {
		t.Fatal("must reject the entire batch when one address lacks required HTTPS success")
	}
	settings.TrueConnectionHTTPS = false
	if err := validateDNSConnectionResults(settings, nil); err == nil {
		t.Fatal("invalid settings must fail even with no results")
	}
}
