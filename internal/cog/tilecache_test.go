package cog

import (
	"image"
	"testing"
)

// keysInShard returns n distinct tile keys that all hash to the same shard.
func keysInShard(shard, n int) []tileKey {
	var keys []tileKey
	for col := 0; len(keys) < n; col++ {
		k := tileKey{id: 1, level: 0, col: col, row: 0}
		if tileKeyHash(k)&(shardCount-1) == uint64(shard) {
			keys = append(keys, k)
		}
	}
	return keys
}

// TestTileCacheLRUEviction verifies that Get refreshes recency: after filling
// a shard, touching the oldest entry must protect it from the next eviction
// (under the old FIFO behavior it would have been evicted regardless).
func TestTileCacheLRUEviction(t *testing.T) {
	tc := NewTileCache(1) // perShard clamps to 4
	keys := keysInShard(0, 5)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))

	// Fill the shard: k0..k3.
	for _, k := range keys[:4] {
		tc.Put(k.id, k.level, k.col, k.row, img)
	}
	// Touch k0 so k1 becomes the LRU entry.
	if got := tc.Get(keys[0].id, keys[0].level, keys[0].col, keys[0].row); got == nil {
		t.Fatal("k0 missing right after Put")
	}
	// Insert k4 → must evict k1, not k0.
	tc.Put(keys[4].id, keys[4].level, keys[4].col, keys[4].row, img)

	if got := tc.Get(keys[0].id, keys[0].level, keys[0].col, keys[0].row); got == nil {
		t.Error("k0 was evicted despite being recently used")
	}
	if got := tc.Get(keys[1].id, keys[1].level, keys[1].col, keys[1].row); got != nil {
		t.Error("k1 should have been evicted as least-recently used")
	}
	for _, k := range keys[2:] {
		if got := tc.Get(k.id, k.level, k.col, k.row); got == nil {
			t.Errorf("key %+v unexpectedly evicted", k)
		}
	}
}

// TestFloatTileCacheLRUEviction is the float-cache analog of the LRU test.
func TestFloatTileCacheLRUEviction(t *testing.T) {
	fc := NewFloatTileCache(1)
	keys := keysInShard(0, 5)
	data := []float32{1, 2, 3, 4}

	for _, k := range keys[:4] {
		fc.Put(k.id, k.level, k.col, k.row, data, 2, 2)
	}
	if d, _, _ := fc.Get(keys[0].id, keys[0].level, keys[0].col, keys[0].row); d == nil {
		t.Fatal("k0 missing right after Put")
	}
	fc.Put(keys[4].id, keys[4].level, keys[4].col, keys[4].row, data, 2, 2)

	if d, w, h := fc.Get(keys[0].id, keys[0].level, keys[0].col, keys[0].row); d == nil || w != 2 || h != 2 {
		t.Errorf("k0 was evicted despite being recently used (data=%v w=%d h=%d)", d, w, h)
	}
	if d, _, _ := fc.Get(keys[1].id, keys[1].level, keys[1].col, keys[1].row); d != nil {
		t.Error("k1 should have been evicted as least-recently used")
	}
}

// TestTileCachePutExistingRefreshes covers the duplicate-Put path: it should
// refresh recency rather than insert a second entry.
func TestTileCachePutExistingRefreshes(t *testing.T) {
	tc := NewTileCache(1)
	keys := keysInShard(0, 5)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))

	for _, k := range keys[:4] {
		tc.Put(k.id, k.level, k.col, k.row, img)
	}
	// Re-Put k0 (duplicate) → refresh, k1 becomes LRU.
	tc.Put(keys[0].id, keys[0].level, keys[0].col, keys[0].row, img)
	tc.Put(keys[4].id, keys[4].level, keys[4].col, keys[4].row, img)

	if got := tc.Get(keys[0].id, keys[0].level, keys[0].col, keys[0].row); got == nil {
		t.Error("k0 was evicted despite duplicate Put refresh")
	}
	if got := tc.Get(keys[1].id, keys[1].level, keys[1].col, keys[1].row); got != nil {
		t.Error("k1 should have been evicted as least-recently used")
	}
}

func TestTileCacheLocal(t *testing.T) {
	tc := NewTileCache(256)
	a := image.NewGray(image.Rect(0, 0, 1, 1))
	b := image.NewGray(image.Rect(0, 0, 1, 1))
	tc.Put(1, 0, 2, 2, a)

	l := tc.Local()
	if l.Get(1, 0, 2, 2) != a {
		t.Fatal("local view should read through to the shared cache")
	}
	// Same memo slot (even col, even row), different key: must not return a.
	if got := l.Get(1, 0, 4, 2); got != nil {
		t.Fatalf("slot collision returned a stale tile: %v", got)
	}
	if got := l.Get(2, 0, 2, 2); got != nil {
		t.Fatalf("different reader id returned a stale tile: %v", got)
	}
	l.Put(1, 0, 4, 2, b)
	if tc.Get(1, 0, 4, 2) != b {
		t.Fatal("Put through a local view should reach the shared cache")
	}
	if l.Get(1, 0, 4, 2) != b || l.Get(1, 0, 2, 2) != a {
		t.Fatal("local view returned the wrong tile after slot reuse")
	}
	if (*TileCache)(nil).Local() != nil {
		t.Fatal("Local on a nil cache should stay nil")
	}
}

func TestFloatTileCacheLocal(t *testing.T) {
	fc := NewFloatTileCache(256)
	a, b := []float32{1}, []float32{2, 3}
	fc.Put(1, 0, 2, 2, a, 1, 1)

	l := fc.Local()
	if d, w, h := l.Get(1, 0, 2, 2); &d[0] != &a[0] || w != 1 || h != 1 {
		t.Fatal("local view should read through to the shared cache")
	}
	// Same memo slot (even col, even row), different key: must not return a.
	if d, _, _ := l.Get(1, 0, 4, 2); d != nil {
		t.Fatalf("slot collision returned a stale tile: %v", d)
	}
	if d, _, _ := l.Get(2, 0, 2, 2); d != nil {
		t.Fatalf("different reader id returned a stale tile: %v", d)
	}
	l.Put(1, 0, 4, 2, b, 2, 1)
	if d, w, h := fc.Get(1, 0, 4, 2); &d[0] != &b[0] || w != 2 || h != 1 {
		t.Fatal("Put through a local view should reach the shared cache")
	}
	if d, w, _ := l.Get(1, 0, 4, 2); &d[0] != &b[0] || w != 2 {
		t.Fatal("local view returned the wrong tile after slot reuse")
	}
	if d, _, _ := l.Get(1, 0, 2, 2); &d[0] != &a[0] {
		t.Fatal("local view lost the evicted slot's tile from the shared cache")
	}
	if (*FloatTileCache)(nil).Local() != nil {
		t.Fatal("Local on a nil cache should stay nil")
	}
}
