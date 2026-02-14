package coord

import (
	"math"
	"testing"
)

func TestForEPSG(t *testing.T) {
	tests := []struct {
		epsg     int
		wantNil  bool
		wantEPSG int
	}{
		{2056, false, 2056},
		{4326, false, 4326},
		{3857, false, 3857},
		{32632, true, 0}, // UTM 32N — unsupported
		{0, true, 0},
	}
	for _, tt := range tests {
		p := ForEPSG(tt.epsg)
		if tt.wantNil {
			if p != nil {
				t.Errorf("ForEPSG(%d) = %v, want nil", tt.epsg, p)
			}
			continue
		}
		if p == nil {
			t.Fatalf("ForEPSG(%d) = nil, want non-nil", tt.epsg)
		}
		if got := p.EPSG(); got != tt.wantEPSG {
			t.Errorf("ForEPSG(%d).EPSG() = %d, want %d", tt.epsg, got, tt.wantEPSG)
		}
	}
}

func TestWGS84Identity(t *testing.T) {
	w := &WGS84Identity{}

	if w.EPSG() != 4326 {
		t.Errorf("WGS84Identity.EPSG() = %d, want 4326", w.EPSG())
	}

	// Identity: ToWGS84 and FromWGS84 should return input unchanged.
	lon, lat := 8.5417, 47.3769 // Zurich
	gotLon, gotLat := w.ToWGS84(lon, lat)
	if gotLon != lon || gotLat != lat {
		t.Errorf("ToWGS84(%v, %v) = (%v, %v), want (%v, %v)", lon, lat, gotLon, gotLat, lon, lat)
	}

	gotLon, gotLat = w.FromWGS84(lon, lat)
	if gotLon != lon || gotLat != lat {
		t.Errorf("FromWGS84(%v, %v) = (%v, %v), want (%v, %v)", lon, lat, gotLon, gotLat, lon, lat)
	}
}

// TestProjectionRoundTrip verifies that ToWGS84(FromWGS84(lon, lat)) ≈ (lon, lat) for all projections.
func TestProjectionRoundTrip(t *testing.T) {
	// Points inside Switzerland (valid for LV95) and also valid for other projections.
	points := [][2]float64{
		{8.5417, 47.3769}, // Zurich
		{6.6323, 46.5197}, // Lausanne
		{7.4474, 46.9480}, // Bern
		{9.3767, 47.4245}, // St. Gallen
		{8.9511, 46.0037}, // Lugano
	}

	projections := []Projection{
		&WGS84Identity{},
		&WebMercatorProj{},
		&SwissLV95{},
	}

	for _, proj := range projections {
		for _, pt := range points {
			lon, lat := pt[0], pt[1]

			// Forward: WGS84 -> CRS
			x, y := proj.FromWGS84(lon, lat)

			// Inverse: CRS -> WGS84
			gotLon, gotLat := proj.ToWGS84(x, y)

			// The roundtrip error should be very small.
			// SwissLV95 uses polynomial approximation, so allow ~1m error (~0.00001°).
			tol := 1e-4
			if dLon := math.Abs(gotLon - lon); dLon > tol {
				t.Errorf("EPSG:%d roundtrip lon for (%.4f, %.4f): got %.6f, want %.6f (delta=%.2e)",
					proj.EPSG(), lon, lat, gotLon, lon, dLon)
			}
			if dLat := math.Abs(gotLat - lat); dLat > tol {
				t.Errorf("EPSG:%d roundtrip lat for (%.4f, %.4f): got %.6f, want %.6f (delta=%.2e)",
					proj.EPSG(), lon, lat, gotLat, lat, dLat)
			}
		}
	}
}

// TestWebMercatorProj_KnownValues checks against well-known Web Mercator values.
func TestWebMercatorProj_KnownValues(t *testing.T) {
	wm := &WebMercatorProj{}

	// (0, 0) in Web Mercator should map to (0, 0) in WGS84.
	lon, lat := wm.ToWGS84(0, 0)
	if math.Abs(lon) > 1e-10 || math.Abs(lat) > 1e-10 {
		t.Errorf("ToWGS84(0, 0) = (%v, %v), want (0, 0)", lon, lat)
	}

	// (0, 0) in WGS84 should map to (0, ~0) in Web Mercator.
	x, y := wm.FromWGS84(0, 0)
	if math.Abs(x) > 1e-6 || math.Abs(y) > 1e-6 {
		t.Errorf("FromWGS84(0, 0) = (%v, %v), want (0, ~0)", x, y)
	}

	// lon=180 should map to x = OriginShift (~20037508.34)
	x, _ = wm.FromWGS84(180, 0)
	if math.Abs(x-OriginShift) > 1 {
		t.Errorf("FromWGS84(180, 0).x = %v, want ~%v", x, OriginShift)
	}

	// lon=-180 should map to x = -OriginShift
	x, _ = wm.FromWGS84(-180, 0)
	if math.Abs(x+OriginShift) > 1 {
		t.Errorf("FromWGS84(-180, 0).x = %v, want ~%v", x, -OriginShift)
	}
}
