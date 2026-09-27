package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// writeGeoTIFF writes a single-strip 8-bit RGB GeoTIFF of w x h pixels of
// size px CRS units, with its top-left corner at (x0, y0) and the given
// ProjectedCSTypeGeoKey (or GeographicTypeGeoKey for 4326).
func writeGeoTIFF(t *testing.T, epsg, w, h int, x0, y0, px float64) *cog.Reader {
	t.Helper()
	bo := binary.LittleEndian
	keys := []uint16{1, 1, 0, 2, 1024, 0, 1, 1, 3072, 0, 1, uint16(epsg)}
	if epsg == 4326 {
		keys = []uint16{1, 1, 0, 2, 1024, 0, 1, 2, 2048, 0, 1, 4326}
	}
	type entry struct {
		tag, typ uint16
		count    uint32
		value    uint32
		data     []byte
	}
	doubles := func(v ...float64) []byte {
		var b []byte
		for _, f := range v {
			b = bo.AppendUint64(b, math.Float64bits(f))
		}
		return b
	}
	var keyBytes []byte
	for _, k := range keys {
		keyBytes = bo.AppendUint16(keyBytes, k)
	}
	pixels := make([]byte, w*h*3)
	for i := range pixels {
		pixels[i] = 100
	}
	entries := []entry{
		{256, 4, 1, uint32(w), nil},
		{257, 4, 1, uint32(h), nil},
		{258, 3, 3, 0, []byte{8, 0, 8, 0, 8, 0}},
		{259, 3, 1, 1, nil},
		{262, 3, 1, 2, nil},
		{273, 4, 1, 0, nil}, // StripOffsets, set below
		{277, 3, 1, 3, nil},
		{278, 4, 1, uint32(h), nil},
		{279, 4, 1, uint32(len(pixels)), nil},
		{284, 3, 1, 1, nil},
		{33550, 12, 3, 0, doubles(px, px, 0)},
		{33922, 12, 6, 0, doubles(0, 0, 0, x0, y0, 0)},
		{34735, 3, uint32(len(keys)), 0, keyBytes},
	}
	off := uint32(8 + 2 + len(entries)*12 + 4)
	for i := range entries {
		if entries[i].data != nil {
			entries[i].value = off
			off += uint32(len(entries[i].data))
		}
	}
	entries[5].value = off

	buf := []byte{'I', 'I'}
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		buf = bo.AppendUint16(buf, e.tag)
		buf = bo.AppendUint16(buf, e.typ)
		buf = bo.AppendUint32(buf, e.count)
		buf = bo.AppendUint32(buf, e.value)
	}
	buf = bo.AppendUint32(buf, 0)
	for _, e := range entries {
		buf = append(buf, e.data...)
	}
	buf = append(buf, pixels...)

	path := filepath.Join(t.TempDir(), fmt.Sprintf("epsg%d.tif", epsg))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := cog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// Sources in several CRSs are accepted; unknown CRSs are rejected up front,
// and --source-epsg (SetEPSG) rescues a user-defined one.
func TestCheckSourceCRSs(t *testing.T) {
	wgs := writeGeoTIFF(t, 4326, 10, 10, 8, 47, 0.01)
	utm := writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10)
	if err := checkSourceCRSs([]*cog.Reader{wgs, utm}); err != nil {
		t.Errorf("mixed 4326 + 32632: %v", err)
	}

	custom := writeGeoTIFF(t, 32767, 10, 10, 500000, 5200000, 10)
	err := checkSourceCRSs([]*cog.Reader{wgs, custom})
	if err == nil || !strings.Contains(err.Error(), "user-defined") || !strings.Contains(err.Error(), custom.Path()) {
		t.Errorf("user-defined CRS: err = %v", err)
	}
	custom.SetEPSG(32632)
	if err := checkSourceCRSs([]*cog.Reader{wgs, custom}); err != nil {
		t.Errorf("user-defined CRS overridden to 32632: %v", err)
	}

	unknown := writeGeoTIFF(t, 1234, 10, 10, 0, 0, 1)
	if err := checkSourceCRSs([]*cog.Reader{unknown}); err == nil || !strings.Contains(err.Error(), "1234") {
		t.Errorf("unknown EPSG: err = %v", err)
	}
}

