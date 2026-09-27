package cog

import (
	"encoding/binary"
	"testing"
	"time"
)

// Layouts the decoders cannot handle must fail the read with an error. A
// predictor on bit-packed samples looped forever (bytesPerSample 0, so the
// row loop never advanced), and planar-separate strips of bit-packed samples
// divided by zero (sub-byte) or interleaved bytes rather than samples.
func TestUnsupportedLayoutsError(t *testing.T) {
	predicted := oneTileReader(16, 2, 4, 1, "", make([]byte, 16))
	predicted.ifds[0].Predictor = 2

	planarStrips := func(bits int) *Reader {
		const w, h, spp = 4, 2, 3
		stripBytes := (w*bits + 7) / 8 * h
		ifd := IFD{Width: w, Height: h, SamplesPerPixel: spp,
			BitsPerSample: []uint16{uint16(bits), uint16(bits), uint16(bits)},
			SampleFormat:  []uint16{1, 1, 1}, Compression: 1, PlanarConfig: 2, RowsPerStrip: h,
			StripOffsets:    []uint64{0, uint64(stripBytes), uint64(2 * stripBytes)},
			StripByteCounts: []uint64{uint64(stripBytes), uint64(stripBytes), uint64(stripBytes)}}
		sl, err := promoteStripsToTiles(&ifd)
		if err != nil {
			t.Fatal(err)
		}
		return &Reader{src: mmapSource(make([]byte, 3*stripBytes)), bo: binary.LittleEndian, ifds: []IFD{ifd}, strip: sl}
	}

	for _, tc := range []struct {
		name string
		r    *Reader
	}{
		{"4-bit predictor 2", predicted},
		{"4-bit planar strips", planarStrips(4)},
		{"12-bit planar strips", planarStrips(12)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan [2]error, 1)
			go func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("panic: %v", p)
						done <- [2]error{}
					}
				}()
				_, err1 := tc.r.ReadTile(0, 0, 0)
				_, _, _, _, err2 := tc.r.ReadUint16Tile(0, 0, 0)
				done <- [2]error{err1, err2}
			}()
			select {
			case errs := <-done:
				if errs[0] == nil || errs[1] == nil {
					t.Errorf("ReadTile: %v, ReadUint16Tile: %v; want errors from both", errs[0], errs[1])
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read did not return")
			}
		})
	}
}
