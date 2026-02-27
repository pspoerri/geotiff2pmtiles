package integration_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

const esaGamma0File = "ESA_WorldCover_10m_2021_v200_N00E009_S1VVVHratio.tif"

func openESAGamma0(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testdataDir, "esaworldcover-gamma0", esaGamma0File)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Run: make test-integration-download")
	}
	return path
}

// TestESAWorldCoverGamma0Properties opens the S1 VV/VH ratio (gamma0) file
// and validates its structure: 3-band 16-bit (VV, VH, VH/VV ratio), dB scaled.
func TestESAWorldCoverGamma0Properties(t *testing.T) {
	path := openESAGamma0(t)

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("cog.Open: %v", err)
	}
	defer r.Close()

	spp := r.SamplesPerPixel()
	bps := r.BitsPerSample()

	// S1 gamma0 composite: 3 bands (VV, VH, VH/VV ratio), 16-bit, dB scaled.
	if spp != 3 {
		t.Errorf("SamplesPerPixel = %d, want 3", spp)
	}
	if bps != 16 {
		t.Errorf("BitsPerSample = %d, want 16", bps)
	}
	if r.IsFloat() {
		t.Error("expected integer data, got float")
	}

	md := r.GDALMeta()
	if md == nil {
		t.Fatal("expected GDAL metadata")
	}
	if got := md.Items["bands"]; got == "" {
		t.Error("expected non-empty 'bands' item")
	}

	t.Logf("Gamma0: %dx%d, %d-bit, %d bands", r.Width(), r.Height(), bps, spp)
	t.Logf("GDAL bands: %s", md.Items["bands"])
	for band, items := range md.BandItems {
		t.Logf("  band %d: %v", band, items)
	}
}

// TestESAWorldCoverGamma0Pipeline converts the 3-band 16-bit SAR composite
// (VV, VH, VH/VV ratio) to PNG PMTiles with linear rescaling.
// Pixel values are dB-scaled (SCALE=0.001, OFFSET=-45): physical dB = pixel * 0.001 - 45.
// The useful range is approximately 0-65535 raw values.
func TestESAWorldCoverGamma0Pipeline(t *testing.T) {
	path := openESAGamma0(t)

	outPath := runPipeline(t, pipelineConfig{
		InputPaths: []string{path},
		Format:     "png",
		MinZoom:    7,
		MaxZoom:    9,
		BandCfg: cog.BandConfig{
			Bands:      [3]int{1, 2, 3},
			AlphaBand:  -1,
			Rescale:    cog.RescaleLinear,
			RescaleMin: 0,
			RescaleMax: 65535,
		},
		Concurrency: runtime.NumCPU(),
	})

	assertPlausiblePMTiles(t, outPath, plausibilityExpectation{
		MinZoom:       7,
		MaxZoom:       9,
		TileType:      pmtiles.TileTypePNG,
		MinLon:        9,
		MaxLon:        10,
		MinLat:        0,
		MaxLat:        1,
		BoundsTol:     2,
		MinTotalTiles: 5,
	})
}
