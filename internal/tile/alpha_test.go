package tile

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"

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
