package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"cf-betterip-ser/internal/searchmemory"
)

func requiredPortSettings(t *testing.T) Settings {
	t.Helper()
	var settings Settings
	err := json.Unmarshal([]byte(`{"ipv4_enabled":true,"ipv6_enabled":true,"ipv4_count":1,"ipv6_count":1,"true_connection_ipv4":true,"true_connection_ipv6":true,"true_connection_https":true,"true_connection_https_ports":[2087,8443],"true_connection_required_https_ports_ipv4":[8443],"true_connection_required_https_ports_ipv6":[2087],"true_connection_https_node":"vless://00000000-0000-4000-8000-000000000001@edge.example.com:8443?security=tls&type=ws&host=edge.example.com&sni=edge.example.com&path=%2Fvideo","true_connection_test_url":"https://www.google.com/generate_204"}`), &settings)
	if err != nil {
		t.Fatal(err)
	}
	settings.CloudflareCredentials = []CloudflareCredentialConfig{{ID: "credential", Name: "test", AuthType: "api_token", APIToken: "test-token"}}
	settings.DNSTargets = []DNSTargetConfig{{ID: "both", Name: "both", RootDomain: "example.com", RecordName: "speed.example.com", ZoneID: "zone", CredentialID: "credential", RecordFamily: "both", Enabled: true}}
	return settings
}

func TestDNSRejectsIPv4WhenOnlyOtherPortPassed(t *testing.T) {
	settings := requiredPortSettings(t)
	results := []IPTestResult{{IP: "192.0.2.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTPS", Port: 2087}}}}
	if _, err := buildDNSSyncTargets(settings, results); err == nil {
		t.Fatal("DNS admitted IPv4 whose required 8443 port never passed")
	}
}

func TestRequiredPortsCannotSilentlyDisableVerification(t *testing.T) {
	settings := requiredPortSettings(t)
	settings.TrueConnectionIPv4 = false
	if err := validateTrueConnectionSettings(settings); err == nil {
		t.Fatal("required IPv4 port accepted with IPv4 verification disabled")
	}
}

