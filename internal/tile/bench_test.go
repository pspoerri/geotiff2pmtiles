package tile

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// --- Helper constructors ---

// grayImage creates a tileSize×tileSize RGBA image where R=G=B=v, A=255.
// This simulates single-channel data like ESA WorldCover.
func grayImage(tileSize int, v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = v
		pix[i+1] = v
		pix[i+2] = v
		pix[i+3] = 255
	}
	return img
}

// grayCheckerImage creates a tileSize×tileSize RGBA image with alternating
// gray values (R=G=B, A=255) to simulate non-uniform single-channel data.
func grayCheckerImage(tileSize int, v1, v2 uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	pix := img.Pix
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			off := (y*tileSize + x) * 4
			v := v1
			if (x+y)%2 == 1 {
				v = v2
			}
			pix[off] = v
			pix[off+1] = v
			pix[off+2] = v
			pix[off+3] = 255
		}
	}
	return img
}

// rgbaCheckerImage creates a tileSize×tileSize RGBA image with two distinct
// RGBA colors (not single-channel).
func rgbaCheckerImage(tileSize int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	c1 := color.RGBA{255, 0, 0, 255}
	c2 := color.RGBA{0, 0, 255, 255}
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			if (x+y)%2 == 0 {
				img.SetRGBA(x, y, c1)
			} else {
				img.SetRGBA(x, y, c2)
			}
		}
	}
	return img
}

// --- Detection benchmarks ---

func BenchmarkDetectUniform_Solid(b *testing.B) {
	img := solidImage(256, color.RGBA{100, 100, 100, 255})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectUniform(img)
	}
}

func BenchmarkDetectUniform_NonUniform(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectUniform(img)
	}
}

func BenchmarkDetectGray_SingleChannel(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectGray(img)
	}
}

func BenchmarkDetectGray_MultiChannel(b *testing.B) {
	img := rgbaCheckerImage(256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectGray(img)
	}
}

// --- newTileData pipeline benchmarks ---

func BenchmarkNewTileData_Uniform(b *testing.B) {
	img := solidImage(256, color.RGBA{42, 42, 42, 255})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newTileData(img, 256)
	}
}

func BenchmarkNewTileData_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		newTileData(img, 256)
	}
}

func BenchmarkNewTileData_RGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		newTileData(img, 256)
	}
}

// --- ToRGBA expansion benchmarks ---

func BenchmarkToRGBA_FromGray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	if !td.IsGray() {
		b.Fatal("expected gray tile")
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.ToRGBA()
	}
}

func BenchmarkToRGBA_FromUniform(b *testing.B) {
	td := newTileDataUniform(color.RGBA{42, 42, 42, 255}, 256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.ToRGBA()
	}
}

func BenchmarkToRGBA_FromRGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	td := newTileData(img, 256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.ToRGBA()
	}
}

// --- Serialization roundtrip benchmarks ---

func BenchmarkSerialize_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	buf := make([]byte, 0, 256*256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		td.SerializeAppend(buf)
	}
}

func BenchmarkSerialize_RGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	td := newTileData(img, 256)
	buf := make([]byte, 0, 256*256*4)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		td.SerializeAppend(buf)
	}
}

func BenchmarkSerialize_Uniform(b *testing.B) {
	td := newTileDataUniform(color.RGBA{42, 42, 42, 255}, 256)
	buf := make([]byte, 0, 4)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		td.SerializeAppend(buf)
	}
}

func BenchmarkDeserialize_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	buf, typ := td.SerializeAppend(nil)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DeserializeTileData(buf, typ, 256)
	}
}

func BenchmarkDeserialize_RGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	td := newTileData(img, 256)
	buf, typ := td.SerializeAppend(nil)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DeserializeTileData(buf, typ, 256)
	}
}

// --- RGBAAt benchmarks (hot in downsample) ---

func BenchmarkRGBAAt_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		td.RGBAAt(i%256, (i/256)%256)
	}
}

func BenchmarkRGBAAt_RGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	td := newTileData(img, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		td.RGBAAt(i%256, (i/256)%256)
	}
}

func BenchmarkRGBAAt_Uniform(b *testing.B) {
	td := newTileDataUniform(color.RGBA{42, 42, 42, 255}, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		td.RGBAAt(i%256, (i/256)%256)
	}
}

// --- Downsample benchmarks ---

func BenchmarkDownsample_GrayChildren_Nearest(b *testing.B) {
	tileSize := 256
	img1 := grayCheckerImage(tileSize, 50, 150)
	img2 := grayCheckerImage(tileSize, 100, 200)
	tl := newTileData(img1, tileSize)
	tr := newTileData(img2, tileSize)
	bl := newTileData(img1, tileSize)
	br := newTileData(img2, tileSize)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		downsampleTile(tl, tr, bl, br, tileSize, ResamplingNearest)
	}
}

