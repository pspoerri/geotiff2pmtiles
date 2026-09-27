package cog

import (
	"image"
	"image/color"
	"math/rand/v2"
	"sync"
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

// cacheUnderTest runs the shared tests below against each of the three
// caches. Every key stores a value derived from it (keyVal), so a lookup can
// check it got its own tile back.
type cacheUnderTest struct {
	name  string
	put   func(k tileKey)
	get   func(k tileKey) (v int, ok bool)
	local func() cacheUnderTest
	stats func() (hits, misses int64)
}

func keyVal(k tileKey) int { return k.col*64 + k.row + 1 }

func tileCacheUnderTest(c *TileCache) cacheUnderTest {
	return cacheUnderTest{
		name: "TileCache",
		put: func(k tileKey) {
			c.Put(k.id, k.level, k.col, k.row, image.NewUniform(color.Gray16{Y: uint16(keyVal(k))}))
		},
		get: func(k tileKey) (int, bool) {
			img := c.Get(k.id, k.level, k.col, k.row)
			if img == nil {
				return 0, false
			}
			return int(img.(*image.Uniform).C.(color.Gray16).Y), true
		},
		local: func() cacheUnderTest { return tileCacheUnderTest(c.Local()) },
		stats: c.Stats,
	}
}

func floatCacheUnderTest(c *FloatTileCache) cacheUnderTest {
	return cacheUnderTest{
		name: "FloatTileCache",
		put:  func(k tileKey) { c.Put(k.id, k.level, k.col, k.row, []float32{float32(keyVal(k))}, 1, 1) },
		get: func(k tileKey) (int, bool) {
			d, _, _ := c.Get(k.id, k.level, k.col, k.row)
			if d == nil {
				return 0, false
			}
			return int(d[0]), true
		},
		local: func() cacheUnderTest { return floatCacheUnderTest(c.Local()) },
		stats: c.Stats,
	}
}

// Every other key is a cached empty tile (nil samples), which must behave
// like any other entry.
func uint16CacheUnderTest(c *Uint16TileCache) cacheUnderTest {
	return cacheUnderTest{
		name: "Uint16TileCache",
		put: func(k tileKey) {
			v := keyVal(k)
			var data []uint16
			if v%2 == 1 {
				data = []uint16{uint16(v)}
			}
			c.Put(k.id, k.level, k.col, k.row, data, v, 1, 1)
		},
		get: func(k tileKey) (int, bool) {
			d, w, _, _, ok := c.Get(k.id, k.level, k.col, k.row)
			if ok && (d == nil) != (w%2 == 0) {
				return -1, true // data and dimensions of different tiles
			}
			return w, ok
		},
		local: func() cacheUnderTest { return uint16CacheUnderTest(c.Local()) },
		stats: c.Stats,
	}
}

// cachesUnderTest returns the three caches, each with maxEntries.
func cachesUnderTest(maxEntries int) []cacheUnderTest {
	return []cacheUnderTest{
		tileCacheUnderTest(NewTileCache(maxEntries)),
		floatCacheUnderTest(NewFloatTileCache(maxEntries)),
		uint16CacheUnderTest(NewUint16TileCache(maxEntries)),
	}
}

// expect checks which keys are cached, and with their own value.
func (c cacheUnderTest) expect(t *testing.T, cached map[tileKey]bool) {
	t.Helper()
	for k, want := range cached {
		v, ok := c.get(k)
		if ok != want || (ok && v != keyVal(k)) {
			t.Errorf("%s: key %+v: got value %d cached=%v, want cached=%v", c.name, k, v, ok, want)
		}
	}
}

// TestCacheLRUEviction verifies that Get refreshes recency: after filling a
// shard, touching the oldest entry must protect it from the next eviction
// (under the old FIFO behavior it would have been evicted regardless).
func TestCacheLRUEviction(t *testing.T) {
	for _, c := range cachesUnderTest(1) { // perShard clamps to 4
		keys := keysInShard(0, 5)
		for _, k := range keys[:4] {
			c.put(k)
		}
		// Touch k0 so k1 becomes the LRU entry, then insert k4.
		if _, ok := c.get(keys[0]); !ok {
			t.Fatalf("%s: k0 missing right after Put", c.name)
		}
		c.put(keys[4])
		c.expect(t, map[tileKey]bool{keys[0]: true, keys[1]: false, keys[2]: true, keys[3]: true, keys[4]: true})
	}
}

// TestCachePutExistingRefreshes covers the duplicate-Put path: it should
// refresh recency rather than insert a second entry.
func TestCachePutExistingRefreshes(t *testing.T) {
	for _, c := range cachesUnderTest(1) {
		keys := keysInShard(0, 5)
		for _, k := range keys[:4] {
			c.put(k)
		}
		c.put(keys[0]) // refresh: k1 becomes LRU
		c.put(keys[4])
		c.expect(t, map[tileKey]bool{keys[0]: true, keys[1]: false, keys[4]: true})
	}
}

// A Local view must read through to and write through to the shared cache,
// and never answer from a memo slot that holds another key, or none.
func TestCacheLocalMemo(t *testing.T) {
	for _, c := range cachesUnderTest(256) {
		a, b := tileKey{id: 1, col: 2, row: 2}, tileKey{id: 1, col: 4, row: 2} // same memo slot
		zero := tileKey{}                                                      // the key of an unused slot
		c.put(a)
		l := c.local()
		l.expect(t, map[tileKey]bool{zero: false, a: true, b: false, {id: 2, col: 2, row: 2}: false})
		l.put(b)
		c.expect(t, map[tileKey]bool{b: true})
		l.expect(t, map[tileKey]bool{b: true, a: true})
	}
	if (*TileCache)(nil).Local() != nil || (*FloatTileCache)(nil).Local() != nil || (*Uint16TileCache)(nil).Local() != nil {
		t.Fatal("Local on a nil cache should stay nil")
	}
}

// Stats counts the lookups that reach the shared cache. A Local view counts
// the hits its memo answers too, and reports them on top of the parent's.
func TestCacheStats(t *testing.T) {
	for _, c := range cachesUnderTest(256) {
		k := tileKey{id: 1, col: 3, row: 5}
		c.put(k)
		c.get(k)                              // hit
		c.get(tileKey{id: 1, col: 4, row: 5}) // miss
		l := c.local()
		for range 10 {
			l.get(k) // one shared hit, then nine memo hits
		}
		l.get(tileKey{id: 2}) // a miss in the shared cache
		if h, m := c.stats(); h != 2 || m != 2 {
			t.Errorf("%s: root Stats = (%d, %d), want (2, 2)", c.name, h, m)
		}
		if h, m := l.stats(); h != 11 || m != 2 {
			t.Errorf("%s: Local Stats = (%d, %d), want (11, 2)", c.name, h, m)
		}
	}
}

// Many goroutines on the shared cache and on Local views, with the cache far
// too small so entries are evicted all the time: every hit must return the
// tile stored under its key. Run with -race.
func TestCacheConcurrent(t *testing.T) {
	for _, c := range cachesUnderTest(64) {
		var wg sync.WaitGroup
		for g := range 16 {
			wg.Go(func() {
				rng := rand.New(rand.NewPCG(uint64(g), 1))
				view := c
				for i := range 2000 {
					if g%2 == 1 && i%100 == 0 {
						view = c.local() // a fresh view per "rendered tile"
					}
					k := tileKey{id: 1, col: rng.IntN(64), row: rng.IntN(64)}
					if v, ok := view.get(k); !ok {
						view.put(k)
					} else if v != keyVal(k) {
						t.Errorf("%s: key %+v returned value %d, want %d", c.name, k, v, keyVal(k))
						return
					}
				}
			})
		}
		wg.Wait()
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
}
