package cog

import "testing"

// geoKeyDir builds a GeoKey directory from [KeyID, Value] pairs, all inline.
func geoKeyDir(pairs ...uint16) []uint16 {
	keys := []uint16{1, 1, 0, uint16(len(pairs) / 2)}
	for i := 0; i < len(pairs); i += 2 {
		keys = append(keys, pairs[i], 0, 1, pairs[i+1])
	}
	return keys
}

// The GeoTIFF origin is the upper-left corner. A PixelIsPoint tiepoint is the
// centre of pixel (I,J), so GDAL places the corner half a pixel up and left.
func TestParseGeoInfoRasterType(t *testing.T) {
	tests := []struct {
		name         string
		keys         []uint16
		tiepoint     []float64
		wantX, wantY float64
	}{
		{"area (default)", geoKeyDir(1024, 1, 3072, 32632), []float64{0, 0, 0, 500000, 5200000, 0}, 500000, 5200000},
		{"area", geoKeyDir(1024, 1, 1025, 1, 3072, 32632), []float64{0, 0, 0, 500000, 5200000, 0}, 500000, 5200000},
		{"point", geoKeyDir(1024, 1, 1025, 2, 3072, 32632), []float64{0, 0, 0, 500000, 5200000, 0}, 499985, 5200015},
		{"point, tiepoint at (2,1)", geoKeyDir(1024, 1, 1025, 2, 3072, 32632), []float64{2, 1, 0, 500060, 5199970, 0}, 499985, 5200015},
	}
	for _, tt := range tests {
		ifd := &IFD{ModelPixelScale: []float64{30, 30, 0}, ModelTiepoint: tt.tiepoint, GeoKeys: tt.keys}
		info := parseGeoInfo(ifd)
		if info.OriginX != tt.wantX || info.OriginY != tt.wantY {
			t.Errorf("%s: origin (%v, %v), want (%v, %v)", tt.name, info.OriginX, info.OriginY, tt.wantX, tt.wantY)
		}
	}
}
