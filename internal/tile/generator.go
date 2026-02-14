package tile

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// Config holds tile generation configuration.
type Config struct {
	MinZoom     int
	MaxZoom     int
	TileSize    int
	Concurrency int
	Verbose     bool
	Encoder     encode.Encoder
	Bounds      cog.Bounds
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

// Generate produces tiles for all zoom levels and writes them via the TileWriter.
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

	// Create a shared tile cache. Size per worker: ~64 tiles covers a good working set.
	cacheSize := cfg.Concurrency * 64
	if cacheSize < 256 {
		cacheSize = 256
	}
	cache := cog.NewTileCache(cacheSize)

	var tileCount, emptyCount, totalBytes atomic.Int64

	// Process zoom levels from lowest to highest.
	for z := cfg.MinZoom; z <= cfg.MaxZoom; z++ {
		tiles := coord.TilesInBounds(z,
			cfg.Bounds.MinLon, cfg.Bounds.MinLat,
			cfg.Bounds.MaxLon, cfg.Bounds.MaxLat)

		if cfg.Verbose {
			log.Printf("Zoom %d: %d tiles to generate", z, len(tiles))
		}

		if len(tiles) == 0 {
			continue
		}

		// Create job channel.
		jobs := make(chan tileJob, cfg.Concurrency*2)
		var wg sync.WaitGroup

		// Error channel.
		errCh := make(chan error, 1)

		// Start workers.
		for w := 0; w < cfg.Concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					img := renderTile(job.Z, job.X, job.Y, cfg.TileSize, sources, proj, cache)
					if img == nil {
						emptyCount.Add(1)
						continue
					}

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
				}
			}()
		}

		// Feed jobs.
		for _, t := range tiles {
			jobs <- tileJob{Z: t[0], X: t[1], Y: t[2]}
		}
		close(jobs)
		wg.Wait()

		// Check for errors.
		select {
		case err := <-errCh:
			return Stats{}, err
		default:
		}

		if cfg.Verbose {
			log.Printf("Zoom %d: completed (%d tiles so far)", z, tileCount.Load())
		}
	}

	return Stats{
		TileCount:  tileCount.Load(),
		EmptyTiles: emptyCount.Load(),
		TotalBytes: totalBytes.Load(),
	}, nil
}
