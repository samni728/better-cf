package geodb

import (
	"net/netip"
	"os"
	"strings"
	"testing"
)

func TestParseFilterAndLocations(t *testing.T) {
	data := strings.NewReader("104.28.37.44/32,CN,CN-SC,Chengdu,\n104.28.43.36/32,CN,CN-JS,Nantong,\n103.22.201.0/24,JP,,Tokyo,\n2a09:bac6:2088::/45,CN,CN-GD,Guangzhou,\n")
	entries, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := Prefixes(entries, 6, Filter{Country: "CN", Region: "CN-GD", City: "Guangzhou"})
	if len(prefixes) != 1 || prefixes[0].String() != "2a09:bac6:2088::/45" {
		t.Fatalf("unexpected prefixes: %v", prefixes)
	}
	locations := Locations(entries)
	if len(locations) != 4 {
		t.Fatalf("unexpected locations: %+v", locations)
	}
}

func TestBundledDatabaseSupportsCountryRegionCityAndBothFamilies(t *testing.T) {
	file, err := os.Open("../../database/local-ip-ranges.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	entries, err := Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	tests := []Filter{
		{Country: "CN", Region: "CN-GD", City: "Guangzhou"},
		{Country: "JP", Region: "JP-13", City: "Tokyo"},
	}
	for _, filter := range tests {
		for _, family := range []int{4, 6} {
			if prefixes := Prefixes(entries, family, filter); len(prefixes) == 0 {
				t.Fatalf("no IPv%d prefixes for %+v", family, filter)
			}
		}
	}
}

func TestRandomAddrStaysInsidePrefix(t *testing.T) {
	tests := []string{"104.28.37.44/32", "103.22.201.0/24", "2a09:bac6:2088::/45", "2606:4700::/32"}
	for _, raw := range tests {
		prefix := netip.MustParsePrefix(raw)
		addr := RandomAddr(prefix, func() byte { return 0xff })
		if !prefix.Contains(addr) {
			t.Fatalf("%s does not contain generated address %s", prefix, addr)
		}
	}
}
