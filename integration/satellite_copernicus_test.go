package integration_test

import (
	"bytes"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TestCopernicusDEM converts a Copernicus DEM float32 GeoTIFF to terrarium PMTiles.
// Requires the test data to be downloaded first: make test-integration-download
func TestCopernicusDEM(t *testing.T) {
	path := filepath.Join(testdataDir, "copernicus", "copernicus_dem_n46_e008.tif")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}

	outPath := runPipeline(t, pipelineConfig{
		InputPaths:  []string{path},
		Format:      "terrarium",
		MinZoom:     7,
		MaxZoom:     10,
		Concurrency: runtime.NumCPU(),
	})

	assertPlausiblePMTiles(t, outPath, plausibilityExpectation{
		MinZoom:       7,
		MaxZoom:       10,
		TileType:      pmtiles.TileTypePNG,
		MinLon:        8,
		MaxLon:        9,
		MinLat:        46,
		MaxLat:        47,
		BoundsTol:     2,
		MinTotalTiles: 10,
	})

	// Decoded terrarium elevations must match the source DEM. Truth values
	// were read from the GeoTIFF with gdallocationinfo; the tolerance covers
	// resampling of 30 m source pixels to ~105 m/px at z10. This guards the
	// whole float path (deflate + floating-point predictor + terrarium
	// encoding), which once produced pure noise while the tile-count checks
	// above still passed.
	for _, pt := range []struct {
		lon, lat, elevation float64
	}{
		{8.7, 46.1, 193.0},  // Lake Lugano shore (flat)
		{8.5, 46.5, 1479.0}, // mid-slope Alpine terrain
	} {
		got := terrariumElevationAt(t, outPath, 10, pt.lon, pt.lat)
		if math.Abs(got-pt.elevation) > 75 {
			t.Errorf("elevation at (%v, %v): got %.1f m, want %.1f m ±75", pt.lon, pt.lat, got, pt.elevation)
		}
	}
}

// terrariumElevationAt decodes the terrarium-encoded elevation at a lon/lat
// from the PMTiles archive at the given zoom.
func terrariumElevationAt(t *testing.T, path string, z int, lon, lat float64) float64 {
	t.Helper()

	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	n := math.Exp2(float64(z))
	fx := (lon + 180) / 360 * n
	latR := lat * math.Pi / 180
	fy := (1 - math.Log(math.Tan(latR)+1/math.Cos(latR))/math.Pi) / 2 * n
	tx, ty := int(fx), int(fy)

	data, err := r.ReadTile(z, tx, ty)
	if err != nil || data == nil {
		t.Fatalf("tile %d/%d/%d: %v", z, tx, ty, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding tile %d/%d/%d: %v", z, tx, ty, err)
	}
	px := int((fx - float64(tx)) * 256)
	py := int((fy - float64(ty)) * 256)
	cr, cg, cb, _ := img.At(px, py).RGBA()
	return float64(cr>>8)*256 + float64(cg>>8) + float64(cb>>8)/256 - 32768
}
