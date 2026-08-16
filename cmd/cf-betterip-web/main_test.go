package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestParseBetterIPOutputIncludesLocation(t *testing.T) {
	output := `
优选 IP: 104.16.1.1
实测带宽: 120 Mbps
峰值速度: 15360 kB/s
往返延迟: 15 毫秒
数据中心: Tokyo / JP
数据中心代码: NRT
数据中心国家: JP
数据中心区域: Asia Pacific
数据中心城市: Tokyo
总计用时: 8 秒
`
	result, err := parseBetterIPOutput(output)
	if err != nil {
		t.Fatalf("parseBetterIPOutput returned error: %v", err)
	}
	if result.IP != "104.16.1.1" || result.DataCenterCode != "NRT" || result.DataCenterCountry != "JP" || result.DataCenterCity != "Tokyo" {
		t.Fatalf("unexpected parsed result: %+v", result)
	}
}

func TestBuildGeoChoicesCascadesByCountry(t *testing.T) {
	locations := []GeoLocation{
		{IATA: "NRT", Country: "JP", Region: "Asia Pacific", City: "Tokyo"},
		{IATA: "KIX", Country: "JP", Region: "Asia Pacific", City: "Osaka"},
		{IATA: "LAX", Country: "US", Region: "North America", City: "Los Angeles"},
	}
	_, regions, cities := buildGeoChoices(locations, Settings{LocationCountry: "JP"})
	if len(regions) != 1 || regions[0].Value != "Asia Pacific" {
		t.Fatalf("unexpected regions: %+v", regions)
	}
	if len(cities) != 2 || cities[0].Value != "Osaka" || cities[1].Value != "Tokyo" {
		t.Fatalf("unexpected cities: %+v", cities)
	}
}

