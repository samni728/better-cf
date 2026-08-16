package main

import (
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestLocationFilterSummaryUsesMeasuredLocationFields(t *testing.T) {
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

func TestLocationFilterMatchesMeasuredDataCenter(t *testing.T) {
	locationMu.Lock()
	previous := locationMap
	locationMap = map[string]location{
		"NRT": {Iata: "NRT", Cca2: "JP", Region: "Asia Pacific", City: "Tokyo"},
		"LAX": {Iata: "LAX", Cca2: "US", Region: "North America", City: "Los Angeles"},
	}
	locationMu.Unlock()
	t.Cleanup(func() {
		locationMu.Lock()
		locationMap = previous
		locationMu.Unlock()
	})

	filter := locationFilter{Mode: "strict", Country: "JP", City: "Tokyo"}
	if !filter.MatchesDataCenter("NRT") {
		t.Fatal("expected NRT CF-RAY data center to match JP/Tokyo")
	}
	if filter.MatchesDataCenter("LAX") {
		t.Fatal("expected LAX CF-RAY data center not to match JP/Tokyo")
	}
}

func TestSubnetSamplerDoesNotRepeatBeforePoolIsExhausted(t *testing.T) {
	sampler := newSubnetSampler([]string{"a", "b", "c", "d"})
	first := sampler.Next(2)
	second := sampler.Next(2)
	seen := make(map[string]bool)
	for _, value := range append(first, second...) {
		if seen[value] {
			t.Fatalf("subnet %q repeated before the pool was exhausted", value)
		}
		seen[value] = true
	}
	if len(seen) != 4 {
		t.Fatalf("visited %d unique subnets, want 4", len(seen))
	}
}

func TestHintSubnetsFromEnvFiltersFamilyAndDuplicates(t *testing.T) {
	old, existed := os.LookupEnv("BETTER_CF_HINT_SUBNETS")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("BETTER_CF_HINT_SUBNETS", old)
		} else {
			_ = os.Unsetenv("BETTER_CF_HINT_SUBNETS")
		}
	})
	_ = os.Setenv("BETTER_CF_HINT_SUBNETS", "162.159.39.0/24,162.159.39.76/24,2606:4700:52::/48,invalid")
	got := hintSubnetsFromEnv(4)
	if len(got) != 1 || got[0] != "162.159.39.0/24" {
		t.Fatalf("hintSubnetsFromEnv(4) = %v", got)
	}
}

func TestHintIPsFromEnvFiltersFamilyAndDuplicates(t *testing.T) {
	old, existed := os.LookupEnv("BETTER_CF_HINT_IPS")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("BETTER_CF_HINT_IPS", old)
		} else {
			_ = os.Unsetenv("BETTER_CF_HINT_IPS")
		}
	})
	_ = os.Setenv("BETTER_CF_HINT_IPS", "162.159.39.76,162.159.39.76,2606:4700:52::1,invalid")
	got := hintIPsFromEnv(4)
	if len(got) != 1 || got[0] != "162.159.39.76" {
		t.Fatalf("hintIPsFromEnv(4) = %v", got)
	}
}

func TestExtendHintSubnetsLearnsFromMatchingRTTResults(t *testing.T) {
	got := extendHintSubnets(
		[]string{"162.159.38.0/24"},
		[]RTTResult{{IP: "162.159.39.76"}, {IP: "162.159.39.195"}},
		4,
	)
	if len(got) != 3 || got[0] != "162.159.38.0/24" || got[1] != "162.159.39.0/24" || got[2] != "162.159.0.0/16" {
		t.Fatalf("extendHintSubnets() = %v", got)
	}
}

func TestHierarchicalCandidatesUseHintsAndRespectCooldown(t *testing.T) {
	sampler := newSubnetSampler([]string{"104.16.0.0/24", "104.17.0.0/24"})
	excluded := []netip.Prefix{netip.MustParsePrefix("172.66.130.0/24")}
	got := hierarchicalCandidateIPs(sampler, []string{"172.66.130.0/24", "172.66.0.0/16"}, map[string]bool{"104.16.0.1": true}, excluded, 40, 4, candidateBudget{Exact: 15, Narrow: 30, Wide: 20, Global: 35})
	if len(got) != 40 {
		t.Fatalf("hierarchicalCandidateIPs returned %d candidates, want 40", len(got))
	}
	seen := make(map[string]bool)
	usedParentHint := false
	parent := netip.MustParsePrefix("172.66.0.0/16")
	for _, raw := range got {
		addr := netip.MustParseAddr(raw)
		if excluded[0].Contains(addr) {
			t.Fatalf("candidate %s belongs to cooled prefix", raw)
		}
		if raw == "104.16.0.1" {
			t.Fatalf("candidate %s belongs to cooled exact IP", raw)
		}
		if seen[raw] {
			t.Fatalf("candidate %s was repeated", raw)
		}
		seen[raw] = true
		if parent.Contains(addr) {
			usedParentHint = true
		}
	}
	if !usedParentHint {
		t.Fatal("expected candidates to explore the successful parent /16")
	}
}

func TestCandidateBudgetFromEnv(t *testing.T) {
	t.Setenv("BETTER_CF_BUDGET_EXACT", "10")
	t.Setenv("BETTER_CF_BUDGET_NARROW", "40")
	t.Setenv("BETTER_CF_BUDGET_WIDE", "30")
	t.Setenv("BETTER_CF_BUDGET_GLOBAL", "20")
	if got := candidateBudgetFromEnv(); got != (candidateBudget{Exact: 10, Narrow: 40, Wide: 30, Global: 20}) {
		t.Fatalf("candidateBudgetFromEnv = %+v", got)
	}
	t.Setenv("BETTER_CF_BUDGET_GLOBAL", "19")
	if got := candidateBudgetFromEnv(); got != (candidateBudget{Exact: 15, Narrow: 30, Wide: 20, Global: 35}) {
		t.Fatalf("invalid budget did not fall back: %+v", got)
	}
}

func TestHierarchicalCandidatesFollowAdaptiveBudget(t *testing.T) {
	sampler := newSubnetSampler([]string{"198.51.100.0/24"})
	got := hierarchicalCandidateIPs(sampler, []string{"172.66.130.0/24", "104.18.0.0/16"}, nil, nil, 100, 4, candidateBudget{Narrow: 40, Wide: 30, Global: 30})
	narrow, wide, global := 0, 0, 0
	narrowPrefix := netip.MustParsePrefix("172.66.130.0/24")
	widePrefix := netip.MustParsePrefix("104.18.0.0/16")
	for _, raw := range got {
		addr := netip.MustParseAddr(raw)
		switch {
		case narrowPrefix.Contains(addr):
			narrow++
		case widePrefix.Contains(addr):
			wide++
		default:
			global++
		}
	}
	if narrow < 38 || wide < 28 || global < 28 {
		t.Fatalf("adaptive allocation narrow=%d wide=%d global=%d", narrow, wide, global)
	}
}
