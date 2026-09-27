package tile

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// assertRGBANear fails when got differs from want by more than tol in any channel.
func assertRGBANear(t *testing.T, what string, got, want color.RGBA, tol int) {
	t.Helper()
	d := func(a, b uint8) bool { return abs(int(a)-int(b)) > tol }
	if d(got.R, want.R) || d(got.G, want.G) || d(got.B, want.B) || d(got.A, want.A) {
		t.Errorf("%s = %v, want %v ±%d", what, got, want, tol)
	}
}

// TestDownsampleTile_OpaqueNextToTransparent is the edge case the alpha
// weighting must not break: a 2×2 block of one opaque red pixel and three
// transparent ones gives red at quarter alpha, not a dark red.
func TestDownsampleTile_OpaqueNextToTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
	td := downsampleTile(fullTile(img, 8), nil, nil, nil, 8, ResamplingBilinear)
	assertRGBANear(t, "pixel", td.RGBAAt(0, 0), color.RGBA{255, 0, 0, 64}, 0)
}

// TestDownsampleTile_AlphaWeighted checks that RGB is averaged by alpha in
// straight-alpha space. Columns alternate opaque red and blue at alpha 1,
// so every output pixel is half red, half near-transparent blue: it must stay
// red at alpha ~128. Averaging RGB without alpha weights gave purple.
func TestDownsampleTile_AlphaWeighted(t *testing.T) {
	const size = 16
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if x%2 == 0 {
				img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{0, 0, 255, 1})
			}
		}
	}
	for _, name := range []string{"bilinear", "bicubic", "lanczos"} {
		t.Run(name, func(t *testing.T) {
			mode, err := ParseResampling(name)
			if err != nil {
				t.Fatal(err)
			}
			td := downsampleTile(fullTile(img, size), nil, nil, nil, size, mode)
			// An interior pixel, clear of the tile edges.
			assertRGBANear(t, "pixel", td.RGBAAt(4, 4), color.RGBA{254, 0, 1, 128}, 1)
		})
	}
}

// The max-zoom samplers weight RGB by alpha, as the pyramid downsample does,
// so a source with continuous alpha (--alpha-band) is resampled like its
// premultiplied colours. Halfway between opaque red and blue at alpha 51,
// the result is mostly red; with a >0 mask it was 100,0,100.
func TestSamplers_AlphaWeighted(t *testing.T) {
	path := writeTestGeoTIFF(t, testGeoTIFF{W: 16, H: 16, Scale: 1, TieY: 16,
		GeoKeys: []uint16{1024, 2, 2048, 4326},
		RGBA: func(x, y int) [4]uint8 {
			if x < 8 {
				return [4]uint8{200, 0, 0, 255}
			}
			return [4]uint8{0, 0, 200, 51}
		}})
	src := openTestSources(t, path)[0]
	want := color.RGBA{167, 0, 33, 153} // (200*255, 200*51) / 306, alpha 306/2
	for _, s := range []struct {
		name   string
		sample func(*cog.Reader, int, float64, float64, int, int, int, int, *cog.TileCache, *gammaLUTs) (uint8, uint8, uint8, uint8, error)
	}{
		{"bilinear", bilinearSampleCached},
		{"bicubic", bicubicSampleCached},
		{"lanczos", lanczosSampleCached},
	} {
		r, g, b, a, err := s.sample(src, 0, 7.5, 8, 16, 16, 16, 16, cog.NewTileCache(4), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertRGBANear(t, s.name, color.RGBA{r, g, b, a}, want, 1)
	}
}

// TestTileData_UniformStraightAlpha checks that a uniform tile with partial
// alpha, which encoders read through At(), is encoded as its straight colour.
// At() used to return it as a (invalid) premultiplied color.RGBA, which the
// PNG encoder un-premultiplied: 255,0,0,128 came out as 253,0,0,128.
func TestTileData_UniformStraightAlpha(t *testing.T) {
	want := color.RGBA{255, 0, 0, 128}
	data, err := (&encode.PNGEncoder{}).Encode(newTileDataUniform(want, 8).AsImage())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	got := color.RGBA(color.NRGBAModel.Convert(img.At(3, 3)).(color.NRGBA))
	assertRGBANear(t, "stored pixel", got, want, 0)
}

// TestDiskTileStore_StraightAlphaRoundTrip checks that a semi-transparent
// tile read back from the store for downsampling keeps its straight colour.
// It used to come back as 72,100,49,128 for 200,100,50,128 with PNG output.
func TestDiskTileStore_StraightAlphaRoundTrip(t *testing.T) {
	want := color.RGBA{200, 100, 50, 128}
	for _, format := range []string{"png", "webp"} {
		t.Run(format, func(t *testing.T) {
			if format == "webp" && encode.WebPLossy {
				t.Skip("lossy WebP colour is not exact; covered by the encode package")
			}
			enc, err := encode.NewEncoder(format, 90)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			img := solidImage(8, want)
			img.SetRGBA(0, 0, color.RGBA{}) // keep the tile non-uniform
			td := newTileData(img, 8)
			data, err := enc.Encode(td.AsImage())
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			store := NewDiskTileStore(DiskTileStoreConfig{TileSize: 8, Format: format})
			defer store.Close()
			store.Put(1, 0, 0, td, data)
			got := store.Get(1, 0, 0)
			if got == nil {
				t.Fatal("Get returned nil")
			}
			assertRGBANear(t, "pixel", got.RGBAAt(4, 4), want, 0)
		})
	}
}
