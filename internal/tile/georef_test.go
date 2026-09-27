package tile

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// testGeoTIFF describes a single-tile, uncompressed GeoTIFF: 3-band uint8
// when RGB is set, 1-band float32 otherwise.
type testGeoTIFF struct {
	W, H       int // multiples of 16
	RGB        func(x, y int) [3]uint8
	Float      func(x, y int) float32
	TieX, TieY float64  // model coordinate of raster pixel (0,0)
	Scale      float64  // pixel size in CRS units
	GeoKeys    []uint16 // [KeyID, Value] pairs, stored inline
	NoData     string   // GDAL_NODATA, optional
}

func writeTestGeoTIFF(t *testing.T, g testGeoTIFF) string {
	t.Helper()
	bo := binary.LittleEndian
	shorts := func(v ...uint16) []byte {
		var b []byte
		for _, s := range v {
			b = bo.AppendUint16(b, s)
		}
		return b
	}
	doubles := func(v ...float64) []byte {
		var b []byte
		for _, d := range v {
			b = bo.AppendUint64(b, math.Float64bits(d))
		}
		return b
	}

	spp, bits, format, photometric := uint16(1), uint16(32), uint16(3), uint16(1)
	if g.RGB != nil {
		spp, bits, format, photometric = 3, 8, 1, 2
	}
	var pix []byte
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			if g.RGB != nil {
				c := g.RGB(x, y)
				pix = append(pix, c[:]...)
			} else {
				pix = bo.AppendUint32(pix, math.Float32bits(g.Float(x, y)))
			}
		}
	}
	keys := []uint16{1, 1, 0, uint16(len(g.GeoKeys) / 2)}
	for i := 0; i < len(g.GeoKeys); i += 2 {
		keys = append(keys, g.GeoKeys[i], 0, 1, g.GeoKeys[i+1])
	}

	type entry struct {
		tag, typ uint16
		count    uint32
		data     []byte
	}
	var bps, sf []uint16
	for i := uint16(0); i < spp; i++ {
		bps = append(bps, bits)
		sf = append(sf, format)
	}
	entries := []entry{
		{256, 3, 1, shorts(uint16(g.W))},
		{257, 3, 1, shorts(uint16(g.H))},
		{258, 3, uint32(spp), shorts(bps...)},
		{259, 3, 1, shorts(1)},
		{262, 3, 1, shorts(photometric)},
		{277, 3, 1, shorts(spp)},
		{322, 3, 1, shorts(uint16(g.W))},
		{323, 3, 1, shorts(uint16(g.H))},
		{324, 4, 1, nil}, // tile offset, set below
		{325, 4, 1, bo.AppendUint32(nil, uint32(len(pix)))},
		{339, 3, uint32(spp), shorts(sf...)},
		{33550, 12, 3, doubles(g.Scale, g.Scale, 0)},
		{33922, 12, 6, doubles(0, 0, 0, g.TieX, g.TieY, 0)},
		{34735, 3, uint32(len(keys)), shorts(keys...)},
	}
	if g.NoData != "" {
		entries = append(entries, entry{42113, 2, uint32(len(g.NoData) + 1), append([]byte(g.NoData), 0)})
	}

	// Layout: header, IFD, out-of-line values, pixels.
	extStart := 8 + 2 + 12*len(entries) + 4
	var ext []byte
	extOff := make([]uint32, len(entries))
	for i, e := range entries {
		if len(e.data) > 4 {
			extOff[i] = uint32(extStart + len(ext))
			ext = append(ext, e.data...)
		}
	}
	entries[8].data = bo.AppendUint32(nil, uint32(extStart+len(ext)))

	buf := []byte{'I', 'I'}
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for i, e := range entries {
		buf = bo.AppendUint16(buf, e.tag)
		buf = bo.AppendUint16(buf, e.typ)
		buf = bo.AppendUint32(buf, e.count)
		if len(e.data) > 4 {
			buf = bo.AppendUint32(buf, extOff[i])
		} else {
			v := make([]byte, 4)
			copy(v, e.data)
			buf = append(buf, v...)
		}
	}
	buf = bo.AppendUint32(buf, 0)
	buf = append(buf, ext...)
	buf = append(buf, pix...)

	path := filepath.Join(t.TempDir(), fmt.Sprintf("src%d.tif", len(buf)))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestSources(t *testing.T, paths ...string) []*cog.Reader {
	t.Helper()
	srcs, err := cog.OpenAll(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, s := range srcs {
			s.Close()
		}
	})
	return srcs
}

