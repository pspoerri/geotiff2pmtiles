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

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from Copernicus DEM")
	}
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected PNG tile type for terrarium, got %d", result.Header.TileType)
	}

	// Verify tiles exist at each zoom level.
	for z := 7; z <= 10; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("Copernicus DEM: %d tiles across zoom 7-10", result.TileCount)
}