func TestCalculateGeoFilterStats(t *testing.T) {
	locations := []GeoLocation{
		{IATA: "NRT", Country: "JP", Region: "Asia Pacific", City: "Tokyo"},
		{IATA: "KIX", Country: "JP", Region: "Asia Pacific", City: "Osaka"},
		{IATA: "LAX", Country: "US", Region: "North America", City: "Los Angeles"},
	}
	stats := calculateGeoFilterStats(locations, Settings{
		LocationCountry: "JP",
	})
	if stats.DataCenterCount != 2 || stats.Codes != "KIX / NRT" {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestLocationSummaryExplainsIgnoredAndStrictSelections(t *testing.T) {
	settings := Settings{
		LocationMode:    "any",
		LocationCountry: "CN",
		LocationRegion:  "CN-GD",
		LocationCity:    "Guangzhou",
	}
	if got := locationFilterSummary(settings); !strings.Contains(got, "当前不参与筛选") {
		t.Fatalf("global summary is ambiguous: %q", got)
	}
	settings.LocationMode = "strict"
	stats := GeoFilterStats{DataCenterCount: 2, Codes: "KIX / NRT"}
	got := locationFilterSummaryWithStats(settings, stats)
	if !strings.Contains(got, "目标机房 2 个") || !strings.Contains(got, "KIX / NRT") || !strings.Contains(got, "CF-RAY") || !strings.Contains(got, "不回退全局") {
		t.Fatalf("strict summary is incomplete: %q", got)
	}
}

func TestRunSummaryFreezesStrictLocationAndCounts(t *testing.T) {
	settings := defaultSettings()
	settings.LocationMode = "strict"
	settings.LocationCountry = "JP"
	summary := runSummary("manual", settings, GeoFilterStats{DataCenterCount: 2, Codes: "KIX / NRT"})
	for _, required := range []string{"立即执行", "严格地区", "JP", "目标机房 2 个", "KIX / NRT", "CF-RAY", "不回退全局"} {
		if !strings.Contains(summary, required) {
			t.Fatalf("run summary %q does not contain %q", summary, required)
		}
	}
}

func TestRegionalHintSubnetsUseMatchingHistoricalResults(t *testing.T) {
	app := &App{store: &Store{state: AppState{Results: []IPTestResult{
		{IP: "162.159.39.76", IPVersion: 4, DataCenterCountry: "JP", DataCenterCity: "Tokyo"},
		{IP: "162.159.39.195", IPVersion: 4, DataCenterCountry: "JP", DataCenterCity: "Tokyo"},
		{IP: "104.16.0.1", IPVersion: 4, DataCenterCountry: "US", DataCenterCity: "Los Angeles"},
		{IP: "2606:4700:52::1", IPVersion: 6, DataCenterCountry: "JP", DataCenterCity: "Tokyo"},
	}}}}
	settings := Settings{LocationMode: "strict", LocationCountry: "JP"}
	got := app.regionalHintSubnets(settings, 4)
	if len(got) != 1 || got[0] != "162.159.39.0/24" {
		t.Fatalf("regionalHintSubnets() = %v", got)
	}
	history := app.regionalHistoryIPs(settings, 4, map[string]bool{"162.159.39.76": true})
	if len(history) != 1 || history[0] != "162.159.39.195" {
		t.Fatalf("regionalHistoryIPs() = %v", history)
	}
}

func TestScheduledRunSummaryUsesScheduledTrigger(t *testing.T) {
	settings := defaultSettings()
	if got := runSummary("scheduled", settings, GeoFilterStats{}); !strings.Contains(got, "定时执行") {
		t.Fatalf("scheduled summary = %q", got)
	}
}

func TestFamilyTimeoutDoesNotAbortOtherFamily(t *testing.T) {
	ctx := context.Background()
	if shouldAbortWholeRun(ctx, errors.New("IPv4 连续 30 分钟无新增结果")) {
		t.Fatal("a family-level failure must not abort the other IP family")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !shouldAbortWholeRun(canceled, context.Canceled) {
		t.Fatal("a canceled whole-run context must abort immediately")
	}
}

func TestValidateGeoFeed(t *testing.T) {
	var data strings.Builder
	for i := 0; i < 1000; i++ {
		data.WriteString("104.28.37.44/32,CN,CN-SC,Chengdu,\n")
	}
	count, err := validateGeoFeed([]byte(data.String()))
	if err != nil || count != 1000 {
		t.Fatalf("validateGeoFeed() = %d, %v", count, err)
	}
}

func TestSettingsTemplateParses(t *testing.T) {
	if _, err := template.New("layout").Parse(layoutTemplate + runsTemplate + resultTemplate + settingsTemplate); err != nil {
		t.Fatalf("settings template did not parse: %v", err)
	}
}

func TestWorkspaceTemplatesParse(t *testing.T) {
	pages := map[string]string{
		"dashboard": dashboardTemplate,
		"run":       runTemplate,
		"history":   historyTemplate,
		"detail":    runDetailTemplate,
		"results":   resultsPageTemplate,
	}
	for name, page := range pages {
		if _, err := template.New("layout").Parse(layoutTemplate + runsTemplate + resultTemplate + page); err != nil {
			t.Fatalf("%s template did not parse: %v", name, err)
		}
	}
}

func TestResultHintPrefixesBuildNarrowThenParent(t *testing.T) {
	if got := resultHintPrefixes("172.66.130.219", 4); !reflect.DeepEqual(got, []string{"172.66.130.0/24", "172.66.0.0/16"}) {
		t.Fatalf("IPv4 resultHintPrefixes = %v", got)
	}
	if got := resultHintPrefixes("2606:4700:1234:5678::1", 6); !reflect.DeepEqual(got, []string{"2606:4700:1234::/48", "2606:4700::/32"}) {
		t.Fatalf("IPv6 resultHintPrefixes = %v", got)
	}
}

func TestCandidateSourceForIP(t *testing.T) {
	exact := []string{"172.66.130.10"}
	hints := []string{"172.66.130.0/24", "104.18.0.0/16"}
	for ip, want := range map[string]string{
		"172.66.130.10": "exact",
		"172.66.130.20": "narrow",
		"104.18.22.10":  "wide",
		"198.51.100.1":  "global",
	} {
		if got := candidateSourceForIP(ip, exact, hints, 4); got != want {
			t.Fatalf("candidateSourceForIP(%s) = %s, want %s", ip, got, want)
		}
	}
}

func TestVersionAndRepositoryAreExposed(t *testing.T) {
	if appVersion != "v1.1.0" || repositoryURL != "https://github.com/samni728/better-cf" {
		t.Fatalf("version metadata = %s / %s", appVersion, repositoryURL)
	}
	if defaultSettings().SearchNetworkLabel != "213 VPS" {
		t.Fatal("default search network label missing")
	}
}

func TestDefaultSettingsIncludesMaxRTT(t *testing.T) {
	settings := defaultSettings()
	if settings.MaxRTTMs != 200 {
		t.Fatalf("default MaxRTTMs = %d, want 200", settings.MaxRTTMs)
	}
}

func TestApplyDefaultsMigratesMaxRTT(t *testing.T) {
	store := &Store{state: AppState{Settings: Settings{}}}
	store.applyDefaults()
	if store.state.Settings.MaxRTTMs != 200 {
		t.Fatalf("migrated MaxRTTMs = %d, want 200", store.state.Settings.MaxRTTMs)
	}
}

func TestApplyDefaultsMigratesLegacySingleDNSConfig(t *testing.T) {
	store := &Store{state: AppState{Settings: Settings{
		CloudflareAPIToken:  "legacy-token",
		CloudflareAccountID: "legacy-account",
		CloudflareZoneID:    "legacy-zone",
		RecordName:          "speed.example.com",
		DNSTargetMode:       "single",
	}}}

	store.applyDefaults()
	settings := store.state.Settings
	if settings.DNSConfigVersion != 2 {
		t.Fatalf("DNSConfigVersion = %d, want 2", settings.DNSConfigVersion)
	}
	if len(settings.CloudflareCredentials) != 1 {
		t.Fatalf("credentials = %+v, want one migrated credential", settings.CloudflareCredentials)
	}
	credential := settings.CloudflareCredentials[0]
	if credential.ID != "credential-legacy-shared" || credential.APIToken != "legacy-token" || credential.AccountID != "legacy-account" {
		t.Fatalf("unexpected migrated credential: %+v", credential)
	}
	if len(settings.DNSTargets) != 1 {
		t.Fatalf("targets = %+v, want one migrated target", settings.DNSTargets)
	}
	target := settings.DNSTargets[0]
	if target.ID != "target-legacy-single" || target.RecordName != "speed.example.com" || target.RootDomain != "" || target.ZoneID != "legacy-zone" || target.RecordFamily != "both" || target.CredentialID != credential.ID || !target.Enabled {
		t.Fatalf("unexpected migrated target: %+v", target)
	}
}

func TestApplyDefaultsMigratesLegacySplitDNSConfig(t *testing.T) {
	store := &Store{state: AppState{Settings: Settings{
		CloudflareAPIToken:  "shared-token",
		CloudflareAccountID: "shared-account",
		CloudflareZoneID:    "shared-zone",
		DNSTargetMode:       "split",
		IPv4Target: TargetConfig{
			RecordName:     "speedv4.example.com",
			CredentialMode: "shared",
		},
		IPv6Target: TargetConfig{
			RecordName:          "speedv6.other.test",
			CredentialMode:      "custom",
			CloudflareAPIToken:  "ipv6-token",
			CloudflareAccountID: "ipv6-account",
			CloudflareZoneID:    "ipv6-zone",
		},
	}}}

	store.applyDefaults()
	settings := store.state.Settings
	if len(settings.CloudflareCredentials) != 2 {
		t.Fatalf("credentials = %+v, want shared and IPv6 credentials", settings.CloudflareCredentials)
	}
	credentials := make(map[string]CloudflareCredentialConfig)
	for _, credential := range settings.CloudflareCredentials {
		credentials[credential.ID] = credential
	}
	if got := credentials["credential-legacy-shared"].APIToken; got != "shared-token" {
		t.Fatalf("shared token = %q, want preserved", got)
	}
	if got := credentials["credential-legacy-ipv6"].APIToken; got != "ipv6-token" {
		t.Fatalf("IPv6 token = %q, want preserved", got)
	}
	if len(settings.DNSTargets) != 2 {
		t.Fatalf("targets = %+v, want IPv4 and IPv6 targets", settings.DNSTargets)
	}
	targets := make(map[string]DNSTargetConfig)
	for _, target := range settings.DNSTargets {
		targets[target.ID] = target
	}
	ipv4 := targets["target-legacy-ipv4"]
	if ipv4.RecordFamily != "ipv4" || ipv4.RootDomain != "" || ipv4.ZoneID != "shared-zone" || ipv4.CredentialID != "credential-legacy-shared" {
		t.Fatalf("unexpected migrated IPv4 target: %+v", ipv4)
	}
	ipv6 := targets["target-legacy-ipv6"]
	if ipv6.RecordFamily != "ipv6" || ipv6.RootDomain != "" || ipv6.ZoneID != "ipv6-zone" || ipv6.CredentialID != "credential-legacy-ipv6" {
		t.Fatalf("unexpected migrated IPv6 target: %+v", ipv6)
	}
}

func TestApplyDefaultsDoesNotRemigrateVersion2DNSConfig(t *testing.T) {
	existingCredential := CloudflareCredentialConfig{ID: "credential-current", Name: "Current", AuthType: "api_token", APIToken: "current-token"}
	existingTarget := DNSTargetConfig{ID: "target-current", Name: "Current target", RootDomain: "example.com", ZoneID: "current-zone", RecordName: "current.example.com", RecordFamily: "ipv4", CredentialID: existingCredential.ID, Enabled: true}
	store := &Store{state: AppState{Settings: Settings{
		DNSConfigVersion:      2,
		CloudflareCredentials: []CloudflareCredentialConfig{existingCredential},
		DNSTargets:            []DNSTargetConfig{existingTarget},
		CloudflareAPIToken:    "stale-legacy-token",
		CloudflareZoneID:      "stale-legacy-zone",
		RecordName:            "stale.example.net",
	}}}

	store.applyDefaults()
	settings := store.state.Settings
	if len(settings.CloudflareCredentials) != 1 || settings.CloudflareCredentials[0].ID != existingCredential.ID || settings.CloudflareCredentials[0].APIToken != existingCredential.APIToken {
		t.Fatalf("version 2 credentials were remigrated: %+v", settings.CloudflareCredentials)
	}
	if len(settings.DNSTargets) != 1 || settings.DNSTargets[0].ID != existingTarget.ID || settings.DNSTargets[0].RecordName != existingTarget.RecordName {
		t.Fatalf("version 2 targets were remigrated: %+v", settings.DNSTargets)
	}
	if settings.CloudflareAPIToken != "" || settings.CloudflareZoneID != "" || settings.RecordName != "" || settings.DNSTargetMode != "" || settings.IPv4Target.CloudflareAPIToken != "" || settings.IPv6Target.CloudflareAPIToken != "" {
		t.Fatalf("stale legacy DNS fields were not cleared: %+v", settings)
	}
}

func TestNewStorePersistsVersion2LegacyFieldCleanup(t *testing.T) {
	path := t.TempDir() + "/app_state.json"
	state := AppState{Settings: Settings{
		DNSConfigVersion:      2,
		CloudflareCredentials: []CloudflareCredentialConfig{{ID: "credential-current", Name: "Current", AuthType: "api_token", APIToken: "current-token"}},
		DNSTargets:            []DNSTargetConfig{{ID: "target-current", Name: "Current", RootDomain: "example.com", ZoneID: "zone", RecordName: "speed.example.com", RecordFamily: "ipv4", CredentialID: "credential-current", Enabled: true}},
		CloudflareAPIToken:    "stale-legacy-token",
		CloudflareZoneID:      "stale-zone",
		RecordName:            "stale.example.net",
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err != nil {
		t.Fatal(err)
	}
	persistedData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted AppState
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Settings.CloudflareAPIToken != "" || persisted.Settings.CloudflareZoneID != "" || persisted.Settings.RecordName != "" {
		t.Fatalf("legacy fields were only cleared in memory: %+v", persisted.Settings)
	}
}

func TestParseDNSConfigFormPreservesSecretsByStableID(t *testing.T) {
	previous := Settings{
		DNSConfigVersion: 2,
		CloudflareCredentials: []CloudflareCredentialConfig{{
			ID: "credential-main", Name: "Old name", AuthType: "global_api_key",
			APIToken: "saved-token", APIKey: "saved-global-key", Email: "old@example.com",
		}},
	}
	form := url.Values{
		"credential_id":                         {"credential-main"},
		"credential_name_credential-main":       {"Renamed"},
		"credential_auth_type_credential-main":  {"api_token"},
		"credential_api_token_credential-main":  {""},
		"credential_api_key_credential-main":    {"  "},
		"credential_email_credential-main":      {"new@example.com"},
		"credential_account_id_credential-main": {"account-2"},
	}

	next, err := parseDNSConfigForm(form, previous)
	if err != nil {
		t.Fatalf("parseDNSConfigForm() error = %v", err)
	}
	if len(next.CloudflareCredentials) != 1 {
		t.Fatalf("credentials = %+v, want one", next.CloudflareCredentials)
	}
	credential := next.CloudflareCredentials[0]
	if credential.ID != "credential-main" || credential.APIToken != "saved-token" || credential.APIKey != "saved-global-key" {
		t.Fatalf("stable ID did not preserve empty submitted secrets: %+v", credential)
	}
	if credential.Name != "Renamed" || credential.AuthType != "api_token" || credential.Email != "new@example.com" || credential.AccountID != "account-2" {
		t.Fatalf("non-secret fields were not updated: %+v", credential)
	}
}

func TestValidateDNSConfigRejectsInvalidBindingsAndCredentials(t *testing.T) {
	validCredential := CloudflareCredentialConfig{ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "token"}
	validTarget := DNSTargetConfig{ID: "target-main", Name: "Main target", RootDomain: "example.com", ZoneID: "zone-1", RecordName: "speed.example.com", RecordFamily: "both", CredentialID: validCredential.ID, Enabled: true}

	tests := []struct {
		name     string
		settings Settings
		want     string
	}{
		{
			name: "overlapping A binding",
			settings: Settings{CloudflareCredentials: []CloudflareCredentialConfig{validCredential}, DNSTargets: []DNSTargetConfig{
				validTarget,
				{ID: "target-overlap", Name: "Overlap", RootDomain: "example.com", ZoneID: "zone-1", RecordName: "speed.example.com", RecordFamily: "ipv4", CredentialID: validCredential.ID, Enabled: true},
			}},
			want: "重复操作同一个 A 记录",
		},
		{
			name: "record outside root domain",
			settings: Settings{CloudflareCredentials: []CloudflareCredentialConfig{validCredential}, DNSTargets: []DNSTargetConfig{
				{ID: "target-outside", Name: "Outside", RootDomain: "example.com", ZoneID: "zone-1", RecordName: "speed.example.net", RecordFamily: "ipv4", CredentialID: validCredential.ID, Enabled: true},
			}},
			want: "不属于根域名",
		},
		{
			name: "missing credential reference",
			settings: Settings{CloudflareCredentials: []CloudflareCredentialConfig{validCredential}, DNSTargets: []DNSTargetConfig{
				{ID: "target-missing", Name: "Missing", RootDomain: "example.com", ZoneID: "zone-1", RecordName: "speed.example.com", RecordFamily: "ipv4", CredentialID: "credential-unknown", Enabled: true},
			}},
			want: "引用的 Cloudflare 凭据不存在",
		},
		{
			name: "API token secret missing",
			settings: Settings{CloudflareCredentials: []CloudflareCredentialConfig{{ID: "credential-empty", Name: "Empty", AuthType: "api_token"}}, DNSTargets: []DNSTargetConfig{
				{ID: "target-empty", Name: "Empty secret", RootDomain: "example.com", ZoneID: "zone-1", RecordName: "speed.example.com", RecordFamily: "ipv4", CredentialID: "credential-empty", Enabled: true},
			}},
			want: "缺少 API Token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDNSConfig(tt.settings)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateDNSConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildDNSSyncTargetsFansOutRecordFamilies(t *testing.T) {
	credential := CloudflareCredentialConfig{ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "token"}
	settings := Settings{
		CloudflareCredentials: []CloudflareCredentialConfig{credential},
		DNSTargets: []DNSTargetConfig{
			{ID: "target-both", Name: "Both", RecordName: "both.example.com", RecordFamily: "both", ZoneID: "zone-1", CredentialID: credential.ID, Enabled: true},
			{ID: "target-v4", Name: "IPv4", RecordName: "v4.example.com", RecordFamily: "ipv4", ZoneID: "zone-1", CredentialID: credential.ID, Enabled: true},
			{ID: "target-v6", Name: "IPv6", RecordName: "v6.example.com", RecordFamily: "ipv6", ZoneID: "zone-1", CredentialID: credential.ID, Enabled: true},
			{ID: "target-disabled", Name: "Disabled", RecordName: "off.example.com", RecordFamily: "both", ZoneID: "zone-1", CredentialID: credential.ID},
		},
	}
	results := []IPTestResult{
		{IP: "192.0.2.10", IPVersion: 4},
		{IP: "192.0.2.11", IPVersion: 4},
		{IP: "2001:db8::10", IPVersion: 6},
	}

	targets, err := buildDNSSyncTargets(settings, results)
	if err != nil {
		t.Fatalf("buildDNSSyncTargets() error = %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("targets = %+v, want four fan-out bindings", targets)
	}
	got := make(map[string]DNSSyncTarget)
	for _, target := range targets {
		got[target.TargetID+"|"+target.RecordType] = target
	}
	assertIPs := func(key string, want ...string) {
		t.Helper()
		target, ok := got[key]
		if !ok {
			t.Fatalf("missing fan-out target %q in %+v", key, targets)
		}
		if strings.Join(target.IPs, ",") != strings.Join(want, ",") {
			t.Fatalf("%s IPs = %v, want %v", key, target.IPs, want)
		}
		if target.Credential.ID != credential.ID {
			t.Fatalf("%s credential = %+v, want %q", key, target.Credential, credential.ID)
		}
	}
	assertIPs("target-both|A", "192.0.2.10", "192.0.2.11")
	assertIPs("target-both|AAAA", "2001:db8::10")
	assertIPs("target-v4|A", "192.0.2.10", "192.0.2.11")
	assertIPs("target-v6|AAAA", "2001:db8::10")
}

func TestRequiredDNSRecordCountUsesActiveFamiliesAndBindings(t *testing.T) {
	settings := Settings{
		IPv4Enabled: true,
		IPv6Enabled: true,
		IPv4Count:   3,
		IPv6Count:   2,
		DNSTargets: []DNSTargetConfig{
			{RecordFamily: "both", Enabled: true},
			{RecordFamily: "ipv4", Enabled: true},
			{RecordFamily: "ipv6", Enabled: true},
			{RecordFamily: "both", Enabled: false},
		},
	}
	if got := requiredDNSRecordCount(settings); got != 10 {
		t.Fatalf("requiredDNSRecordCount() = %d, want 10", got)
	}
	settings.IPv6Enabled = false
	if got := requiredDNSRecordCount(settings); got != 6 {
		t.Fatalf("requiredDNSRecordCount() with IPv6 disabled = %d, want 6", got)
	}
}

func TestCloudflareRequestAuthenticationHeaders(t *testing.T) {
	tests := []struct {
		name       string
		credential CloudflareCredentialConfig
		assert     func(*testing.T, http.Header)
	}{
		{
			name:       "API token",
			credential: CloudflareCredentialConfig{AuthType: "api_token", APIToken: "token-secret"},
			assert: func(t *testing.T, header http.Header) {
				if got := header.Get("Authorization"); got != "Bearer token-secret" {
					t.Fatalf("Authorization = %q, want bearer token", got)
				}
				if header.Get("X-Auth-Email") != "" || header.Get("X-Auth-Key") != "" {
					t.Fatalf("API Token request leaked Global API Key headers: %+v", header)
				}
			},
		},
		{
			name:       "Global API key",
			credential: CloudflareCredentialConfig{AuthType: "global_api_key", Email: "owner@example.com", APIKey: "global-secret"},
			assert: func(t *testing.T, header http.Header) {
				if got := header.Get("X-Auth-Email"); got != "owner@example.com" {
					t.Fatalf("X-Auth-Email = %q", got)
				}
				if got := header.Get("X-Auth-Key"); got != "global-secret" {
					t.Fatalf("X-Auth-Key = %q", got)
				}
				if header.Get("Authorization") != "" {
					t.Fatalf("Global API Key request also sent Authorization: %q", header.Get("Authorization"))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestHeaders := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestHeaders <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":{"id":"ok"}}`))
			}))
			defer server.Close()

			var result struct {
				Result struct {
					ID string `json:"id"`
				} `json:"result"`
			}
			if err := cloudflareRequest(server.Client(), http.MethodGet, server.URL, tt.credential, nil, &result); err != nil {
				t.Fatalf("cloudflareRequest() error = %v", err)
			}
			if result.Result.ID != "ok" {
				t.Fatalf("decoded result ID = %q, want ok", result.Result.ID)
			}
			tt.assert(t, <-requestHeaders)
		})
	}
}

func TestValidateDNSConfigRejectsCredentialDeletedFromDisabledTarget(t *testing.T) {
	settings := Settings{
		CloudflareCredentials: []CloudflareCredentialConfig{{ID: "credential-kept", Name: "Kept", AuthType: "api_token", APIToken: "token"}},
		DNSTargets: []DNSTargetConfig{{
			ID: "target-disabled", Name: "Disabled", CredentialID: "credential-deleted", Enabled: false,
		}},
	}
	err := validateDNSConfig(settings)
	if err == nil || !strings.Contains(err.Error(), "引用的 Cloudflare 凭据不存在") {
		t.Fatalf("validateDNSConfig() error = %v", err)
	}
	settings.DNSTargets[0].CredentialID = ""
	err = validateDNSConfig(settings)
	if err == nil || !strings.Contains(err.Error(), "引用的 Cloudflare 凭据不存在") {
		t.Fatalf("empty disabled credential reference error = %v", err)
	}
}

func TestRunConfigSnapshotOmitsSecretsAndHydratesByStableID(t *testing.T) {
	store, err := NewStore(t.TempDir() + "/app_state.json")
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultSettings()
	settings.CloudflareCredentials = []CloudflareCredentialConfig{{
		ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "secret-v1",
	}}
	settings.DNSTargets = []DNSTargetConfig{{
		ID: "target-main", Name: "Main", RootDomain: "example.com", ZoneID: "zone", RecordName: "speed.example.com", RecordFamily: "both", CredentialID: "credential-main", Enabled: true,
	}}
	run, err := store.createRun("manual", settings, GeoFilterStats{})
	if err != nil {
		t.Fatal(err)
	}
	if run.ConfigSnapshot == nil || run.ConfigSnapshot.CloudflareCredentials[0].APIToken != "" {
		t.Fatalf("run snapshot leaked secret: %+v", run.ConfigSnapshot)
	}
	current := cloneSettings(settings)
	current.CloudflareCredentials[0].APIToken = "secret-v2"
	hydrated := hydrateRunSettings(*run.ConfigSnapshot, current)
	if hydrated.DNSTargets[0].RecordName != "speed.example.com" || hydrated.CloudflareCredentials[0].APIToken != "secret-v2" {
		t.Fatalf("unexpected hydrated snapshot: %+v", hydrated)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-v1") {
		t.Fatal("run snapshot persisted a credential secret")
	}
}

func TestListCloudflareDNSRecordsReadsAllPages(t *testing.T) {
	var pages []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		pages = append(pages, req.URL.Query().Get("page"))
		if req.URL.Query().Get("per_page") != "100" {
			t.Fatalf("per_page = %q", req.URL.Query().Get("per_page"))
		}
		if req.URL.Query().Get("page") == "1" {
			return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":[{"id":"one","type":"A","name":"speed.example.com","content":"192.0.2.1"}],"result_info":{"page":1,"total_pages":2}}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":[{"id":"two","type":"A","name":"speed.example.com","content":"192.0.2.2"}],"result_info":{"page":2,"total_pages":2}}`), nil
	})}
	target := DNSSyncTarget{RecordName: "speed.example.com", RecordType: "A", ZoneID: "zone", Credential: CloudflareCredentialConfig{AuthType: "api_token", APIToken: "token"}}
	records, err := listCloudflareDNSRecords(client, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || strings.Join(pages, ",") != "1,2" {
		t.Fatalf("records=%+v pages=%v", records, pages)
	}
}

func TestSyncResultsContinuesAfterIndependentTargetPreflightFailure(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/zones/zone-bad") {
			return jsonHTTPResponse(http.StatusForbidden, `{"success":false,"errors":[{"code":9109,"message":"invalid token"}]}`), nil
		}
		if req.URL.Path == "/client/v4/zones/zone-good" {
			return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":{"name":"example.com"}}`), nil
		}
		if strings.Contains(req.URL.Path, "/zones/zone-good/dns_records") {
			return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":[{"id":"kept","type":"A","name":"good.example.com","content":"192.0.2.10"}],"result_info":{"page":1,"total_pages":1}}`), nil
		}
		return jsonHTTPResponse(http.StatusNotFound, `{"success":false,"errors":[{"message":"unexpected request"}]}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	credential := CloudflareCredentialConfig{ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "token"}
	settings := defaultSettings()
	settings.IPv6Enabled = false
	settings.IPv6Count = 0
	settings.IPv4Count = 1
	settings.CloudflareCredentials = []CloudflareCredentialConfig{credential}
	settings.DNSTargets = []DNSTargetConfig{
		{ID: "target-bad", Name: "Bad", RootDomain: "example.com", ZoneID: "zone-bad", RecordName: "bad.example.com", RecordFamily: "ipv4", CredentialID: credential.ID, Enabled: true},
		{ID: "target-good", Name: "Good", RootDomain: "example.com", ZoneID: "zone-good", RecordName: "good.example.com", RecordFamily: "ipv4", CredentialID: credential.ID, Enabled: true},
	}
	report, err := syncResultsToCloudflare(settings, []IPTestResult{{IP: "192.0.2.10", IPVersion: 4}}, func(string) {})
	if err == nil {
		t.Fatal("expected partial sync error")
	}
	if report.TotalTargets != 2 || report.ConfirmedTargets != 1 || report.ConfirmedRecords != 1 || len(report.TargetResults) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.ConfirmedByIP["192.0.2.10"] != 1 || report.PlannedByIP["192.0.2.10"] != 2 || report.ConfirmedIPs["192.0.2.10"] {
		t.Fatalf("unexpected per-IP status: %+v", report)
	}
}

func TestParseShareLinkIPListExtractsVmessAndDeduplicates(t *testing.T) {
	encode := func(address string) string {
		payload, err := json.Marshal(map[string]string{"v": "2", "add": address, "port": "8443"})
		if err != nil {
			t.Fatal(err)
		}
		return "vmess://" + base64.RawStdEncoding.EncodeToString(payload)
	}
	input := strings.Join([]string{
		encode("104.17.140.17"),
		encode("104.17.51.166"),
		encode("104.17.140.17"),
		encode("2606:4700::1111"),
	}, "\n")
	ipv4, ipv6, err := parseShareLinkIPList(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ipv4, ",") != "104.17.140.17,104.17.51.166" || strings.Join(ipv6, ",") != "2606:4700::1111" {
		t.Fatalf("ipv4=%v ipv6=%v", ipv4, ipv6)
	}
}

func TestParseShareLinkIPListExtractsVlessServerIPOnly(t *testing.T) {
	input := strings.Join([]string{
		"vless://98fce859-ff05-48ca-8f16-b70c9f4155b5@104.17.200.191:443?encryption=none&security=tls&sni=yellow-mud-7db1.nisam9527.workers.dev&type=ws&host=yellow-mud-7db1.nisam9527.workers.dev#HKG_203.0.113.99",
		"vless://user@[2606:4700::1234]:8443?security=tls&sni=example.com",
		"vless://another@104.17.200.191:443?host=198.51.100.20",
	}, "\n")
	ipv4, ipv6, err := parseShareLinkIPList(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ipv4, ",") != "104.17.200.191" || strings.Join(ipv6, ",") != "2606:4700::1234" {
		t.Fatalf("ipv4=%v ipv6=%v", ipv4, ipv6)
	}
}

func TestParseShareLinkIPListSupportsMixedVmessAndVless(t *testing.T) {
	payload := base64.RawStdEncoding.EncodeToString([]byte(`{"add":"104.17.140.17"}`))
	input := "vmess://" + payload + "\n" + "vless://uuid@104.17.51.166:443?security=tls"
	ipv4, ipv6, err := parseShareLinkIPList(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ipv4, ",") != "104.17.140.17,104.17.51.166" || len(ipv6) != 0 {
		t.Fatalf("ipv4=%v ipv6=%v", ipv4, ipv6)
	}
}

func TestParseShareLinkIPListRejectsHostnameAddress(t *testing.T) {
	payload := base64.RawStdEncoding.EncodeToString([]byte(`{"add":"proxy.example.com"}`))
	_, _, err := parseShareLinkIPList("vmess://" + payload)
	if err == nil || !strings.Contains(err.Error(), "不是 IPv4/IPv6") {
		t.Fatalf("parseShareLinkIPList() error = %v", err)
	}
	_, _, err = parseShareLinkIPList("vless://uuid@proxy.example.com:443?security=tls")
	if err == nil || !strings.Contains(err.Error(), "不是 IPv4/IPv6") {
		t.Fatalf("parseShareLinkIPList(vless hostname) error = %v", err)
	}
}

func TestManualDNSTargetDoesNotChangeAutomaticScanCounts(t *testing.T) {
	credential := CloudflareCredentialConfig{ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "token"}
	settings := defaultSettings()
	settings.IPv4Count = 10
	settings.IPv6Count = 10
	settings.CloudflareCredentials = []CloudflareCredentialConfig{credential}
	settings.DNSTargets = []DNSTargetConfig{{ID: "target-auto", Name: "Auto", RootDomain: "example.com", ZoneID: "zone", RecordName: "auto.example.com", RecordFamily: "both", CredentialID: credential.ID, Enabled: true}}
	settings.ManualDNSTargets = []ManualDNSTargetConfig{{ID: "manual-one", Name: "Manual", RootDomain: "example.com", ZoneID: "zone", RecordName: "manual.example.com", CredentialID: credential.ID}}
	if err := validateDNSConfig(settings); err != nil {
		t.Fatal(err)
	}
	if requiredIPCount(settings) != 20 || requiredDNSRecordCount(settings) != 20 || plannedDNSTargetCount(settings) != 1 {
		t.Fatalf("manual target changed automatic plan: IP=%d DNS=%d targets=%d", requiredIPCount(settings), requiredDNSRecordCount(settings), plannedDNSTargetCount(settings))
	}
	settings.ManualDNSTargets[0].RecordName = "auto.example.com"
	if err := validateDNSConfig(settings); err == nil || !strings.Contains(err.Error(), "不能同时配置") {
		t.Fatalf("overlapping manual/automatic target error = %v", err)
	}
}

func TestReplaceManualDNSRecordsClearsOnlyExactAAndAAAA(t *testing.T) {
	records := []CloudflareDNSRecord{
		{ID: "manual-a", Type: "A", Name: "manual.example.com", Content: "192.0.2.1"},
		{ID: "manual-aaaa", Type: "AAAA", Name: "manual.example.com", Content: "2001:db8::1"},
		{ID: "other-a", Type: "A", Name: "other.example.com", Content: "192.0.2.2"},
	}
	var deleted []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Path == "/client/v4/zones/zone" {
			return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":{"name":"example.com"}}`), nil
		}
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/dns_records") {
			payload, _ := json.Marshal(map[string]interface{}{"success": true, "result": records, "result_info": map[string]int{"page": 1, "total_pages": 1}})
			return jsonHTTPResponse(http.StatusOK, string(payload)), nil
		}
		if req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/dns_records/") {
			id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			deleted = append(deleted, id)
			kept := records[:0]
			for _, record := range records {
				if record.ID != id {
					kept = append(kept, record)
				}
			}
			records = kept
			return jsonHTTPResponse(http.StatusOK, `{"success":true,"result":{"id":"deleted"}}`), nil
		}
		return jsonHTTPResponse(http.StatusBadRequest, `{"success":false,"errors":[{"message":"unexpected request"}]}`), nil
	})}
	credential := CloudflareCredentialConfig{ID: "credential-main", Name: "Main", AuthType: "api_token", APIToken: "token"}
	manual := ManualDNSTargetConfig{ID: "manual-one", Name: "Manual", RootDomain: "example.com", ZoneID: "zone", RecordName: "manual.example.com", CredentialID: credential.ID}
	settings := defaultSettings()
	settings.CloudflareCredentials = []CloudflareCredentialConfig{credential}
	settings.ManualDNSTargets = []ManualDNSTargetConfig{manual}
	if err := replaceManualDNSRecords(client, settings, manual, nil, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(deleted, ",") != "manual-a,manual-aaaa" {
		t.Fatalf("deleted=%v", deleted)
	}
	if len(records) != 1 || records[0].ID != "other-a" {
		t.Fatalf("unrelated records changed: %+v", records)
	}
}

