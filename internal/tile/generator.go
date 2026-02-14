package tile

import (
	"fmt"
	"image"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// Resampling selects the interpolation method.
type Resampling int

const (
	// ResamplingBilinear interpolates between 4 neighboring pixels (smooth).
	ResamplingBilinear Resampling = iota
	// ResamplingNearest picks the closest pixel (sharp, fast).
	ResamplingNearest
)

// ParseResampling converts a string to a Resampling constant.
func ParseResampling(s string) (Resampling, error) {
	switch s {
	case "bilinear":
		return ResamplingBilinear, nil
	case "nearest":
		return ResamplingNearest, nil
	default:
		return 0, fmt.Errorf("unknown resampling method %q (supported: bilinear, nearest)", s)
	}
}

// Config holds tile generation configuration.
type Config struct {
	MinZoom     int
	MaxZoom     int
	TileSize    int
	Concurrency int
	Verbose     bool
	Encoder     encode.Encoder
	Bounds      cog.Bounds
	Resampling  Resampling
	IsTerrarium bool // true for float GeoTIFF → Terrarium encoding
}

// Stats holds generation statistics.
type Stats struct {
	TileCount  int64
	EmptyTiles int64
	TotalBytes int64
}

// TileWriter is the interface for writing tiles (implemented by pmtiles.Writer).
type TileWriter interface {
	WriteTile(z, x, y int, data []byte) error
}

// tileJob represents a single tile to generate.
type tileJob struct {
	Z, X, Y int
}

// TileImageStore is a concurrent-safe store for decoded tile images.
// Used to hold tiles from the current zoom level so the next (lower)
// zoom level can downsample from them.
type TileImageStore struct {
	mu    sync.RWMutex
	tiles map[[3]int]*image.RGBA
}

func newTileImageStore(capacity int) *TileImageStore {
	return &TileImageStore{
		tiles: make(map[[3]int]*image.RGBA, capacity),
	}
}

// Get retrieves a tile image. Returns nil if not present.
func (s *TileImageStore) Get(z, x, y int) *image.RGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tiles[[3]int{z, x, y}]
}

// Put stores a tile image.
func (s *TileImageStore) Put(z, x, y int, img *image.RGBA) {
	s.mu.Lock()
	s.tiles[[3]int{z, x, y}] = img
	s.mu.Unlock()
}

// Clear removes all entries.
func (s *TileImageStore) Clear() {
	s.mu.Lock()
	s.tiles = make(map[[3]int]*image.RGBA)
	s.mu.Unlock()
}

// Len returns the number of stored tiles.
func (s *TileImageStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tiles)
}

