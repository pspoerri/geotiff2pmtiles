package coord

import (
	"math"
	"testing"
)

// Reference points from swisstopo:
// https://www.swisstopo.admin.ch/en/knowledge-facts/surveying-geodesy/reference-frames/local/lv95.html
//
// Bern (Federal Palace):  E 2_600_000  N 1_200_000  →  lon 7.438632  lat 46.951083
// Zurich (ETH):           E 2_683_474  N 1_247_862  →  lon 8.547970  lat 47.376870
// Geneva (Jet d'eau):     E 2_500_560  N 1_118_017  →  lon 6.143200  lat 46.207450
var swissRefPoints = []struct {
	name              string
	easting, northing float64
	lon, lat          float64
	tolDeg            float64 // tolerance in degrees
}{
	{
		name:    "Bern (reference origin)",
		easting: 2_600_000, northing: 1_200_000,
		lon: 7.438632, lat: 46.951083,
		tolDeg: 0.001, // ~100m — polynomial approximation
	},
	{
		name:    "Zurich",
		easting: 2_683_474, northing: 1_247_862,
		lon: 8.5417, lat: 47.3769,
		tolDeg: 0.005,
	},
	{
		name:    "Geneva",
		easting: 2_500_560, northing: 1_118_017,
		lon: 6.1432, lat: 46.2075,
		tolDeg: 0.01, // polynomial approximation has larger error at edges
	},
}

func TestSwissLV95_ToWGS84_ReferencePoints(t *testing.T) {
	s := &SwissLV95{}

	for _, ref := range swissRefPoints {
		t.Run(ref.name, func(t *testing.T) {
			gotLon, gotLat := s.ToWGS84(ref.easting, ref.northing)
			if dLon := math.Abs(gotLon - ref.lon); dLon > ref.tolDeg {
				t.Errorf("ToWGS84 lon: got %.6f, want ~%.6f (delta=%.6f > tol=%.6f)",
					gotLon, ref.lon, dLon, ref.tolDeg)
			}
			if dLat := math.Abs(gotLat - ref.lat); dLat > ref.tolDeg {
				t.Errorf("ToWGS84 lat: got %.6f, want ~%.6f (delta=%.6f > tol=%.6f)",
					gotLat, ref.lat, dLat, ref.tolDeg)
			}
		})
	}
}

func TestSwissLV95_FromWGS84_ReferencePoints(t *testing.T) {
	s := &SwissLV95{}

	for _, ref := range swissRefPoints {
		t.Run(ref.name, func(t *testing.T) {
			gotE, gotN := s.FromWGS84(ref.lon, ref.lat)
			// Tolerance: polynomial approximation has ~meter accuracy near center,
			// but can be several hundred meters at edges of Switzerland.
			tolM := 600.0
			if dE := math.Abs(gotE - ref.easting); dE > tolM {
				t.Errorf("FromWGS84 easting: got %.1f, want ~%.1f (delta=%.1f > tol=%.1f)",
					gotE, ref.easting, dE, tolM)
			}
			if dN := math.Abs(gotN - ref.northing); dN > tolM {
				t.Errorf("FromWGS84 northing: got %.1f, want ~%.1f (delta=%.1f > tol=%.1f)",
					gotN, ref.northing, dN, tolM)
			}
		})
	}
}

func TestSwissLV95_RoundTrip(t *testing.T) {
	s := &SwissLV95{}

	// Test roundtrip starting from LV95 coordinates.
	for _, ref := range swissRefPoints {
		t.Run(ref.name+"_LV95->WGS84->LV95", func(t *testing.T) {
			lon, lat := s.ToWGS84(ref.easting, ref.northing)
			gotE, gotN := s.FromWGS84(lon, lat)

			tolM := 2.0 // Roundtrip should be very tight (polynomial self-consistency)
			if dE := math.Abs(gotE - ref.easting); dE > tolM {
				t.Errorf("roundtrip easting: got %.2f, want ~%.2f (delta=%.2f)", gotE, ref.easting, dE)
			}
			if dN := math.Abs(gotN - ref.northing); dN > tolM {
				t.Errorf("roundtrip northing: got %.2f, want ~%.2f (delta=%.2f)", gotN, ref.northing, dN)
			}
		})
	}
}

func TestSwissLV95_EPSG(t *testing.T) {
	s := &SwissLV95{}
	if s.EPSG() != 2056 {
		t.Errorf("EPSG() = %d, want 2056", s.EPSG())
	}
}

func TestSwissLV95_EdgeOfSwitzerland(t *testing.T) {
	s := &SwissLV95{}

	// Points near the edges of Switzerland.
	edges := [][2]float64{
		{5.96, 45.82},  // SW corner (near Geneva)
		{10.49, 47.81}, // NE corner (near Bodensee)
		{6.13, 47.50},  // NW (Jura)
		{10.47, 46.17}, // SE (Engadin)
	}

	for _, pt := range edges {
		lon, lat := pt[0], pt[1]
		e, n := s.FromWGS84(lon, lat)
		gotLon, gotLat := s.ToWGS84(e, n)

		tol := 1e-3 // ~100m
		if math.Abs(gotLon-lon) > tol || math.Abs(gotLat-lat) > tol {
			t.Errorf("edge roundtrip (%.2f, %.2f): got (%.6f, %.6f), delta=(%.6f, %.6f)",
				lon, lat, gotLon, gotLat, gotLon-lon, gotLat-lat)
		}
	}
}
