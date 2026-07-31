package main

import (
	"os"
	"testing"
	"time"
)

func TestLocationFilterSummaryUsesGeoFeedFields(t *testing.T) {
	filter := locationFilter{Mode: "strict", Country: "CN", Region: "CN-GD", City: "Guangzhou"}
	want := "严格地区 / 国家=CN / 区域=CN-GD / 城市=Guangzhou"
	if got := filter.Summary(); got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}

func TestPreferredLocationFilterFallsBack(t *testing.T) {
	filter := locationFilter{
		Mode:           "prefer",
		Country:        "JP",
		PreferDuration: 10 * time.Minute,
	}
	if !filter.Active(9 * time.Minute).Enabled() {
		t.Fatal("expected preferred filter to remain active before its deadline")
	}
	if filter.Active(10 * time.Minute).Enabled() {
		t.Fatal("expected preferred filter to fall back at its deadline")
	}
}

func TestNormalizeMaxRTT(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 1, want: 10},
		{input: 180, want: 180},
		{input: 3000, want: 2000},
	}
	for _, tt := range tests {
		if got := normalizeMaxRTT(tt.input); got != tt.want {
			t.Fatalf("normalizeMaxRTT(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestMaxRTTFromEnv(t *testing.T) {
	old, existed := os.LookupEnv("BETTER_CF_MAX_RTT_MS")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("BETTER_CF_MAX_RTT_MS", old)
		} else {
			_ = os.Unsetenv("BETTER_CF_MAX_RTT_MS")
		}
	})

	_ = os.Setenv("BETTER_CF_MAX_RTT_MS", "175")
	if got := maxRTTFromEnv(); got != 175 {
		t.Fatalf("maxRTTFromEnv() = %d, want 175", got)
	}
	_ = os.Setenv("BETTER_CF_MAX_RTT_MS", "invalid")
	if got := maxRTTFromEnv(); got != 200 {
		t.Fatalf("maxRTTFromEnv() with invalid input = %d, want 200", got)
	}
}

func TestGeoDatabasePathPrefersExplicitSharedDatabase(t *testing.T) {
	old, existed := os.LookupEnv("BETTER_CF_GEO_DB_PATH")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("BETTER_CF_GEO_DB_PATH", old)
		} else {
			_ = os.Unsetenv("BETTER_CF_GEO_DB_PATH")
		}
	})

	_ = os.Setenv("BETTER_CF_GEO_DB_PATH", "/tmp/shared-geo.csv")
	if got := geoDatabasePath(); got != "/tmp/shared-geo.csv" {
		t.Fatalf("geoDatabasePath() = %q, want shared path", got)
	}
}
