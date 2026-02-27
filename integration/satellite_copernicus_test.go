package integration_test

import (
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
}
