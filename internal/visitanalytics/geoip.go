package visitanalytics

import (
	"embed"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// The offline database is an ip2region xdb file (v2 structure with IPv4, or the
// v3 structure with an explicit IP version). Only the vector index layout is
// supported; the reader keeps the whole file in memory so a lookup performs no
// disk I/O and no network call. See https://github.com/lionsoul2014/ip2region
// for the format (Apache-2.0).
//
// A copy of the official IPv4 database is embedded so a fresh install has real
// regions without any extra deployment step. An external file in
// VisitAnalytics.GeoIPFile takes precedence and can be updated without a
// release.
//
//go:embed data/ip2region_v4.xdb
var embeddedGeoDatabase embed.FS

const (
	embeddedGeoPath = "data/ip2region_v4.xdb"

	xdbHeaderInfoLength = 256
	xdbVectorIndexRows  = 256
	xdbVectorIndexCols  = 256
	xdbVectorIndexSize  = 8

	xdbStructureV2 = 2
	xdbStructureV3 = 3

	xdbVectorIndexPolicy = 1

	xdbSegmentSizeV4 = 14
	xdbSegmentSizeV6 = 38
)

// Location is the resolved region for an address. Unknown values are explicit
// rather than empty so the aggregate keys stay stable.
type Location struct {
	Country string
	Region  string
	City    string
}

var errGeoUnsupported = errors.New("visit analytics geo database is not a supported ip2region xdb file")

type geoIndex struct {
	data         []byte
	segmentBytes int
	ipv6         bool
	structure    uint16
	source       string
	modTime      time.Time
}

// loadEmbeddedGeoIndex decodes the database shipped with the binary.
func loadEmbeddedGeoIndex() (*geoIndex, error) {
	data, err := embeddedGeoDatabase.ReadFile(embeddedGeoPath)
	if err != nil {
		return nil, err
	}
	index, err := parseGeoData(data)
	if err != nil {
		return nil, err
	}
	index.source = "embedded"
	return index, nil
}

// loadGeoIndex reads and validates an xdb file.
func loadGeoIndex(path string) (*geoIndex, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 512<<20 {
		return nil, errGeoUnsupported
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	index, err := parseGeoData(data)
	if err != nil {
		return nil, err
	}
	index.modTime = info.ModTime()
	index.source = "file"
	return index, nil
}

func parseGeoData(data []byte) (*geoIndex, error) {
	if len(data) < xdbHeaderInfoLength+xdbVectorIndexRows*xdbVectorIndexCols*xdbVectorIndexSize {
		return nil, errGeoUnsupported
	}
	version := binary.LittleEndian.Uint16(data[0:])
	if binary.LittleEndian.Uint16(data[2:]) != xdbVectorIndexPolicy {
		return nil, errGeoUnsupported
	}
	switch version {
	case xdbStructureV2:
		return &geoIndex{data: data, segmentBytes: xdbSegmentSizeV4, structure: version}, nil
	case xdbStructureV3:
		switch binary.LittleEndian.Uint16(data[16:]) {
		case 4:
			return &geoIndex{data: data, segmentBytes: xdbSegmentSizeV4, structure: version}, nil
		case 6:
			return &geoIndex{data: data, segmentBytes: xdbSegmentSizeV6, ipv6: true, structure: version}, nil
		default:
			return nil, errGeoUnsupported
		}
	default:
		return nil, errGeoUnsupported
	}
}

// lookup returns the region for an address. Private, loopback, link-local and
// unroutable addresses resolve to "unknown" without a table probe.
func (g *geoIndex) lookup(address netip.Addr) Location {
	unknown := Location{Country: "unknown"}
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsMulticast() {
		return unknown
	}
	if g == nil {
		return unknown
	}
	address = address.Unmap()
	var raw []byte
	if address.Is4() {
		if g.ipv6 {
			return unknown
		}
		v4 := address.As4()
		raw = v4[:]
	} else {
		if !g.ipv6 {
			return unknown
		}
		v6 := address.As16()
		raw = v6[:]
	}
	region, err := g.search(raw)
	if err != nil || region == "" {
		return unknown
	}
	return parseRegion(region)
}

func (g *geoIndex) search(ip []byte) (string, error) {
	if len(ip) != 4 && len(ip) != 16 {
		return "", errGeoUnsupported
	}
	indexOffset := xdbHeaderInfoLength + int(ip[0])*xdbVectorIndexCols*xdbVectorIndexSize + int(ip[1])*xdbVectorIndexSize
	if indexOffset+xdbVectorIndexSize > len(g.data) {
		return "", errGeoUnsupported
	}
	start := binary.LittleEndian.Uint32(g.data[indexOffset:])
	end := binary.LittleEndian.Uint32(g.data[indexOffset+4:])
	if start == 0 || end == 0 || end < start {
		return "", nil
	}
	ipBytes := len(ip)
	doubleBytes := ipBytes << 1
	segmentBytes := g.segmentBytes
	if segmentBytes != ipBytes*2+6 {
		return "", errGeoUnsupported
	}
	low, high := 0, int((end-start)/uint32(segmentBytes))
	dataLength, dataPointer := 0, uint32(0)
	for low <= high {
		middle := (low + high) >> 1
		offset := int(start) + middle*segmentBytes
		if offset+segmentBytes > len(g.data) {
			return "", errGeoUnsupported
		}
		segment := g.data[offset : offset+segmentBytes]
		if g.compare(ip, segment[0:ipBytes]) < 0 {
			high = middle - 1
		} else if g.compare(ip, segment[ipBytes:doubleBytes]) > 0 {
			low = middle + 1
		} else {
			dataLength = int(binary.LittleEndian.Uint16(segment[doubleBytes:]))
			dataPointer = binary.LittleEndian.Uint32(segment[doubleBytes+2:])
			break
		}
	}
	if dataLength == 0 {
		return "", nil
	}
	end2 := int(dataPointer) + dataLength
	if end2 > len(g.data) {
		return "", errGeoUnsupported
	}
	return string(g.data[dataPointer:end2]), nil
}

// compare follows the xdb layout: the input address is big-endian while the
// index stores the segment bounds little-endian.
func (g *geoIndex) compare(ip, segment []byte) int {
	if g.ipv6 {
		return strings.Compare(string(ip), string(segment))
	}
	for index := 0; index < len(ip); index++ {
		left := ip[index]
		right := segment[len(ip)-1-index]
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	return 0
}

// parseRegion splits "国家|省份|城市|ISP|国家代码" and normalizes the placeholder
// values ip2region uses for missing data: "0", "内网IP", "未知" and the
// "Reserved" range used for documentation and special-purpose addresses.
func parseRegion(region string) Location {
	fields := strings.Split(strings.TrimSpace(region), "|")
	value := func(index int) string {
		if index >= len(fields) {
			return ""
		}
		field := strings.TrimSpace(fields[index])
		switch strings.ToLower(field) {
		case "", "0", "-", "内网ip", "未知", "unknown", "reserved":
			return ""
		}
		return truncate(field, 64)
	}
	location := Location{Country: value(0), Region: value(1), City: value(2)}
	if location.Country == "" {
		location.Country = "unknown"
		location.Region, location.City = "", ""
	}
	return location
}

// geoStore swaps the loaded database atomically so reloads never block a
// request and a failed reload keeps serving the previous table.
type geoStore struct {
	current atomic.Pointer[geoIndex]
	path    string
}

func newGeoStore(path string) *geoStore {
	store := &geoStore{path: path}
	if path != "" {
		if index, err := loadGeoIndex(path); err == nil {
			store.current.Store(index)
			return store
		}
	}
	// Fall back to the database embedded in the binary so a missing or
	// unreadable override never degrades regions to "unknown".
	if index, err := loadEmbeddedGeoIndex(); err == nil {
		store.current.Store(index)
	}
	return store
}

// Lookup resolves an IP address to a region label set.
func (s *geoStore) Lookup(address netip.Addr) Location {
	index := s.current.Load()
	if index == nil {
		return Location{Country: "unknown"}
	}
	return index.lookup(address)
}

// Available reports whether a table is loaded.
func (s *geoStore) Available() bool { return s.current.Load() != nil }

// reload re-reads the external file when its modification time changed. The
// embedded database never reloads.
func (s *geoStore) reload() (bool, error) {
	if s.path == "" {
		return false, nil
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return false, err
	}
	previous := s.current.Load()
	if previous != nil && previous.source == "file" && !previous.modTime.Before(info.ModTime()) {
		return false, nil
	}
	index, err := loadGeoIndex(s.path)
	if err != nil {
		return false, err
	}
	s.current.Store(index)
	return true, nil
}

// GeoStatus describes the offline database state for the administrator report.
func (s *geoStore) GeoStatus() map[string]any {
	index := s.current.Load()
	if index == nil {
		return map[string]any{"loaded": false, "path": s.path, "format": "", "source": ""}
	}
	format := "ip2region-v2-ipv4"
	switch {
	case index.ipv6:
		format = "ip2region-v3-ipv6"
	case index.structure == xdbStructureV3:
		format = "ip2region-v3-ipv4"
	}
	status := map[string]any{"loaded": true, "path": s.path, "format": format, "source": index.source}
	if !index.modTime.IsZero() {
		status["modTime"] = index.modTime.Format(time.RFC3339)
	}
	return status
}
