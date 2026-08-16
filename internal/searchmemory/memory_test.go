package searchmemory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSuccessBuildsNarrowAndWideHints(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profileID, err := store.EnsureProfile(context.Background(), Profile{IPVersion: 4, Country: "JP", HTTPEnabled: true, HTTPNodeHash: "node"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), Observation{ProfileID: profileID, IP: "172.66.130.219", IPVersion: 4, Outcome: "true_success", TestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	memory, err := store.Candidates(context.Background(), profileID, 4, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.SuccessIPs) != 1 || memory.SuccessIPs[0] != "172.66.130.219" {
		t.Fatalf("success=%v", memory.SuccessIPs)
	}
	want := map[string]bool{"172.66.130.0/24": false, "172.66.0.0/16": false}
	for _, prefix := range memory.HintPrefixes {
		if _, ok := want[prefix]; ok {
			want[prefix] = true
		}
	}
	for prefix, found := range want {
		if !found {
			t.Fatalf("missing hint %s in %v", prefix, memory.HintPrefixes)
		}
	}
}

func TestRegionalAndBandwidthStagesBecomeHintsAndCooldownFailedExactIP(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	profileID, err := store.EnsureProfile(ctx, Profile{IPVersion: 4, Country: "KR", BandwidthMbps: 100})
	if err != nil {
		t.Fatal(err)
	}
	for index, outcome := range []string{"region_match", "bandwidth_fail"} {
		if err := store.Record(ctx, Observation{ProfileID: profileID, IP: "172.71.111.98", IPVersion: 4, Outcome: outcome, TestedAt: now.Add(time.Duration(index) * time.Nanosecond)}); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := store.Candidates(ctx, profileID, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(memory.HintPrefixes, "172.71.111.0/24") || !slices.Contains(memory.HintPrefixes, "172.71.0.0/16") {
		t.Fatalf("stage hints=%v", memory.HintPrefixes)
	}
	if !slices.Contains(memory.ExcludeIPs, "172.71.111.98") {
		t.Fatalf("bandwidth failure should cool exact IP: %v", memory.ExcludeIPs)
	}
}

func TestFailuresCoolExactIPAndPrefix(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profileID := "profile"
	now := time.Now()
	for i := 1; i <= 8; i++ {
		ip := "192.0.2." + string(rune('0'+i))
		if err := store.Record(context.Background(), Observation{ProfileID: profileID, IP: ip, IPVersion: 4, Outcome: "true_failure", ErrorClass: "eof", TestedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := store.Candidates(context.Background(), profileID, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.ExcludeIPs) != 8 {
		t.Fatalf("exclude ips=%v", memory.ExcludeIPs)
	}
	if len(memory.ExcludePrefixes) != 1 || memory.ExcludePrefixes[0] != "192.0.2.0/24" {
		t.Fatalf("exclude prefixes=%v", memory.ExcludePrefixes)
	}
}

func TestProfileSeparatesNodeMemory(t *testing.T) {
	a, _ := ProfileID(Profile{IPVersion: 4, Country: "JP", HTTPNodeHash: "node-a"})
	b, _ := ProfileID(Profile{IPVersion: 4, Country: "JP", HTTPNodeHash: "node-b"})
	if a == b {
		t.Fatal("different node fingerprints shared one profile")
	}
}

func TestExactFailureCooldownExpiresAfterThreeDays(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	profileID, err := store.EnsureProfile(ctx, Profile{IPVersion: 4, Country: "JP", HTTPEnabled: true, HTTPNodeHash: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	for ip, testedAt := range map[string]time.Time{
		"192.0.2.10": now.Add(-2 * 24 * time.Hour),
		"192.0.2.20": now.Add(-4 * 24 * time.Hour),
	} {
		if err := store.Record(ctx, Observation{ProfileID: profileID, IP: ip, IPVersion: 4, Outcome: "true_failure", TestedAt: testedAt}); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := store.Candidates(ctx, profileID, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(memory.ExcludeIPs, "192.0.2.10") {
		t.Fatal("expected a two-day-old failure to remain cooled")
	}
	if slices.Contains(memory.ExcludeIPs, "192.0.2.20") {
		t.Fatal("expected a four-day-old exact failure to leave cooldown")
	}
}

func TestSchemaVersionOneMigratesCandidateSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_meta(version INTEGER NOT NULL)`,
		`INSERT INTO schema_meta(version) VALUES(1)`,
		`CREATE TABLE ip_observations(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT NOT NULL DEFAULT '',profile_id TEXT NOT NULL,ip TEXT NOT NULL,ip_version INTEGER NOT NULL,prefix_narrow TEXT NOT NULL,prefix_wide TEXT NOT NULL,outcome TEXT NOT NULL,error_class TEXT NOT NULL DEFAULT '',dc_country TEXT NOT NULL DEFAULT '',dc_code TEXT NOT NULL DEFAULT '',rtt_ms INTEGER NOT NULL DEFAULT 0,bandwidth_mbps INTEGER NOT NULL DEFAULT 0,tested_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema version = %d, err=%v", version, err)
	}
	var columnCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('ip_observations') WHERE name='candidate_source'`).Scan(&columnCount); err != nil || columnCount != 1 {
		t.Fatalf("candidate_source columns = %d, err=%v", columnCount, err)
	}
}

func TestManualPrefixBudgetAndHTTPOnlyInsights(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	profile := Profile{IPVersion: 4, Country: "JP", HTTPEnabled: true, HTTPSEnabled: true, NetworkLabel: "移动出口"}
	profileID, err := store.EnsureProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if prefix, err := store.AddManualPrefix(ctx, profileID, 4, "172.66.9.1/16"); err != nil || prefix != "172.66.0.0/16" {
		t.Fatalf("AddManualPrefix = %q, %v", prefix, err)
	}
	if _, err := store.AddManualPrefix(ctx, profileID, 4, "172.66.9.0/24"); err == nil {
		t.Fatal("expected /24 manual parent prefix to be rejected")
	}
	for i := 1; i <= 12; i++ {
		outcome, source := "true_failure", "global"
		ports := []PortObservation{{Scheme: "HTTP", Port: 80, Success: false, ErrorClass: "eof"}, {Scheme: "HTTPS", Port: 443, Success: false, ErrorClass: "eof"}}
		if i <= 6 {
			outcome, source = "true_success", "narrow"
			ports[0] = PortObservation{Scheme: "HTTP", Port: 80, Success: true, LatencyMs: 120}
		}
		if err := store.Record(ctx, Observation{ProfileID: profileID, IP: fmt.Sprintf("172.66.130.%d", i), IPVersion: 4, Outcome: outcome, CandidateSource: source, TestedAt: now, Ports: ports}); err != nil {
			t.Fatal(err)
		}
	}
	insight, err := store.ProfileInsight(ctx, profileID, profile, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(insight.ManualPrefixes, "172.66.0.0/16") {
		t.Fatalf("manual prefixes = %v", insight.ManualPrefixes)
	}
	if insight.Budget.Narrow <= insight.Budget.Global {
		t.Fatalf("expected successful narrow source to outrank failed global source: %+v", insight.Budget)
	}
	if len(insight.Ports) != 2 || len(insight.HTTPOnlyPrefixes) != 1 || insight.HTTPOnlyPrefixes[0].Prefix != "172.66.130.0/24" {
		t.Fatalf("unexpected port/http-only insights: ports=%+v httpOnly=%+v", insight.Ports, insight.HTTPOnlyPrefixes)
	}
	if err := store.ClearProfile(ctx, profileID); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.ListProfileInsights(ctx, now)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profiles after clear = %+v, err=%v", profiles, err)
	}
}
