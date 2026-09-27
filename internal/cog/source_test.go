package cog

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// noPanic runs read and turns a panic into a test failure.
func noPanic(t *testing.T, what string, read func() error) (err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("%s panicked: %v", what, p)
		}
	}()
	return read()
}

// A Reader over a non-mmap source reads the same pixels as Open.
func TestOpenSourceMatchesOpen(t *testing.T) {
	const w, h = 16, 16
	payload := make([]byte, w*h*4)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), "float.tif")
	writeTiledFloatTIFF(t, path, w, h, 1, 1, payload)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	mm, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mm.Close()
	bs, err := OpenSource("float.tif", bytesSource(data))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	want, _, _, err := mm.ReadFloatTile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := bs.ReadFloatTile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel %d = %v over bytesSource, %v over mmap", i, got[i], want[i])
		}
	}
}

// Reads after Close fail with an error, and a second Close is a no-op.
func TestReaderReadAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "float.tif")
	writeTiledFloatTIFF(t, path, 16, 16, 1, 1, make([]byte, 16*16*4))
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := noPanic(t, "ReadFloatTile", func() error { _, _, _, err := r.ReadFloatTile(0, 0, 0); return err }); err == nil {
		t.Error("ReadFloatTile after Close succeeded")
	}
	if err := noPanic(t, "ReadTile", func() error { _, err := r.ReadTile(0, 0, 0); return err }); err == nil {
		t.Error("ReadTile after Close succeeded")
	}
	if b := r.RawBytes(0, 8); b != nil {
		t.Errorf("RawBytes after Close = %v, want nil", b)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Tile and strip byte ranges from the file are checked against the source
// size without overflow, so the source is never asked for a range outside
// [0, Size()]. bytesSource slices naively and panics on such a range.
func TestReaderRejectsTileRangesOutsideSource(t *testing.T) {
	const tile = 16 * 16
	ranges := []struct {
		name         string
		offset, size uint64
	}{
		{"wraps-around", 1<<64 - 6, 16},
		{"past-end", 100, 1 << 20},
		{"starts-past-end", 1 << 40, 1},
	}
	layouts := []struct {
		name    string
		entries func(off, size uint64) []tagEntry
	}{
		{"tiled", func(off, size uint64) []tagEntry {
			return withEntries(imageEntries(16, 16, 16, 16, 8, nil, nil),
				entry(tagTileOffsets, dtLong8, off), entry(tagTileByteCounts, dtLong8, size))
		}},
		{"stripped", func(off, size uint64) []tagEntry {
			return []tagEntry{
				entry(tagImageWidth, dtLong, 16),
				entry(tagImageLength, dtLong, 16),
				entry(tagCompression, dtShort, 1),
				entry(tagStripOffsets, dtLong8, off),
				entry(tagStripByteCounts, dtLong8, size),
			}
		}},
		{"planar-jpeg", func(off, size uint64) []tagEntry {
			return withEntries(imageEntries(16, 16, 16, 16, 8, nil, nil),
				entry(tagCompression, dtShort, 7),
				entry(tagSamplesPerPixel, dtShort, 3),
				entry(tagPlanarConfig, dtShort, 2),
				entry(tagTileOffsets, dtLong8, off, off, off),
				entry(tagTileByteCounts, dtLong8, size, size, size))
		}},
	}
	for _, l := range layouts {
		for _, rg := range ranges {
			t.Run(l.name+"/"+rg.name, func(t *testing.T) {
				data := buildTIFF(true, [][]byte{make([]byte, tile)}, func([]uint64) [][]tagEntry {
					return [][]tagEntry{l.entries(rg.offset, rg.size)}
				})
				r, err := openCrafted(t, "bad-range.tif", data)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				if err := noPanic(t, "ReadTile", func() error { _, err := r.ReadTile(0, 0, 0); return err }); err == nil {
					t.Error("ReadTile succeeded")
				}
				if l.name == "tiled" {
					if err := noPanic(t, "ReadUint16Tile", func() error { _, _, _, _, err := r.ReadUint16Tile(0, 0, 0); return err }); err == nil {
						t.Error("ReadUint16Tile succeeded")
					}
				}
			})
		}
	}
}

// mmapSource checks its own bounds as well; never closed here, since the
// bytes are not a mapping.
func TestMmapSourceSlice(t *testing.T) {
	m := mmapSource([]byte("0123456789"))
	for _, tt := range []struct {
		off, end uint64
		want     string
		ok       bool
	}{
		{2, 5, "234", true},
		{10, 10, "", true},
		{5, 3, "", false},
		{0, 11, "", false},
		{1<<64 - 6, 4, "", false},
	} {
		got, err := m.Slice(tt.off, tt.end)
		if (err == nil) != tt.ok || string(got) != tt.want {
			t.Errorf("Slice(%d, %d) = %q, %v", tt.off, tt.end, got, err)
		}
	}
}

// failingSource is a ByteSource whose reads all fail.
type failingSource struct{ bytesSource }

func (failingSource) Slice(off, end uint64) ([]byte, error) { return nil, errors.New("read failed") }

func TestRawBytes(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	r := &Reader{src: bytesSource(data)}
	tests := []struct {
		name   string
		offset uint64
		n      int
		want   []byte
	}{
		{"inside", 10, 4, data[10:14]},
		{"clamped-at-end", 90, 20, data[90:]},
		{"offset-at-end", 100, 20, nil},
		{"offset-past-end", 200, 20, nil},
	}
	for _, tt := range tests {
		var got []byte
		noPanic(t, tt.name, func() error { got = r.RawBytes(tt.offset, tt.n); return nil })
		if !bytes.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
			t.Errorf("%s: RawBytes(%d, %d) = %v, want %v", tt.name, tt.offset, tt.n, got, tt.want)
		}
	}

	// A failed read is nil, not a buffer of zeros that looks like data.
	r = &Reader{src: failingSource{bytesSource(data)}}
	if got := r.RawBytes(0, 20); got != nil {
		t.Errorf("RawBytes over a failing source = %v, want nil", got)
	}
}