func mustSourceInfos(t *testing.T, srcs []*cog.Reader) []sourceInfo {
	t.Helper()
	infos, err := buildSourceInfos(srcs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

// redValue and terrariumElevation decode an output pixel for checkFeature.
func redValue(img *image.RGBA, x, y int) float64 { return float64(img.Pix[img.PixOffset(x, y)]) }

func terrariumElevation(img *image.RGBA, x, y int) float64 {
	i := img.PixOffset(x, y)
	return float64(img.Pix[i])*256 + float64(img.Pix[i+1]) + float64(img.Pix[i+2])/256 - 32768
}

// checkFeature checks that the centroid of value-minus-background over the
// pixels in area that hold data lies within tol of (wantX, wantY), in
// continuous tile pixel coordinates.
func checkFeature(t *testing.T, name string, img *image.RGBA, area image.Rectangle,
	value func(*image.RGBA, int, int) float64, background, wantX, wantY, tol float64) {
	t.Helper()
	if img == nil {
		t.Errorf("%s: no data", name)
		return
	}
	var sum, sx, sy float64
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if img.Pix[img.PixOffset(x, y)+3] == 0 {
				continue
			}
			w := value(img, x, y) - background
			sum += w
			sx += w * (float64(x) + 0.5)
			sy += w * (float64(y) + 0.5)
		}
	}
	if gotX, gotY := sx/sum, sy/sum; !(math.Abs(gotX-wantX) <= tol && math.Abs(gotY-wantY) <= tol) {
		t.Errorf("%s: feature at (%.3f, %.3f), want (%.3f, %.3f)", name, gotX, gotY, wantX, wantY)
	}
}

// featureRGB and featureFloat are sources with background bg and one pixel
// (fi, fj) of value fg.
func featureRGB(fi, fj int, bg, fg uint8) func(x, y int) [3]uint8 {
	return func(x, y int) [3]uint8 {
		if x == fi && y == fj {
			return [3]uint8{fg, fg, fg}
		}
		return [3]uint8{bg, bg, bg}
	}
}

func featureFloat(fi, fj int, bg, fg float32) func(x, y int) float32 {
	return func(x, y int) float32 {
		if x == fi && y == fj {
			return fg
		}
		return bg
	}
}

var allResamplings = []struct {
	name string
	mode Resampling
}{
	{"nearest", ResamplingNearest},
	{"bilinear", ResamplingBilinear},
	{"bicubic", ResamplingBicubic},
	{"lanczos", ResamplingLanczos},
	{"mode", ResamplingMode},
}

// A single bright source pixel must land where GDAL puts it: its centre is at
// tiepoint + (i+0.5)*scale for PixelIsArea and tiepoint + i*scale for
// PixelIsPoint. Checked at two zoom levels (2 and 4 output pixels per source
// pixel), for every resampling mode, through the RGB and the float path.
func TestFeatureLandsAtGDALPosition(t *testing.T) {
	const (
		z0, tx0, ty0 = 10, 536, 358
		fi, fj       = 13, 18 // feature pixel
		bg, fg       = 40, 200
	)
	res := func(z int) float64 { return 2 * coord.OriginShift / (256 * math.Pow(2, float64(z))) }
	scale := 2 * res(z0)
	// Raster corner on output pixel (64, 64) of tile z0/tx0/ty0.
	tieX := -coord.OriginShift + (float64(tx0)*256+64)*res(z0)
	tieY := coord.OriginShift - (float64(ty0)*256+64)*res(z0)
	merc := coord.ForEPSG(3857)

	for _, rasterType := range []struct {
		name  string
		value uint16
		off   float64 // feature centre in pixels from the tiepoint
	}{
		{"area", 1, 0.5},
		{"point", 2, 0},
	} {
		g := testGeoTIFF{W: 32, H: 32, TieX: tieX, TieY: tieY, Scale: scale,
			GeoKeys: []uint16{1024, 1, 1025, rasterType.value, 3072, 3857}}
		g.RGB = featureRGB(fi, fj, bg, fg)
		rgbPath := writeTestGeoTIFF(t, g)
		g.RGB, g.Float = nil, featureFloat(fi, fj, bg, fg)
		floatPath := writeTestGeoTIFF(t, g)
		srcs := openTestSources(t, rgbPath, floatPath)
		rgbInfos := mustSourceInfos(t, srcs[:1])
		floatInfos := mustSourceInfos(t, srcs[1:])

		lon, lat := merc.ToWGS84(tieX+(fi+rasterType.off)*scale, tieY-(fj+rasterType.off)*scale)
		for _, z := range []int{z0, z0 + 1} {
			tx, ty := coord.LonLatToTile(lon, lat, z)
			wantX, wantY := coord.TilePixelCoords(lon, lat, z, tx, ty, 256)
			for _, rs := range allResamplings {
				name := fmt.Sprintf("%s z%d %s", rasterType.name, z, rs.name)
				whole := image.Rect(0, 0, 256, 256)
				img := renderTile(z, tx, ty, 256, rgbInfos, cog.NewTileCache(16), rs.mode, nil)
				checkFeature(t, name+" rgb", img, whole, redValue, bg, wantX, wantY, 0.05)
				img = renderTileTerrarium(z, tx, ty, 256, floatInfos, cog.NewFloatTileCache(16), rs.mode)
				checkFeature(t, name+" float", img, whole, terrariumElevation, bg, wantX, wantY, 0.05)
			}
		}
	}
}