func TestDNSRequiresEveryPublishedIPToPassFamilyPort(t *testing.T) {
	settings := requiredPortSettings(t)
	results := []IPTestResult{
		{IP: "192.0.2.1", IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTPS", Port: 8443}}},
		{IP: "2001:db8::1", IPVersion: 6, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTPS", Port: 2087}}},
	}
	if _, err := buildDNSSyncTargets(settings, results); err != nil {
		t.Fatalf("valid dual-stack results rejected: %v", err)
	}
	results = append(results, IPTestResult{IP: "192.0.2.2", IPVersion: 4})
	if _, err := buildDNSSyncTargets(settings, results); err == nil {
		t.Fatal("one unverified IP was admitted alongside verified addresses")
	}
}

func TestRequiredPortFormRoundTrip(t *testing.T) {
	settings := requiredPortSettings(t)
	form := url.Values{"true_connection_port_selection_ui": {"1"}, "true_connection_required_ports_ui": {"1"}, "true_connection_https_ports": {"2087", "8443"}, "true_connection_required_https_ports_ipv4": {"2087"}, "true_connection_required_https_ports_ipv6": {"8443"}}
	if err := applyTrueConnectionPortSelectionForm(&settings, form); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(settings)
	if !strings.Contains(string(data), `"true_connection_required_https_ports_ipv4":[2087]`) || !strings.Contains(string(data), `"true_connection_required_https_ports_ipv6":[8443]`) {
		t.Fatal("form did not persist separate family requirements")
	}
}

func TestResumeRejectsPortPolicyChangedSinceSourceRun(t *testing.T) {
	current := requiredPortSettings(t)
	old := cloneSettings(current)
	old.TrueConnectionRequiredHTTPSPortsIPv4 = nil
	if err := validateResumeConnectionPolicy(old, current); err == nil {
		t.Fatal("old frozen run can bypass new IPv4 required port policy")
	}
	if err := validateResumeConnectionPolicy(current, current); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredPortsAreFrozenAndIsolateSearchMemory(t *testing.T) {
	settings := requiredPortSettings(t)
	snapshot := sanitizedRunSettings(settings)
	settings.TrueConnectionRequiredHTTPSPortsIPv4[0] = 2087
	if snapshot.TrueConnectionRequiredHTTPSPortsIPv4[0] != 8443 {
		t.Fatal("mutating settings changed frozen task port policy")
	}
	a, _ := searchmemory.ProfileID(searchProfileForSettings(snapshot, 4))
	b, _ := searchmemory.ProfileID(searchProfileForSettings(settings, 4))
	if a == b {
		t.Fatal("different required ports share success/cooldown memory")
	}
}

// Opt-in integration check against the same Xray core used by the service.
func TestLiveRequiredHTTPSAdmission(t *testing.T) {
	link := strings.TrimSpace(os.Getenv("TRUE_CONNECTION_LIVE_NODE"))
	ip := strings.TrimSpace(os.Getenv("TRUE_CONNECTION_LIVE_IP"))
	if link == "" || ip == "" || os.Getenv("XRAY_BIN") == "" {
		t.Skip("set TRUE_CONNECTION_LIVE_NODE, TRUE_CONNECTION_LIVE_IP and XRAY_BIN")
	}
	address, err := netip.ParseAddr(ip)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(os.Getenv("TRUE_CONNECTION_LIVE_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultSettings()
	settings.TrueConnectionHTTP = false
	settings.TrueConnectionHTTPS = true
	settings.TrueConnectionHTTPSNode = link
	settings.TrueConnectionHTTPSPorts = []int{port}
	family := 6
	if address.Unmap().Is4() {
		family = 4
		settings.TrueConnectionIPv4 = true
		settings.TrueConnectionRequiredHTTPSPortsIPv4 = []int{port}
	} else {
		settings.TrueConnectionIPv6 = true
		settings.TrueConnectionRequiredHTTPSPortsIPv6 = []int{port}
	}
	if err := validateTrueConnectionSettings(settings); err != nil {
		t.Fatal(err)
	}
	ports, attempts, err := runTrueConnectionTests(context.Background(), settings, ip)
	if err != nil {
		t.Fatal(err)
	}
	result := IPTestResult{IP: ip, IPVersion: family, TrueConnectionTested: true, TrueConnectionPorts: ports}
	if err := validateDNSConnectionResults(settings, []IPTestResult{result}); err != nil {
		t.Fatalf("required-port admission failed: %v; attempts=%s", err, formatTrueConnectionAttempts(attempts))
	}
	t.Logf("IPv%d %s required HTTPS %d passed via Xray: %s", family, ip, port, formatTrueConnectionPorts(ports))
}

func TestResumeRejectsChangedVerificationSwitchOrSelectedPorts(t *testing.T) {
	for _, change := range []string{"family switch", "HTTPS selected ports", "HTTPS protocol"} {
		t.Run(change, func(t *testing.T) {
			old := requiredPortSettings(t)
			old.TrueConnectionRequiredHTTPSPortsIPv4 = nil
			old.TrueConnectionRequiredHTTPSPortsIPv6 = nil
			current := cloneSettings(old)
			switch change {
			case "family switch":
				old.TrueConnectionIPv4 = false
			case "HTTPS selected ports":
				current.TrueConnectionHTTPSPorts = []int{8443}
			case "HTTPS protocol":
				old.TrueConnectionHTTPS = false
			}
			if err := validateResumeConnectionPolicy(old, current); err == nil {
				t.Fatal("resume accepted changed verification policy")
			}
		})
	}
}

func TestSettingsPagePreservesFamilyRequiredPortSelections(t *testing.T) {
	settings := requiredPortSettings(t)
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, settingsTemplate, app.pageData("配置", "admin", settings))
	html := recorder.Body.String()
	for _, input := range []string{`name="true_connection_required_https_ports_ipv4" value="8443" checked`, `name="true_connection_required_https_ports_ipv6" value="2087" checked`, `name="true_connection_required_https_ports_ipv4" value="2087" >`} {
		if !strings.Contains(html, input) {
			t.Fatalf("rendered settings lost required-port selection: %s", input)
		}
	}
	oldForm := url.Values{"true_connection_port_selection_ui": {"1"}, "true_connection_https_ports": {"2087", "8443"}}
	if err := applyTrueConnectionPortSelectionForm(&settings, oldForm); err != nil {
		t.Fatal(err)
	}
	if len(settings.TrueConnectionRequiredHTTPSPortsIPv4) != 1 || settings.TrueConnectionRequiredHTTPSPortsIPv4[0] != 8443 {
		t.Fatal("stale form silently cleared new required policy")
	}
}

func TestResumeEndpointBlocksOldUnverifiedTaskBeforeCreatingRun(t *testing.T) {
	settings := requiredPortSettings(t)
	settings.TrueConnectionRequiredHTTPSPortsIPv4 = nil
	settings.TrueConnectionRequiredHTTPSPortsIPv6 = nil
	old := sanitizedRunSettings(settings)
	old.TrueConnectionIPv4 = false
	store, err := NewStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	store.state.Settings = settings
	store.state.Runs = []RunRecord{{ID: "old", Status: "failed", ConfigSnapshot: &old}}
	store.state.Results = []IPTestResult{{RunID: "old", IP: "192.0.2.1", IPVersion: 4, SelectedForDNS: true}}
	app := &App{store: store, tasks: &TaskManager{cancels: make(map[string]context.CancelFunc)}}
	recorder := httptest.NewRecorder()
	app.resumeRun(recorder, httptest.NewRequest(http.MethodPost, "/runs/resume", nil))
	if len(store.snapshot().Runs) != 1 || !strings.Contains(recorder.Body.String(), "真连接开关与原任务不同") {
		t.Fatal("resume did not block old unverified policy before creating a task")
	}
}
