package integration_test

import (
	"image/color"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TestBasicRGBPipeline generates a 512x512 8-bit RGB GeoTIFF with a gradient,
// converts it to JPEG PMTiles, and validates the output.
func TestBasicRGBPipeline(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1, // 51.2° extent — covers significant area at zoom 0-2
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			switch band {
			case 0:
				return uint16(x % 256)
			case 1:
				return uint16(y % 256)
			default:
				return 128
			}
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "jpeg",
		MinZoom:    0,
		MaxZoom:    2,
	})

	result := validatePMTiles(t, outPath)

	if result.Header.TileType != pmtiles.TileTypeJPEG {
		t.Errorf("expected TileType JPEG (%d), got %d", pmtiles.TileTypeJPEG, result.Header.TileType)
	}
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
	if int(result.Header.MinZoom) != 0 {
		t.Errorf("expected MinZoom=0, got %d", result.Header.MinZoom)
	}
	if int(result.Header.MaxZoom) != 2 {
		t.Errorf("expected MaxZoom=2, got %d", result.Header.MaxZoom)
	}
	for z := 0; z <= 2; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	// Verify tiles decode.
	for z := 0; z <= 2; z++ {
		reader, err := pmtiles.OpenReader(outPath)
		if err != nil {
			t.Fatal(err)
		}
		tiles := reader.TilesAtZoom(z)
		reader.Close()
		if len(tiles) > 0 {
			assertTileDecodesAsImage(t, outPath, tiles[0][0], tiles[0][1], tiles[0][2])
		}
	}
}

// TestRGBATransparency generates a 512x512 8-bit RGBA GeoTIFF where the right
// half is transparent, converts to PNG, and verifies tiles are produced.
func TestRGBATransparency(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 4,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1, // 51.2° extent
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			if band == 3 { // alpha
				if x >= 256 {
					return 0 // transparent right half
				}
				return 255
			}
			return 200
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    2,
	})

	result := validatePMTiles(t, outPath)
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected TileType PNG (%d), got %d", pmtiles.TileTypePNG, result.Header.TileType)
	}
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
}

// TestGrayscaleWithNodata generates a 256x256 8-bit grayscale GeoTIFF where
// nodata=0, converts to PNG, and checks that tiles are produced.
func TestGrayscaleWithNodata(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 256, Height: 256,
		SamplesPerPixel: 1,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.2, // 51.2° extent
		EPSG:            4326,
		NoData:          "0",
		PixelFunc: func(x, y, band int) uint16 {
			// Top-left quadrant is nodata, rest is gray value 180
			if x < 64 && y < 64 {
				return 0
			}
			return 180
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    1,
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
}

// Test16BitRescaling generates a 512x512 16-bit 4-band RGBNIR GeoTIFF with
// values 0-10000, converts to PNG with linear rescaling, and validates output.
func Test16BitRescaling(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 4,
		BitsPerSample:   16,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			// Values in 100-10000 range (avoid 0 which could be nodata)
			return uint16(100 + (x+y*band)%9900)
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    2,
		BandCfg: cog.BandConfig{
			Bands:      [3]int{1, 2, 3},
			AlphaBand:  -1,
			Rescale:    cog.RescaleLinear,
			RescaleMin: 0,
			RescaleMax: 10000,
		},
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected PNG, got tile type %d", result.Header.TileType)
	}
}

// Test16BitAutoDetect generates a 512x512 16-bit 4-band GeoTIFF with GDAL
// DESCRIPTION metadata, and verifies rescaling works.
func Test16BitAutoDetect(t *testing.T) {
	gdalMeta := `<GDALMetadata>
  <Item name="DESCRIPTION" sample="0">B04 (Red)</Item>
  <Item name="DESCRIPTION" sample="1">B03 (Green)</Item>
  <Item name="DESCRIPTION" sample="2">B02 (Blue)</Item>
  <Item name="DESCRIPTION" sample="3">B08 (NIR)</Item>
</GDALMetadata>`

	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 4,
		BitsPerSample:   16,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		GDALMetadataXML: gdalMeta,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16(100 + (x*100+y*50+band*200)%9900)
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    2,
		BandCfg: cog.BandConfig{
			Bands:      [3]int{1, 2, 3},
			AlphaBand:  -1,
			Rescale:    cog.RescaleLinear,
			RescaleMin: 0,
			RescaleMax: 10000,
		},
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
}

// TestMultiSourceMosaic generates two 256x256 side-by-side COGs (one red, one blue)
// and verifies that the output PMTiles has tiles covering both sources.
func TestMultiSourceMosaic(t *testing.T) {
	red := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 256, Height: 256,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1, // 25.6° extent
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			if band == 0 {
				return 255
			}
			return 0
		},
	})

	blue := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 256, Height: 256,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       0.6, // adjacent to first (-25 + 25.6 = 0.6)
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			if band == 2 {
				return 255
			}
			return 0
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{red, blue},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    1,
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
	// The merged bounds should span both sources.
	lonSpan := float64(result.Header.MaxLon) - float64(result.Header.MinLon)
	if lonSpan < 25.0 {
		t.Errorf("expected merged bounds to span both sources, lon span = %f", lonSpan)
	}
}

