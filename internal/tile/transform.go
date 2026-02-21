package tile

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TransformMode selects the processing strategy.
type TransformMode int

const (
	// TransformPassthrough copies raw tile bytes without decode/encode.
	TransformPassthrough TransformMode = iota
	// TransformReencode decodes and re-encodes each tile (format change).
	TransformReencode
	// TransformRebuild decodes max-zoom tiles and rebuilds the entire
	// pyramid via downsampling (resampling change or adding lower zooms).
	TransformRebuild
)

// TransformConfig holds configuration for the PMTiles transform pipeline.
type TransformConfig struct {
	MinZoom          int
	MaxZoom          int
	TileSize         int
	Concurrency      int
	Verbose          bool
	Encoder          encode.Encoder
	SourceFormat     string // format of input tiles (for decoding)
	Resampling       Resampling
	Mode             TransformMode
	FillColor        *color.RGBA
	Bounds           [4]float32 // MinLon, MinLat, MaxLon, MaxLat
	MemoryLimitBytes int64
	OutputDir        string
}

// PMTilesReader is the interface for reading tiles from a PMTiles archive.
type PMTilesReader interface {
	ReadTile(z, x, y int) ([]byte, error)
	TilesAtZoom(z int) [][3]int
	Header() pmtiles.Header
}

// Transform reads tiles from an existing PMTiles archive, applies the
// configured transformations, and writes the result via the TileWriter.
func Transform(cfg TransformConfig, reader PMTilesReader, writer TileWriter) (Stats, error) {
	switch cfg.Mode {
	case TransformPassthrough:
		return transformPassthrough(cfg, reader, writer)
	case TransformReencode:
		return transformReencode(cfg, reader, writer)
	case TransformRebuild:
		return transformRebuild(cfg, reader, writer)
	default:
		return Stats{}, fmt.Errorf("unknown transform mode: %d", cfg.Mode)
	}
}

// transformPassthrough copies raw tile bytes directly, filtering by zoom range.
func transformPassthrough(cfg TransformConfig, reader PMTilesReader, writer TileWriter) (Stats, error) {
	var tileCount, emptyCount, totalBytes atomic.Int64

	for z := cfg.MaxZoom; z >= cfg.MinZoom; z-- {
		tiles := reader.TilesAtZoom(z)
		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(len(tiles)))

		nWorkers := cfg.Concurrency
		if nWorkers > len(tiles) {
			nWorkers = len(tiles)
		}
		if nWorkers < 1 {
			nWorkers = 1
		}

		var wg sync.WaitGroup
		errCh := make(chan error, nWorkers)
		tileCh := make(chan [3]int, nWorkers*2)

		go func() {
			for _, t := range tiles {
				tileCh <- t
			}
			close(tileCh)
		}()

		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range tileCh {
					z, x, y := t[0], t[1], t[2]
					data, err := reader.ReadTile(z, x, y)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("reading tile z%d/%d/%d: %w", z, x, y, err):
						default:
						}
						return
					}
					if data == nil {
						emptyCount.Add(1)
						pb.Increment()
						continue
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
			}()
		}

		wg.Wait()
		pb.Finish()

		select {
		case err := <-errCh:
			return Stats{}, err
		default:
		}
	}

	// Fill empty tiles if requested.
	if cfg.FillColor != nil {
		fc, err := fillEmptyTiles(cfg, reader, writer)
		if err != nil {
			return Stats{}, err
		}
		tileCount.Add(fc.TileCount)
		totalBytes.Add(fc.TotalBytes)
	}

	return Stats{
		TileCount:  tileCount.Load(),
		EmptyTiles: emptyCount.Load(),
		TotalBytes: totalBytes.Load(),
	}, nil
}

