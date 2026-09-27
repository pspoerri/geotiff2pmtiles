package tile

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// writeFloatTIFF writes a single-tile uncompressed float32 TIFF.
func writeFloatTIFF(t *testing.T, path string, w, h int, px []float32) {
	t.Helper()
	bo := binary.LittleEndian
	short := func(v uint16) []byte { b := make([]byte, 4); bo.PutUint16(b, v); return b }
	long := func(v uint32) []byte { b := make([]byte, 4); bo.PutUint32(b, v); return b }
	payload := make([]byte, 0, 4*len(px))
	for _, v := range px {
		payload = bo.AppendUint32(payload, math.Float32bits(v))
	}
	entries := []struct {
		tag, typ uint16
		count    uint32
		value    []byte
	}{
		{256, 3, 1, short(uint16(w))}, {257, 3, 1, short(uint16(h))}, {258, 3, 1, short(32)},
		{259, 3, 1, short(1)}, {262, 3, 1, short(1)}, {277, 3, 1, short(1)},
		{322, 3, 1, short(uint16(w))}, {323, 3, 1, short(uint16(h))},
		{324, 4, 1, nil}, {325, 4, 1, long(uint32(len(payload)))}, {339, 3, 1, short(3)},
	}
	entries[8].value = long(uint32(8 + 2 + len(entries)*12 + 4))
	buf := []byte{'I', 'I'}
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		buf = bo.AppendUint16(buf, e.tag)
		buf = bo.AppendUint16(buf, e.typ)
		buf = bo.AppendUint32(buf, e.count)
		buf = append(buf, e.value...)
	}
	buf = bo.AppendUint32(buf, 0)
	buf = append(buf, payload...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Left half is 100 m, right half is the -32767 nodata sentinel. Sampling at
// the boundary must not let the sentinel into the kernel.
func TestFloatSamplersExcludeNodataSentinel(t *testing.T) {
	const w, h, sentinel = 16, 16, -32767
	px := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				px[y*w+x] = 100
			} else {
				px[y*w+x] = sentinel
			}
		}
	}
	path := filepath.Join(t.TempDir(), "dem.tif")
	writeFloatTIFF(t, path, w, h, px)
	r, err := cog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cache := cog.NewFloatTileCache(4)

	fx, fy := 7.4, 8.0 // just left of the edge; kernels reach across it
	kernels := map[string]func(nd float64) (float64, error){
		"bilinear": func(nd float64) (float64, error) {
			return bilinearSampleFloat(r, 0, fx, fy, w, h, cache, nd)
		},
		"bicubic": func(nd float64) (float64, error) {
			return bicubicSampleFloat(r, 0, fx, fy, w, h, w, h, cache, nd)
		},
		"lanczos": func(nd float64) (float64, error) {
			return lanczosSampleFloat(r, 0, fx, fy, w, h, w, h, cache, nd)
		},
	}
	for name, k := range kernels {
		// nodata set: sentinel excluded, result must be the valid value.
		v, err := k(sentinel)
		if err != nil {
			t.Fatal(name, err)
		}
		if math.Abs(v-100) > 1e-6 {
			t.Errorf("%s with nodata=%d: got %v, want 100", name, sentinel, v)
		}
		// nodata unset (NaN): comparisons are no-ops; sentinel leaks like before.
		v, err = k(math.NaN())
		if err != nil {
			t.Fatal(name, err)
		}
		t.Logf("%s nodata unset: %v", name, v)
		if v == 100 {
			t.Errorf("%s with nodata unset: expected sentinel to influence the kernel, got exactly 100", name)
		}
		if math.IsNaN(v) {
			t.Errorf("%s with nodata unset: got NaN, want a finite value", name)
		}
	}
}

func TestParseFloatNodataRoundsThroughFloat32(t *testing.T) {
	for _, s := range []string{"-32767", "-3.4028235e+38", "-99999.99"} {
		nd := parseFloatNodata(s)
		v, _ := strconv.ParseFloat(s, 64)
		if pix := float64(float32(v)); nd != pix {
			t.Errorf("%s: got %v, want float32-rounded %v", s, nd, pix)
		}
	}
	for _, s := range []string{"", "n/a"} {
		if !math.IsNaN(parseFloatNodata(s)) {
			t.Errorf("%q: want NaN", s)
		}
	}
}

// Columns 0-5 are 100, 6 is 200, 7 is 20, 8+ are nodata. At (near-)integer fx
// next to the edge the only valid taps sit at kernel zeros; renormalising by
// their tiny weight sum used to return spikes such as -52.5 or 1502 m.
func TestFloatKernelsNearNodataEdge(t *testing.T) {
	const w, h, nd = 16, 16, -9999
	px := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case x <= 5:
				px[y*w+x] = 100
			case x == 6:
				px[y*w+x] = 200
			case x == 7:
				px[y*w+x] = 20
			default:
				px[y*w+x] = nd
			}
		}
	}
	path := filepath.Join(t.TempDir(), "edge.tif")
	writeFloatTIFF(t, path, w, h, px)
	r, err := cog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cache := cog.NewFloatTileCache(4)

	kernels := map[string]func(fx float64) (float64, error){
		"lanczos": func(fx float64) (float64, error) {
			return lanczosSampleFloat(r, 0, fx, 8, w, h, w, h, cache, nd)
		},
		"bicubic": func(fx float64) (float64, error) {
			return bicubicSampleFloat(r, 0, fx, 8, w, h, w, h, cache, nd)
		},
	}
	for name, k := range kernels {
		for _, fx := range []float64{8, 7.99999, 7.999999, 8.999999, 8.999999999999} {
			v, err := k(fx)
			if err != nil {
				t.Fatal(name, err)
			}
			// The nearest pixel is nodata, so the result must be too.
			if v != nd && !math.IsNaN(v) {
				t.Errorf("%s at fx=%v: got %v, want nodata", name, fx, v)
			}
		}
	}
}

// The kernel tables end at the support edge, where the kernel is 0; the last
// table entry used to be returned for the whole final interval.
func TestKernelLUTTapersToSupportEdge(t *testing.T) {
	for _, d := range []float64{1e-5, 1e-6} {
		if got, want := lanczos3LUT(3-d), lanczos3(3-d); math.Abs(got-want) > 2e-8 {
			t.Errorf("lanczos3LUT(3-%g) = %g, want %g", d, got, want)
		}
		if got, want := bicubicLUT(2-d), bicubic(2-d); math.Abs(got-want) > 2e-8 {
			t.Errorf("bicubicLUT(2-%g) = %g, want %g", d, got, want)
		}
	}
}
