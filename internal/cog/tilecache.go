package cog

import (
	"container/list"
	"image"
	"sync"
)

// tileKey identifies a tile within a specific file and IFD level.
// Uses a numeric reader ID instead of the file path string for fast hashing
// and comparison — the original string-keyed version consumed 18% of total CPU
// time on the inner-loop cache lookups.
type tileKey struct {
	id    int
	level int
	col   int
	row   int
}

// shardCount is the number of independent cache shards.
// Must be a power of two so we can use a bitmask for fast modulo.
const shardCount = 64

// tileKeyHash computes a fast hash for shard selection.
// All fields are integers so this is a few multiplies with no string iteration.
func tileKeyHash(key tileKey) uint64 {
	// FNV-1a inspired mixing of the integer key fields.
	h := uint64(14695981039346656037)
	h ^= uint64(key.id)
	h *= 1099511628211
	h ^= uint64(key.level)
	h *= 1099511628211
	h ^= uint64(key.col)
	h *= 1099511628211
	h ^= uint64(key.row)
	h *= 1099511628211
	return h
}

// --- shardedLRU and its Local views: behind all three caches ---

// shardedLRU is a sharded LRU cache of tiles. Sharding distributes lock
// contention across many independent mutexes, allowing concurrent access
// with minimal serialization. Each shard keeps a doubly-linked recency list:
// a lookup (lruView.sharedGet) promotes the entry to the front, put evicts
// from the back when the shard is full.
type shardedLRU[V any] struct {
	shards [shardCount]lruShard[V]
}

type lruShard[V any] struct {
	cache   map[tileKey]*list.Element
	order   *list.List // front = most recently used; values are *lruEntry[V]
	maxSize int
	mu      sync.Mutex
	// Counted under mu, which a lookup holds anyway. One global counter would
	// be a cache line that every lookup from every goroutine writes.
	hits, misses int64
}

type lruEntry[V any] struct {
	val V
	key tileKey
}

func newShardedLRU[V any](maxEntries int) *shardedLRU[V] {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	perShard := maxEntries / shardCount
	if perShard < 4 {
		perShard = 4
	}
	c := &shardedLRU[V]{}
	for i := range c.shards {
		c.shards[i] = lruShard[V]{
			cache:   make(map[tileKey]*list.Element, perShard),
			order:   list.New(),
			maxSize: perShard,
		}
	}
	return c
}

// put stores a tile, evicting the least-recently-used entry in its shard if
// full.
func (c *shardedLRU[V]) put(key tileKey, v V) {
	s := &c.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	if el, ok := s.cache[key]; ok {
		s.order.MoveToFront(el)
		s.mu.Unlock()
		return // already cached
	}
	for len(s.cache) >= s.maxSize {
		oldest := s.order.Back()
		if oldest == nil {
			break
		}
		s.order.Remove(oldest)
		delete(s.cache, oldest.Value.(*lruEntry[V]).key)
	}
	s.cache[key] = s.order.PushFront(&lruEntry[V]{val: v, key: key})
	s.mu.Unlock()
}

func (c *shardedLRU[V]) stats() (hits, misses int64) {
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		hits += s.hits
		misses += s.misses
		s.mu.Unlock()
	}
	return hits, misses
}

// lruView is a shardedLRU as a cache type sees it: either the shared cache
// itself, or a Local view of it that remembers the last few tiles it returned.
type lruView[V any] struct {
	lru *shardedLRU[V]

	// Set only on views created by Local.
	local    bool
	memoHits int64 // goroutine-local, like the memo
	memo     [4]memoEntry[V]
}

// memoEntry holds a copy of the value, not a pointer into the shared cache:
// a memo hit then reads only memory of its own goroutine, never an entry that
// shares a cache line with list elements other goroutines are relinking.
type memoEntry[V any] struct {
	val V
	key tileKey
	// present distinguishes a cached empty tile from an empty memo slot.
	present bool
}

// memoSlot maps the 2×2 tile neighbourhood a resampling kernel can straddle
// to four distinct slots.
func memoSlot(col, row int) int { return col&1 | row&1<<1 }

