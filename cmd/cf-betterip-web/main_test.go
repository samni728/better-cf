package main

import (
	"html/template"
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
