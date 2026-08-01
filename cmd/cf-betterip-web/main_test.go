package main

import (
	"context"
	"errors"
	"html/template"
	"strings"
	"testing"
)

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
