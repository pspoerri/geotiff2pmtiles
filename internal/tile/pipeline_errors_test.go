package tile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// --- Helpers ---

// writeTestTIFF writes an uncompressed single-strip 8-bit RGB TIFF with a TFW
// sidecar placing its top-left corner at (lon, lat) with square pixels of
// deg degrees, so cog.Open infers EPSG:4326.
func writeTestTIFF(t *testing.T, w, h int, lon, lat, deg float64, pixel func(x, y, band int) uint8) string {
	t.Helper()
	bo := binary.LittleEndian
	const nEntries = 10
	bitsOff := 8 + 2 + nEntries*12 + 4
	dataOff := bitsOff + 6

	buf := []byte{'I', 'I'}
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, nEntries)
	entry := func(tag, typ uint16, count, value uint32) {
		buf = bo.AppendUint16(buf, tag)
		buf = bo.AppendUint16(buf, typ)
		buf = bo.AppendUint32(buf, count)
		buf = bo.AppendUint32(buf, value)
	}
	entry(256, 4, 1, uint32(w))       // ImageWidth
	entry(257, 4, 1, uint32(h))       // ImageLength
	entry(258, 3, 3, uint32(bitsOff)) // BitsPerSample 8,8,8
	entry(259, 3, 1, 1)               // Compression: none
	entry(262, 3, 1, 2)               // Photometric: RGB
	entry(273, 4, 1, uint32(dataOff)) // StripOffsets
	entry(277, 3, 1, 3)               // SamplesPerPixel
	entry(278, 4, 1, uint32(h))       // RowsPerStrip
	entry(279, 4, 1, uint32(w*h*3))   // StripByteCounts
	entry(284, 3, 1, 1)               // PlanarConfiguration: chunky
	buf = bo.AppendUint32(buf, 0)
	buf = append(buf, 8, 0, 8, 0, 8, 0)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for b := 0; b < 3; b++ {
				buf = append(buf, pixel(x, y, b))
			}
		}
	}

	path := filepath.Join(t.TempDir(), "src.tif")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	// TFW: pixel size, rotations, negative y size, then the centre of the
	// top-left pixel.
	tfw := fmt.Sprintf("%g\n0\n0\n%g\n%g\n%g\n", deg, -deg, lon+deg/2, lat-deg/2)
	if err := os.WriteFile(path[:len(path)-4]+".tfw", []byte(tfw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// noisePixel gives tiles that are neither uniform nor dedupable.
func noisePixel(x, y, band int) uint8 {
	h := uint32(x*73856093) ^ uint32(y*19349663) ^ uint32(band*83492791)
	h ^= h >> 13
	h *= 0x5bd1e995
	return uint8(h ^ h>>15)
}

// openTestSource opens a 20°×20° noise raster at 0..20°E, 0..20°N.
func openTestSource(t *testing.T) []*cog.Reader {
	t.Helper()
	r, err := cog.Open(writeTestTIFF(t, 200, 200, 0, 20, 0.1, noisePixel))
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return []*cog.Reader{r}
}

// failingWriter fails every WriteTile for which fail returns true and
// counts all calls.
type failingWriter struct {
	calls atomic.Int64
	fail  func(z int) bool
	hook  func(z int) // optional, runs before the fail check
}

var errWriteFailed = errors.New("disk full (simulated)")

func (w *failingWriter) WriteTile(z, x, y int, data []byte) error {
	w.calls.Add(1)
	if w.hook != nil {
		w.hook(z)
	}
	if w.fail != nil && w.fail(z) {
		return errWriteFailed
	}
	return nil
}

// spillFiles lists the disk tile store files left in dir.
func spillFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "pmtiles-tilestore-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// noiseReader serves the same non-uniform PNG at every position of zoom z.
func noiseReader(t *testing.T, z, tileSize int) *countingReader {
	t.Helper()
	td := newTileData(checkerImage(tileSize, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}), tileSize)
	data := encodePNG(t, td)
	tiles := map[[3]int][]byte{}
	for x := 0; x < 1<<z; x++ {
		for y := 0; y < 1<<z; y++ {
			tiles[[3]int{z, x, y}] = data
		}
	}
	return &countingReader{mockPMTilesReader: mockPMTilesReader{
		tiles:  tiles,
		header: pmtiles.Header{MinZoom: uint8(z), MaxZoom: uint8(z), MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85},
	}}
}

// countingReader counts ReadTile calls and fails call number failAt
// (0 = never).
type countingReader struct {
	mockPMTilesReader
	calls  atomic.Int64
	failAt int64
}

func (r *countingReader) ReadTile(z, x, y int) ([]byte, error) {
	if n := r.calls.Add(1); n == r.failAt {
		return nil, errors.New("corrupt tile (simulated)")
	}
	return r.mockPMTilesReader.ReadTile(z, x, y)
}

func worldTransformConfig(t *testing.T, mode TransformMode, maxZoom int) TransformConfig {
	return TransformConfig{
		MinZoom:          0,
		MaxZoom:          maxZoom,
		TileSize:         8,
		Concurrency:      4,
		Encoder:          testEncoder(t),
		SourceFormat:     "png",
		Mode:             mode,
		Bounds:           [4]float32{-180, -85, 180, 85},
		MemoryLimitBytes: 64 << 20,
		OutputDir:        t.TempDir(),
	}
}

// waitGoroutines waits up to a second for the goroutine count to fall back
// to at most n and returns the final count.
func waitGoroutines(n int) int {
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// --- Generate ---

// An error at a lower zoom level must not leave the previous level's spill
// file behind: `defer store.Close()` used to bind the initial placeholder.
func TestGenerate_ErrorRemovesSpillFiles(t *testing.T) {
	sources := openTestSource(t)
	outDir := t.TempDir()
	cfg := Config{
		Encoder:          testEncoder(t),
		OutputDir:        outDir,
		MinZoom:          3,
		MaxZoom:          6,
		TileSize:         64,
		Concurrency:      2,
		Bounds:           cog.MergedBoundsWGS84(sources),
		MemoryLimitBytes: 64 << 20,
	}
	w := &failingWriter{fail: func(z int) bool { return z == 5 }}

	if _, err := Generate(cfg, sources, w); !errors.Is(err, errWriteFailed) {
		t.Fatalf("Generate error = %v, want %v", err, errWriteFailed)
	}
	if left := spillFiles(t, outDir); len(left) > 0 {
		t.Errorf("spill files left after the error: %v", left)
	}
}

// The first worker error must stop the level: the other workers finish at
// most the tile they are on instead of rendering the rest of the level.
// Workers can still take a few tiles while the failing one is on its way
// to cancel, so the bound is loose; without cancellation every tile is
// written.
func TestGenerate_WorkerErrorCancelsLevel(t *testing.T) {
	sources := openTestSource(t)
	cfg := Config{
		Encoder:     testEncoder(t),
		OutputDir:   t.TempDir(),
		MinZoom:     8,
		MaxZoom:     8,
		TileSize:    64,
		Concurrency: 4,
		Bounds:      cog.MergedBoundsWGS84(sources),
	}
	total := len(coord.TilesInBounds(8, cfg.Bounds.MinLon, cfg.Bounds.MinLat, cfg.Bounds.MaxLon, cfg.Bounds.MaxLat))
	var failed atomic.Bool
	w := &failingWriter{fail: func(int) bool { return failed.CompareAndSwap(false, true) }}

	if _, err := Generate(cfg, sources, w); !errors.Is(err, errWriteFailed) {
		t.Fatalf("Generate error = %v, want %v", err, errWriteFailed)
	}
	if n := w.calls.Load(); n > int64(total/4) {
		t.Errorf("WriteTile called for %d of %d tiles, want the level to stop early", n, total)
	}
}

// --- Transform ---

var transformModes = map[string]TransformMode{
	"passthrough": TransformPassthrough,
	"reencode":    TransformReencode,
	"rebuild":     TransformRebuild,
}

func TestTransform_WorkerErrorCancelsLevel(t *testing.T) {
	for name, mode := range transformModes {
		t.Run(name, func(t *testing.T) {
			cfg := worldTransformConfig(t, mode, 5)
			cfg.MinZoom = 5
			reader := noiseReader(t, 5, cfg.TileSize)
			reader.failAt = 1
			total := len(reader.tiles)

			if _, err := Transform(cfg, reader, newMockTileWriter()); err == nil {
				t.Fatal("Transform: want the read error")
			}
			if n := reader.calls.Load(); n > int64(total/4) {
				t.Errorf("ReadTile called for %d of %d tiles, want the level to stop early", n, total)
			}
		})
	}
}

// When every worker has failed, the goroutine feeding them must not stay
// blocked on a full channel.
func TestTransform_ErrorLeaksNoGoroutines(t *testing.T) {
	for name, mode := range transformModes {
		t.Run(name, func(t *testing.T) {
			cfg := worldTransformConfig(t, mode, 5)
			cfg.MinZoom = 5
			reader := noiseReader(t, 5, cfg.TileSize)
			w := &failingWriter{fail: func(int) bool { return true }}

			before := runtime.NumGoroutine()
			for i := 0; i < 3; i++ {
				if _, err := Transform(cfg, reader, w); !errors.Is(err, errWriteFailed) {
					t.Fatalf("Transform error = %v, want %v", err, errWriteFailed)
				}
			}
			if after := waitGoroutines(before); after > before {
				t.Errorf("goroutines: %d before, %d after three failed runs", before, after)
			}
		})
	}
}

func TestTransformRebuild_ErrorRemovesSpillFiles(t *testing.T) {
	cfg := worldTransformConfig(t, TransformRebuild, 3)
	w := &failingWriter{fail: func(z int) bool { return z == 1 }}

	if _, err := Transform(cfg, noiseReader(t, 3, cfg.TileSize), w); !errors.Is(err, errWriteFailed) {
		t.Fatalf("Transform error = %v, want %v", err, errWriteFailed)
	}
	if left := spillFiles(t, cfg.OutputDir); len(left) > 0 {
		t.Errorf("spill files left after the error: %v", left)
	}
}

// A spill file that cannot be written must fail the run.
func TestTransformRebuild_SpillWriteErrorFails(t *testing.T) {
	cfg := worldTransformConfig(t, TransformRebuild, 3)
	cfg.OutputDir = filepath.Join(cfg.OutputDir, "missing")

	if _, err := Transform(cfg, noiseReader(t, 3, cfg.TileSize), newMockTileWriter()); err == nil {
		t.Fatal("Transform succeeded although no spill file could be created")
	}
}

// A spilled tile that cannot be read back must fail the run instead of
// being downsampled as a missing (transparent) quadrant.
func TestTransformRebuild_SpillReadErrorFails(t *testing.T) {
	cfg := worldTransformConfig(t, TransformRebuild, 3)
	cfg.Concurrency = 1
	var once sync.Once
	w := &failingWriter{hook: func(z int) {
		if z != 2 {
			return
		}
		// The z3 spill file is complete; lose its contents once the
		// first z2 tile has been built from it.
		once.Do(func() {
			for _, p := range spillFiles(t, cfg.OutputDir) {
				if err := os.Truncate(p, 0); err != nil {
					t.Error(err)
				}
			}
		})
	}}

	if _, err := Transform(cfg, noiseReader(t, 3, cfg.TileSize), w); err == nil {
		t.Fatal("Transform succeeded although spilled tiles could not be read back")
	}
}
