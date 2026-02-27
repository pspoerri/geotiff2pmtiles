package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

const esaSWIRFile = "ESA_WorldCover_10m_2021_v200_N00E009_SWIR.tif"

func openESASWIR(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testdataDir, "esaworldcover-swir", esaSWIRFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}
	return path
}

// TestESAWorldCoverSWIRProperties opens the SWIR file and validates its structure.
// The SWIR composite has 2 bands (B11, B12) at 8-bit, 20m resolution.
func TestESAWorldCoverSWIRProperties(t *testing.T) {
	path := openESASWIR(t)

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}
	defer r.Close()

	spp := r.SamplesPerPixel()
	bps := r.BitsPerSample()

	// SWIR composite: 2 bands (B11-p50, B12-p50), 8-bit.
	if spp != 2 {
		t.Errorf("SamplesPerPixel = %d, want 2", spp)
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

	t.Logf("SWIR: %dx%d, %d-bit, %d bands", r.Width(), r.Height(), bps, spp)
	t.Logf("GDAL bands: %s", md.Items["bands"])
}

// TestESAWorldCoverSWIRPipeline converts the 2-band SWIR composite to
// PNG PMTiles, mapping B11→R, B12→G, B11→B for a false-color composite.
func TestESAWorldCoverSWIRPipeline(t *testing.T) {
	path := openESASWIR(t)

	// 2-band 8-bit: map B11→R, B12→G, B11→B for a composite view.
	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{path},
		Format:     "png",
		MinZoom:    7,
		MaxZoom:    9,
		BandCfg: cog.BandConfig{
			Bands:     [3]int{1, 2, 1},
			AlphaBand: -1,
		},
		Concurrency: runtime.NumCPU(),
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from ESA WorldCover SWIR")
	}
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected PNG tile type, got %d", result.Header.TileType)
	}

	for z := 7; z <= 9; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("ESA WorldCover SWIR: %d tiles across zoom 7-9", result.TileCount)
}
