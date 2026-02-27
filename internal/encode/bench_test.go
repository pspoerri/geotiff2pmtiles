package encode

import (
	"image"
	"image/color"
	"testing"
)

// gradientImage creates a tileSize×tileSize RGBA image with a smooth gradient.
// Used for PNG and JPEG encode benchmarks where a natural-looking image matters.
func gradientImage(tileSize int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	return img
}

// terrariumImage creates a tileSize×tileSize RGBA image with Terrarium-encoded
// elevation values, simulating typical DEM tile content.
func terrariumImage(tileSize int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			// Elevation from -100 m to +2900 m, varying by position.
			elev := float64(x+y)/float64(2*tileSize)*3000.0 - 100.0
			c := ElevationToTerrarium(elev)
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// --- PNG encode benchmarks ---

// BenchmarkPNGEncode measures PNG encoding of a full-color gradient tile.
// PNG is the default output format and is used for every tile in the archive.
func BenchmarkPNGEncode(b *testing.B) {
	enc := &PNGEncoder{}
	img := gradientImage(256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = enc.Encode(img)
	}
}

// BenchmarkPNGEncode_Gray measures PNG encoding of a gray (single-channel)
// image, which is the common case for classification rasters.
func BenchmarkPNGEncode_Gray(b *testing.B) {
	enc := &PNGEncoder{}
	// Build a gray RGBA image (R=G=B, A=255).
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			v := uint8((x + y) % 256)
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = enc.Encode(img)
	}
}

// --- JPEG encode benchmarks ---

// BenchmarkJPEGEncode_Q85 measures JPEG encoding at quality 85, the default
// for satellite imagery tiles.
func BenchmarkJPEGEncode_Q85(b *testing.B) {
	enc := &JPEGEncoder{Quality: 85}
	img := gradientImage(256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = enc.Encode(img)
	}
}

// BenchmarkJPEGEncode_Q75 measures JPEG encoding at quality 75 for comparison.
func BenchmarkJPEGEncode_Q75(b *testing.B) {
	enc := &JPEGEncoder{Quality: 75}
	img := gradientImage(256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = enc.Encode(img)
	}
}

// --- Terrarium encode/decode benchmarks ---

// BenchmarkTerrariumEncode measures PNG encoding of a Terrarium-format tile.
// Terrarium tiles contain pre-encoded elevation data, so the encode step is
// just PNG compression of the RGB values.
func BenchmarkTerrariumEncode(b *testing.B) {
	enc := &TerrariumEncoder{}
	img := terrariumImage(256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = enc.Encode(img)
	}
}

// BenchmarkElevationToTerrarium measures the per-pixel elevation→RGB
// conversion called when writing each Terrarium tile pixel.
func BenchmarkElevationToTerrarium(b *testing.B) {
	elevations := [8]float64{0, 100, -50, 1500, 3000, -100, 500, 2500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ElevationToTerrarium(elevations[i&7])
	}
}

// BenchmarkTerrariumToElevation measures the per-pixel RGB→elevation
// decoding called in the Terrarium-aware downsample path.
func BenchmarkTerrariumToElevation(b *testing.B) {
	pixels := [8]color.RGBA{
		ElevationToTerrarium(0),
		ElevationToTerrarium(100),
		ElevationToTerrarium(-50),
		ElevationToTerrarium(1500),
		ElevationToTerrarium(3000),
		ElevationToTerrarium(-100),
		ElevationToTerrarium(500),
		ElevationToTerrarium(2500),
	}
	var sink float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink += TerrariumToElevation(pixels[i&7])
	}
	_ = sink
}
