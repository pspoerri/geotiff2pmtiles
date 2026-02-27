package tile

import (
	"image/color"
	"os"
	"sync"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// encodePNG encodes a TileData as PNG bytes for use in store tests.
func encodePNG(t *testing.T, td *TileData) []byte {
	t.Helper()
	enc := &encode.PNGEncoder{}
	data, err := enc.Encode(td.AsImage())
	if err != nil {
		t.Fatalf("encodePNG: %v", err)
	}
	return data
}

// --- Basic Put/Get ---

func TestDiskTileStore_PutGet_Uniform(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	c := color.RGBA{255, 0, 0, 255}
	td := newTileDataUniform(c, 4)
	store.Put(1, 2, 3, td, nil)

	got := store.Get(1, 2, 3)
	if got == nil {
		t.Fatal("expected non-nil for stored uniform tile")
	}
	if !got.IsUniform() {
		t.Error("expected uniform tile back")
	}
	if got.Color() != c {
		t.Errorf("color = %v, want %v", got.Color(), c)
	}
}

func TestDiskTileStore_PutGet_NonUniform(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	img := checkerImage(4, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255})
	td := newTileData(img, 4)
	encoded := encodePNG(t, td)
	store.Put(2, 5, 7, td, encoded)

	got := store.Get(2, 5, 7)
	if got == nil {
		t.Fatal("expected non-nil for stored non-uniform tile")
	}
}

func TestDiskTileStore_GetMissing(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4})
	defer store.Close()

	if got := store.Get(0, 0, 0); got != nil {
		t.Errorf("expected nil for missing tile, got %T", got)
	}
}

// --- Len ---

func TestDiskTileStore_Len(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	if l := store.Len(); l != 0 {
		t.Errorf("initial Len = %d, want 0", l)
	}

	// Uniform tile.
	store.Put(0, 0, 0, newTileDataUniform(color.RGBA{255, 0, 0, 255}, 4), nil)
	if l := store.Len(); l != 1 {
		t.Errorf("Len after 1 put = %d, want 1", l)
	}

	// Non-uniform tile.
	img := grayCheckerImage(4, 50, 100)
	td := newTileData(img, 4)
	store.Put(0, 1, 0, td, encodePNG(t, td))
	if l := store.Len(); l != 2 {
		t.Errorf("Len after 2 puts = %d, want 2", l)
	}
}

// --- Disk spill ---

func TestDiskTileStore_DiskSpill_PutGetRoundtrip(t *testing.T) {
	dir := t.TempDir()

	// MemoryLimitBytes > 0 enables the I/O goroutine and disk spilling.
	// Use a large-enough value so that mapOverhead (64 bytes/tile) never
	// permanently triggers backpressure for our small test set.
	store := NewDiskTileStore(DiskTileStoreConfig{
		TileSize:         4,
		TempDir:          dir,
		MemoryLimitBytes: 1024 * 1024, // 1 MB: enables spilling, no deadlock
		Format:           "png",
	})
	defer store.Close()

	img := grayCheckerImage(4, 100, 200)
	td := newTileData(img, 4)
	encoded := encodePNG(t, td)

	const nTiles = 20
	for i := 0; i < nTiles; i++ {
		store.Put(5, i, 0, td, encoded)
	}
	store.Drain()

	for i := 0; i < nTiles; i++ {
		if got := store.Get(5, i, 0); got == nil {
			t.Errorf("tile (5,%d,0) missing after drain", i)
		}
	}
}

func TestDiskTileStore_DiskSpill_TempFileCreated(t *testing.T) {
	dir := t.TempDir()

	store := NewDiskTileStore(DiskTileStoreConfig{
		TileSize:         4,
		TempDir:          dir,
		MemoryLimitBytes: 1024 * 1024,
		Format:           "png",
	})
	defer store.Close()

	// Before any spill, temp file should not exist.
	if p := store.TempFilePath(); p != "" {
		t.Errorf("TempFilePath before any write = %q, want empty", p)
	}

	img := grayCheckerImage(4, 10, 20)
	td := newTileData(img, 4)
	store.Put(1, 0, 0, td, encodePNG(t, td))
	store.Drain()

	if p := store.TempFilePath(); p == "" {
		t.Error("TempFilePath after disk write should be non-empty")
	}
}