// A lookup is memoGet, then sharedGet if that returns nil. Both return a
// pointer to the cached value, to be read at once (the next lookup may
// overwrite a memo slot) and never modified, or nil; a cached zero value (an
// empty tile) is still a hit. The exported Get methods make the two calls
// themselves rather than through a combined method: memoGet makes no calls,
// so it is inlined into them, and a memo hit, which nearly every lookup of a
// render is, costs no call beyond Get.

// memoGet answers from the memo. A root view's memo stays empty.
func (v *lruView[V]) memoGet(key tileKey) *V {
	if m := &v.memo[memoSlot(key.col, key.row)]; m.present && m.key == key {
		v.memoHits++
		return &m.val
	}
	return nil
}

// sharedGet looks key up in the shared cache and marks it most-recently used.
// On a Local view a hit is remembered in the memo.
func (v *lruView[V]) sharedGet(key tileKey) *V {
	s := &v.lru.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	var val *V
	if el, found := s.cache[key]; found {
		s.order.MoveToFront(el)
		val = &el.Value.(*lruEntry[V]).val
		s.hits++
	} else {
		s.misses++
	}
	s.mu.Unlock()
	if v.local && val != nil {
		v.memo[memoSlot(key.col, key.row)] = memoEntry[V]{val: *val, key: key, present: true}
	}
	return val
}

func (v *lruView[V]) put(key tileKey, val V) {
	if v.local {
		v.memo[memoSlot(key.col, key.row)] = memoEntry[V]{val: val, key: key, present: true}
	}
	v.lru.put(key, val)
}

// stats reports the shared cache's counts, plus the view's own memo hits.
func (v *lruView[V]) stats() (hits, misses int64) {
	hits, misses = v.lru.stats()
	return hits + v.memoHits, misses
}

// --- TileCache (image.Image tiles) ---

// TileCache provides a sharded LRU cache for decoded COG tiles.
// Sharding distributes lock contention across many independent mutexes,
// allowing concurrent access with minimal serialization. Each shard keeps
// a doubly-linked recency list: Get promotes the entry to the front, Put
// evicts from the back when the shard is full.
type TileCache struct {
	c lruView[image.Image]
}

// NewTileCache creates a sharded tile cache with the given maximum total entries.
func NewTileCache(maxEntries int) *TileCache {
	return &TileCache{lruView[image.Image]{lru: newShardedLRU[image.Image](maxEntries)}}
}

// Local returns a single-goroutine view of tc that remembers the last few
// tiles it returned, so per-pixel lookups that keep hitting the same source
// tile skip the shard mutex and LRU update entirely. At high concurrency that
// lock traffic otherwise dominates: neighbouring output tiles read the same
// source tiles and therefore the same shards. Create one per rendered tile;
// the view must not be shared between goroutines.
func (tc *TileCache) Local() *TileCache {
	if tc == nil {
		return nil
	}
	return &TileCache{lruView[image.Image]{lru: tc.c.lru, local: true}}
}

// Stats reports cumulative hits and misses of the lookups that reach the
// shared cache, for sizing it. On a Local view it adds the hits that view's
// memo answered, which never reach the shared cache.
func (tc *TileCache) Stats() (hits, misses int64) { return tc.c.stats() }

// Get retrieves a tile from the cache and marks it most-recently used.
// Returns nil if not found.
func (tc *TileCache) Get(id int, level, col, row int) image.Image {
	key := tileKey{id: id, level: level, col: col, row: row}
	img := tc.c.memoGet(key) // see lruView.memoGet
	if img == nil {
		if img = tc.c.sharedGet(key); img == nil {
			return nil
		}
	}
	return *img
}

// Put stores a tile in the cache, evicting the least-recently-used entry in
// its shard if full.
func (tc *TileCache) Put(id int, level, col, row int, img image.Image) {
	tc.c.put(tileKey{id: id, level: level, col: col, row: row}, img)
}

// CachedReader wraps a Reader with a tile cache.
type CachedReader struct {
	*Reader
	cache *TileCache
}

// NewCachedReader wraps a Reader with shared tile cache.
func NewCachedReader(r *Reader, cache *TileCache) *CachedReader {
	return &CachedReader{Reader: r, cache: cache}
}

// ReadTileCached reads a tile, using the cache if available.
func (cr *CachedReader) ReadTileCached(level, col, row int) (image.Image, error) {
	if img := cr.cache.Get(cr.id, level, col, row); img != nil {
		return img, nil
	}

	img, err := cr.Reader.ReadTile(level, col, row)
	if err != nil {
		return nil, err
	}

	cr.cache.Put(cr.id, level, col, row, img)
	return img, nil
}

