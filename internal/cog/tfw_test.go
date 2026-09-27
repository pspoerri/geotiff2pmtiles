package cog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTFWTIFF writes a 16x16 TIFF without ModelPixelScale/Tiepoint, plus a
// world file placing it at UTM 32N (500000, 5300000) with 10 m pixels.
// extra is added to the IFD, e.g. GeoKeys.
func writeTFWTIFF(t *testing.T, extra ...tagEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ortho.tif")
	if err := os.WriteFile(path, tinyImage(false, extra...), 0o644); err != nil {
		t.Fatal(err)
	}
	tfw := "10\n0\n0\n-10\n500005\n5299995\n"
	if err := os.WriteFile(strings.TrimSuffix(path, ".tif")+".tfw", []byte(tfw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureLog redirects the standard logger for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestOpenTFWEPSG(t *testing.T) {
	// ProjectedCSTypeGeoKey = 32632 (WGS 84 / UTM 32N), no pixel grid tags.
	utm32 := entry(tagGeoKeyDirectoryTag, dtShort,
		1, 1, 0, 2,
		gkModelTypeGeoKey, 0, 1, 1,
		gkProjectedCSTypeGeoKey, 0, 1, 32632)

	tests := []struct {
		name        string
		extra       []tagEntry
		wantEPSG    int
		wantGuessed bool
	}{
		// The world file gives only the pixel grid; the GeoKeys CRS stays.
		{"geokeys-and-tfw", []tagEntry{utm32}, 32632, false},
		// No CRS anywhere: the CRS is guessed from the coordinate ranges.
		{"tfw-only", nil, 3857, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLog(t)
			path := writeTFWTIFF(t, tt.extra...)
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()

			if got := r.EPSG(); got != tt.wantEPSG {
				t.Errorf("EPSG() = %d, want %d", got, tt.wantEPSG)
			}
			if g := r.GeoInfo(); g.OriginX != 500000 || g.OriginY != 5300000 || g.PixelSizeX != 10 {
				t.Errorf("GeoInfo() = %+v, want the world file's grid", g)
			}
			if got := r.EPSGGuessed(); got != tt.wantGuessed {
				t.Errorf("EPSGGuessed() = %v, want %v", got, tt.wantGuessed)
			}
			// The caller warns, once for all files and only if it does not
			// set the CRS itself.
			if logs.Len() != 0 {
				t.Errorf("Open logged %q", logs)
			}
		})
	}
}

func TestSetEPSG(t *testing.T) {
	captureLog(t)
	r, err := Open(writeTFWTIFF(t))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.SetEPSG(32632)
	if got := r.EPSG(); got != 32632 {
		t.Errorf("EPSG() after SetEPSG(32632) = %d", got)
	}
	if r.EPSGGuessed() {
		t.Error("EPSGGuessed() after SetEPSG = true")
	}
}
