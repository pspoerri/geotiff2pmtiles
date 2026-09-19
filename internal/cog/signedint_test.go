package cog

import (
	"encoding/binary"
	"testing"
)

// Signed-integer DEMs (e.g. GEBCO Int16 bathymetry) must decode through the
// elevation path with negative values intact.
func TestSignedInt16DecodesAsElevation(t *testing.T) {
	ifd := IFD{
		TileWidth: 2, TileHeight: 1, SamplesPerPixel: 1,
		BitsPerSample: []uint16{16}, SampleFormat: []uint16{2},
	}
	r := &Reader{ifds: []IFD{ifd}, bo: binary.LittleEndian}
	if !r.IsFloat() {
		t.Fatal("IsFloat() = false for signed int16, want true (elevation path)")
	}

	data := make([]byte, 4)
	depth := int16(-9536)
	binary.LittleEndian.PutUint16(data[0:], uint16(depth))
	binary.LittleEndian.PutUint16(data[2:], 8627)
	got, _, _, err := r.decodeRawFloat32Tile(&r.ifds[0], data)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != -9536 || got[1] != 8627 {
		t.Fatalf("got %v, want [-9536 8627]", got)
	}
}
