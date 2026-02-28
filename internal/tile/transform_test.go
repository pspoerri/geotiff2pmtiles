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

// TestTransformRebuild_FillColor_Sparse verifies that sparse source data
// combined with fill-color produces tiles at all positions within bounds.
func TestTransformRebuild_FillColor_Sparse(t *testing.T) {
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
		FillColor:    &fill,
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

// TestTransformRebuild_FillColor_FillTilesIdentical verifies that all fill
// tiles at the same zoom level contain identical pre-encoded bytes.
func TestTransformRebuild_FillColor_FillTilesIdentical(t *testing.T) {
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
		FillColor:    &fill,
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

// TestTransformRebuild_FillColor_RealPositionPropagation verifies that real
// positions propagate correctly to parent zoom levels: only parents with at
// least one real child go through the downsample pipeline.
func TestTransformRebuild_FillColor_RealPositionPropagation(t *testing.T) {
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
		FillColor:    &fill,
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

// TestTransformRebuild_FillColor_Dense verifies that when all positions have
// source data, no fill tiles are written.
func TestTransformRebuild_FillColor_Dense(t *testing.T) {
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
		FillColor:    &fill,
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
		FillColor:    nil, // no fill
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

// TestTransformRebuild_FillColor_StatsConsistency verifies that Stats counters
// are consistent: fill tiles are counted as uniform.
func TestTransformRebuild_FillColor_StatsConsistency(t *testing.T) {
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
		FillColor:    &fill,
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
