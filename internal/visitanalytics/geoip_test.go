package visitanalytics

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

type testSegment struct {
	firstByte byte
	region    string
}

// buildSyntheticXDB writes a minimal but layout-faithful xdb file: 256 byte
// header, 256x256 vector index, one 14 byte segment per /16 and the region
// strings. It is used to prove the reader's byte layout without shipping the
// vendor database in the repository.
func buildSyntheticXDB(segments []testSegment) []byte {
	const headerSize = xdbHeaderInfoLength
	const vectorSize = xdbVectorIndexRows * xdbVectorIndexCols * xdbVectorIndexSize
	const segmentSize = xdbSegmentSizeV4

	regionLength := 0
	for _, segment := range segments {
		regionLength += len(segment.region)
	}
	data := make([]byte, headerSize+vectorSize+segmentSize*len(segments)+regionLength+segmentSize)
	binary.LittleEndian.PutUint16(data[0:], xdbStructureV2)
	binary.LittleEndian.PutUint16(data[2:], xdbVectorIndexPolicy)

	segmentStart := headerSize + vectorSize
	regionOffset := segmentStart + segmentSize*len(segments)
	regionWritten := 0
	for index, segment := range segments {
		offset := segmentStart + index*segmentSize
		// start  b.0.0.0   little endian
		copy(data[offset:offset+4], []byte{0, 0, 0, segment.firstByte})
		// end    b.0.0.255 little endian
		copy(data[offset+4:offset+8], []byte{255, 0, 0, segment.firstByte})
		binary.LittleEndian.PutUint16(data[offset+8:], uint16(len(segment.region)))
		binary.LittleEndian.PutUint32(data[offset+10:], uint32(regionOffset+regionWritten))
		copy(data[regionOffset+regionWritten:], segment.region)
		regionWritten += len(segment.region)

		vector := headerSize + int(segment.firstByte)*xdbVectorIndexCols*xdbVectorIndexSize
		binary.LittleEndian.PutUint32(data[vector:], uint32(offset))
		binary.LittleEndian.PutUint32(data[vector+4:], uint32(offset+segmentSize))
	}
	// Sentinel after the region block so the binary search terminates like the
	// reference implementation when an address is above every stored range.
	sentinel := regionOffset + regionLength
	for index := 0; index < segmentSize; index++ {
		data[sentinel+index] = 0xFF
	}
	return data
}

func TestGeoIndexLookup(t *testing.T) {
	data := buildSyntheticXDB([]testSegment{
		{1, "中国|广东省|深圳市|电信|CN"},
		{2, "美国|加利福尼亚州|洛杉矶|Comcast|US"},
	})
	index, err := parseGeoData(data)
	if err != nil {
		t.Fatal(err)
	}
	if location := index.lookup(netip.MustParseAddr("1.0.0.5")); location.Country != "中国" || location.Region != "广东省" || location.City != "深圳市" {
		t.Fatalf("lookup 1.0.0.5 = %+v", location)
	}
	if location := index.lookup(netip.MustParseAddr("2.0.0.200")); location.Country != "美国" || location.City != "洛杉矶" {
		t.Fatalf("lookup 2.0.0.200 = %+v", location)
	}
	// Address inside a registered /16 but above the stored range resolves to
	// unknown instead of the previous row.
	if location := index.lookup(netip.MustParseAddr("1.0.9.9")); location.Country != "unknown" {
		t.Fatalf("lookup 1.0.9.9 = %+v, want unknown", location)
	}
	// No vector entry for this prefix.
	if location := index.lookup(netip.MustParseAddr("9.9.9.9")); location.Country != "unknown" {
		t.Fatalf("lookup 9.9.9.9 = %+v, want unknown", location)
	}
}

func TestGeoIndexRejectsReservedAddresses(t *testing.T) {
	index, err := parseGeoData(buildSyntheticXDB([]testSegment{{1, "中国|广东省|深圳市|0|CN"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "::1", "169.254.1.1", "::ffff:127.0.0.1"} {
		if location := index.lookup(netip.MustParseAddr(address)); location.Country != "unknown" {
			t.Fatalf("lookup %s = %+v, want unknown", address, location)
		}
	}
}

func TestParseGeoDataRejectsInvalidFiles(t *testing.T) {
	if _, err := parseGeoData([]byte("too short")); err == nil {
		t.Fatal("short file accepted")
	}
	data := buildSyntheticXDB([]testSegment{{1, "中国|0|0|0|CN"}})
	binary.LittleEndian.PutUint16(data[0:], 99)
	if _, err := parseGeoData(data); err == nil {
		t.Fatal("unknown structure version accepted")
	}
	data = buildSyntheticXDB([]testSegment{{1, "中国|0|0|0|CN"}})
	binary.LittleEndian.PutUint16(data[2:], 2)
	if _, err := parseGeoData(data); err == nil {
		t.Fatal("btree index policy accepted")
	}
}

func TestParseRegionPlaceholders(t *testing.T) {
	cases := map[string]Location{
		"中国|0|0|0|CN":                {Country: "中国"},
		"内网IP|内网IP|内网IP":             {Country: "unknown"},
		"Reserved|Reserved|Reserved": {Country: "unknown"},
		"美国|加利福尼亚州|洛杉矶|0|US":         {Country: "美国", Region: "加利福尼亚州", City: "洛杉矶"},
		"": {Country: "unknown"},
	}
	for raw, expected := range cases {
		location := parseRegion(raw)
		if location.Country != expected.Country || location.Region != expected.Region || location.City != expected.City {
			t.Fatalf("parseRegion(%q) = %+v, want %+v", raw, location, expected)
		}
	}
}

func TestEmbeddedGeoDatabaseResolvesRegions(t *testing.T) {
	store := newGeoStore("")
	if !store.Available() {
		t.Fatal("embedded geo database did not load")
	}
	status := store.GeoStatus()
	if status["loaded"] != true || status["source"] != "embedded" {
		t.Fatalf("status = %+v", status)
	}
	if status["format"] != "ip2region-v3-ipv4" {
		t.Fatalf("format = %v", status["format"])
	}
	// Two well-known public addresses must resolve to a real country; the exact
	// labels follow the upstream database revision and are not asserted here.
	for _, raw := range []string{"114.114.114.114", "8.8.8.8"} {
		location := store.Lookup(netip.MustParseAddr(raw))
		if location.Country == "" || location.Country == "unknown" {
			t.Fatalf("%s resolved to %+v", raw, location)
		}
	}
	if changed, err := store.reload(); err != nil || changed {
		t.Fatalf("embedded reload = (%v,%v), want no reload", changed, err)
	}
}

func TestExternalGeoFileTakesPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip2region_v4.xdb")
	if err := os.WriteFile(path, buildSyntheticXDB([]testSegment{{1, "中国|广东省|深圳市|电信|CN"}}), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newGeoStore(path)
	if status := store.GeoStatus(); status["source"] != "file" {
		t.Fatalf("status = %+v", status)
	}
	if location := store.Lookup(netip.MustParseAddr("1.0.0.5")); location.Country != "中国" || location.City != "深圳市" {
		t.Fatalf("external table lookup = %+v", location)
	}
	// An unreadable override falls back to the embedded database instead of
	// degrading every region to unknown.
	fallback := newGeoStore(filepath.Join(t.TempDir(), "missing.xdb"))
	if !fallback.Available() || fallback.GeoStatus()["source"] != "embedded" {
		t.Fatalf("missing override did not fall back: %+v", fallback.GeoStatus())
	}
}
