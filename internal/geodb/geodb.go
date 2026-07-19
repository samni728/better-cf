package geodb

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
)

// Entry is one Cloudflare GeoFeed row: network, country, subdivision, city.
type Entry struct {
	Prefix  netip.Prefix
	Network string
	Country string
	Region  string
	City    string
}

type Filter struct {
	Country string
	Region  string
	City    string
}

type Location struct {
	Country string
	Region  string
	City    string
}

func Parse(reader io.Reader) ([]Entry, error) {
	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1
	entries := make([]Entry, 0, 1024)
	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse GeoFeed CSV: %w", err)
		}
		if len(record) == 0 || strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}
		if len(record) < 4 {
			return nil, fmt.Errorf("GeoFeed row has %d fields, want at least 4", len(record))
		}
		network := strings.TrimSpace(record[0])
		prefix, err := netip.ParsePrefix(network)
		if err != nil {
			addr, addrErr := netip.ParseAddr(network)
			if addrErr != nil {
				return nil, fmt.Errorf("invalid GeoFeed network %q", network)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		entries = append(entries, Entry{
			Prefix:  prefix.Masked(),
			Network: network,
			Country: strings.ToUpper(strings.TrimSpace(record[1])),
			Region:  strings.TrimSpace(record[2]),
			City:    strings.TrimSpace(record[3]),
		})
	}
	return entries, nil
}

func Matches(entry Entry, filter Filter) bool {
	if value := strings.TrimSpace(filter.Country); value != "" && !strings.EqualFold(value, entry.Country) {
		return false
	}
	if value := strings.TrimSpace(filter.Region); value != "" && !strings.EqualFold(value, entry.Region) {
		return false
	}
	if value := strings.TrimSpace(filter.City); value != "" && !strings.EqualFold(value, entry.City) {
		return false
	}
	return true
}

func Prefixes(entries []Entry, family int, filter Filter) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0)
	for _, entry := range entries {
		if family == 4 && !entry.Prefix.Addr().Is4() {
			continue
		}
		if family == 6 && !entry.Prefix.Addr().Is6() {
			continue
		}
		if Matches(entry, filter) {
			prefixes = append(prefixes, entry.Prefix)
		}
	}
	return prefixes
}

// Locations returns unique country/region/city combinations for cascading UI filters.
func Locations(entries []Entry) []Location {
	seen := make(map[string]bool)
	locations := make([]Location, 0)
	for _, entry := range entries {
		if entry.Country == "" {
			continue
		}
		key := entry.Country + "\x00" + entry.Region + "\x00" + entry.City
		if seen[key] {
			continue
		}
		seen[key] = true
		locations = append(locations, Location{Country: entry.Country, Region: entry.Region, City: entry.City})
	}
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].Country != locations[j].Country {
			return locations[i].Country < locations[j].Country
		}
		if locations[i].Region != locations[j].Region {
			return locations[i].Region < locations[j].Region
		}
		return locations[i].City < locations[j].City
	})
	return locations
}

// RandomAddr returns an address inside prefix. randomByte must return a value in [0, 255].
func RandomAddr(prefix netip.Prefix, randomByte func() byte) netip.Addr {
	prefix = prefix.Masked()
	addr := prefix.Addr()
	bitLen := addr.BitLen()
	byteLen := bitLen / 8
	bytes := make([]byte, byteLen)
	base := addr.As16()
	if addr.Is4() {
		copy(bytes, base[12:])
	} else {
		copy(bytes, base[:])
	}
	for i := range bytes {
		bytes[i] |= randomByte()
	}
	bits := prefix.Bits()
	fullBytes := bits / 8
	remainingBits := bits % 8
	baseBytes := base[:]
	if addr.Is4() {
		baseBytes = base[12:]
	}
	copy(bytes[:fullBytes], baseBytes[:fullBytes])
	if remainingBits > 0 {
		mask := byte(0xff << (8 - remainingBits))
		bytes[fullBytes] = (baseBytes[fullBytes] & mask) | (bytes[fullBytes] &^ mask)
	}
	if addr.Is4() {
		return netip.AddrFrom4([4]byte(bytes))
	}
	return netip.AddrFrom16([16]byte(bytes))
}