// --- Close removes temp file ---

func TestDiskTileStore_Close_RemovesTempFile(t *testing.T) {
	dir := t.TempDir()

	store := NewDiskTileStore(DiskTileStoreConfig{
		TileSize:         4,
		TempDir:          dir,
		MemoryLimitBytes: 1024 * 1024,
		Format:           "png",
	})

	img := grayCheckerImage(4, 10, 20)
	td := newTileData(img, 4)
	store.Put(1, 0, 0, td, encodePNG(t, td))
	store.Drain()

	path := store.TempFilePath()
	if path == "" {
		t.Fatal("expected temp file to have been created")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file missing before Close: %v", err)
	}

	store.Close()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp file still exists after Close (err=%v)", err)
	}
}

// --- TempFilePath with no spill ---

func TestDiskTileStore_TempFilePath_NoSpill(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	if p := store.TempFilePath(); p != "" {
		t.Errorf("TempFilePath for no-spill store = %q, want empty", p)
	}
}

// --- Drain idempotency ---

func TestDiskTileStore_Drain_Idempotent(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{
		TileSize:         4,
		MemoryLimitBytes: 1024 * 1024,
		Format:           "png",
	})
	defer store.Close()

	img := grayCheckerImage(4, 1, 2)
	td := newTileData(img, 4)
	store.Put(0, 0, 0, td, encodePNG(t, td))

	// Multiple Drain calls must not panic or deadlock.
	store.Drain()
	store.Drain()
	store.Drain()
}

// --- Stats ---

func TestDiskTileStore_Stats_ReturnsNonEmpty(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	store.Put(0, 0, 0, newTileDataUniform(color.RGBA{100, 0, 0, 255}, 4), nil)
	s := store.Stats()
	if s == "" {
		t.Error("Stats() returned empty string")
	}
}

// --- MemoryBytes ---

func TestDiskTileStore_MemoryBytes_IncreasesAfterPut(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	before := store.MemoryBytes()
	store.Put(0, 0, 0, newTileDataUniform(color.RGBA{100, 0, 0, 255}, 4), nil)
	after := store.MemoryBytes()

	if after <= before {
		t.Errorf("MemoryBytes should increase after Put: before=%d after=%d", before, after)
	}
}

// --- Concurrent Put ---

func TestDiskTileStore_ConcurrentPut(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{
		InitialCapacity: 100,
		TileSize:        4,
		Format:          "png",
	})
	defer store.Close()

	img := grayCheckerImage(4, 50, 150)
	td := newTileData(img, 4)
	encoded := encodePNG(t, td)

	const nGoroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < nGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			store.Put(7, idx, 0, td, encoded)
		}(i)
	}
	wg.Wait()

	if l := store.Len(); l != nGoroutines {
		t.Errorf("Len after concurrent puts = %d, want %d", l, nGoroutines)
	}
}

// --- Multiple uniform tiles with different keys ---

func TestDiskTileStore_MultipleUniforms_SeparateKeys(t *testing.T) {
	store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 4, Format: "png"})
	defer store.Close()

	colors := []color.RGBA{
		{255, 0, 0, 255},
		{0, 255, 0, 255},
		{0, 0, 255, 255},
	}
	for i, c := range colors {
		store.Put(0, i, 0, newTileDataUniform(c, 4), nil)
	}

	for i, want := range colors {
		got := store.Get(0, i, 0)
		if got == nil {
			t.Fatalf("tile (0,%d,0) missing", i)
		}
		if got.Color() != want {
			t.Errorf("tile (0,%d,0): color=%v want=%v", i, got.Color(), want)
		}
	}
}