func TestParseTrueConnectionVLESSSeparatesHTTPAndHTTPS(t *testing.T) {
	httpsLink := "vless://98fce859-ff05-48ca-8f16-b70c9f4155b5@104.17.144.26:443?encryption=none&security=tls&sni=yellow-mud.example.com&fp=randomized&type=ws&host=yellow-mud.example.com&path=%2F%3Fed%3D2048"
	node, err := parseTrueConnectionNode(httpsLink, true)
	if err != nil {
		t.Fatal(err)
	}
	if node.Protocol != "vless" || !node.TLS || node.Network != "ws" || node.Host != "yellow-mud.example.com" || node.Path != "/?ed=2048" {
		t.Fatalf("node=%+v", node)
	}
	if _, err := parseTrueConnectionNode(httpsLink, false); err == nil || !strings.Contains(err.Error(), "HTTP/非 TLS") {
		t.Fatalf("HTTPS link accepted as HTTP: %v", err)
	}

	httpLink := "vless://98fce859-ff05-48ca-8f16-b70c9f4155b5@104.17.144.26:80?encryption=none&security=none&type=ws&host=plain.example.com&path=%2Fws"
	if node, err = parseTrueConnectionNode(httpLink, false); err != nil || node.TLS {
		t.Fatalf("HTTP node=%+v err=%v", node, err)
	}
}

