package cog

import (
	"encoding/binary"
	"strings"
	"testing"
)

// tinyImage returns a valid 16x16 8-bit single-tile TIFF, with extra entries
// added to (or overriding, when listed later) its IFD.
func tinyImage(big bool, extra ...tagEntry) []byte {
	return buildTIFF(big, [][]byte{make([]byte, 256)}, func(offs []uint64) [][]tagEntry {
		return [][]tagEntry{imageEntries(16, 16, 16, 16, 8, offs, []uint64{256}, extra...)}
	})
}

// withEntries returns entries with each one whose tag appears in repl
// replaced by that entry.
func withEntries(entries []tagEntry, repl ...tagEntry) []tagEntry {
	out := entries[:0:0]
	for _, e := range entries {
		for _, r := range repl {
			if r.tag == e.tag {
				e = r
			}
		}
		out = append(out, e)
	}
	return out
}

// linkIFD points the next-IFD pointer of the classic TIFF IFD at ifdOff to
// target.
func linkIFD(data []byte, ifdOff, target uint32) {
	n := binary.LittleEndian.Uint16(data[ifdOff:])
	binary.LittleEndian.PutUint32(data[ifdOff+2+uint32(n)*12:], target)
}

// Malformed files must fail to open with an error naming the file: never a
// panic, a hang or an allocation sized by a garbage count.
func TestOpenSourceMalformed(t *testing.T) {
	selfLoop := tinyImage(false)
	linkIFD(selfLoop, 8+256, 8+256)

	twoCycle := buildTIFF(false, [][]byte{make([]byte, 256)}, func(offs []uint64) [][]tagEntry {
		img := imageEntries(16, 16, 16, 16, 8, offs, []uint64{256})
		return [][]tagEntry{img, img}
	})
	first := binary.LittleEndian.Uint32(twoCycle[4:])
	second := first + 2 + 10*12 + 4 // the first IFD's entries are all inline
	linkIFD(twoCycle, second, first)

	// A BigTIFF IFD claiming 2^60 entries.
	hugeDir := []byte{'I', 'I', 43, 0, 8, 0, 0, 0, 16, 0, 0, 0, 0, 0, 0, 0}
	hugeDir = binary.LittleEndian.AppendUint64(hugeDir, 1<<60)
	hugeDir = append(hugeDir, make([]byte, 64)...)

	stripped := func(big bool, entries ...tagEntry) []byte {
		return buildTIFF(big, nil, func([]uint64) [][]tagEntry { return [][]tagEntry{entries} })
	}

	tests := []struct {
		name string
		data []byte
	}{
		{"ifd-self-loop.tif", selfLoop},
		{"ifd-two-cycle.tif", twoCycle},
		{"bigtiff-huge-entry-count.tif", hugeDir},
		// 2^61 LONG8 values: the byte size wraps to 0 and passed as inline.
		{"bigtiff-huge-tag-count.tif", tinyImage(true,
			tagEntry{tag: tagTileOffsets, typ: dtLong8, count: 1 << 61, val: make([]byte, 8)})},
		{"tag-count-past-eof.tif", tinyImage(false,
			tagEntry{tag: tagTileOffsets, typ: dtLong, count: 1 << 20, val: []byte{16, 0, 0, 0}})},
		{"unknown-type-huge-count.tif", tinyImage(false,
			tagEntry{tag: 65000, typ: 99, count: 1<<32 - 1, val: []byte{16, 0, 0, 0}})},
		// A LONG8 width with no values in a classic TIFF, whose value field
		// holds only 4 bytes.
		{"empty-long8-width.tif", tinyImage(false, tagEntry{tag: tagImageWidth, typ: dtLong8})},
		// No RowsPerStrip and zero rows.
		{"zero-height-strips.tif", stripped(false,
			entry(tagImageWidth, dtLong, 16),
			entry(tagImageLength, dtLong, 0),
			entry(tagCompression, dtShort, 1),
			entry(tagStripOffsets, dtLong, 8),
			entry(tagStripByteCounts, dtLong, 0))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := openCrafted(t, tt.name, tt.data)
			if err == nil {
				r.Close()
				t.Fatalf("OpenSource succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.name) {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

// Integer tags stored with an unusual but unambiguous type are read by their
// declared type, not assumed to be SHORT.
func TestOpenSourceTagTypes(t *testing.T) {
	data := buildTIFF(false, [][]byte{make([]byte, 16*16*3)}, func(offs []uint64) [][]tagEntry {
		return [][]tagEntry{withEntries(imageEntries(16, 16, 16, 16, 8, offs, []uint64{16 * 16 * 3}),
			entry(tagBitsPerSample, dtByte, 8, 8, 8), // 3 bytes, inline
			entry(tagSamplesPerPixel, dtShort, 3),
			entry(tagPhotometric, dtShort, 2),
		)}
	})
	r, err := openCrafted(t, "byte-bits.tif", data)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.ifds[0].BitsPerSample; len(got) != 3 || got[0] != 8 || got[2] != 8 {
		t.Errorf("BitsPerSample = %v, want [8 8 8]", got)
	}
}

func TestDataTypeSize(t *testing.T) {
	for _, tt := range []struct {
		dt   uint16
		want int
	}{
		{dtByte, 1}, {dtShort, 2}, {dtLong, 4}, {dtIFD, 4},
		{dtDouble, 8}, {dtLong8, 8}, {dtIFD8, 8},
	} {
		if got := dataTypeSize(tt.dt); got != tt.want {
			t.Errorf("dataTypeSize(%d) = %d, want %d", tt.dt, got, tt.want)
		}
	}
}

// Only IFD 0 and its overviews are levels: GDAL's interleaved 1-bit masks,
// pages, thumbnails and striped reduced images are dropped at open, while an
// overview without NewSubfileType is kept.
func TestOpenSourceKeepsOnlyOverviewIFDs(t *testing.T) {
	const tile = 16 * 16
	subfile := func(v uint64) tagEntry { return entry(tagNewSubfileType, dtLong, v) }
	image := func(offs []uint64) []tagEntry {
		return imageEntries(32, 32, 16, 16, 8, offs, []uint64{tile, tile, tile, tile})
	}
	overview := func(w int, offs []uint64, extra ...tagEntry) []tagEntry {
		return imageEntries(w, w, 16, 16, 8, offs[:1], []uint64{tile}, extra...)
	}
	blobs := [][]byte{make([]byte, tile), make([]byte, tile), make([]byte, tile), make([]byte, tile)}

	tests := []struct {
		name   string
		extra  func(offs []uint64) []tagEntry
		levels int
	}{
		{"overview", func(offs []uint64) []tagEntry {
			return overview(16, offs, subfile(1))
		}, 2},
		{"overview-without-subfile-type", func(offs []uint64) []tagEntry {
			return overview(16, offs)
		}, 2},
		{"mask", func(offs []uint64) []tagEntry {
			return withEntries(overview(32, offs, subfile(4)),
				entry(tagBitsPerSample, dtShort, 1), entry(tagPhotometric, dtShort, 4))
		}, 1},
		{"page", func(offs []uint64) []tagEntry {
			return overview(32, offs, subfile(2))
		}, 1},
		{"larger-than-image", func(offs []uint64) []tagEntry {
			return overview(64, offs)
		}, 1},
		{"rgb-thumbnail", func(offs []uint64) []tagEntry {
			return withEntries(overview(8, offs, subfile(1)),
				entry(tagSamplesPerPixel, dtShort, 3), entry(tagPhotometric, dtShort, 2))
		}, 1},
		{"unsupported-compression", func(offs []uint64) []tagEntry {
			return withEntries(overview(16, offs, subfile(1)), entry(tagCompression, dtShort, 6))
		}, 1},
		{"striped-overview", func(offs []uint64) []tagEntry {
			return []tagEntry{
				subfile(1),
				entry(tagImageWidth, dtLong, 16),
				entry(tagImageLength, dtLong, 16),
				entry(tagBitsPerSample, dtShort, 8),
				entry(tagCompression, dtShort, 1),
				entry(tagRowsPerStrip, dtShort, 16),
				entry(tagStripOffsets, dtLong, offs[0]),
				entry(tagStripByteCounts, dtLong, tile),
			}
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildTIFF(false, blobs, func(offs []uint64) [][]tagEntry {
				return [][]tagEntry{image(offs), tt.extra(offs)}
			})
			r, err := openCrafted(t, tt.name, data)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if got := r.IFDCount(); got != tt.levels {
				t.Fatalf("IFDCount() = %d, want %d", got, tt.levels)
			}
			for level := 0; level < r.IFDCount(); level++ {
				if _, err := r.ReadTile(level, 0, 0); err != nil {
					t.Errorf("ReadTile(%d, 0, 0): %v", level, err)
				}
			}
		})
	}
}

// A 16-bit COG with a GDAL internal mask ends its IFD chain with the smallest
// overview's 1-bit mask; the auto rescale scan must use the overview instead.
func TestValueRangeSkipsMaskIFDs(t *testing.T) {
	const tileBytes = 16 * 16 * 2
	ov := make([]byte, tileBytes)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			binary.LittleEndian.PutUint16(ov[(y*16+x)*2:], uint16(1000+y*8+x))
		}
	}
	blobs := [][]byte{make([]byte, tileBytes), ov, make([]byte, 32)}
	data := buildTIFF(false, blobs, func(offs []uint64) [][]tagEntry {
		img, small, mask := offs[0], offs[1], offs[2]
		maskIFD := func(w int, subfile uint64) []tagEntry {
			return imageEntries(w, w, 16, 16, 1, []uint64{mask}, []uint64{32},
				entry(tagNewSubfileType, dtLong, subfile), entry(tagPhotometric, dtShort, 4))
		}
		// GDAL order: image, mask, then each overview followed by its mask.
		return [][]tagEntry{
			imageEntries(16, 16, 16, 16, 16, []uint64{img}, []uint64{tileBytes}),
			maskIFD(16, 4),
			imageEntries(8, 8, 16, 16, 16, []uint64{small}, []uint64{tileBytes},
				entry(tagNewSubfileType, dtLong, 1)),
			maskIFD(8, 5),
		}
	})
	r, err := openCrafted(t, "cog16mask.tif", data)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.IFDCount(); got != 2 {
		t.Fatalf("IFDCount() = %d, want 2", got)
	}
	lo, hi, _, err := r.ValueRange()
	if err != nil {
		t.Fatal(err)
	}
	if lo != 1000 || hi != 1063 {
		t.Errorf("ValueRange() = [%v, %v], want [1000, 1063]", lo, hi)
	}
}
