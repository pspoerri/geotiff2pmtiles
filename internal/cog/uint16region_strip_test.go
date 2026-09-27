package cog

import (
	"fmt"
	"path/filepath"
	"testing"
)

// ReadUint16Region on strip TIFFs, chunky and planar, whose last virtual tile
// is short, and on single-band tiles.
func TestReadUint16RegionStripsAndSingleBand(t *testing.T) {
	const w, h = 9, 11
	pixel := func(x, y, band int) uint8 { return uint8(10*x + y + 100*band) }
	for _, planar := range []bool{false, true} {
		for _, rps := range []int{1, 2, 3, 7} {
			path := filepath.Join(t.TempDir(), fmt.Sprintf("strip-%v-%d.tif", planar, rps))
			writeStripTIFF(t, path, w, h, rps, planar, pixel)
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			const x0, y0, rw, rh = 1, 2, 7, 9 // down to the last row
			got, spp, err := r.ReadUint16Region(0, x0, y0, rw, rh)
			r.Close()
			if err != nil || spp != 3 {
				t.Fatalf("planar=%v rps=%d: spp %d, err %v", planar, rps, spp, err)
			}
			for i, v := range got {
				px, s := i/3, i%3
				if want := pixel(x0+px%rw, y0+px/rw, s); v != uint16(want) {
					t.Fatalf("planar=%v rps=%d: pixel (%d,%d) band %d = %d, want %d",
						planar, rps, x0+px%rw, y0+px/rw, s, v, want)
				}
			}
		}
	}

	r, want := uint16TiledReader(t, 13, 11, 4, 4, 1, -1)
	got, spp, err := r.ReadUint16Region(0, 3, 3, 10, 8)
	if err != nil || spp != 1 {
		t.Fatalf("single band: spp %d, err %v", spp, err)
	}
	for i, v := range got {
		if x, y := 3+i%10, 3+i/10; v != want[y*13+x] {
			t.Fatalf("single band: pixel (%d,%d) = %d, want %d", x, y, v, want[y*13+x])
		}
	}
}
