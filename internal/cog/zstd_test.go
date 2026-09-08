package cog

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// ZSTD (compression 50000) tiles are independent zstd frames, with the
// predictor applied after decompression like Deflate/LZW.
func TestReadFloatTileZSTDPredictor(t *testing.T) {
	const w, h = 16, 16
	bo := binary.LittleEndian

	want := make([]float32, w*h)
	for i := range want {
		want[i] = float32(i) * 0.5
	}
	var raw []byte
	for y := 0; y < h; y++ {
		raw = append(raw, encodeFloatPredictor(want[y*w:(y+1)*w], 1, bo)...)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := enc.EncodeAll(raw, nil)

	path := filepath.Join(t.TempDir(), "float-zstd.tif")
	writeTiledFloatTIFF(t, path, w, h, 50000, 3, payload)

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got, gw, gh, err := r.ReadFloatTile(0, 0, 0)
	if err != nil {
		t.Fatalf("ReadFloatTile: %v", err)
	}
	if gw != w || gh != h {
		t.Fatalf("tile size = %dx%d, want %dx%d", gw, gh, w, h)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel %d = %v, want %v", i, got[i], want[i])
		}
	}
	if d := r.FormatDescription(); d != "ZSTD, 1x float32" {
		t.Fatalf("FormatDescription = %q", d)
	}
}
