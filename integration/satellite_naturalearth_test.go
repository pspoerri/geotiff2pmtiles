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

	assertPlausiblePMTiles(t, outPath, plausibilityExpectation{
		MinZoom:       0,
		MaxZoom:       4,
		TileType:      pmtiles.TileTypeJPEG,
		MinLon:        -180,
		MaxLon:        180,
		MinLat:        -85,
		MaxLat:        85,
		BoundsTol:     5,
		MinTotalTiles: 5,
	})
}