// Every reader gets its own cache ID, however it was opened, so readers
// sharing a TileCache never get each other's tiles; SetID still overrides it.
func TestReaderIDsAreUnique(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a.tif", "b.tif"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, tinyImage(false), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	readers, err := OpenAll(paths)
	if err != nil {
		t.Fatal(err)
	}
	more, err := OpenAll(paths[:1])
	if err != nil {
		t.Fatal(err)
	}
	readers = append(readers, more...)
	for _, name := range []string{"c", "d"} {
		r, err := OpenSource(name, bytesSource(tinyImage(false)))
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, r)
	}

	seen := make(map[int]int)
	for i, r := range readers {
		defer r.Close()
		if j, dup := seen[r.ID()]; dup {
			t.Errorf("readers %d and %d share ID %d", j, i, r.ID())
		}
		seen[r.ID()] = i
	}

	readers[0].SetID(-1)
	if got := readers[0].ID(); got != -1 {
		t.Errorf("ID() after SetID(-1) = %d", got)
	}
}

// sourceReadSeeker behaves like bytes.Reader under io.ReadFull and Seek,
// across read-ahead block boundaries, past EOF and for bad seeks.
func TestSourceReadSeekerMatchesBytesReader(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, 3*readAhead+123)
	for i := range data {
		data[i] = byte(rng.IntN(256))
	}
	got := &sourceReadSeeker{src: bytesSource(data)}
	want := bytes.NewReader(data)
	size := int64(len(data))
	for i := 0; i < 5000; i++ {
		if rng.IntN(3) == 0 {
			whence := rng.IntN(4) // 3 is invalid
			off := rng.Int64N(2*size) - size/2
			gp, gerr := got.Seek(off, whence)
			wp, werr := want.Seek(off, whence)
			if (gerr != nil) != (werr != nil) || gp != wp && werr == nil {
				t.Fatalf("op %d: Seek(%d, %d) = %d, %v; bytes.Reader: %d, %v", i, off, whence, gp, gerr, wp, werr)
			}
			continue
		}
		n := rng.IntN(readAhead + 100)
		if rng.IntN(4) == 0 {
			n = rng.IntN(16) // parser-sized reads, including 0
		}
		gb, wb := make([]byte, n), make([]byte, n)
		gn, gerr := io.ReadFull(got, gb)
		wn, werr := io.ReadFull(want, wb)
		if gn != wn || gerr != werr || !bytes.Equal(gb[:gn], wb[:wn]) {
			t.Fatalf("op %d: ReadFull(%d) = %d, %v; bytes.Reader: %d, %v", i, n, gn, gerr, wn, werr)
		}
	}
}

// countingSource counts Slice calls.
type countingSource struct {
	bytesSource
	calls *int
}

func (c countingSource) Slice(off, end uint64) ([]byte, error) {
	*c.calls++
	return c.bytesSource.Slice(off, end)
}

// Opening a COG reads its header and IFDs in one Slice call rather than one
// per directory entry: over HTTP each call is a round trip.
func TestOpenSourceBatchesHeaderReads(t *testing.T) {
	const tile = 16 * 16
	geo := []tagEntry{
		doubles(tagModelPixelScaleTag, 10, 10, 0),
		doubles(tagModelTiepointTag, 0, 0, 0, 500000, 5300000, 0),
		entry(tagGeoKeyDirectoryTag, dtShort, 1, 1, 0, 1, gkProjectedCSTypeGeoKey, 0, 1, 32632),
	}
	data := buildTIFF(false, [][]byte{make([]byte, tile)}, func(offs []uint64) [][]tagEntry {
		var ifds [][]tagEntry
		for w := 128; w >= 16; w /= 2 {
			n := (w / 16) * (w / 16)
			tileOffs, tileCounts := make([]uint64, n), make([]uint64, n)
			for i := range tileOffs {
				tileOffs[i], tileCounts[i] = offs[0], tile
			}
			ifds = append(ifds, imageEntries(w, w, 16, 16, 8, tileOffs, tileCounts, geo...))
		}
		return ifds
	})

	var calls int
	r, err := OpenSource("cog.tif", countingSource{bytesSource(data), &calls})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.IFDCount() != 4 {
		t.Fatalf("IFDCount() = %d, want 4", r.IFDCount())
	}
	if calls > 1 {
		t.Errorf("OpenSource made %d Slice calls for a %d-byte file, want 1", calls, len(data))
	}
}

// closeCounter counts Close calls on a source.
type closeCounter struct {
	bytesSource
	closed *int
}

func (c closeCounter) Close() error { *c.closed++; return nil }

// OpenSource owns src: it is closed once when OpenSource fails, and by
// Reader.Close otherwise.
func TestOpenSourceClosesSource(t *testing.T) {
	var closed int
	if _, err := OpenSource("garbage", closeCounter{bytesSource("XXnotatiff"), &closed}); err == nil {
		t.Fatal("OpenSource succeeded on garbage")
	}
	if closed != 1 {
		t.Errorf("failed OpenSource closed the source %d times, want 1", closed)
	}

	closed = 0
	r, err := OpenSource("tiny", closeCounter{bytesSource(tinyImage(false)), &closed})
	if err != nil {
		t.Fatal(err)
	}
	if closed != 0 {
		t.Errorf("successful OpenSource closed the source")
	}
	r.Close()
	r.Close()
	if closed != 1 {
		t.Errorf("Reader.Close twice closed the source %d times, want 1", closed)
	}
}
