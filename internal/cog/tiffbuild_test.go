package cog

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// bytesSource is a ByteSource over a plain byte slice: the test stand-in for
// a remote source. Slice is as naive as a third-party source may be, so a
// range outside the ByteSource contract panics instead of being caught.
type bytesSource []byte

func (b bytesSource) Size() int64                           { return int64(len(b)) }
func (b bytesSource) Slice(off, end uint64) ([]byte, error) { return b[off:end:end], nil }
func (bytesSource) Close() error                            { return nil }

// tagEntry is one directory entry for buildTIFF. val is the raw value field:
// stored inline when it fits the entry, else after the IFD with its offset
// inline. count is written as given, so it may disagree with val.
type tagEntry struct {
	tag, typ uint16
	count    uint64
	val      []byte
}

// entry encodes vals as a little-endian array of the integer type typ.
func entry(tag, typ uint16, vals ...uint64) tagEntry {
	size := dataTypeSize(typ)
	b := make([]byte, 0, len(vals)*size)
	for _, v := range vals {
		switch size {
		case 1:
			b = append(b, byte(v))
		case 2:
			b = binary.LittleEndian.AppendUint16(b, uint16(v))
		case 4:
			b = binary.LittleEndian.AppendUint32(b, uint32(v))
		default:
			b = binary.LittleEndian.AppendUint64(b, v)
		}
	}
	return tagEntry{tag: tag, typ: typ, count: uint64(len(vals)), val: b}
}

// doubles encodes vals as a DOUBLE array.
func doubles(tag uint16, vals ...float64) tagEntry {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
	}
	return tagEntry{tag: tag, typ: dtDouble, count: uint64(len(vals)), val: b}
}

// buildTIFF lays out a little-endian TIFF (BigTIFF if big): the header, the
// blobs, then the IFDs chained in order, each followed by its out-of-line
// values. ifds receives each blob's offset, so tile offsets can point at them.
func buildTIFF(big bool, blobs [][]byte, ifds func(blobOffs []uint64) [][]tagEntry) []byte {
	bo := binary.LittleEndian
	// next is where the pointer to the next IFD goes: the header's first.
	inline, entrySize, next := 4, 12, 4
	b := []byte{'I', 'I', 42, 0, 0, 0, 0, 0}
	if big {
		inline, entrySize, next = 8, 20, 8
		b = []byte{'I', 'I', 43, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	}
	putOffset := func(at int, v uint64) {
		if big {
			bo.PutUint64(b[at:], v)
		} else {
			bo.PutUint32(b[at:], uint32(v))
		}
	}

	offs := make([]uint64, len(blobs))
	for i, blob := range blobs {
		offs[i] = uint64(len(b))
		b = append(b, blob...)
	}

	for _, entries := range ifds(offs) {
		putOffset(next, uint64(len(b)))
		if big {
			b = bo.AppendUint64(b, uint64(len(entries)))
		} else {
			b = bo.AppendUint16(b, uint16(len(entries)))
		}
		extStart := len(b) + len(entries)*entrySize + inline
		var ext []byte
		for _, e := range entries {
			b = bo.AppendUint16(b, e.tag)
			b = bo.AppendUint16(b, e.typ)
			if big {
				b = bo.AppendUint64(b, e.count)
			} else {
				b = bo.AppendUint32(b, uint32(e.count))
			}
			field := make([]byte, inline)
			if len(e.val) <= inline {
				copy(field, e.val)
			} else if big {
				bo.PutUint64(field, uint64(extStart+len(ext)))
				ext = append(ext, e.val...)
			} else {
				bo.PutUint32(field, uint32(extStart+len(ext)))
				ext = append(ext, e.val...)
			}
			b = append(b, field...)
		}
		next = len(b)
		b = append(b, make([]byte, inline)...) // next IFD: none unless another follows
		b = append(b, ext...)
	}
	return b
}

// imageEntries returns the entries of an uncompressed, single-band, tiled
// image IFD, plus extra.
func imageEntries(w, h, tw, th, bits int, tileOffs, tileCounts []uint64, extra ...tagEntry) []tagEntry {
	return append([]tagEntry{
		entry(tagImageWidth, dtLong, uint64(w)),
		entry(tagImageLength, dtLong, uint64(h)),
		entry(tagBitsPerSample, dtShort, uint64(bits)),
		entry(tagCompression, dtShort, 1),
		entry(tagPhotometric, dtShort, 1),
		entry(tagSamplesPerPixel, dtShort, 1),
		entry(tagTileWidth, dtShort, uint64(tw)),
		entry(tagTileLength, dtShort, uint64(th)),
		entry(tagTileOffsets, dtLong, tileOffs...),
		entry(tagTileByteCounts, dtLong, tileCounts...),
	}, extra...)
}

// openCrafted runs OpenSource over data and fails the test if it panics or
// does not return, so a malformed file cannot take the test binary down.
func openCrafted(t *testing.T, name string, data []byte) (*Reader, error) {
	t.Helper()
	type result struct {
		r     *Reader
		err   error
		panic any
	}
	done := make(chan result, 1)
	go func() {
		var res result
		defer func() {
			res.panic = recover()
			done <- res
		}()
		res.r, res.err = OpenSource(name, bytesSource(data))
	}()
	select {
	case res := <-done:
		if res.panic != nil {
			t.Fatalf("OpenSource(%s) panicked: %v", name, res.panic)
		}
		return res.r, res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("OpenSource(%s) did not return within 5s", name)
	}
	return nil, nil
}