// --- FloatTileCache (float32 tiles) ---

// FloatTileCache provides a sharded LRU cache for decoded float32 COG tiles.
type FloatTileCache struct {
	c lruView[floatTile]
}

type floatTile struct {
	data          []float32
	width, height int
}

// NewFloatTileCache creates a sharded float tile cache with the given maximum total entries.
func NewFloatTileCache(maxEntries int) *FloatTileCache {
	return &FloatTileCache{lruView[floatTile]{lru: newShardedLRU[floatTile](maxEntries)}}
}

// Local returns a single-goroutine view of fc; see TileCache.Local.
func (fc *FloatTileCache) Local() *FloatTileCache {
	if fc == nil {
		return nil
	}
	return &FloatTileCache{lruView[floatTile]{lru: fc.c.lru, local: true}}
}

// Stats reports cumulative hits and misses; see TileCache.Stats.
func (fc *FloatTileCache) Stats() (hits, misses int64) { return fc.c.stats() }

// Get retrieves a float tile from the cache and marks it most-recently used.
// Returns nil if not found.
func (fc *FloatTileCache) Get(id int, level, col, row int) ([]float32, int, int) {
	key := tileKey{id: id, level: level, col: col, row: row}
	t := fc.c.memoGet(key) // see lruView.memoGet
	if t == nil {
		if t = fc.c.sharedGet(key); t == nil {
			return nil, 0, 0
		}
	}
	return t.data, t.width, t.height
}

// Put stores a float tile in the cache, evicting the least-recently-used
// entry in its shard if full.
func (fc *FloatTileCache) Put(id int, level, col, row int, data []float32, width, height int) {
	fc.c.put(tileKey{id: id, level: level, col: col, row: row}, floatTile{data, width, height})
}

// --- Uint16TileCache (raw integer tiles) ---
//
// A cache for ReadUint16Tile's raw samples. It differs from FloatTileCache in one way that matters: a hit is reported
// by an explicit ok, not by a non-nil slice. ReadUint16Tile legitimately
// returns nil samples for an empty tile, and there are a great many empty
// tiles at the edge of a Sentinel-2 datastrip, so treating nil as a miss
// would re-read those on every single pixel that lands in one.

// Uint16TileCache provides a sharded LRU cache for decoded uint16 COG tiles.
type Uint16TileCache struct {
	c lruView[uint16Tile]
}

type uint16Tile struct {
	data               []uint16
	width, height, spp int
}

// NewUint16TileCache creates a sharded uint16 tile cache with the given
// maximum total entries.
func NewUint16TileCache(maxEntries int) *Uint16TileCache {
	return &Uint16TileCache{lruView[uint16Tile]{lru: newShardedLRU[uint16Tile](maxEntries)}}
}

// Stats reports cumulative hits and misses; see TileCache.Stats.
func (uc *Uint16TileCache) Stats() (hits, misses int64) { return uc.c.stats() }

// Local returns a single-goroutine view of uc; see TileCache.Local.
func (uc *Uint16TileCache) Local() *Uint16TileCache {
	if uc == nil {
		return nil
	}
	return &Uint16TileCache{lruView[uint16Tile]{lru: uc.c.lru, local: true}}
}

// Get retrieves a uint16 tile from the cache and marks it most-recently used.
// ok reports whether the tile was cached at all; data may be nil for a cached
// empty tile.
func (uc *Uint16TileCache) Get(id int, level, col, row int) (data []uint16, width, height, spp int, ok bool) {
	key := tileKey{id: id, level: level, col: col, row: row}
	t := uc.c.memoGet(key) // see lruView.memoGet
	if t == nil {
		if t = uc.c.sharedGet(key); t == nil {
			return nil, 0, 0, 0, false
		}
	}
	return t.data, t.width, t.height, t.spp, true
}

// Put stores a uint16 tile in the cache, evicting the least-recently-used
// entry in its shard if full.
func (uc *Uint16TileCache) Put(id int, level, col, row int, data []uint16, width, height, spp int) {
	uc.c.put(tileKey{id: id, level: level, col: col, row: row}, uint16Tile{data, width, height, spp})
}
