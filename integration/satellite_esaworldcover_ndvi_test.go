package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

const esaNDVIFile = "ESA_WorldCover_10m_2021_v200_N00E009_NDVI.tif"

func openESANDVI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testdataDir, "esaworldcover-ndvi", esaNDVIFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}
	return path
}

// TestESAWorldCoverNDVIProperties opens the NDVI composite and validates
// its structure: 3-band 8-bit (NDVI-p90, NDVI-p50, NDVI-p10).
func TestESAWorldCoverNDVIProperties(t *testing.T) {
	path := openESANDVI(t)

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}
	defer r.Close()

	spp := r.SamplesPerPixel()
	bps := r.BitsPerSample()

	// NDVI composite: 3 bands (NDVI-p90, NDVI-p50, NDVI-p10), 8-bit.
	if spp != 3 {
		t.Errorf("SamplesPerPixel = %d, want 3", spp)
	}
	if bps != 8 {
		t.Errorf("BitsPerSample = %d, want 8", bps)
	}

	md := r.GDALMeta()
	if md == nil {
		t.Fatal("expected GDAL metadata")
	}
	if got := md.Items["bands"]; got == "" {
		t.Error("expected non-empty 'bands' item")
	}

	t.Logf("NDVI: %dx%d, %d-bit, %d bands", r.Width(), r.Height(), bps, spp)
	t.Logf("GDAL bands: %s", md.Items["bands"])
}

// TestESAWorldCoverNDVIPipeline converts the 3-band NDVI composite to
// PNG PMTiles. Bands map naturally as RGB: p90→R, p50→G, p10→B.
func TestESAWorldCoverNDVIPipeline(t *testing.T) {
	path := openESANDVI(t)

	// 3-band 8-bit: default band mapping (1,2,3) works as-is.
	outPath := runPipeline(t, pipelineConfig{
		InputPaths:  []string{path},
		Format:      "png",
		MinZoom:     7,
		MaxZoom:     9,
		Concurrency: runtime.NumCPU(),
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from ESA WorldCover NDVI")
	}
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected PNG tile type, got %d", result.Header.TileType)
	}

	for z := 7; z <= 9; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("ESA WorldCover NDVI: %d tiles across zoom 7-9", result.TileCount)
}
