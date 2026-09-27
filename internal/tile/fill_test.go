package tile

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// tileColorAt decodes a PNG tile and returns the pixel covering lon/lat.
func tileColorAt(t *testing.T, data []byte, z, x, y int, lon, lat float64) color.RGBA {
	t.Helper()
	img, err := encode.DecodeImage(data, "png")
	if err != nil {
		t.Fatal(err)
	}
	px, py := coord.TilePixelCoords(lon, lat, z, x, y, img.Bounds().Dx())
	return color.RGBAModel.Convert(img.At(int(px), int(py))).(color.RGBA)
}

// NodataColor recolours transparent pixels of rendered tiles only;
// FillMissing only writes solid tiles where there is no data. Before the
// split one colour did both.
func TestGenerate_NodataColorAndFillMissingAreSeparate(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	gray := color.RGBA{200, 200, 200, 255}

	// A 10°x10° source at 0..10°E, 0..10°N whose western half is nodata
	// (0,0,0). At z4 it lies in tile 8/7 (0..22.5°E); the bounds add the
	// empty tile 9/7 east of it. Both have the parent 3/4/3.
	path := writeTestTIFF(t, 100, 100, 0, 10, 0.1, func(x, y, band int) uint8 {
		if x < 50 {
			return 0
		}
		return 200
	})
	const z = 4
	data, nodata, missing := [3]int{z, 8, 7}, [3]int{z, 9, 7}, [3]int{z - 1, 4, 3}

	for _, tt := range []struct {
		name                 string
		nodataColor, fillMis *color.RGBA
		// want: pixel in the nodata half, pixel of the tile outside the
		// source, and the parent pixel over the missing tile.
		wantNodata, wantOutside, wantParentMissing color.RGBA
		wantMissingTile                            bool
	}{
		{"neither", nil, nil, color.RGBA{}, color.RGBA{}, color.RGBA{}, false},
		{"nodata only", &red, nil, red, red, color.RGBA{}, false},
		{"fill-missing only", nil, &blue, color.RGBA{}, color.RGBA{}, blue, true},
		{"both", &red, &blue, red, red, blue, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srcs := openTestSources(t, path)
			srcs[0].SetBandConfig(cog.BandConfig{Bands: [3]int{1, 2, 3}, HasNodata: true})
			tiles := tileMap{}
			cfg := Config{
				Encoder: testEncoder(t), MinZoom: z - 1, MaxZoom: z, TileSize: 256, Concurrency: 2,
				Bounds:      cog.Bounds{MinLon: 0.01, MinLat: 0.01, MaxLon: 40, MaxLat: 10},
				Resampling:  ResamplingNearest,
				NodataColor: tt.nodataColor, FillMissing: tt.fillMis,
				MemoryLimitBytes: -1,
			}
			if _, err := Generate(cfg, srcs, tiles); err != nil {
				t.Fatal(err)
			}

			if got := tileColorAt(t, tiles[data], z, data[1], data[2], 7.5, 5); got != gray {
				t.Errorf("data pixel = %v, want %v", got, gray)
			}
			if got := tileColorAt(t, tiles[data], z, data[1], data[2], 2.5, 5); got != tt.wantNodata {
				t.Errorf("nodata pixel = %v, want %v", got, tt.wantNodata)
			}
			if got := tileColorAt(t, tiles[data], z, data[1], data[2], 15, 5); got != tt.wantOutside {
				t.Errorf("pixel outside the source = %v, want %v", got, tt.wantOutside)
			}
			if _, ok := tiles[nodata]; ok != tt.wantMissingTile {
				t.Errorf("tile %v written = %v, want %v", nodata, ok, tt.wantMissingTile)
			}
			if got := tileColorAt(t, tiles[missing], missing[0], missing[1], missing[2], 35, 5); got != tt.wantParentMissing {
				t.Errorf("parent pixel over the missing tile = %v, want %v", got, tt.wantParentMissing)
			}
		})
	}
}

// Passthrough with FillMissing copies the source tiles byte for byte (no
// re-encode) and adds fill tiles; NodataColor needs decoding, so the
// re-encode mode applies it and passthrough leaves the tiles alone.
func TestTransform_FillMissingWithoutReencoding(t *testing.T) {
	const tileSize = 8
	bounds := testBounds()
	blue := color.RGBA{0, 0, 255, 255}
	// An uncompressed PNG, which no re-encode would reproduce byte for byte.
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&buf, image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	reader := &mockPMTilesReader{
		tiles: map[[3]int][]byte{{2, 2, 1}: src},
		header: pmtiles.Header{MinZoom: 2, MaxZoom: 2,
			MinLon: bounds[0], MinLat: bounds[1], MaxLon: bounds[2], MaxLat: bounds[3]},
	}
	writer := newMockTileWriter()
	cfg := TransformConfig{
		MinZoom: 2, MaxZoom: 2, TileSize: tileSize, Concurrency: 2,
		Encoder: testEncoder(t), SourceFormat: "png",
		Mode: TransformPassthrough, FillMissing: &blue, Bounds: bounds,
	}
	stats, err := Transform(cfg, reader, writer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(writer.tiles[[3]int{2, 2, 1}], src) {
		t.Error("passthrough with FillMissing changed the bytes of an existing tile")
	}
	if n := writer.tileCountAtZoom(2); n != 4 || stats.TileCount != 4 {
		t.Errorf("wrote %d tiles (stats %d), want the source tile plus 3 fill tiles", n, stats.TileCount)
	}
	img, err := encode.DecodeImage(writer.tiles[[3]int{2, 3, 2}], "png")
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(img.At(3, 3)); got != blue {
		t.Errorf("fill tile pixel = %v, want %v", got, blue)
	}

	// Without an encoder the fill tile cannot be made: an error, not a panic.
	cfg.Encoder = nil
	if _, err := Transform(cfg, reader, newMockTileWriter()); err == nil {
		t.Error("FillMissing without an encoder: want an error")
	}
}
