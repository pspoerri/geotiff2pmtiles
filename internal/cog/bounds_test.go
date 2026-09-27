package cog

import (
	"math"
	"testing"
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
		// EPSG:3035 area of use; its corners used to come back as +Inf.
		{"3035 EEA grid", []*Reader{extentReader(3035, 900000, 900000, 7400000, 5500000)},
			Bounds{MinLon: -56.505142, MaxLon: 72.906137, MinLat: 24.284177, MaxLat: 58.952751}},
		// A source whose corners do not transform is skipped.
		{"skip non-finite", []*Reader{
			extentReader(4326, math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1)),
			extentReader(4326, 5, 45, 10, 48),
		}, Bounds{MinLon: 5, MaxLon: 10, MinLat: 45, MaxLat: 48}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergedBoundsWGS84(tt.sources)
			if math.Abs(got.MinLon-tt.want.MinLon) > 1e-6 || math.Abs(got.MaxLon-tt.want.MaxLon) > 1e-6 ||
				math.Abs(got.MinLat-tt.want.MinLat) > 1e-6 || math.Abs(got.MaxLat-tt.want.MaxLat) > 1e-6 {
				t.Errorf("MergedBoundsWGS84 = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMergedBoundsWGS84_NoFiniteCorner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MergedBoundsWGS84 returned bounds although no corner transforms")
		}
	}()
	MergedBoundsWGS84([]*Reader{extentReader(4326, math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1))})
}