// --source-epsg supplies a CRS, never a geotransform: a source without one
// fails whatever its EPSG code, also after a valid source in the same CRS.
func TestCheckSourceCRSsNeedsGeotransform(t *testing.T) {
	utm := writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10)
	noGeo := writeGeoTIFF(t, 32632, 10, 10, 0, 0, 0)
	noGeo.SetEPSG(32632)
	for _, sources := range [][]*cog.Reader{{noGeo}, {utm, noGeo}} {
		err := checkSourceCRSs(sources)
		if err == nil || !strings.Contains(err.Error(), "geotransform") || !strings.Contains(err.Error(), noGeo.Path()) {
			t.Errorf("source without a geotransform: err = %v", err)
		}
	}
}

// A projected grid read as EPSG:4326 lies far beyond the poles; a global
// grid registered on pixel centres reaches half a pixel past them.
func TestCheckBoundsWGS84(t *testing.T) {
	forced := writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10)
	forced.SetEPSG(4326)
	global := writeGeoTIFF(t, 4326, 360, 181, -180.5, 90.5, 1)
	for _, tc := range []struct {
		src  *cog.Reader
		fail bool
	}{{forced, true}, {global, false}, {writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10), false}} {
		b, err := cog.MergedBoundsWGS84([]*cog.Reader{tc.src})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkBoundsWGS84(b, []*cog.Reader{tc.src}); (err != nil) != tc.fail {
			t.Errorf("EPSG:%d, bounds %+v: err = %v, want failure %v", tc.src.EPSG(), b, err, tc.fail)
		}
	}
}

// Coverage boxes are compared only when all sources share one CRS: a degree
// box next to a metre box is not a hole.
func TestCoverageGapsPerCRS(t *testing.T) {
	a := writeGeoTIFF(t, 4326, 100, 100, 8, 47, 0.01)
	b := writeGeoTIFF(t, 32632, 100, 100, 500000, 5200000, 10)
	if gaps := coverageGaps([]*cog.Reader{a, b}); len(gaps) != 0 {
		t.Errorf("coverageGaps across CRSs = %v, want none", gaps)
	}

	// Two 4326 tiles with a tile-sized hole between them.
	c := writeGeoTIFF(t, 4326, 100, 100, 10, 47, 0.01)
	gaps := coverageGaps([]*cog.Reader{a, c})
	if len(gaps) == 0 {
		t.Fatal("hole between two EPSG:4326 sources not found")
	}
	for _, g := range gaps {
		if g.epsg != 4326 || g.MinX < 8 || g.MaxX > 11 {
			t.Errorf("gap %+v, want one in EPSG:4326 between 9 and 10°E", g)
		}
	}
	if gaps := coverageGaps([]*cog.Reader{a, b, c}); len(gaps) != 0 {
		t.Errorf("coverageGaps with mixed CRSs = %v, want none: they are not looked for", gaps)
	}
}

// A hole among the sources of one CRS that a source in another CRS fills
// is no hole.
func TestCoverageGapsFilledByAnotherCRS(t *testing.T) {
	// Three 1 km UTM tiles in an L; the fourth corner, 501-502 km E,
	// 5198-5199 km N, lies at about 9.013-9.026°E, 46.936-46.945°N.
	utm := []*cog.Reader{
		writeGeoTIFF(t, 32632, 100, 100, 500000, 5200000, 10),
		writeGeoTIFF(t, 32632, 100, 100, 501000, 5200000, 10),
		writeGeoTIFF(t, 32632, 100, 100, 500000, 5199000, 10),
	}
	if gaps := coverageGaps(utm); len(gaps) != 1 {
		t.Fatalf("coverageGaps of the L = %v, want its corner", gaps)
	}
	wgs := writeGeoTIFF(t, 4326, 50, 40, 8.99, 46.96, 0.001) // 8.99-9.04°E, 46.92-46.96°N
	sources := append(utm, wgs)
	if gaps := coverageGaps(sources); len(gaps) != 0 {
		t.Errorf("coverageGaps = %v, want none: the EPSG:4326 source covers the corner", gaps)
	}
	bounds := cog.Bounds{MinLon: 8.99, MinLat: 46.92, MaxLon: 9.04, MaxLat: 46.96}
	desc := buildDescription(sources, bounds, nil, "png", 85, 256, 0, 10, "bicubic", 1, nil, nil, cog.BandConfig{Bands: [3]int{1, 2, 3}})
	if strings.Contains(desc, "Holes") {
		t.Errorf("description of mixed CRSs reports holes:\n%s", desc)
	}
	desc = buildDescription(utm, bounds, nil, "png", 85, 256, 0, 10, "bicubic", 1, nil, nil, cog.BandConfig{Bands: [3]int{1, 2, 3}})
	if !strings.HasSuffix(desc, "\n  Holes: none") {
		t.Errorf("single-CRS description lacks the hole count:\n%s", desc)
	}
}

