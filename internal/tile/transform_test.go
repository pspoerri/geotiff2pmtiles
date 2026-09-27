package tile

import (
	"bytes"
	"image"
	"image/color"
	"sync"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// --- Mock implementations ---

// mockPMTilesReader implements PMTilesReader for unit testing.
type mockPMTilesReader struct {
	tiles  map[[3]int][]byte
	header pmtiles.Header
}

func (r *mockPMTilesReader) ReadTile(z, x, y int) ([]byte, error) {
	return r.tiles[[3]int{z, x, y}], nil
}

func (r *mockPMTilesReader) TilesAtZoom(z int) [][3]int {
	var result [][3]int
	for k := range r.tiles {
		if k[0] == z {
			result = append(result, k)
		}
	}
	return result
}

func (r *mockPMTilesReader) Header() pmtiles.Header {
	return r.header
}

// mockTileWriter collects written tiles for verification.
type mockTileWriter struct {
	mu    sync.Mutex
	tiles map[[3]int][]byte
}

func newMockTileWriter() *mockTileWriter {
	return &mockTileWriter{tiles: make(map[[3]int][]byte)}
}

func (w *mockTileWriter) WriteTile(z, x, y int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tiles[[3]int{z, x, y}] = append([]byte{}, data...)
	return nil
}

func (w *mockTileWriter) tileCountAtZoom(z int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for k := range w.tiles {
		if k[0] == z {
			count++
		}
	}
	return count
}

// encodePNGTile creates a PNG-encoded tile image with the given color.
func encodePNGTile(t *testing.T, tileSize int, c color.RGBA) []byte {
	t.Helper()
	enc, err := encode.NewEncoder("png", 0)
	if err != nil {
		t.Fatalf("NewEncoder(png): %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = c.R
		pix[i+1] = c.G
		pix[i+2] = c.B
		pix[i+3] = c.A
	}
	data, err := enc.Encode(img)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return data
}

// --- Test setup ---
//
// Bounds (0, 0, 90, 45) produce these tile positions:
//   Zoom 2: (2,2,1), (2,3,1), (2,2,2), (2,3,2)  — 4 tiles
//   Zoom 1: (1,1,0), (1,1,1)                      — 2 tiles
//   Zoom 0: (0,0,0)                                — 1 tile
//
// Parent mapping from zoom 2 to zoom 1:
//   (2,2,1) and (2,3,1) → parent (1,1,0)
//   (2,2,2) and (2,3,2) → parent (1,1,1)

func testBounds() [4]float32 {
	return [4]float32{0, 0, 90, 45}
}

func testEncoder(t *testing.T) encode.Encoder {
	t.Helper()
	enc, err := encode.NewEncoder("png", 0)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	return enc
}

// --- Transform rebuild fill tests ---

// TestTransformRebuild_FillMissing_Sparse verifies that sparse source data
// combined with FillMissing produces tiles at all positions within bounds.
func TestTransformRebuild_FillMissing_Sparse(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	fill := color.RGBA{255, 0, 0, 255}
	bounds := testBounds()

	// Single source tile at (2, 2, 1).
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, green),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      0,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  2,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingBilinear,
		Mode:         TransformRebuild,
		FillMissing:  &fill,
		Bounds:       bounds,
	}

	stats, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	// Zoom 2: 4 positions in bounds, all should have tiles.
	if n := writer.tileCountAtZoom(2); n != 4 {
		t.Errorf("zoom 2: got %d tiles, want 4", n)
	}
	// Zoom 1: 2 positions in bounds.
	if n := writer.tileCountAtZoom(1); n != 2 {
		t.Errorf("zoom 1: got %d tiles, want 2", n)
	}
	// Zoom 0: 1 position.
	if n := writer.tileCountAtZoom(0); n != 1 {
		t.Errorf("zoom 0: got %d tiles, want 1", n)
	}
	// Total: 7 tiles.
	if stats.TileCount != 7 {
		t.Errorf("TileCount = %d, want 7", stats.TileCount)
	}
}