// transformReencode decodes each tile and re-encodes in the target format.
func transformReencode(cfg TransformConfig, reader PMTilesReader, writer TileWriter) (Stats, error) {
	var tileCount, emptyCount, uniformCount, totalBytes atomic.Int64

	for z := cfg.MaxZoom; z >= cfg.MinZoom; z-- {
		tiles := reader.TilesAtZoom(z)
		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(len(tiles)))

		nWorkers := cfg.Concurrency
		if nWorkers > len(tiles) {
			nWorkers = len(tiles)
		}
		if nWorkers < 1 {
			nWorkers = 1
		}

		var wg sync.WaitGroup
		errCh := make(chan error, nWorkers)
		tileCh := make(chan [3]int, nWorkers*2)

		go func() {
			for _, t := range tiles {
				tileCh <- t
			}
			close(tileCh)
		}()

		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range tileCh {
					z, x, y := t[0], t[1], t[2]
					rawData, err := reader.ReadTile(z, x, y)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("reading tile z%d/%d/%d: %w", z, x, y, err):
						default:
						}
						return
					}
					if rawData == nil {
						emptyCount.Add(1)
						pb.Increment()
						continue
					}

					img, err := encode.DecodeImage(rawData, cfg.SourceFormat)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("decoding tile z%d/%d/%d: %w", z, x, y, err):
						default:
						}
						return
					}

					rgba := imageToRGBA(img)
					td := newTileData(rgba, cfg.TileSize)
					if td.IsUniform() {
						uniformCount.Add(1)
					}

					data, err := cfg.Encoder.Encode(td.AsImage())
					td.Release()
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
			}()
		}

		wg.Wait()
		pb.Finish()

		select {
		case err := <-errCh:
			return Stats{}, err
		default:
		}
	}

	if cfg.FillColor != nil {
		fc, err := fillEmptyTiles(cfg, reader, writer)
		if err != nil {
			return Stats{}, err
		}
		tileCount.Add(fc.TileCount)
		totalBytes.Add(fc.TotalBytes)
	}

	return Stats{
		TileCount:    tileCount.Load(),
		EmptyTiles:   emptyCount.Load(),
		UniformTiles: uniformCount.Load(),
		TotalBytes:   totalBytes.Load(),
	}, nil
}

