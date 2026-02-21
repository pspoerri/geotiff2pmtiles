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

// scheduleBatchSize is the number of Hilbert-contiguous tiles handed to a
// worker at a time. Smaller values give better load balance (workers that
// finish a batch of easy/empty tiles immediately pull the next batch instead
// of sitting idle). Larger values give better spatial locality for the COG
// tile cache. 32 is a sweet spot: with typical tile counts (thousands) and
// worker counts (8-16), each worker processes many batches, so the tail
// imbalance is at most one batch (~1.6 s at z18), which is negligible.
const scheduleBatchSize = 32

// Resampling selects the interpolation method.
type Resampling int

const (
	// ResamplingBilinear interpolates between 4 neighboring pixels (smooth).
	ResamplingBilinear Resampling = iota
	// ResamplingNearest picks the closest pixel (sharp, fast).
	ResamplingNearest
	// ResamplingLanczos uses a Lanczos-3 (sinc-windowed sinc) kernel for
	// high-quality interpolation with a 6×6 pixel neighborhood. Produces
	// sharper results than bilinear at the cost of more computation.
	ResamplingLanczos
	// ResamplingBicubic uses a Catmull-Rom (a = -0.5) bicubic kernel with
	// a 4×4 pixel neighborhood. Sharper than bilinear with less ringing
	// and lower cost than Lanczos.
	ResamplingBicubic
	// ResamplingMode picks the most common (modal) value in the
	// neighborhood. Ideal for categorical/classified rasters (e.g. land
	// cover) where interpolated values are meaningless. At the max zoom
	// level (COG rendering) it behaves like nearest-neighbor; during
	// pyramid downsampling it selects the most frequent pixel in each
	// 2×2 block.
	ResamplingMode
)

// ParseResampling converts a string to a Resampling constant.
func ParseResampling(s string) (Resampling, error) {
	switch s {
	case "lanczos":
		return ResamplingLanczos, nil
	case "bicubic":
		return ResamplingBicubic, nil
	case "bilinear":
		return ResamplingBilinear, nil
	case "nearest":
		return ResamplingNearest, nil
	case "mode":
		return ResamplingMode, nil
	default:
		return 0, fmt.Errorf("unknown resampling method %q (supported: lanczos, bicubic, bilinear, nearest, mode)", s)
	}
}

// Config holds tile generation configuration.
type Config struct {
	MinZoom          int
	MaxZoom          int
	TileSize         int
	Concurrency      int
	Verbose          bool
	Encoder          encode.Encoder
	Bounds           cog.Bounds
	Resampling       Resampling
	IsTerrarium      bool   // true for float GeoTIFF → Terrarium encoding
	MemoryLimitBytes int64  // max tile store memory before disk spilling (0 = auto)
	OutputDir        string // directory for spill files (defaults to OS temp dir)
}

// Stats holds generation statistics.
type Stats struct {
	TileCount    int64
	EmptyTiles   int64
	UniformTiles int64
	TotalBytes   int64
}

// TileWriter is the interface for writing tiles (implemented by pmtiles.Writer).
type TileWriter interface {
	WriteTile(z, x, y int, data []byte) error
}

