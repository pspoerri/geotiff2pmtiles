package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

func openSwissImage(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(testdataDir, "swissimage")
	matches, err := filepath.Glob(filepath.Join(dir, "*.tif"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("Run: make test-integration-download")
	}
	// Verify at least one file is accessible.
	if _, err := os.Stat(matches[0]); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}
	return matches
}

// TestSwissImagePipeline converts swisstopo SWISSIMAGE DOP10 orthophotos
// (8-bit RGB, EPSG:2056 LV95, 10cm resolution) to JPEG PMTiles.
// This exercises multi-source mosaic from a Swiss projected CRS.
// Requires test data: make test-integration-download
func TestSwissImagePipeline(t *testing.T) {
	paths := openSwissImage(t)

	outPath := runPipeline(t, pipelineConfig{
		InputPaths:  paths,
		Format:      "jpeg",
		MinZoom:     14,
		MaxZoom:     18,
		Concurrency: runtime.NumCPU(),
	})

	assertPlausiblePMTiles(t, outPath, plausibilityExpectation{
		MinZoom:       14,
		MaxZoom:       18,
		TileType:      pmtiles.TileTypeJPEG,
		MinLon:        7.4,
		MaxLon:        7.5,
		MinLat:        46.9,
		MaxLat:        47.0,
		BoundsTol:     2,
		MinTotalTiles: 50,
	})

	t.Logf("SWISSIMAGE: from %d source files", len(paths))
}
