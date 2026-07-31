package main

import (
	"html/template"
	"path/filepath"
	"strings"
	"testing"

	"cf-betterip-ser/internal/geodb"
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
	locations := []geodb.Location{
		{Country: "JP", Region: "JP-13", City: "Tokyo"},
		{Country: "JP", Region: "JP-27", City: "Osaka"},
		{Country: "CN", Region: "CN-GD", City: "Guangzhou"},
	}
	_, regions, cities := buildGeoChoices(locations, Settings{LocationCountry: "JP"})
	if len(regions) != 2 || regions[0].Value != "JP-13" || regions[1].Value != "JP-27" {
		t.Fatalf("unexpected regions: %+v", regions)
	}
	if len(cities) != 2 || cities[0].Value != "Osaka" || cities[1].Value != "Tokyo" {
		t.Fatalf("unexpected cities: %+v", cities)
	}
}

func TestCalculateGeoFilterStats(t *testing.T) {
	locations := []geodb.Location{
		{Country: "CN", Region: "CN-GD", City: "Guangzhou", IPv4Count: 40, IPv6Count: 10},
		{Country: "CN", Region: "CN-GD", City: "Shenzhen", IPv4Count: 12, IPv6Count: 4},
		{Country: "JP", Region: "JP-13", City: "Tokyo", IPv4Count: 8, IPv6Count: 2},
	}
	stats := calculateGeoFilterStats(locations, Settings{
		LocationCountry: "CN",
		LocationRegion:  "CN-GD",
		LocationCity:    "Guangzhou",
	})
	if stats.IPv4Count != 40 || stats.IPv6Count != 10 || stats.Total != 50 {
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
	stats := GeoFilterStats{IPv4Count: 40, IPv6Count: 10, Total: 50}
	got := locationFilterSummaryWithStats(settings, stats)
	if !strings.Contains(got, "IPv4 40 段") || !strings.Contains(got, "IPv6 10 段") || !strings.Contains(got, "不回退全局") {
		t.Fatalf("strict summary is incomplete: %q", got)
	}
}

func TestRunSummaryFreezesStrictLocationAndCounts(t *testing.T) {
	settings := defaultSettings()
	settings.LocationMode = "strict"
	settings.LocationCountry = "CN"
	settings.LocationRegion = "CN-GD"
	settings.LocationCity = "Guangzhou"
	summary := runSummary("manual", settings, GeoFilterStats{IPv4Count: 40, IPv6Count: 10, Total: 50})
	for _, required := range []string{"立即执行", "严格地区", "CN-GD", "Guangzhou", "IPv4 40 段", "IPv6 10 段", "不回退全局"} {
		if !strings.Contains(summary, required) {
			t.Fatalf("run summary %q does not contain %q", summary, required)
		}
	}
}

func TestAppGeoDatabasePathIsAbsoluteAndShared(t *testing.T) {
	app := &App{dataDir: t.TempDir()}
	got := app.geoDatabasePath()
	want := filepath.Join(app.dataDir, "local-ip-ranges.csv")
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("geoDatabasePath() = %q, want absolute %q", got, want)
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