func TestParseTrueConnectionVMessAndBuildCandidateConfig(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"v": "2", "add": "104.17.1.1", "port": "8443", "id": "98fce859-ff05-48ca-8f16-b70c9f4155b5",
		"aid": "0", "scy": "auto", "net": "ws", "host": "edge.example.com", "path": "/ws", "tls": "tls", "sni": "edge.example.com",
	})
	link := "vmess://" + base64.RawStdEncoding.EncodeToString(payload)
	node, err := parseTrueConnectionNode(link, true)
	if err != nil {
		t.Fatal(err)
	}
	config, err := buildXrayTrueConnectionConfig(node, "162.159.39.76", 2053, 18081)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(config)
	text := string(encoded)
	if !strings.Contains(text, `"address":"162.159.39.76"`) || !strings.Contains(text, `"port":2053`) {
		t.Fatalf("candidate address/port missing: %s", text)
	}
	if strings.Contains(text, `"address":"104.17.1.1"`) {
		t.Fatalf("original address was not replaced: %s", text)
	}
}

func TestValidateTrueConnectionRequiresSeparateNodes(t *testing.T) {
	settings := defaultSettings()
	settings.TrueConnectionIPv4 = true
	settings.TrueConnectionHTTP = true
	if err := validateTrueConnectionSettings(settings); err == nil || !strings.Contains(err.Error(), "HTTP 真连接节点") {
		t.Fatalf("missing HTTP node error=%v", err)
	}
	settings.TrueConnectionHTTPNode = "vless://uuid@104.17.1.1:80?security=none&type=ws&host=plain.example.com&path=%2Fws"
	settings.TrueConnectionHTTPS = true
	if err := validateTrueConnectionSettings(settings); err == nil || !strings.Contains(err.Error(), "HTTPS 真连接节点") {
		t.Fatalf("missing HTTPS node error=%v", err)
	}
	settings.TrueConnectionHTTPSNode = "vless://uuid@104.17.1.1:443?security=tls&type=ws&host=tls.example.com&sni=tls.example.com&path=%2Fws"
	if err := validateTrueConnectionSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func TestTrueConnectionNodesAreNotDuplicatedIntoRunSnapshot(t *testing.T) {
	settings := defaultSettings()
	settings.TrueConnectionHTTPNode = "vless://secret-http"
	settings.TrueConnectionHTTPSNode = "vless://secret-https"
	snapshot := sanitizedRunSettings(settings)
	if snapshot.TrueConnectionHTTPNode != "" || snapshot.TrueConnectionHTTPSNode != "" {
		t.Fatalf("snapshot leaked node credentials: %+v", snapshot)
	}
	hydrated := hydrateRunSettings(snapshot, settings)
	if hydrated.TrueConnectionHTTPNode != settings.TrueConnectionHTTPNode || hydrated.TrueConnectionHTTPSNode != settings.TrueConnectionHTTPSNode {
		t.Fatalf("resume did not hydrate current saved nodes: %+v", hydrated)
	}
}

func TestLiveTrueConnectionPort(t *testing.T) {
	link := strings.TrimSpace(os.Getenv("TRUE_CONNECTION_LIVE_NODE"))
	ip := strings.TrimSpace(os.Getenv("TRUE_CONNECTION_LIVE_IP"))
	xrayBin := strings.TrimSpace(os.Getenv("XRAY_BIN"))
	if link == "" || ip == "" || xrayBin == "" {
		t.Skip("set TRUE_CONNECTION_LIVE_NODE, TRUE_CONNECTION_LIVE_IP and XRAY_BIN for the optional live test")
	}
	expectTLS := parseLooseBool(os.Getenv("TRUE_CONNECTION_LIVE_TLS"))
	port := parseInt(os.Getenv("TRUE_CONNECTION_LIVE_PORT"), 443)
	node, err := parseTrueConnectionNode(link, expectTLS)
	if err != nil {
		t.Fatal(err)
	}
	if parseLooseBool(os.Getenv("TRUE_CONNECTION_LIVE_ALL_PORTS")) {
		settings := defaultSettings()
		settings.TrueConnectionTestURL = "https://www.google.com/generate_204"
		if expectTLS {
			settings.TrueConnectionHTTPS = true
			settings.TrueConnectionHTTPSNode = link
		} else {
			settings.TrueConnectionHTTP = true
			settings.TrueConnectionHTTPNode = link
		}
		results, _, err := runTrueConnectionTests(context.Background(), settings, ip)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) == 0 {
			t.Fatal("all selected Cloudflare ports failed the live true-connection test")
		}
		t.Logf("available ports: %s", formatTrueConnectionPorts(results))
		return
	}
	latency, err := testTrueConnectionPort(context.Background(), xrayBin, node, ip, port, "https://www.google.com/generate_204")
	if err != nil {
		t.Fatal(err)
	}
	if latency <= 0 {
		t.Fatalf("latency=%d", latency)
	}
	t.Logf("true connection succeeded in %dms", latency)
}
