package cog

import (
	"image"
	"sync"
)

// tileKey identifies a tile within a specific file and IFD level.
type tileKey struct {
	path  string
	level int
	col   int
	row   int
}

// shardCount is the number of independent cache shards.
// Must be a power of two so we can use a bitmask for fast modulo.
const shardCount = 64

// tileKeyHash computes a fast hash for shard selection.
func tileKeyHash(key tileKey) uint64 {
	// FNV-1a inspired mixing of the key fields.
	h := uint64(14695981039346656037)
	h ^= uint64(key.level)
	h *= 1099511628211
	h ^= uint64(key.col)
	h *= 1099511628211
	h ^= uint64(key.row)
	h *= 1099511628211
	for i := 0; i < len(key.path); i++ {
		h ^= uint64(key.path[i])
		h *= 1099511628211
	}
	return h
}

// --- TileCache (image.Image tiles) ---

// TileCache provides a sharded LRU-like cache for decoded COG tiles.
// Sharding distributes lock contention across many independent mutexes,
// allowing concurrent access with minimal serialization.
type TileCache struct {
	shards [shardCount]tileCacheShard
}

type tileCacheShard struct {
	mu      sync.Mutex
	cache   map[tileKey]*cacheEntry
	order   []tileKey
	maxSize int
}

type cacheEntry struct {
	img image.Image
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
			cache:   make(map[tileKey]*cacheEntry, perShard),
			order:   make([]tileKey, 0, perShard),
			maxSize: perShard,
		}
	}
	return tc
}

// Get retrieves a tile from the cache. Returns nil if not found.
func (tc *TileCache) Get(path string, level, col, row int) image.Image {
	key := tileKey{path: path, level: level, col: col, row: row}
	s := &tc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return entry.img
	}
	return nil
}

// Put stores a tile in the cache, evicting the oldest entry in its shard if full.
func (tc *TileCache) Put(path string, level, col, row int, img image.Image) {
	key := tileKey{path: path, level: level, col: col, row: row}
	s := &tc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	if _, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return // already cached
	}
	// Evict if full.
	for len(s.cache) >= s.maxSize && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, oldest)
	}
	s.cache[key] = &cacheEntry{img: img}
	s.order = append(s.order, key)
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
	if img := cr.cache.Get(cr.path, level, col, row); img != nil {
		return img, nil
	}

	img, err := cr.Reader.ReadTile(level, col, row)
	if err != nil {
		return nil, err
	}

	cr.cache.Put(cr.path, level, col, row, img)
	return img, nil
}

// --- FloatTileCache (float32 tiles) ---

// FloatTileCache provides a sharded LRU-like cache for decoded float32 COG tiles.
type FloatTileCache struct {
	shards [shardCount]floatCacheShard
}

type floatCacheShard struct {
	mu      sync.Mutex
	cache   map[tileKey]*floatCacheEntry
	order   []tileKey
	maxSize int
}

type floatCacheEntry struct {
	data   []float32
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
			cache:   make(map[tileKey]*floatCacheEntry, perShard),
			order:   make([]tileKey, 0, perShard),
			maxSize: perShard,
		}
	}
	return fc
}

// Get retrieves a float tile from the cache. Returns nil if not found.
func (fc *FloatTileCache) Get(path string, level, col, row int) ([]float32, int, int) {
	key := tileKey{path: path, level: level, col: col, row: row}
	s := &fc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return entry.data, entry.width, entry.height
	}
	return nil, 0, 0
}

// Put stores a float tile in the cache, evicting the oldest entry in its shard if full.
func (fc *FloatTileCache) Put(path string, level, col, row int, data []float32, width, height int) {
	key := tileKey{path: path, level: level, col: col, row: row}
	s := &fc.shards[tileKeyHash(key)&(shardCount-1)]
	s.mu.Lock()
	if _, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return // already cached
	}
	for len(s.cache) >= s.maxSize && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, oldest)
	}
	s.cache[key] = &floatCacheEntry{data: data, width: width, height: height}
	s.order = append(s.order, key)
	s.mu.Unlock()
}