// TestTransformRebuild_FillMissing_FillTilesIdentical verifies that all fill
// tiles at the same zoom level contain identical pre-encoded bytes.
func TestTransformRebuild_FillMissing_FillTilesIdentical(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	fill := color.RGBA{128, 128, 128, 255}
	bounds := testBounds()

	// Single source tile at (2, 2, 1); the other 3 positions are fill.
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, green),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      2,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  1,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingBilinear,
		Mode:         TransformRebuild,
		FillMissing:  &fill,
		Bounds:       bounds,
	}

	_, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	// The 3 fill tiles should have identical encoded data.
	fillPositions := [][3]int{{2, 3, 1}, {2, 2, 2}, {2, 3, 2}}
	var fillData []byte
	for _, pos := range fillPositions {
		data := writer.tiles[pos]
		if data == nil {
			t.Fatalf("missing fill tile at %v", pos)
		}
		if fillData == nil {
			fillData = data
		} else if !bytes.Equal(data, fillData) {
			t.Errorf("fill tile at %v has different data than first fill tile", pos)
		}
	}

	// The real tile should have different data than fill tiles.
	realData := writer.tiles[[3]int{2, 2, 1}]
	if realData == nil {
		t.Fatal("missing real tile at (2,2,1)")
	}
	if bytes.Equal(realData, fillData) {
		t.Error("real tile should have different data than fill tiles")
	}
}

// TestTransformRebuild_FillMissing_RealPositionPropagation verifies that real
// positions propagate correctly to parent zoom levels: only parents with at
// least one real child go through the downsample pipeline.
func TestTransformRebuild_FillMissing_RealPositionPropagation(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	fill := color.RGBA{255, 0, 0, 255}
	bounds := testBounds()

	// Source tile at (2, 2, 1): parent is (1, 1, 0).
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, green),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      1,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  1,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingNearest,
		Mode:         TransformRebuild,
		FillMissing:  &fill,
		Bounds:       bounds,
	}

	_, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	// (1,1,0) should be a real downsampled tile (not identical to fill).
	// (1,1,1) should be a fill tile.
	realParent := writer.tiles[[3]int{1, 1, 0}]
	fillParent := writer.tiles[[3]int{1, 1, 1}]
	if realParent == nil {
		t.Fatal("missing real parent tile at (1,1,0)")
	}
	if fillParent == nil {
		t.Fatal("missing fill parent tile at (1,1,1)")
	}

	// The real parent was downsampled from a mix of real+fill children,
	// so it should differ from the pure fill tile.
	if bytes.Equal(realParent, fillParent) {
		t.Error("real parent at (1,1,0) should differ from fill parent at (1,1,1)")
	}
}

// TestTransformRebuild_FillMissing_Dense verifies that when all positions have
// source data, no fill tiles are written.
func TestTransformRebuild_FillMissing_Dense(t *testing.T) {
	tileSize := 8
	fill := color.RGBA{255, 0, 0, 255}
	bounds := testBounds()

	// All 4 zoom-2 positions have source tiles (with distinct colors).
	colors := [4]color.RGBA{
		{100, 0, 0, 255},
		{0, 100, 0, 255},
		{0, 0, 100, 255},
		{100, 100, 0, 255},
	}
	positions := [4][3]int{{2, 2, 1}, {2, 3, 1}, {2, 2, 2}, {2, 3, 2}}
	tiles := make(map[[3]int][]byte)
	for i, pos := range positions {
		tiles[pos] = encodePNGTile(t, tileSize, colors[i])
	}

	reader := &mockPMTilesReader{
		tiles:  tiles,
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2, MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      2,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  2,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingBilinear,
		Mode:         TransformRebuild,
		FillMissing:  &fill,
		Bounds:       bounds,
	}

	stats, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if n := writer.tileCountAtZoom(2); n != 4 {
		t.Errorf("zoom 2: got %d tiles, want 4", n)
	}
	if stats.TileCount != 4 {
		t.Errorf("TileCount = %d, want 4", stats.TileCount)
	}

	// All tiles should be distinct (no pre-encoded fill reuse).
	seen := make(map[string]bool)
	for _, pos := range positions {
		data := writer.tiles[pos]
		if data == nil {
			t.Errorf("missing tile at %v", pos)
			continue
		}
		seen[string(data)] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct tile contents, got %d", len(seen))
	}
}