// Inputs in different CRSs, here UTM zones 32 and 33 either side of 12°E like
// neighbouring Sentinel-2 tiles, are each reprojected with their own CRS.
func TestMixedCRSSourcesLandInPlace(t *testing.T) {
	const (
		z      = 14
		bg, fg = 40, 200
	)
	tx, ty := coord.LonLatToTile(12.008, 47, z)
	minLon, minLat, maxLon, maxLat := coord.TileBounds(z, tx, ty)
	lat := (minLat + maxLat) / 2
	scale := 4 * coord.ResolutionAtLat(lat, z, 256) // ~4 output pixels per source pixel
	features := []struct {
		epsg int
		lon  float64
	}{
		{32632, minLon + (maxLon-minLon)/4},
		{32633, minLon + (maxLon-minLon)*3/4},
	}

	var rgbPaths, floatPaths []string
	for _, f := range features {
		// Centre of pixel (16,16) on the feature.
		x, y := coord.ForEPSG(f.epsg).FromWGS84(f.lon, lat)
		g := testGeoTIFF{W: 32, H: 32, TieX: x - 16.5*scale, TieY: y + 16.5*scale, Scale: scale,
			GeoKeys: []uint16{1024, 1, 3072, uint16(f.epsg)}}
		g.RGB = featureRGB(16, 16, bg, fg)
		rgbPaths = append(rgbPaths, writeTestGeoTIFF(t, g))
		g.RGB, g.Float = nil, featureFloat(16, 16, bg, fg)
		floatPaths = append(floatPaths, writeTestGeoTIFF(t, g))
	}
	srcs := openTestSources(t, append(rgbPaths, floatPaths...)...)
	rgbInfos := mustSourceInfos(t, srcs[:2])
	floatInfos := mustSourceInfos(t, srcs[2:])

	for _, rs := range allResamplings {
		rgb := renderTile(z, tx, ty, 256, rgbInfos, cog.NewTileCache(16), rs.mode, nil)
		dem := renderTileTerrarium(z, tx, ty, 256, floatInfos, cog.NewFloatTileCache(16), rs.mode)
		for _, f := range features {
			wantX, wantY := coord.TilePixelCoords(f.lon, lat, z, tx, ty, 256)
			area := image.Rect(int(wantX)-16, int(wantY)-16, int(wantX)+16, int(wantY)+16)
			name := fmt.Sprintf("EPSG:%d %s", f.epsg, rs.name)
			checkFeature(t, name+" rgb", rgb, area, redValue, bg, wantX, wantY, 0.1)
			checkFeature(t, name+" float", dem, area, terrariumElevation, bg, wantX, wantY, 0.1)
		}
	}
}

func TestBuildSourceInfosRejectsUnknownCRS(t *testing.T) {
	for _, tt := range []struct {
		keys []uint16
		want string
	}{
		{[]uint16{1024, 1, 2048, 4326, 3072, 32767}, "user-defined CRS"},
		{[]uint16{1024, 1, 3072, 1}, "unsupported EPSG code: 1"},
	} {
		path := writeTestGeoTIFF(t, testGeoTIFF{W: 16, H: 16, Scale: 10, TieX: 1e6, TieY: 1e6, GeoKeys: tt.keys,
			Float: featureFloat(0, 0, 0, 0)})
		_, err := buildSourceInfos(openTestSources(t, path), nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("keys %v: got error %v, want %q", tt.keys, err, tt.want)
		}
	}
}
