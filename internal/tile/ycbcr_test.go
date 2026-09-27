package tile

import (
	"fmt"
	"image"
	"path/filepath"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// JPEG tiles may use any chroma subsampling. At integer coordinates the
// Lanczos and bicubic samplers must return the tile's own pixel; the fast
// paths used to index chroma as 4:4:4 for 4:4:0/4:1:1/4:1:0 (wrong colours,
// then an index-out-of-range panic near the bottom of the tile).
func TestYCbCrFastPathsAllSubsampleRatios(t *testing.T) {
	const size = 64
	path := filepath.Join(t.TempDir(), "any.tif")
	writeFloatTIFF(t, path, 16, 16, make([]float32, 16*16))
	r, err := cog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	fill := func(y, cb, cr []uint8) {
		for i := range y {
			y[i] = uint8(i * 7)
		}
		for i := range cb {
			cb[i] = uint8(i * 37)
			cr[i] = uint8(255 - i*11)
		}
	}
	rect := image.Rect(0, 0, size, size)
	for _, ratio := range []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420,
		image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410,
	} {
		ycc := image.NewYCbCr(rect, ratio)
		fill(ycc.Y, ycc.Cb, ycc.Cr)
		ycca := image.NewNYCbCrA(rect, ratio)
		fill(ycca.Y, ycca.Cb, ycca.Cr)
		for i := range ycca.A {
			ycca.A[i] = 255
		}
		for _, tile := range []image.Image{ycc, ycca} {
			name := fmt.Sprintf("%T %v", tile, ratio)
			t.Run(name, func(t *testing.T) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic: %v", p)
					}
				}()
				cache := cog.NewTileCache(4)
				cache.Put(r.ID(), 0, 0, 0, tile)
				for _, p := range [][2]int{{10, 20}, {33, 9}, {50, 57}, {61, 62}, {63, 63}} {
					want := pixelFromImage(tile, p[0], p[1])
					fx, fy := float64(p[0]), float64(p[1])
					lr, lg, lb, la, _ := lanczosSampleCached(r, 0, fx, fy, size, size, size, size, cache, nil)
					if got := [4]uint8{lr, lg, lb, la}; got != want {
						t.Errorf("lanczos at %v: got %v, want %v", p, got, want)
					}
					br, bg, bb, ba, _ := bicubicSampleCached(r, 0, fx, fy, size, size, size, size, cache, nil)
					if got := [4]uint8{br, bg, bb, ba}; got != want {
						t.Errorf("bicubic at %v: got %v, want %v", p, got, want)
					}
				}
			})
		}
	}
}