// TestTransformRebuild_NoFill verifies that without fill-color, only source
// tiles and their downsampled parents are produced (regression test).
func TestTransformRebuild_NoFill(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	bounds := testBounds()

	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, green),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      0,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  1,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingBilinear,
		Mode:         TransformRebuild,
		FillMissing:  nil, // no fill
		Bounds:       bounds,
	}

	stats, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	// Without fill, only the source tile is at max zoom.
	if n := writer.tileCountAtZoom(2); n != 1 {
		t.Errorf("zoom 2: got %d tiles, want 1", n)
	}
	// Lower zooms get downsampled parents (partial coverage).
	if n := writer.tileCountAtZoom(1); n != 1 {
		t.Errorf("zoom 1: got %d tiles, want 1", n)
	}
	if n := writer.tileCountAtZoom(0); n != 1 {
		t.Errorf("zoom 0: got %d tiles, want 1", n)
	}
	if stats.TileCount != 3 {
		t.Errorf("TileCount = %d, want 3", stats.TileCount)
	}
}

// Without fill, the lower zooms visit only the parents of the tiles below,
// not every position in the header bounds: those are -180..180 for data
// crossing the antimeridian, a full-width band whatever the data covers.
func TestTransformRebuild_NoFill_VisitsParentsOnly(t *testing.T) {
	const tileSize = 8
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{6, 63, 20}: encodePNGTile(t, tileSize, color.RGBA{0, 200, 0, 255}),
			{6, 0, 20}:  encodePNGTile(t, tileSize, color.RGBA{0, 0, 200, 255}),
		},
		header: pmtiles.Header{MinZoom: 6, MaxZoom: 6, MinLon: -180, MinLat: -60, MaxLon: 180, MaxLat: 60},
	}
	writer := newMockTileWriter()
	stats, err := Transform(TransformConfig{
		MinZoom: 0, MaxZoom: 6, TileSize: tileSize, Concurrency: 2,
		Encoder: testEncoder(t), SourceFormat: "png", Resampling: ResamplingBilinear,
		Mode: TransformRebuild, Bounds: [4]float32{-180, -60, 180, 60},
	}, reader, writer)
	if err != nil {
		t.Fatal(err)
	}
	for z, want := range []int{1, 2, 2, 2, 2, 2, 2} {
		if n := writer.tileCountAtZoom(z); n != want {
			t.Errorf("zoom %d: %d tiles, want %d", z, n, want)
		}
	}
	if stats.EmptyTiles != 0 {
		t.Errorf("EmptyTiles = %d, want 0: positions without children were visited", stats.EmptyTiles)
	}
}

// TestTransformRebuild_FillMissing_StatsConsistency verifies that Stats counters
// are consistent: fill tiles are counted as uniform.
func TestTransformRebuild_FillMissing_StatsConsistency(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	fill := color.RGBA{255, 0, 0, 255}
	bounds := testBounds()

	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, green),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	enc := testEncoder(t)

	cfg := TransformConfig{
		MinZoom:      0,
		MaxZoom:      2,
		TileSize:     tileSize,
		Concurrency:  1,
		Encoder:      enc,
		SourceFormat: "png",
		Resampling:   ResamplingNearest,
		Mode:         TransformRebuild,
		FillMissing:  &fill,
		Bounds:       bounds,
	}

	stats, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	// Fill tiles should be counted as uniform.
	// Zoom 2: 3 fill (uniform) + 1 real (uniform since solid green).
	// Zoom 1: 1 fill (uniform) + 1 real (downsampled, likely uniform or mixed).
	// Zoom 0: 1 real.
	if stats.UniformTiles == 0 {
		t.Error("expected some uniform tiles (fill tiles)")
	}
	// At minimum, 4 fill tiles: 3 at z2 + 1 at z1.
	if stats.UniformTiles < 4 {
		t.Errorf("UniformTiles = %d, want >= 4 (at least the fill tiles)", stats.UniformTiles)
	}
	if stats.TotalBytes <= 0 {
		t.Error("expected positive TotalBytes")
	}
	if stats.EmptyTiles != 0 {
		t.Errorf("EmptyTiles = %d, want 0 (fill should cover all positions)", stats.EmptyTiles)
	}
}

// TestTransformReencode_NodataColor_ReplacesTransparentPixels verifies that
// re-encode mode substitutes NodataColor for transparent pixels.
func TestTransformReencode_NodataColor_ReplacesTransparentPixels(t *testing.T) {
	tileSize := 8
	fill := color.RGBA{255, 0, 0, 255}
	bounds := testBounds()
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: encodePNGTile(t, tileSize, color.RGBA{}),
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	cfg := TransformConfig{
		MinZoom: 2, MaxZoom: 2, TileSize: tileSize, Concurrency: 1,
		Encoder: testEncoder(t), SourceFormat: "png",
		Mode: TransformReencode, NodataColor: &fill, Bounds: bounds,
	}
	if _, err := Transform(cfg, reader, writer); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	img, err := encode.DecodeImage(writer.tiles[[3]int{2, 2, 1}], "png")
	if err != nil {
		t.Fatalf("DecodeImage: %v", err)
	}
	if got := color.RGBAModel.Convert(img.At(3, 3)); got != fill {
		t.Errorf("pixel = %v, want fill %v", got, fill)
	}
}

