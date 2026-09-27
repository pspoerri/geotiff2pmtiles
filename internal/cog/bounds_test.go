package cog

import (
	"math"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// extentReader returns a header-only reader covering [minX, maxX] x [minY, maxY]
// in the given CRS, enough for BoundsInCRS and MergedBoundsWGS84.
func extentReader(epsg int, minX, minY, maxX, maxY float64) *Reader {
	const w, h = 100, 100
	return &Reader{
		ifds: []IFD{{Width: w, Height: h}},
		geo: GeoInfo{
			EPSG:       epsg,
			OriginX:    minX,
			OriginY:    maxY,
			PixelSizeX: (maxX - minX) / w,
			PixelSizeY: (maxY - minY) / h,
		},
	}
}

func TestMergedBoundsWGS84(t *testing.T) {
	tests := []struct {
		name    string
		sources []*Reader
		want    Bounds
	}{
		// The standard EEA grid (CORINE Land Cover, EU-DEM) reaches past the
		// EPSG:3035 area of use; its corners used to come back as +Inf. Its
		// north edge bulges to 72.6°N between corners at 58.95°N.
		{"3035 EEA grid", []*Reader{extentReader(3035, 900000, 900000, 7400000, 5500000)},
			Bounds{MinLon: -56.505142, MaxLon: 72.906137, MinLat: 24.284177, MaxLat: 72.606609}},
		// A pole inside a polar stereographic raster: every longitude.
		{"3413 around the North Pole", []*Reader{extentReader(3413, -1000000, -1000000, 1000000, 1000000)},
			Bounds{MinLon: -180, MaxLon: 180, MinLat: 76.998816, MaxLat: 90}},
		{"3031 around the South Pole", []*Reader{extentReader(3031, -1000000, -1000000, 1000000, 1000000)},
			Bounds{MinLon: -180, MaxLon: 180, MinLat: -90, MaxLat: -77.037401}},
		{"3413 beside the pole", []*Reader{extentReader(3413, 500000, 500000, 1000000, 1000000)},
			Bounds{MinLon: 71.565051, MaxLon: 108.434949, MinLat: 76.998816, MaxLat: 83.479261}},
		// In EPSG:4326 the pole is the top edge, not a point inside.
		{"4326 up to 90N", []*Reader{extentReader(4326, -10, 80, 10, 90)},
			Bounds{MinLon: -10, MaxLon: 10, MinLat: 80, MaxLat: 90}},
		// A source whose corners do not transform is skipped.
		{"skip non-finite", []*Reader{
			extentReader(4326, math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1)),
			extentReader(4326, 5, 45, 10, 48),
		}, Bounds{MinLon: 5, MaxLon: 10, MinLat: 45, MaxLat: 48}},
		// Crossing the antimeridian: MaxLon continues past 180.
		{"UTM 60 across 180", []*Reader{extentReader(32660, 700000, 5000000, 900000, 5100000)},
			Bounds{MinLon: 179.543123, MaxLon: 182.160168, MinLat: 45.040405, MaxLat: 46.024353}},
		{"UTM 1 across 180", []*Reader{extentReader(32601, 100000, 5000000, 300000, 5100000)},
			Bounds{MinLon: 177.839832, MaxLon: 180.456877, MinLat: 45.040405, MaxLat: 46.024353}},
		{"0..360 grid", []*Reader{extentReader(4326, 0, -90, 360, 90)},
			Bounds{MinLon: -180, MaxLon: 180, MinLat: -90, MaxLat: 90}},
		{"Pacific 100..260 grid", []*Reader{extentReader(4326, 100, -60, 260, 60)},
			Bounds{MinLon: 100, MaxLon: 260, MinLat: -60, MaxLat: 60}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergedBoundsWGS84(tt.sources)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.MinLon-tt.want.MinLon) > 1e-6 || math.Abs(got.MaxLon-tt.want.MaxLon) > 1e-6 ||
				math.Abs(got.MinLat-tt.want.MinLat) > 1e-6 || math.Abs(got.MaxLat-tt.want.MaxLat) > 1e-6 {
				t.Errorf("MergedBoundsWGS84 = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A UTM tile straddling its central meridian reaches furthest north at the
// meridian, mid-edge; corners alone missed ~400 m at 60°N, a row of tiles
// from z16 on.
func TestMergedBoundsWGS84_CurvedEdge(t *testing.T) {
	const minX, maxX, maxY = 445100, 554900, 6709800 // a Sentinel-2 tile, EPSG:32632
	got, err := MergedBoundsWGS84([]*Reader{extentReader(32632, minX, 6600000, maxX, maxY)})
	if err != nil {
		t.Fatal(err)
	}
	proj := coord.ForEPSG(32632)
	_, top := proj.ToWGS84(500000, maxY)
	_, corner := proj.ToWGS84(minX, maxY)
	const twoMeters = 2.0 / 111320
	if got.MaxLat < top-twoMeters || got.MaxLat > top {
		t.Errorf("MaxLat = %.7f, want within 2 m below %.7f (corners: %.7f)", got.MaxLat, top, corner)
	}
}

func TestMergedBoundsWGS84_NoFiniteCorner(t *testing.T) {
	b, err := MergedBoundsWGS84([]*Reader{extentReader(4326, math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1))})
	if err == nil {
		t.Errorf("MergedBoundsWGS84 = %+v, want an error: no point transforms", b)
	}
}