// Generate produces tiles for all zoom levels and writes them via the TileWriter.
//
// The pipeline uses a pyramid approach:
//  1. The maximum zoom level is rendered from source COG data (per-pixel reprojection).
//  2. Each lower zoom level is built by downsampling 2x2 groups of child tiles.
//
// This avoids redundant COG reads and coordinate transforms for lower zoom levels.
func Generate(cfg Config, sources []*cog.Reader, writer TileWriter) (Stats, error) {
	if len(sources) == 0 {
		return Stats{}, fmt.Errorf("no source files")
	}

	// Determine the projection from the first source.
	epsg := sources[0].EPSG()
	proj := coord.ForEPSG(epsg)
	if proj == nil {
		return Stats{}, fmt.Errorf("unsupported EPSG code: %d", epsg)
	}

	// Create shared COG tile caches for the max-zoom rendering pass.
	cacheSize := cfg.Concurrency * 128
	if cacheSize < 256 {
		cacheSize = 256
	}
	cogCache := cog.NewTileCache(cacheSize)
	var floatCache *cog.FloatTileCache
	if cfg.IsTerrarium {
		floatCache = cog.NewFloatTileCache(cacheSize)
	}

	// Tile image store: holds decoded RGBA tiles for the current zoom level
	// so the next (lower) zoom level can downsample from them.
	store := newTileImageStore(4096)

	var tileCount, emptyCount, totalBytes atomic.Int64

	// Process zoom levels from highest to lowest (pyramid approach).
	for z := cfg.MaxZoom; z >= cfg.MinZoom; z-- {
		tiles := coord.TilesInBounds(z,
			cfg.Bounds.MinLon, cfg.Bounds.MinLat,
			cfg.Bounds.MaxLon, cfg.Bounds.MaxLat)

		if cfg.Verbose {
			log.Printf("Zoom %d: %d tiles to generate", z, len(tiles))
		}

		if len(tiles) == 0 {
			continue
		}

		// Sort tiles along the Hilbert curve so that workers process spatially
		// nearby tiles consecutively. This dramatically improves COG tile cache
		// hit rates because the active working set stays in a compact 2D region
		// rather than spanning full rows.
		coord.SortTilesByHilbert(tiles)

		// Create progress bar for this zoom level.
		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(len(tiles)))

		isMaxZoom := (z == cfg.MaxZoom)

		// For non-max zoom levels we need the store from the previous (higher) zoom.
		// After processing this level, we'll replace the store contents.
		nextStore := newTileImageStore(len(tiles))

		// Partition tiles into contiguous chunks along the Hilbert curve.
		// Each worker gets its own spatial region so that nearby COG tiles
		// stay hot in the cache without cross-worker interference.
		nTiles := len(tiles)
		nWorkers := cfg.Concurrency
		if nWorkers > nTiles {
			nWorkers = nTiles
		}

		var wg sync.WaitGroup
		errCh := make(chan error, nWorkers)

		chunkBase := nTiles / nWorkers
		chunkRem := nTiles % nWorkers
		offset := 0

		for w := 0; w < nWorkers; w++ {
			// Distribute remainder tiles evenly across the first workers.
			size := chunkBase
			if w < chunkRem {
				size++
			}
			chunk := tiles[offset : offset+size]
			offset += size

			wg.Add(1)
			go func(chunk [][3]int) {
				defer wg.Done()
				for _, t := range chunk {
					z, x, y := t[0], t[1], t[2]
					var img *image.RGBA

					if isMaxZoom {
						if cfg.IsTerrarium {
							img = renderTileTerrarium(z, x, y, cfg.TileSize, sources, proj, floatCache, cfg.Resampling)
						} else {
							img = renderTile(z, x, y, cfg.TileSize, sources, proj, cogCache, cfg.Resampling)
						}
					} else {
						childZ := z + 1
						tl := store.Get(childZ, 2*x, 2*y)
						tr := store.Get(childZ, 2*x+1, 2*y)
						bl := store.Get(childZ, 2*x, 2*y+1)
						br := store.Get(childZ, 2*x+1, 2*y+1)
						if cfg.IsTerrarium {
							img = downsampleTileTerrarium(tl, tr, bl, br, cfg.TileSize, cfg.Resampling)
						} else {
							img = downsampleTile(tl, tr, bl, br, cfg.TileSize, cfg.Resampling)
						}
					}

					if img == nil {
						emptyCount.Add(1)
						pb.Increment()
						continue
					}

					if z > cfg.MinZoom {
						nextStore.Put(z, x, y, img)
					}

					data, err := cfg.Encoder.Encode(img)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("encoding tile z%d/%d/%d: %w", z, x, y, err):
						default:
						}
						return
					}

					if err := writer.WriteTile(z, x, y, data); err != nil {
						select {
						case errCh <- fmt.Errorf("writing tile z%d/%d/%d: %w", z, x, y, err):
						default:
						}
						return
					}

					tileCount.Add(1)
					totalBytes.Add(int64(len(data)))
					pb.Increment()
				}
			}(chunk)
		}

		wg.Wait()
		pb.Finish()

		// Check for errors.
		select {
		case err := <-errCh:
			return Stats{}, err
		default:
		}

		if cfg.Verbose {
			log.Printf("Zoom %d: completed (%d tiles so far)", z, tileCount.Load())
		}

		// Swap stores: the tiles we just generated become the source for the next level.
		store = nextStore
	}

	return Stats{
		TileCount:  tileCount.Load(),
		EmptyTiles: emptyCount.Load(),
		TotalBytes: totalBytes.Load(),
	}, nil
}
