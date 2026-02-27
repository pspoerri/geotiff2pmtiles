package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

const esaWorldCoverFile = "ESA_WorldCover_10m_2021_v200_N00E009_S2RGBNIR.tif"

func openESAWorldCover(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testdataDir, "esaworldcover", esaWorldCoverFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}
	return path
}

// TestESAWorldCoverPreset validates GDAL metadata parsing and preset
// auto-detection against the real ESA WorldCover S2 RGBNIR file.
func TestESAWorldCoverPreset(t *testing.T) {
	path := openESAWorldCover(t)

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}
	defer r.Close()

	// Verify GDAL metadata was parsed.
	md := r.GDALMeta()
	if md == nil {
		t.Fatal("expected GDAL metadata")
	}
	if got := md.Items["bands"]; got == "" {
		t.Error("expected non-empty 'bands' item")
	}

	// Verify auto-detection works.
	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset to be detected")
	}
	if preset.Name != "multispectral-rgbnir" {
		t.Errorf("name = %q, want %q", preset.Name, "multispectral-rgbnir")
	}
	// ESA WorldCover: Band 1=Red, Band 2=Green, Band 3=Blue, Band 4=NIR.
	if preset.BandCfg.Bands != [3]int{1, 2, 3} {
		t.Errorf("bands = %v, want [1,2,3]", preset.BandCfg.Bands)
	}
	if preset.BandCfg.RescaleMax != 10000 {
		t.Errorf("rescale max = %.0f, want 10000", preset.BandCfg.RescaleMax)
	}
	if preset.BandCfg.RescaleMin != 0 {
		t.Errorf("rescale min = %.0f, want 0", preset.BandCfg.RescaleMin)
	}
}

// TestESAWorldCoverPipeline runs the full GeoTIFF→PMTiles pipeline on the
// ESA WorldCover S2 RGBNIR 16-bit 4-band file using auto-detected preset
// settings (bands 1,2,3 with linear rescaling 0-10000).
func TestESAWorldCoverPipeline(t *testing.T) {
	path := openESAWorldCover(t)

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}

	// Use auto-detected preset for band config.
	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset to be detected")
	}
	r.Close()

	outPath := runPipeline(t, pipelineConfig{
		InputPaths:  []string{path},
		Format:      "png",
		MinZoom:     7,
		MaxZoom:     9,
		BandCfg:     preset.BandCfg,
		Concurrency: runtime.NumCPU(),
	})

	result := validatePMTiles(t, outPath)
	if result.TileCount == 0 {
		t.Error("expected tiles from ESA WorldCover")
	}
	if result.Header.TileType != pmtiles.TileTypePNG {
		t.Errorf("expected PNG tile type, got %d", result.Header.TileType)
	}

	for z := 7; z <= 9; z++ {
		if result.ZoomCounts[z] == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
	}

	t.Logf("ESA WorldCover: %d tiles across zoom 7-9", result.TileCount)
}
