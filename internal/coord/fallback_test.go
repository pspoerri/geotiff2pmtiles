package coord

import (
	"math"
	"testing"
)

// TestCRSFallback_OutsideAreaOfUse covers points wroge/crs rejects by default,
// because they lie outside the EPSG area of use or outside every datum
// transformation's area. They used to come back as +Inf (nodata).
func TestCRSFallback_OutsideAreaOfUse(t *testing.T) {
	tests := []struct {
		name     string
		epsg     int
		x, y     float64
		lon, lat float64
	}{
		// Corners of the standard EEA grid (CORINE Land Cover, EU-DEM).
		{"3035 EEA grid SW", 3035, 900000, 900000, -23.825994, 24.284177},
		{"3035 EEA grid NW", 3035, 900000, 5500000, -56.505142, 56.484652},
		{"3035 EEA grid SE", 3035, 7400000, 900000, 40.662707, 25.544711},
		{"3035 EEA grid NE", 3035, 7400000, 5500000, 72.906137, 58.952751},
		// Outside all ETRS89 -> WGS84 transformation areas.
		{"3035 Canaries", 3035, 1759253, 989737, -16, 28},
		// NSIDC sea ice grid corner, across the antimeridian from the
		// central meridian (-45); the longitude is not wrapped.
		{"3413 NSIDC corner", 3413, -3850000, 5850000, -191.650299, 30.979512},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ForEPSG(tt.epsg)
			lon, lat := p.ToWGS84(tt.x, tt.y)
			if math.Abs(lon-tt.lon) > 1e-5 || math.Abs(lat-tt.lat) > 1e-5 {
				t.Errorf("ToWGS84(%v, %v) = (%v, %v), want (%v, %v)", tt.x, tt.y, lon, lat, tt.lon, tt.lat)
			}
			x, y := p.FromWGS84(tt.lon, tt.lat)
			if math.Abs(x-tt.x) > 1 || math.Abs(y-tt.y) > 1 {
				t.Errorf("FromWGS84(%v, %v) = (%.1f, %.1f), want (%v, %v)", tt.lon, tt.lat, x, y, tt.x, tt.y)
			}
		})
	}
}

// TestCRSFallback_InverseShift checks ToWGS84 against FromWGS84 for datums
// with large shifts to 1e-7° (~1 cm): the inverse must evaluate the shift at
// the WGS84 point, not at the source-datum point 2.3° away for NTF (Paris).
func TestCRSFallback_InverseShift(t *testing.T) {
	for _, tc := range []struct {
		epsg     int
		lon, lat float64
	}{
		{4807, 2.3522, 48.8566},   // NTF (Paris) in grads, 2.3° prime meridian offset
		{27700, -0.1276, 51.5072}, // OSGB36, ~100 m
		{23030, -3.7038, 40.4168}, // ED50, ~150 m
	} {
		p := ForEPSG(tc.epsg)
		lon, lat := p.ToWGS84(p.FromWGS84(tc.lon, tc.lat))
		if math.Abs(lon-tc.lon) > 1e-7 || math.Abs(lat-tc.lat) > 1e-7 {
			t.Errorf("EPSG:%d round trip (%v, %v) = (%v, %v)", tc.epsg, tc.lon, tc.lat, lon, lat)
		}
	}
}

// TestCRSFallback_ReferenceShift checks that a point no datum transformation
// covers still gets a datum shift. ED50 is ~150 m off WGS84, so a zero shift
// would misplace the pixel by that much.
func TestCRSFallback_ReferenceShift(t *testing.T) {
	p := ForEPSG(23030).(*CRSFallback) // ED50 / UTM 30N
	lon, lat := -20.0, 40.0            // Atlantic, west of every ED50 transformation
	x, y := p.FromWGS84(lon, lat)
	x0, y0, _, err := p.project(lon, lat, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := math.Hypot(x-x0, y-y0); !(d > 100 && d < 300) {
		t.Errorf("FromWGS84(%v, %v) is %.1f m from the unshifted position, want 100-300 m", lon, lat, d)
	}
}
