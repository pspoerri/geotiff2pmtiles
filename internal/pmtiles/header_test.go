package pmtiles

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
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

	// E7 encoding keeps bounds to 1e-7 degrees.
	tol := 1e-7
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

// readHeaderE7 returns MinLon, MinLat, MaxLon, MaxLat, CenterLon, CenterLat
// of a serialized header in E7 units.
func readHeaderE7(buf []byte) [6]int32 {
	var e7 [6]int32
	for i, off := range []int{102, 106, 110, 114, 119, 123} {
		e7[i] = int32(binary.LittleEndian.Uint32(buf[off : off+4]))
	}
	return e7
}

// Bounds must not pass through float32 (7.6e-6 degree spacing at 64-128)
// before E7 encoding, and the rounding must keep the data inside the bbox.
func TestNewHeader_BoundsE7(t *testing.T) {
	tests := []struct {
		bounds cog.Bounds
		want   [6]int32
	}{
		{ // Decimal inputs stay exact: no floor/ceil off-by-one.
			cog.Bounds{MinLon: 5.95, MinLat: 45.82, MaxLon: 10.49, MaxLat: 47.81},
			[6]int32{59_500_000, 458_200_000, 104_900_000, 478_100_000, 82_200_000, 468_150_000},
		},
		{ // float32 turned these into -451234550, 471234550, 1800000000.
			cog.Bounds{MinLon: -179.9999999, MinLat: -45.1234567, MaxLon: 179.9999999, MaxLat: 47.1234567},
			[6]int32{-1_799_999_999, -451_234_567, 1_799_999_999, 471_234_567, 0, 10_000_000},
		},
		{ // Sub-E7 digits: minimums round down, maximums up, center to nearest.
			cog.Bounds{MinLon: 5.12345678, MinLat: -5.12345678, MaxLon: 6.12345671, MaxLat: -4.12345671},
			[6]int32{51_234_567, -51_234_568, 61_234_568, -41_234_567, 56_234_567, -46_234_567},
		},
	}
	for _, tt := range tests {
		h := NewHeader(WriterOptions{Bounds: tt.bounds})
		if got := readHeaderE7(h.Serialize()); got != tt.want {
			t.Errorf("NewHeader(%+v) E7 = %v, want %v", tt.bounds, got, tt.want)
		}
	}
}

// Data crossing the antimeridian (MaxLon > 180) is recorded as -180..180 in
// the header and the metadata, since TileJSON bounds must not wrap and
// MapLibre shows nothing for west > east; the centre stays on the data.
// Longitudes past ±214.7 used to overflow int32 E7.
func TestArchiveBounds_Antimeridian(t *testing.T) {
	tests := []struct {
		name          string
		bounds        cog.Bounds
		wantMinLon    float64
		wantMaxLon    float64
		wantCenterLon float64
	}{
		{"UTM 60 across 180", cog.Bounds{MinLon: 176.5, MaxLon: 182.1, MinLat: 45, MaxLat: 46}, -180, 180, 179.3},
		{"centre past 180", cog.Bounds{MinLon: 179, MaxLon: 189, MinLat: 45, MaxLat: 46}, -180, 180, -176},
		{"Pacific 100..260", cog.Bounds{MinLon: 100, MaxLon: 260, MinLat: -60, MaxLat: 60}, -180, 180, 180},
		{"not crossing", cog.Bounds{MinLon: 5, MaxLon: 10, MinLat: 45, MaxLat: 48}, 5, 10, 7.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHeader(WriterOptions{Bounds: tt.bounds})
			e7 := readHeaderE7(h.Serialize())
			want := [3]int32{int32(tt.wantMinLon * 1e7), int32(tt.wantMaxLon * 1e7), int32(math.Round(tt.wantCenterLon * 1e7))}
			if got := [3]int32{e7[0], e7[2], e7[4]}; got != want {
				t.Errorf("header MinLon, MaxLon, CenterLon E7 = %v, want %v", got, want)
			}
			if b := h.Bounds(); b.MinLon != tt.wantMinLon || b.MaxLon != tt.wantMaxLon {
				t.Errorf("Bounds() = %+v, want lon %v..%v", b, tt.wantMinLon, tt.wantMaxLon)
			}

			raw, err := (&Writer{opts: WriterOptions{Bounds: tt.bounds, MaxZoom: 4}}).buildMetadata()
			if err != nil {
				t.Fatal(err)
			}
			var meta map[string]any
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatal(err)
			}
			b := tt.bounds
			wantBounds := fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", tt.wantMinLon, b.MinLat, tt.wantMaxLon, b.MaxLat)
			wantCenter := fmt.Sprintf("%.6f,%.6f,2", tt.wantCenterLon, (b.MinLat+b.MaxLat)/2)
			if meta["bounds"] != wantBounds || meta["center"] != wantCenter {
				t.Errorf("metadata bounds %v center %v, want %v and %v", meta["bounds"], meta["center"], wantBounds, wantCenter)
			}
		})
	}

	for _, v := range []float32{260, 360, -300} {
		if got, want := int32(lonLatToE7(v)), int32(math.Copysign(1_800_000_000, float64(v))); got != want {
			t.Errorf("lonLatToE7(%v) = %d, want %d (clamped)", v, got, want)
		}
	}
}

// The metadata "format" names every tile type, as TileTypeString does; MVT,
// AVIF and MLT used to be written as "unknown".
func TestBuildMetadata_Format(t *testing.T) {
	for tt, want := range map[uint8]string{
		TileTypeUnknown: "unknown", TileTypeMVT: "mvt", TileTypePNG: "png", TileTypeJPEG: "jpeg",
		TileTypeWebP: "webp", TileTypeAVIF: "avif", TileTypeMLT: "mlt",
	} {
		raw, err := (&Writer{opts: WriterOptions{TileFormat: tt}}).buildMetadata()
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		if meta["format"] != want {
			t.Errorf("tile type %d: format %v, want %q", tt, meta["format"], want)
		}
	}
}

// Reading and rewriting a header (pmheader patching the zoom, say) must
// keep the bounds bit for bit, while a changed field still takes effect.
func TestHeader_RoundTripKeepsE7(t *testing.T) {
	want := [6]int32{-1_799_999_999, -451_234_567, 1_799_999_999, 471_234_567, 1_234_567, -851_234_567}
	src := (&Header{}).Serialize()
	for i, off := range []int{102, 106, 110, 114, 119, 123} {
		binary.LittleEndian.PutUint32(src[off:off+4], uint32(want[i]))
	}

	h, err := DeserializeHeader(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := readHeaderE7(h.Serialize()); got != want {
		t.Errorf("round trip E7 = %v, want %v", got, want)
	}
	b := h.Bounds()
	if b.MinLat != -45.1234567 || b.MaxLat != 47.1234567 || b.MaxLon != 179.9999999 {
		t.Errorf("Bounds() = %+v, want the exact E7 values", b)
	}

	h.MaxLat = 50
	want[3] = 500_000_000
	if got := readHeaderE7(h.Serialize()); got != want {
		t.Errorf("after setting MaxLat: E7 = %v, want %v", got, want)
	}
}

func TestTileTypeString(t *testing.T) {
	for tileType, want := range []string{"unknown", "mvt", "png", "jpeg", "webp", "avif", "mlt", "unknown"} {
		if got := TileTypeString(uint8(tileType)); got != want {
			t.Errorf("TileTypeString(%d) = %q, want %q", tileType, got, want)
		}
	}
}
