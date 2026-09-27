package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// openSource opens path as checkpmtiles does for a local file.
func openSource(t *testing.T, path string) dataSource {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return &fileSource{f: f, size: fi.Size()}
}

// writeArchive writes a gzip-directory archive with the writer.
func writeArchive(t *testing.T, tiles int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.pmtiles")
	w, err := pmtiles.NewWriter(path, pmtiles.WriterOptions{
		MinZoom: 0, MaxZoom: 1, TileSize: 256, TileFormat: pmtiles.TileTypePNG,
		Bounds: cog.Bounds{MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range tiles {
		if err := w.WriteTile(1, i%2, i/2%2, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path
}

// rawDir serializes an uncompressed PMTiles directory.
func rawDir(entries ...pmtiles.Entry) []byte {
	b := binary.AppendUvarint(nil, uint64(len(entries)))
	var last uint64
	for _, e := range entries {
		b = binary.AppendUvarint(b, e.TileID-last)
		last = e.TileID
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, uint64(e.RunLength))
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, uint64(e.Length))
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, e.Offset+1)
	}
	return b
}

// writeNestedUncompressed writes an archive with internal compression none
// whose root points to a leaf that points to another leaf, which holds 3
// addressed tiles.
func writeNestedUncompressed(t *testing.T) string {
	t.Helper()
	inner := rawDir(pmtiles.Entry{TileID: 0, RunLength: 1, Length: 4}, pmtiles.Entry{TileID: 1, RunLength: 2, Length: 4})
	outer := rawDir(pmtiles.Entry{TileID: 0, Offset: 0, Length: uint32(len(inner))})
	root := rawDir(pmtiles.Entry{TileID: 0, Offset: uint64(len(inner)), Length: uint32(len(outer))})
	meta := []byte("{}")
	leaves := append(append([]byte{}, inner...), outer...)
	tiles := []byte("tile")

	h := pmtiles.Header{
		RootDirOffset: pmtiles.HeaderSize, RootDirLength: uint64(len(root)),
		NumAddressedTiles: 3, NumTileEntries: 2, NumTileContents: 1,
		Clustered: true, InternalCompression: pmtiles.CompressionNone, TileCompression: pmtiles.CompressionNone,
		TileType: pmtiles.TileTypePNG, MinZoom: 0, MaxZoom: 1,
	}
	h.MetadataOffset = h.RootDirOffset + h.RootDirLength
	h.MetadataLength = uint64(len(meta))
	h.LeafDirOffset = h.MetadataOffset + h.MetadataLength
	h.LeafDirLength = uint64(len(leaves))
	h.TileDataOffset = h.LeafDirOffset + h.LeafDirLength
	h.TileDataLength = uint64(len(tiles))

	var buf []byte
	for _, part := range [][]byte{h.Serialize(), root, meta, leaves, tiles} {
		buf = append(buf, part...)
	}
	path := filepath.Join(t.TempDir(), "nested.pmtiles")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// patchHeader rewrites the header of the archive at path.
func patchHeader(t *testing.T, path string, patch func(*pmtiles.Header)) {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := pmtiles.DeserializeHeader(buf[:pmtiles.HeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	patch(&h)
	copy(buf, h.Serialize())
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	if !check(openSource(t, writeArchive(t, 4))) {
		t.Error("a writer archive failed the checks")
	}

	// Uncompressed directories with leaves two levels deep: the old
	// checker only knew gzip and one leaf level.
	if !check(openSource(t, writeNestedUncompressed(t))) {
		t.Error("an archive with uncompressed, nested leaf directories failed")
	}

	if check(openSource(t, writeArchive(t, 0))) {
		t.Error("an archive without tiles passed")
	}

	swapped := writeArchive(t, 4)
	patchHeader(t, swapped, func(h *pmtiles.Header) { h.MinZoom, h.MaxZoom = 5, 2 })
	if check(openSource(t, swapped)) {
		t.Error("an archive with min zoom 5 > max zoom 2 passed")
	}

	miscounted := writeNestedUncompressed(t)
	patchHeader(t, miscounted, func(h *pmtiles.Header) { h.NumAddressedTiles = 7 })
	if check(openSource(t, miscounted)) {
		t.Error("an archive whose header miscounts its tiles passed")
	}
}