// TestFillColor generates a 256x256 GeoTIFF with a nodata region and uses
// fill-color to substitute it.
func TestFillColor(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 256, Height: 256,
		SamplesPerPixel: 4,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.2,
		EPSG:            4326,
		NoData:          "0",
		PixelFunc: func(x, y, band int) uint16 {
			if x < 128 {
				return 0 // nodata
			}
			if band == 3 {
				return 255
			}
			return 100
		},
	})

	fc := &color.RGBA{R: 255, G: 0, B: 0, A: 255}
	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    1,
		FillColor:  fc,
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
}

// TestDiskSpilling generates a 1024x1024 GeoTIFF with 4x4 tiles and converts
// with a 1MB memory limit, verifying it completes without crash.
func TestDiskSpilling(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 1024, Height: 1024,
		TileWidth: 256, TileHt: 256,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.05, // 51.2° extent
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16((x*7 + y*13 + band*31) % 256)
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "jpeg",
		MinZoom:    0,
		MaxZoom:    2,
		MemLimitMB: 1,
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles in disk-spilling mode")
	}
}

// TestTransformPassthrough creates a PMTiles from a GeoTIFF, then passes it
// through the transform pipeline, and verifies tile counts match.
func TestTransformPassthrough(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16((x + y*3 + band*7) % 256)
		},
	})

	srcPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "jpeg",
		MinZoom:    0,
		MaxZoom:    2,
	})

	srcResult := validatePMTiles(t, srcPath)

	outPath := runTransform(t, transformConfig{
		InputPath: srcPath,
		MinZoom:   -1,
		MaxZoom:   -1,
	})

	outResult := validatePMTiles(t, outPath)

	if outResult.TileCount != srcResult.TileCount {
		t.Errorf("passthrough tile count mismatch: src=%d, out=%d", srcResult.TileCount, outResult.TileCount)
	}
	if outResult.Header.TileType != srcResult.Header.TileType {
		t.Errorf("passthrough tile type changed: src=%d, out=%d", srcResult.Header.TileType, outResult.Header.TileType)
	}
}

// TestTransformReencode creates a PNG PMTiles and re-encodes to JPEG.
func TestTransformReencode(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16((x*5 + y*3) % 256)
		},
	})

	srcPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "png",
		MinZoom:    0,
		MaxZoom:    2,
	})

	outPath := runTransform(t, transformConfig{
		InputPath: srcPath,
		Format:    "jpeg",
		MinZoom:   -1,
		MaxZoom:   -1,
	})

	result := validatePMTiles(t, outPath)
	if result.Header.TileType != pmtiles.TileTypeJPEG {
		t.Errorf("expected JPEG after re-encode, got tile type %d", result.Header.TileType)
	}
	if result.TileCount == 0 {
		t.Error("expected tiles after re-encode")
	}

	reader, err := pmtiles.OpenReader(outPath)
	if err != nil {
		t.Fatal(err)
	}
	tiles := reader.TilesAtZoom(int(result.Header.MaxZoom))
	reader.Close()
	if len(tiles) > 0 {
		assertTileDecodesAsImage(t, outPath, tiles[0][0], tiles[0][1], tiles[0][2])
	}
}

// TestTransformRebuild creates a PMTiles at zoom 2-3 and rebuilds with min-zoom 0.
func TestTransformRebuild(t *testing.T) {
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16((x + y) % 256)
		},
	})

	srcPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "jpeg",
		MinZoom:    2,
		MaxZoom:    3,
	})

	srcResult := validatePMTiles(t, srcPath)
	if srcResult.ZoomCounts[0] != 0 {
		t.Fatalf("source should have no tiles at zoom 0, got %d", srcResult.ZoomCounts[0])
	}

	outPath := runTransform(t, transformConfig{
		InputPath: srcPath,
		MinZoom:   0,
		MaxZoom:   -1,
		Rebuild:   true,
	})

	outResult := validatePMTiles(t, outPath)

	if outResult.ZoomCounts[0] == 0 {
		t.Error("expected tiles at zoom 0 after rebuild")
	}
	if outResult.ZoomCounts[1] == 0 {
		t.Error("expected tiles at zoom 1 after rebuild")
	}
}

// TestWebPEncoding generates a 512x512 8-bit RGB GeoTIFF and converts to WebP.
// Skipped if CGO/libwebp is not available.
func TestWebPEncoding(t *testing.T) {
	_, err := encode.NewEncoder("webp", 85)
	if err != nil {
		t.Skip("WebP encoder not available (requires CGO + libwebp)")
	}

	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 512, Height: 512,
		SamplesPerPixel: 3,
		BitsPerSample:   8,
		OriginLon:       -25.0,
		OriginLat:       70.0,
		PixelSizeDeg:    0.1,
		EPSG:            4326,
		PixelFunc: func(x, y, band int) uint16 {
			return uint16((x*3 + y*7 + band*11) % 256)
		},
	})

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{tiffPath},
		Format:     "webp",
		MinZoom:    0,
		MaxZoom:    2,
	})

	result := validatePMTiles(t, outPath)
	if result.Header.TileType != pmtiles.TileTypeWebP {
		t.Errorf("expected WebP tile type (%d), got %d", pmtiles.TileTypeWebP, result.Header.TileType)
	}
	if result.TileCount == 0 {
		t.Error("expected at least one tile")
	}
}
