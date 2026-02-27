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

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from SWISSIMAGE DOP10")
	}
	if result.Header.TileType != pmtiles.TileTypeJPEG {
		t.Errorf("expected JPEG tile type, got %d", result.Header.TileType)
	}

	// Multi-source mosaic should produce tiles across the zoom range.
	for z := 14; z <= 18; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("SWISSIMAGE: %d tiles across zoom 14-18 from %d source files", result.TileCount, len(paths))
}
