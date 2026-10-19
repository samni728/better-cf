package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const matrixTLSNode = "vless://matrix-secret@104.17.1.1:443?security=tls&type=ws&host=tls.example.com&sni=tls.example.com&path=%2Fws"

func matrixSettings(t *testing.T) Settings {
	t.Helper()
	return gateSettings(t, fmt.Sprintf(`{"true_connection_ipv4":true,"true_connection_https":true,"true_connection_ipv4_full_https":true,"true_connection_https_node":%q}`, matrixTLSNode))
}

func TestIPv4MatrixRequiresEveryHTTPSPort(t *testing.T) {
	settings := matrixSettings(t)
	if got := requiredHTTPSPortsForFamily(settings, 4); !reflect.DeepEqual(got, []int{443, 2053, 2083, 2087, 2096, 8443}) {
		t.Fatalf("full matrix requirements = %v", got)
	}
	if got := requiredHTTPSPortsForFamily(settings, 6); len(got) != 0 {
		t.Fatalf("IPv6 requirements changed: %v", got)
	}
	settings.TrueConnectionHTTPSPorts = []int{443}
	if err := validateRequiredHTTPSSettings(settings); err == nil {
		t.Fatal("partial selection accepted for full matrix")
	}
}

func TestIPv4MatrixRejectsDisabledProtocolsAndUnknownKeys(t *testing.T) {
	for _, extra := range []string{
		`"true_connection_ipv4":false`,
		`"true_connection_https":false`,
		`"true_connection_ipv4_https_nodes":{"80":""}`,
		`"true_connection_ipv4_https_nodes":{"0443":""}`,
	} {
		t.Run(extra, func(t *testing.T) {
			settings := matrixSettings(t)
			if err := json.Unmarshal([]byte("{"+extra+"}"), &settings); err != nil {
				t.Fatal(err)
			}
			if err := validateRequiredHTTPSSettings(settings); err == nil {
				t.Fatal("invalid full matrix policy accepted")
			}
		})
	}
}

func TestIPv4MatrixRejectsMissingAndNonTLSNode(t *testing.T) {
	for _, node := range []string{"", "vless://private-secret@104.17.1.1:80?security=none&type=ws&host=plain.example.com"} {
		settings := matrixSettings(t)
		settings.TrueConnectionHTTPSNode = node
		if err := validateRequiredHTTPSSettings(settings); err == nil {
			t.Fatal("missing or non-TLS full matrix template accepted")
		} else if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("secret leaked in error")
		}
	}
}

