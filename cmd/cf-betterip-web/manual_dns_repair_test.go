package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Strictly opt-in, root-executed maintenance: reconcile a specific failed run's
// ALREADY validated candidates using normal production DNS logic. Does not
// touch app_state.json, scan settings, or OpenWrt; retains historical run status.
func TestManualDNSRepairFromVerifiedRun(t *testing.T) {
	if os.Getenv("CF_BETTERIP_DNS_REPAIR_APPROVED") != "yes" {
		t.Skip("manual repair opt-in missing")
	}
	path := os.Getenv("CF_BETTERIP_DNS_REPAIR_STATE")
	id := os.Getenv("CF_BETTERIP_DNS_REPAIR_RUN_ID")
	if path == "" || id == "" {
		t.Fatal("repair state or run ID required")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s AppState
	if err := json.Unmarshal(content, &s); err != nil {
		t.Fatal(err)
	}
	var failed *RunRecord
	for i := range s.Runs {
		if s.Runs[i].ID == id {
			failed = &s.Runs[i]
			break
		}
	}
	if failed == nil || failed.Status != "failed" || failed.DNSStatus != "partial" {
		t.Fatal("run is not the specified partial DNS failure")
	}
	var verified []IPTestResult
	for _, r := range s.Results {
		if r.RunID == id {
			verified = append(verified, r)
		}
	}
	if len(verified) != 20 || countFamily(verified, 4) != 10 || countFamily(verified, 6) != 10 {
		t.Fatalf("unqualified candidate counts %d", len(verified))
	}
	for _, r := range verified {
		if r.DataCenterCountry != "JP" {
			t.Fatalf("non JP candidate: %s", r.IP)
		}
	}
	if err := validateDNSConnectionResults(s.Settings, verified); err != nil {
		t.Fatalf("connection verification failed: %v", err)
	}
	report, err := syncResultsToCloudflare(s.Settings, verified, func(message string) { t.Log(message) })
	if err != nil {
		t.Fatalf("manual DNS sync failed: %v; confirmed=%d/%d", err, report.ConfirmedTargets, report.TotalTargets)
	}
	if report.ConfirmedTargets != 3 || report.ConfirmedRecords != 40 {
		t.Fatalf("incomplete DNS results: %d/%d targets, %d records", report.ConfirmedTargets, report.TotalTargets, report.ConfirmedRecords)
	}
	fmt.Println("CF_BETTERIP_MANUAL_REPAIR_CONFIRMED=3/3 DNS_RECORDS=40 IPV4_JP=10 IPV6_JP=10")
}
