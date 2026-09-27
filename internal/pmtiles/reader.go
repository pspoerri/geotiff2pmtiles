package pmtiles

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Reader provides read access to an existing PMTiles v3 archive.
//
// The index is the archive's tile entries, sorted by TileID and kept as
// run-length entries: memory grows with the directory, not with the number
// of addressed tiles (a global archive addresses tens of millions of tiles,
// most of them in a few long ocean runs).
type Reader struct {
	file     *os.File
	entries  []Entry // tile entries (RunLength >= 1), sorted by TileID
	numTiles int     // addressed tiles: the sum of the run lengths
	header   Header
}

// OpenReader opens a PMTiles v3 archive for reading.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// Read header.
	headerBuf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, headerBuf); err != nil {
		f.Close()
		return nil, fmt.Errorf("reading header: %w", err)
	}

	header, err := DeserializeHeader(headerBuf)
	if err != nil {
		f.Close()
		return nil, err
	}

	// Resolve the root and leaf directories into a flat list of tile entries.
	allEntries, err := readEntries(f, header, header.RootDirOffset, header.RootDirLength, map[uint64]bool{}, nil)
	if err != nil {
		f.Close()
		return nil, err
	}

	// Directories are sorted by the spec; sorting is linear on sorted input
	// and keeps the binary searches below correct if a writer got it wrong.
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].TileID < allEntries[j].TileID
	})
	numTiles := 0
	for _, e := range allEntries {
		numTiles += int(e.RunLength)
	}

	return &Reader{
		file:     f,
		header:   header,
		entries:  allEntries,
		numTiles: numTiles,
	}, nil
}

// readEntries appends the tile entries of the directory at absolute file
// offset off to entries, following leaf directory pointers (RunLength 0)
// to any depth. seen holds the offsets of the directories read so far: a
// corrupt archive whose leaf pointers loop back fails instead of recursing
// forever.
func readEntries(f *os.File, h Header, off, length uint64, seen map[uint64]bool, entries []Entry) ([]Entry, error) {
	name := "root directory"
	if off != h.RootDirOffset {
		name = fmt.Sprintf("leaf directory at offset %d", off)
	}
	if seen[off] {
		return nil, fmt.Errorf("%s is referenced more than once (corrupt archive)", name)
	}
	seen[off] = true

	data := make([]byte, length)
	if _, err := f.ReadAt(data, int64(off)); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	dir, err := DeserializeDirectoryCompressed(data, h.InternalCompression)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}
	for _, e := range dir {
		if e.RunLength > 0 {
			entries = append(entries, e)
			continue
		}
		// Leaf directory pointer: offset/length are relative to the leaf dir section.
		entries, err = readEntries(f, h, h.LeafDirOffset+e.Offset, uint64(e.Length), seen, entries)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// find returns the entry whose run covers tileID. Per the PMTiles v3 spec
// a run of N tile IDs shares ONE blob: every ID in [TileID, TileID+N)
// resolves to the same Offset/Length.
func (r *Reader) find(tileID uint64) (Entry, bool) {
	// The last entry starting at or before tileID is the only candidate.
	i := sort.Search(len(r.entries), func(i int) bool {
		return r.entries[i].TileID > tileID
	}) - 1
	if i < 0 || tileID-r.entries[i].TileID >= uint64(r.entries[i].RunLength) {
		return Entry{}, false
	}
	return r.entries[i], true
}

// Header returns the parsed PMTiles header.
func (r *Reader) Header() Header {
	return r.header
}

// ReadTile returns the raw encoded bytes for a tile at z/x/y.
// Returns nil, nil if the tile does not exist.
func (r *Reader) ReadTile(z, x, y int) ([]byte, error) {
	e, ok := r.find(ZXYToTileID(z, x, y))
	if !ok {
		return nil, nil
	}

	data := make([]byte, e.Length)
	if _, err := r.file.ReadAt(data, int64(r.header.TileDataOffset+e.Offset)); err != nil {
		return nil, fmt.Errorf("reading tile z%d/%d/%d: %w", z, x, y, err)
	}
	return data, nil
}

// TilesAtZoom returns all [z, x, y] coordinates that have tiles at the given zoom level.
func (r *Reader) TilesAtZoom(z int) [][3]int {
	if z < 0 || z > 31 {
		return nil // no tile ID addresses zoom 32 or deeper
	}
	// The tile ID range [minID, maxID) of this zoom level.
	minID := ZXYToTileID(z, 0, 0)
	maxID := minID + uint64(1)<<uint(2*z)

	// The entries whose runs overlap [minID, maxID); a run may start at a
	// lower zoom and continue into this one.
	start := sort.Search(len(r.entries), func(i int) bool {
		return r.entries[i].TileID+uint64(r.entries[i].RunLength) > minID
	})
	end := sort.Search(len(r.entries), func(i int) bool {
		return r.entries[i].TileID >= maxID
	})
	entries := r.entries[start:end]
	clip := func(e Entry) (lo, hi uint64) {
		return max(e.TileID, minID), min(e.TileID+uint64(e.RunLength), maxID)
	}

	// Count first so the result is allocated once, at its final size.
	n := 0
	for _, e := range entries {
		lo, hi := clip(e)
		n += int(hi - lo)
	}
	if n == 0 {
		return nil
	}
	tiles := make([][3]int, 0, n)
	for _, e := range entries {
		lo, hi := clip(e)
		for id := lo; id < hi; id++ {
			_, x, y := TileIDToZXY(id)
			tiles = append(tiles, [3]int{z, x, y})
		}
	}
	return tiles
}

// NumTiles returns the total number of addressed tiles in the archive.
func (r *Reader) NumTiles() int {
	return r.numTiles
}

// ReadMetadata reads and decompresses the JSON metadata from the archive.
// Returns nil if the archive has no metadata.
func (r *Reader) ReadMetadata() (map[string]interface{}, error) {
	if r.header.MetadataLength == 0 {
		return nil, nil
	}

	metaRaw := make([]byte, r.header.MetadataLength)
	if _, err := r.file.ReadAt(metaRaw, int64(r.header.MetadataOffset)); err != nil {
		return nil, fmt.Errorf("reading metadata: %w", err)
	}

	jsonData, err := decompress(metaRaw, r.header.InternalCompression)
	if err != nil {
		return nil, fmt.Errorf("decompressing metadata: %w", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(jsonData, &meta); err != nil {
		return nil, fmt.Errorf("parsing metadata JSON: %w", err)
	}

	return meta, nil
}

// Close closes the underlying file.
func (r *Reader) Close() error {
	return r.file.Close()
}