func TestIPv4MatrixVariantsUseMatchingTemplatesAndPreserveIPv6(t *testing.T) {
	settings := matrixSettings(t)
	settings.TrueConnectionHTTP = true
	settings.TrueConnectionHTTPNode = "vless://plain@104.17.1.1:80?security=none&type=ws&host=plain.example.com"
	settings.TrueConnectionIPv4HTTPSNodes = map[string]string{"2053": strings.Replace(matrixTLSNode, "matrix-secret", "port-2053", 1), "8443": strings.Replace(matrixTLSNode, "matrix-secret", "port-8443", 1)}
	variants, err := buildTrueConnectionVariantsForFamily(settings, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 6 {
		t.Fatalf("IPv4 produced %d variants", len(variants))
	}
	for i, port := range []int{443, 2053, 2083, 2087, 2096, 8443} {
		wantID := "matrix-secret"
		if port == 2053 || port == 8443 {
			wantID = fmt.Sprintf("port-%d", port)
		}
		if variants[i].Scheme != "HTTPS" || variants[i].Port != port || variants[i].Node.ID != wantID {
			t.Fatalf("wrong template for port %d: scheme=%s port=%d", port, variants[i].Scheme, variants[i].Port)
		}
	}
	variants, err = buildTrueConnectionVariantsForFamily(settings, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 13 || variants[0].Scheme != "HTTP" || variants[len(variants)-1].Node.ID != "matrix-secret" {
		t.Fatal("IPv6 changed by IPv4 matrix")
	}
	settings.TrueConnectionHTTPSNode = ""
	if _, err := buildTrueConnectionVariantsForFamily(settings, 4); err == nil {
		t.Fatal("missing effective template accepted")
	}
}

func TestIPv4MatrixFingerprintTracksEffectiveTemplates(t *testing.T) {
	settings := matrixSettings(t)
	baseline := ipv4MatrixFingerprint(settings)
	if len(baseline) != 32 || strings.Contains(baseline, "matrix-secret") {
		t.Fatalf("unsafe fingerprint %q", baseline)
	}
	settings.TrueConnectionIPv4HTTPSNodes = map[string]string{"443": matrixTLSNode, "2053": " "}
	if ipv4MatrixFingerprint(settings) != baseline {
		t.Fatal("equivalent effective templates changed fingerprint")
	}
	settings.TrueConnectionIPv4HTTPSNodes["2053"] = strings.Replace(matrixTLSNode, "matrix-secret", "changed-secret", 1)
	changed := ipv4MatrixFingerprint(settings)
	if changed == baseline {
		t.Fatal("template credentials did not change fingerprint")
	}
	settings.TrueConnectionIPv4HTTPSNodes = map[string]string{"2053": settings.TrueConnectionIPv4HTTPSNodes["2053"], "443": matrixTLSNode}
	if ipv4MatrixFingerprint(settings) != changed {
		t.Fatal("map insertion order changed fingerprint")
	}
}

func TestIPv4MatrixResumeRejectsPolicyOrTemplateChanges(t *testing.T) {
	current := matrixSettings(t)
	snapshot := current
	snapshot.TrueConnectionIPv4MatrixFingerprint = ipv4MatrixFingerprint(current)
	snapshot.TrueConnectionHTTPSNode = ""
	if err := validateResumeConnectionPolicy(snapshot, current); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.TrueConnectionHTTPSNode = strings.Replace(matrixTLSNode, "matrix-secret", "other-secret", 1)
	if err := validateResumeConnectionPolicy(snapshot, changed); err == nil {
		t.Fatal("changed credentials accepted for resume")
	}
	changed = current
	changed.TrueConnectionIPv4FullHTTPS = false
	if err := validateResumeConnectionPolicy(snapshot, changed); err == nil {
		t.Fatal("changed matrix mode accepted for resume")
	}
	snapshot.TrueConnectionIPv4MatrixFingerprint = ""
	if err := validateResumeConnectionPolicy(snapshot, current); err == nil {
		t.Fatal("unfingerprinted full matrix snapshot accepted")
	}
	if err := validateResumeConnectionPolicy(Settings{}, Settings{}); err != nil {
		t.Fatal("legacy snapshot compatibility broken")
	}
}

func TestIPv4MatrixAdmissionRequiresDirectIngressAndMatchingEvidence(t *testing.T) {
	settings := matrixSettings(t)
	result := IPTestResult{IP: "104.16.1.1", IPVersion: 4, TrueConnectionTested: true}
	for _, port := range []int{443, 2053, 2083, 2087, 2096, 8443} {
		result.TrueConnectionPorts = append(result.TrueConnectionPorts, TrueConnectionPortResult{Scheme: "HTTPS", Port: port})
	}
	if err := validateResultTrueConnection(settings, result); err == nil {
		t.Fatal("legacy evidence accepted for full matrix")
	}
	result.IPv4DirectIngressVerified = true
	result.IPv4MatrixFingerprint = "other-policy"
	if err := validateResultTrueConnection(settings, result); err == nil {
		t.Fatal("mismatched templates accepted")
	}
	result.IPv4MatrixFingerprint = ipv4MatrixFingerprint(settings)
	if err := validateResultTrueConnection(settings, result); err != nil {
		t.Fatal(err)
	}
	result.TrueConnectionPorts = result.TrueConnectionPorts[:5]
	if err := validateResultTrueConnection(settings, result); err == nil {
		t.Fatal("incomplete port evidence accepted")
	}
}

func TestIPv4MatrixPerPortTemplatesNeedNoSharedFallback(t *testing.T) {
	settings := matrixSettings(t)
	settings.TrueConnectionHTTPSNode = ""
	settings.TrueConnectionIPv4HTTPSNodes = make(map[string]string)
	for _, port := range []int{443, 2053, 2083, 2087, 2096, 8443} {
		settings.TrueConnectionIPv4HTTPSNodes[fmt.Sprint(port)] = matrixTLSNode
	}
	variants, err := buildTrueConnectionVariantsForFamily(settings, 4)
	if err != nil || len(variants) != 6 {
		t.Fatalf("complete per-port templates rejected: count=%d err=%v", len(variants), err)
	}
	settings.TrueConnectionIPv4HTTPSNodes["2053"] = "vless://private-secret@104.17.1.1:443?security=none&type=ws"
	if _, err := buildTrueConnectionVariantsForFamily(settings, 4); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("invalid per-port template accepted or secret leaked")
	}
}

func TestIPv4MatrixDoesNotRequireDirectEvidenceForIPv6(t *testing.T) {
	settings := matrixSettings(t)
	settings.TrueConnectionIPv6 = true
	settings.TrueConnectionRequiredHTTPSPortsIPv6 = []int{2087}
	result := IPTestResult{IP: "2606:4700::1", IPVersion: 6, TrueConnectionTested: true, TrueConnectionPorts: []TrueConnectionPortResult{{Scheme: "HTTPS", Port: 2087}}}
	if err := validateResultTrueConnection(settings, result); err != nil {
		t.Fatal(err)
	}
	settings.TrueConnectionIPv4FullHTTPS = false
	settings.TrueConnectionIPv4HTTPSNodes = map[string]string{"80": ""}
	if err := validateRequiredHTTPSSettings(settings); err == nil {
		t.Fatal("unsupported template key accepted when full mode disabled")
	}
}

func TestIPv4MatrixFingerprintBindsProbeURL(t *testing.T) {
	s := matrixSettings(t)
	s.TrueConnectionTestURL = "https://www.google.com/generate_204"
	before := ipv4MatrixFingerprint(s)
	s.TrueConnectionTestURL = "https://example.com/check"
	if ipv4MatrixFingerprint(s) == before {
		t.Fatal("changed tunnel test URL reused old admission evidence")
	}
}

func TestIPv4MatrixRejectsInsecureTLS(t *testing.T) {
	s := matrixSettings(t)
	s.TrueConnectionHTTPSNode += "&allowInsecure=1"
	if validateIPv4MatrixSettings(s) == nil {
		t.Fatal("strict matrix accepted disabled certificate verification")
	}
}
