package cog

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TFW holds the six parameters from a TIFF World File (.tfw).
//
// Line 1: pixel width (x-component of pixel size)
// Line 2: rotation about y-axis (typically 0)
// Line 3: rotation about x-axis (typically 0)
// Line 4: pixel height (y-component, typically negative for north-up)
// Line 5: x-coordinate of the center of the upper-left pixel
// Line 6: y-coordinate of the center of the upper-left pixel
type TFW struct {
	PixelSizeX float64 // line 1: x-component of pixel width
	RotationY  float64 // line 2: rotation about y-axis
	RotationX  float64 // line 3: rotation about x-axis
	PixelSizeY float64 // line 4: y-component of pixel height (negative = north-up)
	OriginX    float64 // line 5: x of upper-left pixel center
	OriginY    float64 // line 6: y of upper-left pixel center
}

// parseTFW reads a TFW (TIFF World File) from the given path.
func parseTFW(path string) (*TFW, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading TFW %s: %w", path, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 6 {
		return nil, fmt.Errorf("TFW %s: expected 6 lines, got %d", path, len(lines))
	}

	vals := make([]float64, 6)
	for i := 0; i < 6; i++ {
		v, err := strconv.ParseFloat(strings.TrimSpace(lines[i]), 64)
		if err != nil {
			return nil, fmt.Errorf("TFW %s line %d: %w", path, i+1, err)
		}
		vals[i] = v
	}

	tfw := &TFW{
		PixelSizeX: vals[0],
		RotationY:  vals[1],
		RotationX:  vals[2],
		PixelSizeY: vals[3],
		OriginX:    vals[4],
		OriginY:    vals[5],
	}

	if tfw.RotationX != 0 || tfw.RotationY != 0 {
		return nil, fmt.Errorf("TFW %s: rotated world files are not supported (rotation: %f, %f)",
			path, tfw.RotationX, tfw.RotationY)
	}

	return tfw, nil
}

// findTFW looks for a TFW sidecar file alongside the given TIFF path.
// Checks extensions: .tfw, .TFW, .tifw, .TIFW
func findTFW(tiffPath string) string {
	ext := filepath.Ext(tiffPath)
	base := tiffPath[:len(tiffPath)-len(ext)]

	candidates := []string{".tfw", ".TFW", ".tifw", ".TIFW"}
	for _, c := range candidates {
		p := base + c
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// toGeoInfo converts TFW parameters into a GeoInfo struct.
// The TFW origin is the center of the upper-left pixel; we adjust to the
// corner (top-left edge) which is what the rest of the pipeline expects.
func (tfw *TFW) toGeoInfo() GeoInfo {
	return GeoInfo{
		PixelSizeX: math.Abs(tfw.PixelSizeX),
		PixelSizeY: math.Abs(tfw.PixelSizeY),
		OriginX:    tfw.OriginX - math.Abs(tfw.PixelSizeX)/2,
		OriginY:    tfw.OriginY + math.Abs(tfw.PixelSizeY)/2,
	}
}

// inferEPSG guesses the EPSG code from the coordinate ranges.
// Falls back to EPSG:4326 (WGS84) when coordinates look like geographic lon/lat.
func inferEPSG(info GeoInfo, width, height uint32) int {
	maxX := info.OriginX + float64(width)*info.PixelSizeX
	minY := info.OriginY - float64(height)*info.PixelSizeY

	if info.OriginX >= -180 && maxX <= 360 &&
		minY >= -90 && info.OriginY <= 90 {
		return 4326
	}

	if math.Abs(info.OriginX) > 100000 || math.Abs(info.OriginY) > 100000 {
		if info.OriginX >= 2400000 && info.OriginX <= 2900000 &&
			info.OriginY >= 1000000 && info.OriginY <= 1400000 {
			return 2056
		}
		if math.Abs(info.OriginX) <= 20037508.34 && math.Abs(info.OriginY) <= 20048966.10 {
			return 3857
		}
	}

	return 4326
}
