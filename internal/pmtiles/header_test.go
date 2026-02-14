package pmtiles

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

func TestHeaderSerialize_MagicBytes(t *testing.T) {
	h := NewHeader(WriterOptions{
		MinZoom:    0,
		MaxZoom:    10,
		Bounds:     cog.Bounds{MinLon: -180, MaxLon: 180, MinLat: -85, MaxLat: 85},
		TileFormat: TileTypePNG,
		TileSize:   256,
	})

	buf := h.Serialize()

	if len(buf) != HeaderSize {
		t.Fatalf("header size = %d, want %d", len(buf), HeaderSize)
	}

	// First 7 bytes should be "PMTiles".
	magic := string(buf[0:7])
	if magic != "PMTiles" {
		t.Errorf("magic = %q, want \"PMTiles\"", magic)
	}

	// Version byte.
	if buf[7] != 3 {
		t.Errorf("version = %d, want 3", buf[7])
	}
}

func TestHeaderSerialize_TileType(t *testing.T) {
	tests := []struct {
		tileType uint8
		name     string
	}{
		{TileTypePNG, "PNG"},
		{TileTypeJPEG, "JPEG"},
		{TileTypeWebP, "WebP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHeader(WriterOptions{
				TileFormat: tt.tileType,
				Bounds:     cog.Bounds{},
			})
			buf := h.Serialize()
			if buf[99] != tt.tileType {
				t.Errorf("tile type byte = %d, want %d", buf[99], tt.tileType)
			}
		})
	}
}

func TestHeaderSerialize_ZoomRange(t *testing.T) {
	h := NewHeader(WriterOptions{
		MinZoom:    3,
		MaxZoom:    15,
		Bounds:     cog.Bounds{},
		TileFormat: TileTypePNG,
	})
	buf := h.Serialize()

	if buf[100] != 3 {
		t.Errorf("min zoom = %d, want 3", buf[100])
	}
	if buf[101] != 15 {
		t.Errorf("max zoom = %d, want 15", buf[101])
	}
}

func TestHeaderSerialize_Bounds(t *testing.T) {
	bounds := cog.Bounds{
		MinLon: 5.95,
		MinLat: 45.82,
		MaxLon: 10.49,
		MaxLat: 47.81,
	}
	h := NewHeader(WriterOptions{
		MinZoom:    5,
		MaxZoom:    12,
		Bounds:     bounds,
		TileFormat: TileTypePNG,
	})
	buf := h.Serialize()

	// Bounds are stored as E7 (int32 * 1e7) in little-endian at offsets 102-118.
	readE7 := func(offset int) float64 {
		raw := binary.LittleEndian.Uint32(buf[offset : offset+4])
		return float64(int32(raw)) / 1e7
	}

	gotMinLon := readE7(102)
	gotMinLat := readE7(106)
	gotMaxLon := readE7(110)
	gotMaxLat := readE7(114)

	// Bounds pass through float32 and E7 encoding, so allow for precision loss.
	tol := 1e-4
	if math.Abs(gotMinLon-bounds.MinLon) > tol {
		t.Errorf("minLon = %v, want ~%v", gotMinLon, bounds.MinLon)
	}
	if math.Abs(gotMinLat-bounds.MinLat) > tol {
		t.Errorf("minLat = %v, want ~%v", gotMinLat, bounds.MinLat)
	}
	if math.Abs(gotMaxLon-bounds.MaxLon) > tol {
		t.Errorf("maxLon = %v, want ~%v", gotMaxLon, bounds.MaxLon)
	}
	if math.Abs(gotMaxLat-bounds.MaxLat) > tol {
		t.Errorf("maxLat = %v, want ~%v", gotMaxLat, bounds.MaxLat)
	}
}

func TestHeaderSerialize_Offsets(t *testing.T) {
	h := Header{
		RootDirOffset:       127,
		RootDirLength:       500,
		MetadataOffset:      627,
		MetadataLength:      100,
		LeafDirOffset:       727,
		LeafDirLength:       0,
		TileDataOffset:      727,
		TileDataLength:      50000,
		NumAddressedTiles:   100,
		NumTileEntries:      80,
		NumTileContents:     80,
		Clustered:           true,
		InternalCompression: CompressionGzip,
		TileCompression:     CompressionNone,
		TileType:            TileTypePNG,
		MinZoom:             5,
		MaxZoom:             12,
	}

	buf := h.Serialize()

	// Read back uint64 fields.
	readU64 := func(offset int) uint64 {
		return binary.LittleEndian.Uint64(buf[offset : offset+8])
	}

	if got := readU64(8); got != 127 {
		t.Errorf("RootDirOffset = %d, want 127", got)
	}
	if got := readU64(16); got != 500 {
		t.Errorf("RootDirLength = %d, want 500", got)
	}
	if got := readU64(24); got != 627 {
		t.Errorf("MetadataOffset = %d, want 627", got)
	}
	if got := readU64(32); got != 100 {
		t.Errorf("MetadataLength = %d, want 100", got)
	}
	if got := readU64(72); got != 100 {
		t.Errorf("NumAddressedTiles = %d, want 100", got)
	}
	if got := readU64(80); got != 80 {
		t.Errorf("NumTileEntries = %d, want 80", got)
	}

	// Clustered flag.
	if buf[96] != 1 {
		t.Errorf("clustered = %d, want 1", buf[96])
	}

	// Compression.
	if buf[97] != CompressionGzip {
		t.Errorf("internal compression = %d, want %d", buf[97], CompressionGzip)
	}
	if buf[98] != CompressionNone {
		t.Errorf("tile compression = %d, want %d", buf[98], CompressionNone)
	}
}

func TestHeaderSerialize_CenterZoom(t *testing.T) {
	h := NewHeader(WriterOptions{
		MinZoom:    4,
		MaxZoom:    10,
		Bounds:     cog.Bounds{MinLon: 6.0, MinLat: 46.0, MaxLon: 10.0, MaxLat: 48.0},
		TileFormat: TileTypePNG,
	})
	buf := h.Serialize()

	// Center zoom = (4+10)/2 = 7
	if buf[118] != 7 {
		t.Errorf("center zoom = %d, want 7", buf[118])
	}

	// Center lon = (6+10)/2 = 8.0, center lat = (46+48)/2 = 47.0
	readE7 := func(offset int) float64 {
		raw := binary.LittleEndian.Uint32(buf[offset : offset+4])
		return float64(int32(raw)) / 1e7
	}

	gotCenterLon := readE7(119)
	gotCenterLat := readE7(123)

	if math.Abs(gotCenterLon-8.0) > 1e-6 {
		t.Errorf("center lon = %v, want 8.0", gotCenterLon)
	}
	if math.Abs(gotCenterLat-47.0) > 1e-6 {
		t.Errorf("center lat = %v, want 47.0", gotCenterLat)
	}
}

func TestLonLatToE7(t *testing.T) {
	tests := []struct {
		input float32
		want  int32
	}{
		{0, 0},
		{180, 1_800_000_000},
		{-180, -1_800_000_000},
		{47.3769, 473_769_000},
		{-85.05, -850_500_000},
	}

	for _, tt := range tests {
		got := lonLatToE7(tt.input)
		gotSigned := int32(got)
		// float32 precision limits the accuracy to ~1e-3 degrees → ~10000 in E7 units.
		if math.Abs(float64(gotSigned-tt.want)) > 100 {
			t.Errorf("lonLatToE7(%v) = %d, want ~%d", tt.input, gotSigned, tt.want)
		}
	}
}
