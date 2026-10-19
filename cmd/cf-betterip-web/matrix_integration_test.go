package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMatrixCredentialsCloneAndSnapshot(t *testing.T) {
	var settings Settings
	if err := json.Unmarshal([]byte(`{"true_connection_ipv4_full_https":true,"true_connection_ipv4_https_nodes":{"443":"secret-node"}}`), &settings); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := json.Marshal(sanitizedRunSettings(settings))
	if strings.Contains(string(snapshot), "secret-node") {
		t.Fatal("matrix credentials leaked into run snapshot")
	}
	cloned := cloneSettings(settings)
	raw, _ := json.Marshal(cloned)
	if !strings.Contains(string(raw), "secret-node") {
		t.Fatal("matrix was lost while cloning settings")
	}
	hydrated, _ := json.Marshal(hydrateRunSettings(sanitizedRunSettings(settings), settings))
	if !strings.Contains(string(hydrated), "secret-node") {
		t.Fatal("matrix not restored for resumed run")
	}
}

// Opt-in: test binary can run on the domestic VM with a private temporary
// settings file; it never publishes DNS or writes production state.
func TestLiveIPv4MatrixAdmission(t *testing.T) {
	path := os.Getenv("CF_MATRIX_LIVE_SETTINGS")
	if path == "" {
		t.Skip("CF_MATRIX_LIVE_SETTINGS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private live settings")
	}
	var s Settings
	if json.Unmarshal(data, &s) != nil {
		t.Fatal("invalid private live settings")
	}
	if err := validateTrueConnectionSettings(s); err != nil {
		t.Fatal(err)
	}
	ip := os.Getenv("CF_MATRIX_LIVE_IP")
	ports, attempts, err := runTrueConnectionTests(context.Background(), s, ip)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(formatTrueConnectionAttempts(attempts))
	r := IPTestResult{IP: ip, IPVersion: 4, TrueConnectionTested: true, TrueConnectionPorts: ports, IPv4DirectIngressVerified: len(ports) == 6, IPv4MatrixFingerprint: ipv4MatrixFingerprint(s)}
	if err := validateResultTrueConnection(s, r); err != nil {
		t.Fatal(err)
	}
}

func TestMatrixUIAndProfileKeepTemplatesPrivate(t *testing.T) {
	s := matrixSettings(t)
	s.TrueConnectionIPv4HTTPSNodes = map[string]string{"443": matrixTLSNode + "&fp=firefox"}
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, settingsTemplate, app.pageData("配置", "admin", s))
	html := recorder.Body.String()
	if strings.Contains(html, "matrix-secret") || !strings.Contains(html, `name="true_connection_ipv4_full_https" checked`) || !strings.Contains(html, `name="true_connection_ipv4_https_node_443"`) {
		t.Fatal("matrix UI lost controls or exposed credentials")
	}
	profile := searchProfileForSettings(s, 4)
	data, _ := json.Marshal(profile)
	if strings.Contains(string(data), "matrix-secret") {
		t.Fatal("search profile exposed node credentials")
	}
	oldV4 := profile.HTTPSNodeHash
	oldV6 := searchProfileForSettings(s, 6)
	s.TrueConnectionIPv4HTTPSNodes["443"] = matrixTLSNode + "&fp=chrome"
	if searchProfileForSettings(s, 4).HTTPSNodeHash == oldV4 {
		t.Fatal("IPv4 matrix credentials changed without segregating history")
	}
	if searchProfileForSettings(s, 6) != oldV6 {
		t.Fatal("IPv4 matrix changed IPv6 search profile")
	}
	copy := cloneSettings(s)
	copy.TrueConnectionIPv4HTTPSNodes["443"] = "changed"
	if s.TrueConnectionIPv4HTTPSNodes["443"] == "changed" {
		t.Fatal("settings clone shared secret map")
	}
}