// Generate produces tiles for all zoom levels and writes them via the TileWriter.
//
// The pipeline uses a pyramid approach:
//  1. The maximum zoom level is rendered from source COG data (per-pixel reprojection).
//  2. Each lower zoom level is built by downsampling 2x2 groups of child tiles.
//
// This avoids redundant COG reads and coordinate transforms for lower zoom levels.
//
// When memory pressure is high (configurable via Config.MemoryLimitBytes), the
// tile store spills to a temporary file on disk. Tiles are stored along the
// Hilbert curve for spatial locality during the downsampling read-back pass.
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

	// Compute memory limit for disk spilling.
	// -1 = disabled, 0 = auto-detect, >0 = explicit limit.
	memLimit := cfg.MemoryLimitBytes
	if memLimit < 0 {
		memLimit = 0 // disable: 0 in DiskTileStore means "never flush"
	} else if memLimit == 0 {
		memLimit = ComputeMemoryLimit(DefaultMemoryPressurePercent, cfg.Verbose)
	}

	// Tile image store: holds decoded tiles for the current zoom level
	// so the next (lower) zoom level can downsample from them.
	// The initial store is a lightweight placeholder (no I/O goroutine)
	// because the max-zoom level renders from COG sources, not from a
	// previous store. Each zoom level creates its own store with disk
	// spilling enabled.
	store := NewDiskTileStore(DiskTileStoreConfig{
		InitialCapacity: 64,
		TileSize:        cfg.TileSize,
	})
	defer store.Close()

	var tileCount, emptyCount, uniformCount, grayCount, totalBytes atomic.Int64

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
		nextStore := NewDiskTileStore(DiskTileStoreConfig{
			InitialCapacity:  len(tiles),
			TileSize:         cfg.TileSize,
			TempDir:          cfg.OutputDir,
			MemoryLimitBytes: memLimit,
			Format:           cfg.Encoder.Format(),
			Verbose:          cfg.Verbose,
		})

		// Partition tiles into small Hilbert-contiguous batches and distribute
		// them via a channel. This gives much better load balance than static
		// partitioning (where edge workers with many empty tiles finish early)
		// while preserving spatial locality within each batch for cache reuse
		// and efficient prefetching.
		nTiles := len(tiles)
		nWorkers := cfg.Concurrency
		if nWorkers > nTiles {
			nWorkers = nTiles
		}

		// Batch size balances spatial locality (larger = better cache reuse)
		// against load balance (smaller = less idle time at the end).
		// Each worker should get many batches so that the tail imbalance
		// (at most one batch) is a small fraction of total work.
		batchSize := scheduleBatchSize
		if batchSize > nTiles {
			batchSize = nTiles
		}

		var wg sync.WaitGroup
		errCh := make(chan error, nWorkers)

		// Feed batches into a channel; workers pull batches on demand.
		batchCh := make(chan [][3]int, nWorkers*2)
		go func() {
			for i := 0; i < nTiles; i += batchSize {
				end := i + batchSize
				if end > nTiles {
					end = nTiles
				}
				batchCh <- tiles[i:end]
			}
			close(batchCh)
		}()

		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				// Build source info once per worker (read-only after init).
				var srcInfos []sourceInfo
				if isMaxZoom {
					srcInfos = buildSourceInfos(sources)
				}

				for batch := range batchCh {
					for _, t := range batch {
						z, x, y := t[0], t[1], t[2]
						var td *TileData

						if isMaxZoom {
							var img *image.RGBA
							if cfg.IsTerrarium {
								img = renderTileTerrarium(z, x, y, cfg.TileSize, srcInfos, proj, floatCache, cfg.Resampling)
							} else {
								img = renderTile(z, x, y, cfg.TileSize, srcInfos, proj, cogCache, cfg.Resampling)
							}
							if img != nil {
								td = newTileData(img, cfg.TileSize)
							}
						} else {
							childZ := z + 1
							tl := store.Get(childZ, 2*x, 2*y)
							tr := store.Get(childZ, 2*x+1, 2*y)
							bl := store.Get(childZ, 2*x, 2*y+1)
							br := store.Get(childZ, 2*x+1, 2*y+1)
							if cfg.IsTerrarium {
								td = downsampleTileTerrarium(tl, tr, bl, br, cfg.TileSize, cfg.Resampling)
							} else {
								td = downsampleTile(tl, tr, bl, br, cfg.TileSize, cfg.Resampling)
							}
						}

						if td == nil {
							emptyCount.Add(1)
							pb.Increment()
							continue
						}

						if td.IsUniform() {
							uniformCount.Add(1)
						} else if td.IsGray() {
							grayCount.Add(1)
						}

						// Encode first so we can reuse the encoded bytes for
						// both the output and the disk tile store.
						data, err := cfg.Encoder.Encode(td.AsImage())
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

						// Store for next zoom level's downsampling, reusing the
						// already-encoded bytes for efficient disk storage.
						if z > cfg.MinZoom {
							nextStore.Put(z, x, y, td, data)
						}

						td.Release()

						tileCount.Add(1)
						totalBytes.Add(int64(len(data)))
						pb.Increment()
					}
				}
			}()
		}

		wg.Wait()
		pb.Finish()

		// Drain the I/O goroutine so all tiles are on disk before the
		// next zoom level starts reading from this store.
		nextStore.Drain()

		// Check for errors.
		select {
		case err := <-errCh:
			nextStore.Close()
			return Stats{}, err
		default:
		}

		if cfg.Verbose {
			log.Printf("Zoom %d: completed (%d tiles so far, %d gray, %d uniform, %d empty)",
				z, tileCount.Load(), grayCount.Load(), uniformCount.Load(), emptyCount.Load())
			log.Printf("  Store: %s", nextStore.Stats())
		}

		// Swap stores: the tiles we just generated become the source for the next level.
		store.Close() // release old store's temp file
		store = nextStore
	}

	store.Close()

	return Stats{
		TileCount:    tileCount.Load(),
		EmptyTiles:   emptyCount.Load(),
		UniformTiles: uniformCount.Load(),
		TotalBytes:   totalBytes.Load(),
	}, nil
}
