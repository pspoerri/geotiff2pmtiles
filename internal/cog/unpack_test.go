package cog

import (
	"encoding/binary"
	"image"
	"math/rand"
	"testing"
)

// unpackBits is tested against a packer written directly to the TIFF spec.

// pack writes samples MSB-first at the given depth, restarting each row on a
// byte boundary -- the layout unpackBits is specified to read.
func pack(vals []uint16, perRow, rows, bits int) []byte {
	rowBytes := (perRow*bits + 7) / 8
	out := make([]byte, rowBytes*rows)
	for y := 0; y < rows; y++ {
		base := y * rowBytes
		bit := 0
		for i := 0; i < perRow; i++ {
			v := uint32(vals[y*perRow+i]) & (1<<bits - 1)
			for need := bits; need > 0; {
				avail := 8 - bit&7
				take := avail
				if need < take {
					take = need
				}
				chunk := byte(v >> (need - take) & (1<<take - 1))
				out[base+bit>>3] |= chunk << (avail - take)
				bit += take
				need -= take
			}
		}
	}
	return out
}

func TestUnpackBitsRoundTrip(t *testing.T) {
	// 15 is the depth Planetary Computer publishes Sentinel-2 reflectance at;
	// the others guard the byte-aligned and awkward cases around it.
	for _, bits := range []int{1, 4, 8, 9, 12, 15, 16} {
		for _, dims := range [][2]int{{1, 1}, {7, 3}, {512, 4}, {13, 11}} {
			perRow, rows := dims[0], dims[1]
			rng := rand.New(rand.NewSource(int64(bits*1000 + perRow)))
			want := make([]uint16, perRow*rows)
			for i := range want {
				want[i] = uint16(rng.Intn(1 << bits))
			}

			rowBytes := (perRow*bits + 7) / 8
			got := make([]uint16, perRow*rows)
			unpackBits(got, pack(want, perRow, rows, bits), perRow, rows, bits, rowBytes)

			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("bits=%d %dx%d: sample %d = %d, want %d",
						bits, perRow, rows, i, got[i], want[i])
				}
			}
		}
	}
}

// TestUnpackBitsRowAlignment pins the property that actually bites: when a row
// does not end on a byte boundary the next row must restart on one, rather
// than continuing the bit stream. Getting this wrong decodes the first row
// correctly and shifts every row after it.
func TestUnpackBitsRowAlignment(t *testing.T) {
	const bits, perRow, rows = 15, 3, 4 // 45 bits per row -> 6 bytes, 3 bits of padding
	want := []uint16{1, 2, 3, 100, 200, 300, 1000, 2000, 3000, 30000, 20000, 10000}
	rowBytes := (perRow*bits + 7) / 8
	if rowBytes != 6 {
		t.Fatalf("rowBytes = %d, want 6", rowBytes)
	}
	got := make([]uint16, len(want))
	unpackBits(got, pack(want, perRow, rows, bits), perRow, rows, bits, rowBytes)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestDecodeRawTilePackedMatchesAligned pins the packed decode against the
// byte-aligned one, which is the path that was already trusted.
//
// The same logical samples are presented twice -- once bit-packed at 15 bits,
// once as ordinary 16-bit words -- with the same rescale range. Nothing else
// about the two tiles differs, so the rendered images have to be identical. A
// packed decode that is shifted, byte-swapped or reading the wrong sample
// shows up immediately as a pixel mismatch.
func TestDecodeRawTilePackedMatchesAligned(t *testing.T) {
	const w, h, spp = 7, 5, 1 // 7*15 = 105 bits per row: deliberately not byte-aligned

	vals := make([]uint16, w*h)
	rng := rand.New(rand.NewSource(7))
	for i := range vals {
		vals[i] = uint16(rng.Intn(1 << 15))
	}
	// Endpoints of the rescale range, to pin saturation at both ends.
	vals[0], vals[1] = 0, 32767

	decode := func(bits int, data []byte) *image.RGBA {
		t.Helper()
		ifd := IFD{
			TileWidth: w, TileHeight: h, SamplesPerPixel: spp,
			BitsPerSample: []uint16{uint16(bits)}, SampleFormat: []uint16{1},
		}
		r := &Reader{ifds: []IFD{ifd}, bo: binary.LittleEndian}
		r.SetBandConfig(BandConfig{Rescale: RescaleLinear, RescaleMin: 0, RescaleMax: 32767})
		img, err := r.decodeRawTile(&r.ifds[0], data)
		if err != nil {
			t.Fatalf("decodeRawTile(%d-bit): %v", bits, err)
		}
		return img.(*image.RGBA)
	}

	aligned := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(aligned[i*2:], v)
	}

	got := decode(15, pack(vals, w*spp, h, 15))
	want := decode(16, aligned)

	for i := range want.Pix {
		if got.Pix[i] != want.Pix[i] {
			t.Fatalf("packed and aligned decodes differ at byte %d: %d vs %d (pixel %d)",
				i, got.Pix[i], want.Pix[i], i/4)
		}
	}
	// Guard against both paths being uniformly wrong.
	if want.Pix[0] != 0 || want.Pix[4] != 255 {
		t.Errorf("rescale endpoints: got %d and %d, want 0 and 255", want.Pix[0], want.Pix[4])
	}
}