// transformRebuild reads max-zoom tiles, then rebuilds the entire pyramid
// from the top down using the specified resampling method.
func transformRebuild(cfg TransformConfig, reader PMTilesReader, writer TileWriter) (Stats, error) {
	srcHeader := reader.Header()
	srcMaxZoom := int(srcHeader.MaxZoom)

	// The effective max zoom is the minimum of source and target max zoom,
	// since we can't create detail that doesn't exist.
	effectiveMaxZoom := cfg.MaxZoom
	if effectiveMaxZoom > srcMaxZoom {
		effectiveMaxZoom = srcMaxZoom
		if cfg.Verbose {
			log.Printf("Target max zoom %d exceeds source max zoom %d, clamping to %d",
				cfg.MaxZoom, srcMaxZoom, effectiveMaxZoom)
		}
	}

	memLimit := cfg.MemoryLimitBytes
	if memLimit < 0 {
		memLimit = 0
	} else if memLimit == 0 {
		memLimit = ComputeMemoryLimit(DefaultMemoryPressurePercent, cfg.Verbose)
	}

	store := NewDiskTileStore(DiskTileStoreConfig{
		InitialCapacity: 64,
		TileSize:        cfg.TileSize,
	})
	defer store.Close()

	var tileCount, emptyCount, uniformCount, grayCount, totalBytes atomic.Int64

	for z := effectiveMaxZoom; z >= cfg.MinZoom; z-- {
		isMaxZoom := (z == effectiveMaxZoom)

		var tiles [][3]int
		if isMaxZoom {
			tiles = reader.TilesAtZoom(z)
			// Also include tiles from source zoom levels above this one
			// that are within range, in case the source had tiles at
			// intermediate zoom levels we're keeping.
		} else {
			// For lower zoom levels, enumerate tiles from bounds so we
			// downsample even where the source had gaps.
			tiles = coord.TilesInBounds(z,
				float64(cfg.Bounds[0]), float64(cfg.Bounds[1]),
				float64(cfg.Bounds[2]), float64(cfg.Bounds[3]))
		}

		if len(tiles) == 0 {
			continue
		}

		if cfg.Verbose {
			log.Printf("Zoom %d: %d tiles to process", z, len(tiles))
		}

		coord.SortTilesByHilbert(tiles)

		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(len(tiles)))

		nextStore := NewDiskTileStore(DiskTileStoreConfig{
			InitialCapacity:  len(tiles),
			TileSize:         cfg.TileSize,
			TempDir:          cfg.OutputDir,
			MemoryLimitBytes: memLimit,
			Format:           cfg.Encoder.Format(),
			Verbose:          cfg.Verbose,
		})

		nTiles := len(tiles)
		nWorkers := cfg.Concurrency
		if nWorkers > nTiles {
			nWorkers = nTiles
		}
		if nWorkers < 1 {
			nWorkers = 1
		}

		batchSize := scheduleBatchSize
		if batchSize > nTiles {
			batchSize = nTiles
		}

		var wg sync.WaitGroup
		errCh := make(chan error, nWorkers)

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

				for batch := range batchCh {
					for _, t := range batch {
						z, x, y := t[0], t[1], t[2]
						var td *TileData

						if isMaxZoom {
							rawData, err := reader.ReadTile(z, x, y)
							if err != nil {
								select {
								case errCh <- fmt.Errorf("reading tile z%d/%d/%d: %w", z, x, y, err):
								default:
								}
								return
							}
							if rawData != nil {
								img, err := encode.DecodeImage(rawData, cfg.SourceFormat)
								if err != nil {
									select {
									case errCh <- fmt.Errorf("decoding tile z%d/%d/%d: %w", z, x, y, err):
									default:
									}
									return
								}
								rgba := imageToRGBA(img)
								if cfg.FillColor != nil {
									applyFillColorTransform(rgba, *cfg.FillColor)
								}
								td = newTileData(rgba, cfg.TileSize)
							}
						} else {
							childZ := z + 1
							tl := store.Get(childZ, 2*x, 2*y)
							tr := store.Get(childZ, 2*x+1, 2*y)
							bl := store.Get(childZ, 2*x, 2*y+1)
							br := store.Get(childZ, 2*x+1, 2*y+1)
							// Color transform at source: substitute nil children with fill tiles
							// so downsample operates on 4 tiles; no transform in downsample path.
							if cfg.FillColor != nil {
								fillTile := newTileDataUniform(*cfg.FillColor, cfg.TileSize)
								if tl == nil {
									tl = fillTile
								}
								if tr == nil {
									tr = fillTile
								}
								if bl == nil {
									bl = fillTile
								}
								if br == nil {
									br = fillTile
								}
							}
							td = downsampleTile(tl, tr, bl, br, cfg.TileSize, cfg.Resampling)
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

		nextStore.Drain()

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

		store.Close()
		store = nextStore
	}

	store.Close()

	if cfg.FillColor != nil {
		fc, err := fillEmptyTiles(cfg, reader, writer)
		if err != nil {
			return Stats{}, err
		}
		tileCount.Add(fc.TileCount)
		totalBytes.Add(fc.TotalBytes)
	}

	return Stats{
		TileCount:    tileCount.Load(),
		EmptyTiles:   emptyCount.Load(),
		UniformTiles: uniformCount.Load(),
		TotalBytes:   totalBytes.Load(),
	}, nil
}

// fillEmptyTiles generates tiles for positions within the bounds that are
// missing from the archive, filling them with the configured solid color.
func fillEmptyTiles(cfg TransformConfig, reader PMTilesReader, writer TileWriter) (Stats, error) {
	if cfg.FillColor == nil {
		return Stats{}, nil
	}

	fillImg := image.NewRGBA(image.Rect(0, 0, cfg.TileSize, cfg.TileSize))
	c := *cfg.FillColor
	pix := fillImg.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = c.R
		pix[i+1] = c.G
		pix[i+2] = c.B
		pix[i+3] = c.A
	}

	fillData, err := cfg.Encoder.Encode(fillImg)
	if err != nil {
		return Stats{}, fmt.Errorf("encoding fill tile: %w", err)
	}

	var tileCount, totalBytes atomic.Int64

	for z := cfg.MaxZoom; z >= cfg.MinZoom; z-- {
		allTiles := coord.TilesInBounds(z,
			float64(cfg.Bounds[0]), float64(cfg.Bounds[1]),
			float64(cfg.Bounds[2]), float64(cfg.Bounds[3]))

		existingTiles := reader.TilesAtZoom(z)
		existing := make(map[[2]int]bool, len(existingTiles))
		for _, t := range existingTiles {
			existing[[2]int{t[1], t[2]}] = true
		}

		var fillCount int
		for _, t := range allTiles {
			x, y := t[1], t[2]
			if existing[[2]int{x, y}] {
				continue
			}
			if err := writer.WriteTile(z, x, y, fillData); err != nil {
				return Stats{}, fmt.Errorf("writing fill tile z%d/%d/%d: %w", z, x, y, err)
			}
			fillCount++
		}

		if fillCount > 0 && cfg.Verbose {
			log.Printf("Zoom %d: filled %d empty tile(s)", z, fillCount)
		}
		tileCount.Add(int64(fillCount))
		totalBytes.Add(int64(fillCount) * int64(len(fillData)))
	}

	return Stats{
		TileCount:  tileCount.Load(),
		TotalBytes: totalBytes.Load(),
	}, nil
}

// imageToRGBA converts an image.Image to *image.RGBA.
func imageToRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	rgba := GetRGBA(bounds.Dx(), bounds.Dy())
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba
}
