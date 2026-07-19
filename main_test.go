package main

import (
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
