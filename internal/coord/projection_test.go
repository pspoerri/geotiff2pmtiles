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
		{32632, false, 32632}, // UTM 32N
		{32733, false, 32733}, // UTM 33S
		{25832, false, 25832}, // ETRS89 / UTM 32N
		{2154, false, 2154},   // RGF93 / Lambert-93 — wroge/crs fallback
		{999999, true, 0},
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

func TestWGS84Identity_Lon360(t *testing.T) {
	w := &WGS84Identity{Lon360: true}
	for _, tt := range []struct{ lon, want float64 }{
		{-170, 190},
		{-0.5, 359.5},
		{0, 0},
		{120, 120},
		{180, 180},
	} {
		if got, _ := w.FromWGS84(tt.lon, 10); got != tt.want {
			t.Errorf("FromWGS84(%v) = %v, want %v", tt.lon, got, tt.want)
		}
	}

	// A grid starting half a 0.25° pixel west of 0 keeps that strip.
	w = &WGS84Identity{Lon360: true, Lon360Min: -0.125}
	for _, tt := range []struct{ lon, want float64 }{
		{-170, 190},
		{-0.5, 359.5},
		{-0.125, -0.125},
		{-0.1, -0.1},
		{120, 120},
	} {
		if got, _ := w.FromWGS84(tt.lon, 10); got != tt.want {
			t.Errorf("Lon360Min -0.125: FromWGS84(%v) = %v, want %v", tt.lon, got, tt.want)
		}
	}
}

func TestWGS84ForGrid(t *testing.T) {
	for _, tt := range []struct {
		name           string
		minX, maxX, px float64
		want           WGS84Identity
	}{
		{"world", -180, 180, 0.25, WGS84Identity{}},
		{"world with rounding noise", -180, 180.0000001, 0.25, WGS84Identity{}},
		{"0..360", 0, 360, 0.25, WGS84Identity{Lon360: true}},
		{"GFS pixel centres from 0", -0.125, 359.875, 0.25, WGS84Identity{Lon360: true, Lon360Min: -0.125}},
		{"Pacific 100..260", 100, 260, 0.1, WGS84Identity{Lon360: true}},
		{"more than a pixel west of 0", -0.3, 359.7, 0.25, WGS84Identity{}},
		{"-10..350", -10, 350, 1, WGS84Identity{}},
	} {
		if got := WGS84ForGrid(tt.minX, tt.maxX, tt.px); got != tt.want {
			t.Errorf("%s: WGS84ForGrid(%v, %v, %v) = %+v, want %+v", tt.name, tt.minX, tt.maxX, tt.px, got, tt.want)
		}
	}
}

func TestWGS84Identity_LonRange(t *testing.T) {
	plain := &WGS84Identity{}
	lon360 := &WGS84Identity{Lon360: true, Lon360Min: -0.125}
	for _, tt := range []struct {
		name             string
		w                *WGS84Identity
		minLon, maxLon   float64
		wantMin, wantMax float64
	}{
		{"plain identity", plain, -180, 0, -180, 0},
		{"east of the wrap point", lon360, 10, 20, 10, 20},
		{"west of it: shifted whole", lon360, -90, -45, 270, 315},
		{"east edge on the wrap point", lon360, -90, -0.125, 270, 359.875},
		{"across it (z0 tile)", lon360, -180, 180, -0.125, 359.875},
	} {
		if gotMin, gotMax := tt.w.LonRange(tt.minLon, tt.maxLon); gotMin != tt.wantMin || gotMax != tt.wantMax {
			t.Errorf("%s: LonRange(%v, %v) = (%v, %v), want (%v, %v)", tt.name, tt.minLon, tt.maxLon, gotMin, gotMax, tt.wantMin, tt.wantMax)
		}
	}
}

func TestWrapLonRange(t *testing.T) {
	tests := []struct {
		name             string
		minLon, maxLon   float64
		wantMin, wantMax float64
	}{
		{"inside", 5, 10, 5, 10},
		{"world", -180, 180, -180, 180},
		{"0..360 grid", 0, 360, -180, 180},
		{"wider than the world", -180.5, 180.5, -180, 180},
		{"rounding noise at -180", -180.0000000001, 170, -180, 170},
		{"rounding noise at 180", 170, 180.0000000001, 170, 180},
		{"UTM 60 across 180", 176.5, 182.1, 176.5, 182.1},
		{"UTM 1 across 180", -182.1, -176.5, 177.9, 183.5},
		{"Pacific grid", 100, 260, 100, 260},
		{"east of 180", 190, 200, -170, -160},
		{"west of -180", -200, -190, 160, 170},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMin, gotMax := WrapLonRange(tt.minLon, tt.maxLon)
			if math.Abs(gotMin-tt.wantMin) > 1e-9 || math.Abs(gotMax-tt.wantMax) > 1e-9 {
				t.Errorf("WrapLonRange(%v, %v) = (%v, %v), want (%v, %v)",
					tt.minLon, tt.maxLon, gotMin, gotMax, tt.wantMin, tt.wantMax)
			}
		})
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