func TestSelectTransformMode(t *testing.T) {
	tests := []struct {
		name       string
		opts       TransformModeOptions
		wantMode   TransformMode
		wantExtend bool
	}{
		{"default flags", TransformModeOptions{MinZoom: 5, SourceMinZoom: 5}, TransformPassthrough, false},
		{"min zoom below the source", TransformModeOptions{MinZoom: 2, SourceMinZoom: 5}, TransformPassthrough, true},
		{"min zoom above the source", TransformModeOptions{MinZoom: 7, SourceMinZoom: 5}, TransformPassthrough, false},
		// --fill-missing is not an input: it never forces decoding.
		{"fill-missing alone", TransformModeOptions{MinZoom: 5, SourceMinZoom: 5}, TransformPassthrough, false},
		{"nodata-color", TransformModeOptions{NodataColor: true, MinZoom: 5, SourceMinZoom: 5}, TransformReencode, false},
		{"format change", TransformModeOptions{FormatChanged: true, MinZoom: 5, SourceMinZoom: 5}, TransformReencode, false},
		{"format change and added levels", TransformModeOptions{FormatChanged: true, MinZoom: 0, SourceMinZoom: 5}, TransformReencode, true},
		{"rebuild", TransformModeOptions{Rebuild: true, MinZoom: 5, SourceMinZoom: 5}, TransformRebuild, false},
		{"rebuild below the source", TransformModeOptions{Rebuild: true, NodataColor: true, MinZoom: 0, SourceMinZoom: 5}, TransformRebuild, false},
	}
	for _, tt := range tests {
		mode, extend := SelectTransformMode(tt.opts)
		if mode != tt.wantMode || extend != tt.wantExtend {
			t.Errorf("%s: got mode %d, extendDown %v; want %d, %v", tt.name, mode, extend, tt.wantMode, tt.wantExtend)
		}
	}
}

// Passthrough to a min zoom below the source's copies the source levels
// byte for byte and downsamples the lowest one into the added levels.
func TestTransformPassthrough_AddsLowerLevels(t *testing.T) {
	const tileSize = 8
	bounds := testBounds()
	red := encodePNGTile(t, tileSize, color.RGBA{255, 0, 0, 255})
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{
			{2, 2, 1}: red, {2, 3, 1}: red, {2, 2, 2}: red,
		},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	for _, maxZoom := range []int{2, 1} {
		writer := newMockTileWriter()
		cfg := TransformConfig{
			MinZoom: 0, MaxZoom: maxZoom, TileSize: tileSize, Concurrency: 2,
			Encoder: testEncoder(t), SourceFormat: "png", Resampling: ResamplingBilinear,
			Mode: TransformPassthrough, Bounds: bounds, MemoryLimitBytes: -1, OutputDir: t.TempDir(),
		}
		stats, err := Transform(cfg, reader, writer)
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]int{0: 1, 1: 2, 2: 3}
		if maxZoom < 2 {
			want[2] = 0 // capped: the rebuild's z2 is dropped, not written
		}
		total := 0
		for z, n := range want {
			if got := writer.tileCountAtZoom(z); got != n {
				t.Errorf("max zoom %d: %d tiles at z%d, want %d", maxZoom, got, z, n)
			}
			total += n
		}
		if stats.TileCount != int64(total) {
			t.Errorf("max zoom %d: stats count %d tiles, want %d", maxZoom, stats.TileCount, total)
		}
		if maxZoom == 2 && !bytes.Equal(writer.tiles[[3]int{2, 2, 1}], red) {
			t.Error("the source level was not copied as-is")
		}
	}

	// Adding levels needs an encoder: an error, not a nil dereference.
	cfg := TransformConfig{MinZoom: 0, MaxZoom: 2, TileSize: tileSize, Concurrency: 1,
		SourceFormat: "png", Mode: TransformPassthrough, Bounds: bounds, OutputDir: t.TempDir()}
	if _, err := Transform(cfg, reader, newMockTileWriter()); err == nil {
		t.Error("adding levels without an encoder: want an error")
	}
}
