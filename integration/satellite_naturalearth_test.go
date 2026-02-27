package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TestNaturalEarthRaster converts a Natural Earth 8-bit RGB raster with TFW
// sidecar to JPEG PMTiles. Requires test data: make test-integration-download
func TestNaturalEarthRaster(t *testing.T) {
	path := filepath.Join(testdataDir, "naturalearth", "HYP_HR_SR_OB_DR.tif")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}

	outPath := runPipeline(t, pipelineConfig{
		InputPaths:  []string{path},
		Format:      "jpeg",
		MinZoom:     0,
		MaxZoom:     4,
		Concurrency: runtime.NumCPU(),
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from Natural Earth raster")
	}
	if result.Header.TileType != pmtiles.TileTypeJPEG {
		t.Errorf("expected JPEG tile type, got %d", result.Header.TileType)
	}

	// Global dataset should have tiles at every zoom.
	for z := 0; z <= 4; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("Natural Earth: %d tiles across zoom 0-4", result.TileCount)
}
