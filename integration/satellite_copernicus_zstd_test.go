package integration_test

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TestCopernicusZSTD converts the ZSTD-recompressed Copernicus DEM (TIFF
// compression 50000, predictor 3) and checks it decodes to the same elevations
// as the Deflate original in TestCopernicusDEM.
// Requires GDAL at download time: make test-integration-download
func TestCopernicusZSTD(t *testing.T) {
	path := filepath.Join(testdataDir, "copernicus-zstd", "copernicus_dem_n46_e008_zstd.tif")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download (needs gdal_translate)")
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
