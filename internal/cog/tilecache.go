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

// TileCache provides an LRU-like cache for decoded COG tiles.
// This prevents re-reading and re-decoding the same source tiles
// when multiple output pixels map to the same source tile.
type TileCache struct {
	mu       sync.Mutex
	cache    map[tileKey]*cacheEntry
	order    []tileKey
	maxSize  int
}

type cacheEntry struct {
	img image.Image
}

// NewTileCache creates a tile cache with the given maximum number of entries.
func NewTileCache(maxEntries int) *TileCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	return &TileCache{
		cache:   make(map[tileKey]*cacheEntry, maxEntries),
		order:   make([]tileKey, 0, maxEntries),
		maxSize: maxEntries,
	}
}

// Get retrieves a tile from the cache. Returns nil if not found.
func (tc *TileCache) Get(path string, level, col, row int) image.Image {
	key := tileKey{path: path, level: level, col: col, row: row}
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if entry, ok := tc.cache[key]; ok {
		return entry.img
	}
	return nil
}

// Put stores a tile in the cache, evicting the oldest entry if full.
func (tc *TileCache) Put(path string, level, col, row int, img image.Image) {
	key := tileKey{path: path, level: level, col: col, row: row}
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if _, ok := tc.cache[key]; ok {
		return // already cached
	}

	// Evict if full.
	for len(tc.cache) >= tc.maxSize && len(tc.order) > 0 {
		oldest := tc.order[0]
		tc.order = tc.order[1:]
		delete(tc.cache, oldest)
	}

	tc.cache[key] = &cacheEntry{img: img}
	tc.order = append(tc.order, key)
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
