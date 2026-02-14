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

		// Create progress bar for this zoom level.
		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(len(tiles)))

		isMaxZoom := (z == cfg.MaxZoom)

		// For non-max zoom levels we need the store from the previous (higher) zoom.
		// After processing this level, we'll replace the store contents.
		nextStore := newTileImageStore(len(tiles))

		// Create job channel and error channel.
		jobs := make(chan tileJob, cfg.Concurrency*2)
		var wg sync.WaitGroup
		errCh := make(chan error, 1)

		// Start workers.
		for w := 0; w < cfg.Concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					var img *image.RGBA

					if isMaxZoom {
						if cfg.IsTerrarium {
							// Render float data as Terrarium RGB.
							img = renderTileTerrarium(job.Z, job.X, job.Y, cfg.TileSize, sources, proj, floatCache, cfg.Resampling)
						} else {
							// Render from source COG data.
							img = renderTile(job.Z, job.X, job.Y, cfg.TileSize, sources, proj, cogCache, cfg.Resampling)
						}
					} else {
						// Downsample from 4 child tiles at z+1.
						childZ := job.Z + 1
						tl := store.Get(childZ, 2*job.X, 2*job.Y)
						tr := store.Get(childZ, 2*job.X+1, 2*job.Y)
						bl := store.Get(childZ, 2*job.X, 2*job.Y+1)
						br := store.Get(childZ, 2*job.X+1, 2*job.Y+1)
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

					// Store for next pyramid level (if not the lowest zoom).
					if z > cfg.MinZoom {
						nextStore.Put(job.Z, job.X, job.Y, img)
					}

					// Encode and write.
					data, err := cfg.Encoder.Encode(img)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("encoding tile z%d/%d/%d: %w", job.Z, job.X, job.Y, err):
						default:
						}
						return
					}

					if err := writer.WriteTile(job.Z, job.X, job.Y, data); err != nil {
						select {
						case errCh <- fmt.Errorf("writing tile z%d/%d/%d: %w", job.Z, job.X, job.Y, err):
						default:
						}
						return
					}

					tileCount.Add(1)
					totalBytes.Add(int64(len(data)))
					pb.Increment()
				}
			}()
		}

		// Feed jobs.
		for _, t := range tiles {
			jobs <- tileJob{Z: t[0], X: t[1], Y: t[2]}
		}
		close(jobs)
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
