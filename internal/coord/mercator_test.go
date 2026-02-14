package coord

import (
	"math"
	"testing"
)

func TestLonLatToTile(t *testing.T) {
	tests := []struct {
		name      string
		lon, lat  float64
		zoom      int
		wantX     int
		wantY     int
	}{
		{"origin z0", 0, 0, 0, 0, 0},
		{"london z10", -0.1278, 51.5074, 10, 511, 340},
		{"zurich z10", 8.5417, 47.3769, 10, 536, 358},
		{"nyc z10", -74.0060, 40.7128, 10, 301, 385},
		{"tokyo z10", 139.6917, 35.6895, 10, 909, 403},
		{"south pole clamped", 0, -89.9, 1, 1, 1},
		{"north pole clamped", 0, 89.9, 1, 1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := LonLatToTile(tt.lon, tt.lat, tt.zoom)
			if x != tt.wantX || y != tt.wantY {
				t.Errorf("LonLatToTile(%.4f, %.4f, %d) = (%d, %d), want (%d, %d)",
					tt.lon, tt.lat, tt.zoom, x, y, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestTileBounds(t *testing.T) {
	// The tile at z=0, x=0, y=0 should cover the entire world.
	minLon, minLat, maxLon, maxLat := TileBounds(0, 0, 0)

	if math.Abs(minLon-(-180)) > 1e-6 {
		t.Errorf("z0 minLon = %v, want -180", minLon)
	}
	if math.Abs(maxLon-180) > 1e-6 {
		t.Errorf("z0 maxLon = %v, want 180", maxLon)
	}
	// Web Mercator latitude range: ~-85.05 to ~85.05
	if minLat < -85.1 || minLat > -85.0 {
		t.Errorf("z0 minLat = %v, want ~-85.05", minLat)
	}
	if maxLat < 85.0 || maxLat > 85.1 {
		t.Errorf("z0 maxLat = %v, want ~85.05", maxLat)
	}
}

func TestTileBounds_AdjacentTilesShare(t *testing.T) {
	// Adjacent tiles at z=2 should share edges.
	_, _, maxLon0, _ := TileBounds(2, 0, 0)
	minLon1, _, _, _ := TileBounds(2, 1, 0)

	if math.Abs(maxLon0-minLon1) > 1e-10 {
		t.Errorf("Adjacent tile edge mismatch: maxLon(0)=%v, minLon(1)=%v", maxLon0, minLon1)
	}

	_, minLat0, _, _ := TileBounds(2, 0, 0)
	_, _, _, maxLat1 := TileBounds(2, 0, 1)

	if math.Abs(minLat0-maxLat1) > 1e-10 {
		t.Errorf("Adjacent tile edge mismatch: minLat(row0)=%v, maxLat(row1)=%v", minLat0, maxLat1)
	}
}

func TestPixelToLonLat_TileCorners(t *testing.T) {
	// The top-left corner of tile (0,0,0) at pixel (0,0) should be (-180, ~85.05).
	lon, lat := PixelToLonLat(0, 0, 0, 256, 0, 0)
	if math.Abs(lon-(-180)) > 1e-6 {
		t.Errorf("top-left lon = %v, want -180", lon)
	}
	if lat < 85.0 || lat > 85.1 {
		t.Errorf("top-left lat = %v, want ~85.05", lat)
	}

	// The bottom-right pixel (256, 256) should be (180, ~-85.05).
	lon, lat = PixelToLonLat(0, 0, 0, 256, 256, 256)
	if math.Abs(lon-180) > 1e-6 {
		t.Errorf("bottom-right lon = %v, want 180", lon)
	}
	if lat < -85.1 || lat > -85.0 {
		t.Errorf("bottom-right lat = %v, want ~-85.05", lat)
	}
}

func TestPixelToLonLat_RoundTrip(t *testing.T) {
	// For a given tile, converting pixel->lonlat->pixel should roundtrip.
	z, tx, ty := 10, 535, 358
	tileSize := 256

	for px := 0.5; px < float64(tileSize); px += 50 {
		for py := 0.5; py < float64(tileSize); py += 50 {
			lon, lat := PixelToLonLat(z, tx, ty, tileSize, px, py)
			gotPx, gotPy := TilePixelCoords(lon, lat, z, tx, ty, tileSize)

			if math.Abs(gotPx-px) > 1e-6 || math.Abs(gotPy-py) > 1e-6 {
				t.Errorf("roundtrip pixel (%v, %v) -> (%v, %v) -> (%v, %v)",
					px, py, lon, lat, gotPx, gotPy)
			}
		}
	}
}

func TestResolutionAtLat(t *testing.T) {
	// At the equator, zoom 0, each pixel covers ~156543 meters.
	res0 := ResolutionAtLat(0, 0)
	expected0 := EarthCircumference / 256
	if math.Abs(res0-expected0)/expected0 > 1e-6 {
		t.Errorf("ResolutionAtLat(0, 0) = %v, want ~%v", res0, expected0)
	}

	// Each zoom level halves the resolution.
	res1 := ResolutionAtLat(0, 1)
	if math.Abs(res1-res0/2)/res0 > 1e-6 {
		t.Errorf("ResolutionAtLat(0, 1) = %v, want ~%v", res1, res0/2)
	}

	// Resolution at 60° latitude should be cos(60°) ≈ 0.5 of equatorial.
	res60 := ResolutionAtLat(60, 0)
	if math.Abs(res60-res0*0.5)/res0 > 1e-6 {
		t.Errorf("ResolutionAtLat(60, 0) = %v, want ~%v", res60, res0*0.5)
	}
}

func TestMaxZoomForResolution(t *testing.T) {
	tests := []struct {
		name       string
		pixelSize  float64
		lat        float64
		tileSize   int
		wantZoom   int
	}{
		{"10m equator", 10, 0, 256, 13},
		{"1m equator", 1, 0, 256, 17},
		{"100m equator", 100, 0, 256, 10},
		{"invalid zero", 0, 0, 256, 0},
		{"negative", -1, 0, 256, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxZoomForResolution(tt.pixelSize, tt.lat, tt.tileSize)
			if got != tt.wantZoom {
				t.Errorf("MaxZoomForResolution(%v, %v, %v) = %d, want %d",
					tt.pixelSize, tt.lat, tt.tileSize, got, tt.wantZoom)
			}
		})
	}
}

func TestTilesInBounds(t *testing.T) {
	// A small bounding box around Zurich at zoom 10.
	tiles := TilesInBounds(10, 8.4, 47.3, 8.6, 47.5)

	if len(tiles) == 0 {
		t.Fatal("TilesInBounds returned no tiles for Zurich area")
	}

	// Verify all tiles are within expected range.
	for _, tile := range tiles {
		z, x, y := tile[0], tile[1], tile[2]
		if z != 10 {
			t.Errorf("expected zoom 10, got %d", z)
		}
		// Zurich is roughly at tile (535, 358) at z10.
		if x < 530 || x > 540 {
			t.Errorf("tile x=%d outside expected range for Zurich", x)
		}
		if y < 355 || y > 360 {
			t.Errorf("tile y=%d outside expected range for Zurich", y)
		}
	}
}

func TestPixelSizeInGroundMeters(t *testing.T) {
	// For EPSG:4326, 1 degree at equator ≈ 111,320 m.
	got4326 := PixelSizeInGroundMeters(1.0, 4326, 0)
	expected := EarthCircumference / 360.0
	if math.Abs(got4326-expected)/expected > 1e-6 {
		t.Errorf("PixelSizeInGroundMeters(1.0, 4326, 0) = %v, want ~%v", got4326, expected)
	}

	// For EPSG:3857, 1 meter at equator = 1 ground meter.
	got3857 := PixelSizeInGroundMeters(1.0, 3857, 0)
	if math.Abs(got3857-1.0) > 1e-6 {
		t.Errorf("PixelSizeInGroundMeters(1.0, 3857, 0) = %v, want 1.0", got3857)
	}

	// For EPSG:2056 (metric), pixel size in meters = ground meters.
	got2056 := PixelSizeInGroundMeters(2.0, 2056, 47)
	if math.Abs(got2056-2.0) > 1e-6 {
		t.Errorf("PixelSizeInGroundMeters(2.0, 2056, 47) = %v, want 2.0", got2056)
	}
}

func TestMetersToPixelSizeCRS_InverseOfPixelSizeInGroundMeters(t *testing.T) {
	epsgs := []int{4326, 3857, 2056}
	lats := []float64{0, 30, 47, 60}
	pixelSizes := []float64{1.0, 10.0, 100.0}

	for _, epsg := range epsgs {
		for _, lat := range lats {
			for _, ps := range pixelSizes {
				crs := MetersToPixelSizeCRS(ps, epsg, lat)
				roundtrip := PixelSizeInGroundMeters(crs, epsg, lat)
				if math.Abs(roundtrip-ps)/ps > 1e-6 {
					t.Errorf("EPSG:%d lat=%.0f: MetersToPixelSizeCRS/PixelSizeInGroundMeters roundtrip %.4f -> %.4f -> %.4f",
						epsg, lat, ps, crs, roundtrip)
				}
			}
		}
	}
}

func TestLonLatToTile_Clamping(t *testing.T) {
	// Coordinates far outside valid range should be clamped.
	x, y := LonLatToTile(-200, 0, 5)
	if x < 0 {
		t.Errorf("negative x for lon=-200: %d", x)
	}

	x, y = LonLatToTile(200, 0, 5)
	maxTile := (1 << 5) - 1
	if x > maxTile {
		t.Errorf("x exceeds max for lon=200: %d > %d", x, maxTile)
	}
	_ = y
}