// BenchmarkUnpackBits sizes the input like the thing that actually matters:
// one Sentinel-2 512x512 tile of bit-packed 15-bit reflectance.
func BenchmarkUnpackBits(b *testing.B) {
	const w, h, bits = 512, 512, 15
	vals := make([]uint16, w*h)
	rng := rand.New(rand.NewSource(11))
	for i := range vals {
		vals[i] = uint16(rng.Intn(1 << bits))
	}
	data := pack(vals, w, h, bits)
	rowBytes := (w*bits + 7) / 8
	dst := make([]uint16, w*h)

	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		unpackBits(dst, data, w, h, bits, rowBytes)
	}
}

// unpackBitsReference is the straightforward bit-at-a-time implementation the
// windowed one replaced. Kept here so the fast path can be checked against it
// rather than only against a packer: an unpacker and a packer can agree on a
// shared misreading of the spec, but two independent unpackers agreeing on
// random data across every depth is much harder to fake.
func unpackBitsReference(dst []uint16, data []byte, perRow, rows, bits, rowBytes int) {
	for y := 0; y < rows; y++ {
		base := y * rowBytes
		bit := 0
		for i := 0; i < perRow; i++ {
			var v uint32
			for need := bits; need > 0; {
				idx := base + bit>>3
				if idx >= len(data) {
					break
				}
				avail := 8 - bit&7
				take := avail
				if need < take {
					take = need
				}
				chunk := (data[idx] >> (avail - take)) & byte((1<<take)-1)
				v = v<<take | uint32(chunk)
				bit += take
				need -= take
			}
			dst[y*perRow+i] = uint16(v)
		}
	}
}

// The windowed unpacker must produce exactly what the bit-at-a-time one did,
// for every depth and for dimensions whose rows do not end on byte
// boundaries. Anything else silently changes the pixels of every band tile.
func TestUnpackBitsMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for bits := 1; bits <= 16; bits++ {
		for _, dims := range [][2]int{{1, 1}, {7, 5}, {8, 3}, {17, 4}, {512, 2}, {13, 11}} {
			perRow, rows := dims[0], dims[1]
			vals := make([]uint16, perRow*rows)
			for i := range vals {
				vals[i] = uint16(rng.Intn(1 << uint(bits)))
			}
			data := pack(vals, perRow, rows, bits)
			rowBytes := (perRow*bits + 7) / 8

			got := make([]uint16, perRow*rows)
			want := make([]uint16, perRow*rows)
			unpackBits(got, data, perRow, rows, bits, rowBytes)
			unpackBitsReference(want, data, perRow, rows, bits, rowBytes)

			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("bits %d, %dx%d, sample %d: got %d, reference %d",
						bits, perRow, rows, i, got[i], want[i])
				}
			}
		}
	}
}
