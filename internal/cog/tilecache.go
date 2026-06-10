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

// --- TileCache (image.Image tiles) ---

// TileCache provides a sharded LRU cache for decoded COG tiles.
// Sharding distributes lock contention across many independent mutexes,
// allowing concurrent access with minimal serialization. Each shard keeps
// a doubly-linked recency list: Get promotes the entry to the front, Put
// evicts from the back when the shard is full.
type TileCache struct {
	shards [shardCount]tileCacheShard
}

type tileCacheShard struct {
	cache   map[tileKey]*list.Element
	order   *list.List // front = most recently used; values are *cacheEntry
	maxSize int
	mu      sync.Mutex
}

type cacheEntry struct {
	img image.Image
	key tileKey
}

// NewTileCache creates a sharded tile cache with the given maximum total entries.
func NewTileCache(maxEntries int) *TileCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	perShard := maxEntries / shardCount
	if perShard < 4 {
		perShard = 4
	}
	tc := &TileCache{}
	for i := range tc.shards {
		tc.shards[i] = tileCacheShard{
			cache:   make(map[tileKey]*list.Element, perShard),
			order:   list.New(),
			maxSize: perShard,
		}
	}
	return tc
}

// Get retrieves a tile from the cache and marks it most-recently used.
// Returns nil if not found.
func (tc *TileCache) Get(id int, level, col, row int) image.Image {
	key := tileKey{id: id, level: level, col: col, row: row}
	s := &tc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	var img image.Image
	if el, ok := s.cache[key]; ok {
		s.order.MoveToFront(el)
		img = el.Value.(*cacheEntry).img
	}
	s.mu.Unlock()
	return img
}

// Put stores a tile in the cache, evicting the least-recently-used entry in
// its shard if full.
func (tc *TileCache) Put(id int, level, col, row int, img image.Image) {
	key := tileKey{id: id, level: level, col: col, row: row}
	s := &tc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	if el, ok := s.cache[key]; ok {
		s.order.MoveToFront(el)
		s.mu.Unlock()
		return // already cached
	}
	// Evict if full.
	for len(s.cache) >= s.maxSize {
		oldest := s.order.Back()
		if oldest == nil {
			break
		}
		s.order.Remove(oldest)
		delete(s.cache, oldest.Value.(*cacheEntry).key)
	}
	s.cache[key] = s.order.PushFront(&cacheEntry{img: img, key: key})
	s.mu.Unlock()
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
	shards [shardCount]floatCacheShard
}

type floatCacheShard struct {
	cache   map[tileKey]*list.Element
	order   *list.List // front = most recently used; values are *floatCacheEntry
	maxSize int
	mu      sync.Mutex
}

type floatCacheEntry struct {
	data   []float32
	key    tileKey
	width  int
	height int
}

// NewFloatTileCache creates a sharded float tile cache with the given maximum total entries.
func NewFloatTileCache(maxEntries int) *FloatTileCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	perShard := maxEntries / shardCount
	if perShard < 4 {
		perShard = 4
	}
	fc := &FloatTileCache{}
	for i := range fc.shards {
		fc.shards[i] = floatCacheShard{
			cache:   make(map[tileKey]*list.Element, perShard),
			order:   list.New(),
			maxSize: perShard,
		}
	}
	return fc
}

// Get retrieves a float tile from the cache and marks it most-recently used.
// Returns nil if not found.
func (fc *FloatTileCache) Get(id int, level, col, row int) ([]float32, int, int) {
	key := tileKey{id: id, level: level, col: col, row: row}
	s := &fc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	var data []float32
	var width, height int
	if el, ok := s.cache[key]; ok {
		s.order.MoveToFront(el)
		entry := el.Value.(*floatCacheEntry)
		data, width, height = entry.data, entry.width, entry.height
	}
	s.mu.Unlock()
	return data, width, height
}

// Put stores a float tile in the cache, evicting the least-recently-used
// entry in its shard if full.
func (fc *FloatTileCache) Put(id int, level, col, row int, data []float32, width, height int) {
	key := tileKey{id: id, level: level, col: col, row: row}
	s := &fc.shards[tileKeyHash(key)&(shardCount-1)]
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
		delete(s.cache, oldest.Value.(*floatCacheEntry).key)
	}
	s.cache[key] = s.order.PushFront(&floatCacheEntry{data: data, key: key, width: width, height: height})
	s.mu.Unlock()
}
