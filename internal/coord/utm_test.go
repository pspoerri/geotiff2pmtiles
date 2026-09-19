package coord

import (
	"math"
	"testing"

	"github.com/wroge/crs"
)

// TestUTM_AgainstCRS cross-checks the native UTM implementation against
// wroge/crs across zones, and hemispheres.
func TestUTM_AgainstCRS(t *testing.T) {
	for _, epsg := range []int{32601, 32632, 32660, 32701, 32733, 32760} {
		u, ok := ForEPSG(epsg).(*UTM)
		if !ok {
			t.Fatalf("ForEPSG(%d) is not native UTM", epsg)
		}
		ref, err := crs.Transform(4326, epsg)
		if err != nil {
			t.Fatal(err)
		}
		lon0 := u.lon0 * 180 / math.Pi
		latSign := 1.0
		if u.north != 0 {
			latSign = -1
		}
		for _, dLon := range []float64{-2.9, -1, 0, 2, 2.9} {
			for _, lat := range []float64{0.5, 30, 47.4, 80} {
				lon, lat := lon0+dLon, lat*latSign
				wantE, wantN, _, err := ref(lon, lat, 0)
				if err != nil {
					t.Fatal(err)
				}
				e, n := u.FromWGS84(lon, lat)
				if math.Abs(e-wantE) > 1e-3 || math.Abs(n-wantN) > 1e-3 {
					t.Errorf("EPSG:%d FromWGS84(%v, %v) = (%.4f, %.4f), crs says (%.4f, %.4f)", epsg, lon, lat, e, n, wantE, wantN)
				}
				gotLon, gotLat := u.ToWGS84(e, n)
				if math.Abs(gotLon-lon) > 1e-8 || math.Abs(gotLat-lat) > 1e-8 {
					t.Errorf("EPSG:%d roundtrip (%v, %v) = (%v, %v)", epsg, lon, lat, gotLon, gotLat)
				}
			}
		}
	}
}

func TestCRSFallback_RoundTrip(t *testing.T) {
	p, ok := ForEPSG(2154).(*CRSFallback) // Lambert-93
	if !ok {
		t.Fatal("ForEPSG(2154) is not CRSFallback")
	}
	x, y := p.FromWGS84(2.3522, 48.8566) // Paris ≈ (652 km, 6862 km)
	if math.Abs(x-652_000) > 1000 || math.Abs(y-6_862_000) > 1000 {
		t.Errorf("FromWGS84(Paris) = (%.0f, %.0f)", x, y)
	}
	lon, lat := p.ToWGS84(x, y)
	if math.Abs(lon-2.3522) > 1e-6 || math.Abs(lat-48.8566) > 1e-6 {
		t.Errorf("roundtrip = (%v, %v)", lon, lat)
	}
}

func BenchmarkUTM_FromWGS84(b *testing.B) {
	u := utmForEPSG(32632)
	for i := 0; i < b.N; i++ {
		u.FromWGS84(10.0, 50.0)
	}
}

// TestCRSFallback_MatchesExact checks the cached datum shift against the
// exact (slow) wroge/crs transform. OSGB36 has a ~100 m shift to WGS84.
func TestCRSFallback_MatchesExact(t *testing.T) {
	for _, tc := range []struct {
		epsg     int
		lon, lat float64
	}{
		{2154, 2.3522, 48.8566},   // Lambert-93, Paris
		{27700, -0.1276, 51.5072}, // British National Grid, London
		{3035, 13.4050, 52.5200},  // ETRS89-LAEA, Berlin
	} {
		p, ok := ForEPSG(tc.epsg).(*CRSFallback)
		if !ok {
			t.Fatalf("ForEPSG(%d) is not CRSFallback", tc.epsg)
		}
		wantX, wantY, _, err := p.fromWGS84(tc.lon, tc.lat, 0)
		if err != nil {
			t.Fatal(err)
		}
		x, y := p.FromWGS84(tc.lon, tc.lat)
		if math.Abs(x-wantX) > 0.01 || math.Abs(y-wantY) > 0.01 {
			t.Errorf("EPSG:%d FromWGS84 = (%.3f, %.3f), exact (%.3f, %.3f)", tc.epsg, x, y, wantX, wantY)
		}
	}
}

func BenchmarkCRSFallback_FromWGS84(b *testing.B) {
	p := ForEPSG(2154)
	for i := 0; i < b.N; i++ {
		p.FromWGS84(2.35, 48.85)
	}
}