func BenchmarkDownsample_GrayChildren_Bilinear(b *testing.B) {
	tileSize := 256
	img1 := grayCheckerImage(tileSize, 50, 150)
	img2 := grayCheckerImage(tileSize, 100, 200)
	tl := newTileData(img1, tileSize)
	tr := newTileData(img2, tileSize)
	bl := newTileData(img1, tileSize)
	br := newTileData(img2, tileSize)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		downsampleTile(tl, tr, bl, br, tileSize, ResamplingBilinear)
	}
}

func BenchmarkDownsample_RGBAChildren_Nearest(b *testing.B) {
	tileSize := 256
	c1 := color.RGBA{255, 0, 0, 255}
	c2 := color.RGBA{0, 255, 0, 255}
	img1 := checkerImage(tileSize, c1, c2)
	img2 := checkerImage(tileSize, c2, c1)
	tl := newTileData(img1, tileSize)
	tr := newTileData(img2, tileSize)
	bl := newTileData(img1, tileSize)
	br := newTileData(img2, tileSize)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		downsampleTile(tl, tr, bl, br, tileSize, ResamplingNearest)
	}
}

func BenchmarkDownsample_RGBAChildren_Bilinear(b *testing.B) {
	tileSize := 256
	c1 := color.RGBA{255, 0, 0, 255}
	c2 := color.RGBA{0, 255, 0, 255}
	img1 := checkerImage(tileSize, c1, c2)
	img2 := checkerImage(tileSize, c2, c1)
	tl := newTileData(img1, tileSize)
	tr := newTileData(img2, tileSize)
	bl := newTileData(img1, tileSize)
	br := newTileData(img2, tileSize)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		downsampleTile(tl, tr, bl, br, tileSize, ResamplingBilinear)
	}
}

func BenchmarkDownsample_UniformChildren(b *testing.B) {
	tileSize := 256
	ocean := color.RGBA{0, 50, 150, 255}
	child := solidTile(tileSize, ocean)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		downsampleTile(child, child, child, child, tileSize, ResamplingBilinear)
	}
}

// --- DiskTileStore benchmarks ---

func BenchmarkDiskTileStore_PutGet_InMemory(b *testing.B) {
	store := NewDiskTileStore(DiskTileStoreConfig{
		InitialCapacity:  b.N,
		TileSize:         256,
		MemoryLimitBytes: 0, // no spilling
		Format:           "png",
	})
	defer store.Close()

	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)

	// Pre-encode the tile (store keeps encoded bytes in memory).
	enc := &encode.PNGEncoder{}
	encoded, err := enc.Encode(td.AsImage())
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Put(13, i%8192, i/8192, td, encoded)
	}
	b.StopTimer()

	// Verify gets work (decodes from in-memory encoded bytes).
	if store.Get(13, 0, 0) == nil {
		b.Fatal("expected non-nil get")
	}
}

func BenchmarkDiskTileStore_PutFlushGet(b *testing.B) {
	dir, _ := os.MkdirTemp("", "bench-diskstore-*")
	defer os.RemoveAll(dir)

	store := NewDiskTileStore(DiskTileStoreConfig{
		InitialCapacity:  1024,
		TileSize:         256,
		TempDir:          dir,
		MemoryLimitBytes: 1, // >0 enables continuous disk I/O
		Format:           "png",
	})
	defer store.Close()

	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)

	// Pre-encode the tile for disk storage.
	enc := &encode.PNGEncoder{}
	encoded, err := enc.Encode(td.AsImage())
	if err != nil {
		b.Fatal(err)
	}

	// Put tiles (I/O goroutine writes them to disk continuously).
	nTiles := 1000
	for i := 0; i < nTiles; i++ {
		store.Put(13, i%100, i/100, td, encoded)
	}

	// Drain I/O before reading back.
	store.Drain()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x := i % 100
		y := (i / 100) % 10
		store.Get(13, x, y)
	}
}

// --- Memory allocation benchmarks ---

func BenchmarkMemoryBytes_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		td.MemoryBytes()
	}
}

// --- AsImage benchmarks (hot in encode path) ---

func BenchmarkAsImage_Gray(b *testing.B) {
	img := grayCheckerImage(256, 100, 200)
	td := newTileData(img, 256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.AsImage()
	}
}

func BenchmarkAsImage_RGBA(b *testing.B) {
	img := rgbaCheckerImage(256)
	td := newTileData(img, 256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.AsImage()
	}
}

func BenchmarkAsImage_Uniform(b *testing.B) {
	td := newTileDataUniform(color.RGBA{42, 42, 42, 255}, 256)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.AsImage()
	}
}