// The auto max zoom follows the finest source, not the first one.
func TestFinestPixelSize(t *testing.T) {
	coarse := writeGeoTIFF(t, 4326, 10, 10, 8, 47, 0.01) // ~760 m at 47°N
	fine := writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10)
	got := finestPixelSize([]*cog.Reader{coarse, fine}, 47)
	if math.Abs(got-10) > 0.5 {
		t.Errorf("finestPixelSize = %g m, want ~10 m", got)
	}
}

func TestBuildDescriptionMixedCRS(t *testing.T) {
	a := writeGeoTIFF(t, 4326, 10, 10, 8, 47, 0.01)
	b := writeGeoTIFF(t, 32632, 10, 10, 500000, 5200000, 10)
	bounds := cog.Bounds{MinLon: 8, MinLat: 46.9, MaxLon: 9, MaxLat: 47}

	desc := buildDescription([]*cog.Reader{a, b}, bounds, nil, "png", 85, 256, 0, 10, "bicubic", 1, nil, nil, cog.BandConfig{Bands: [3]int{1, 2, 3}})
	for _, want := range []string{"EPSG:4326, EPSG:32632", "Pixel size: 10 m (finest source"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q:\n%s", want, desc)
		}
	}
	if strings.Contains(desc, "Extent (CRS)") {
		t.Errorf("description has a merged CRS extent for mixed CRSs:\n%s", desc)
	}

	desc = buildDescription([]*cog.Reader{a}, bounds, nil, "png", 85, 256, 0, 10, "bicubic", 1, nil, nil, cog.BandConfig{Bands: [3]int{1, 2, 3}})
	for _, want := range []string{"GeoTIFF file(s), EPSG:4326\n", "Extent (CRS)", "Pixel size: 0.01\n"} {
		if !strings.Contains(desc, want) {
			t.Errorf("single-CRS description lacks %q:\n%s", want, desc)
		}
	}
}

func TestMergeValueRanges(t *testing.T) {
	ok := func(lo, hi float64) sourceRange { return sourceRange{path: "ok.tif", lo: lo, hi: hi, origin: "scan"} }
	ocean := sourceRange{path: "ocean.tif", err: fmt.Errorf("x: %w", cog.ErrNoValueRange)}
	broken := sourceRange{path: "broken.tif", err: errors.New("read error")}

	lo, hi, err := mergeValueRanges([]sourceRange{ok(10, 200), ocean, ok(5, 100)})
	if err != nil || lo != 5 || hi != 200 {
		t.Errorf("with an all-nodata source: %g, %g, %v; want 5, 200", lo, hi, err)
	}
	if _, _, err := mergeValueRanges([]sourceRange{ocean}); !errors.Is(err, cog.ErrNoValueRange) {
		t.Errorf("a single all-nodata source: err = %v, want ErrNoValueRange", err)
	}
	if _, _, err := mergeValueRanges([]sourceRange{ocean, ocean}); !errors.Is(err, cog.ErrNoValueRange) {
		t.Errorf("only all-nodata sources: err = %v, want ErrNoValueRange", err)
	}
	if _, _, err := mergeValueRanges([]sourceRange{ok(0, 1), broken}); err == nil || !strings.Contains(err.Error(), "broken.tif") {
		t.Errorf("a failing source: err = %v", err)
	}
}

// JPEG drops the alpha of a fill or nodata colour and writes its RGB.
func TestJPEGAlphaWarning(t *testing.T) {
	msg := jpegAlphaWarning("--fill-missing", &color.RGBA{255, 0, 0, 128})
	if !strings.Contains(msg, "--fill-missing alpha 128") || !strings.Contains(msg, "rgb(255,0,0)") || strings.Contains(msg, "black") {
		t.Errorf("warning %q, want the colour written: rgb(255,0,0)", msg)
	}
}
