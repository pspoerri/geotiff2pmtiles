package cog

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeTiledFloatTIFF writes a minimal single-tile float32 TIFF with the given
// compression and predictor tags and the (already compressed) tile payload.
func writeTiledFloatTIFF(t *testing.T, path string, w, h, compression, predictor int, payload []byte) {
	t.Helper()
	bo := binary.LittleEndian
	short := func(v uint16) []byte { b := make([]byte, 4); bo.PutUint16(b, v); return b }
	long := func(v uint32) []byte { b := make([]byte, 4); bo.PutUint32(b, v); return b }

	entries := []struct {
		tag, dataType uint16
		count         uint32
		value         []byte
	}{
		{256, 3, 1, short(uint16(w))},           // ImageWidth
		{257, 3, 1, short(uint16(h))},           // ImageLength
		{258, 3, 1, short(32)},                  // BitsPerSample
		{259, 3, 1, short(uint16(compression))}, // Compression
		{262, 3, 1, short(1)},                   // PhotometricInterpretation
		{277, 3, 1, short(1)},                   // SamplesPerPixel
		{317, 3, 1, short(uint16(predictor))},   // Predictor
		{322, 3, 1, short(uint16(w))},           // TileWidth
		{323, 3, 1, short(uint16(h))},           // TileLength
		{324, 4, 1, nil},                        // TileOffsets (patched below)
		{325, 4, 1, long(uint32(len(payload)))}, // TileByteCounts
		{339, 3, 1, short(3)},                   // SampleFormat: IEEE float
	}
	dataOffset := uint32(8 + 2 + len(entries)*12 + 4)
	entries[9].value = long(dataOffset)

	var buf []byte
	buf = append(buf, 'I', 'I')
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		buf = bo.AppendUint16(buf, e.tag)
		buf = bo.AppendUint16(buf, e.dataType)
		buf = bo.AppendUint32(buf, e.count)
		buf = append(buf, e.value...)
	}
	buf = bo.AppendUint32(buf, 0) // no next IFD
	buf = append(buf, payload...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// An uncompressed tile aliases the read-only memory mapping, so undoing the
// predictor in place would write to it and fault.
func TestReadFloatTileUncompressedPredictor(t *testing.T) {
	const w, h = 16, 16
	bo := binary.LittleEndian

	want := make([]float32, w*h)
	for i := range want {
		want[i] = float32(i) * 0.5
	}
	var payload []byte
	for y := 0; y < h; y++ {
		payload = append(payload, encodeFloatPredictor(want[y*w:(y+1)*w], 1, bo)...)
	}

	path := filepath.Join(t.TempDir(), "float-predictor.tif")
	writeTiledFloatTIFF(t, path, w, h, 1, 3, payload)

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
}
