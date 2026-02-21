package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Reader provides read access to an existing PMTiles v3 archive.
type Reader struct {
	file    *os.File
	header  Header
	entries []Entry            // all tile entries (expanded from run lengths)
	tileIdx map[uint64]tileRef // tileID -> location in file
}

// tileRef records the absolute file offset and length of a tile's data.
type tileRef struct {
	offset uint64
	length uint32
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

	// Read root directory.
	rootDirData := make([]byte, header.RootDirLength)
	if _, err := f.ReadAt(rootDirData, int64(header.RootDirOffset)); err != nil {
		f.Close()
		return nil, fmt.Errorf("reading root directory: %w", err)
	}

	rootEntries, err := DeserializeDirectory(rootDirData)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("parsing root directory: %w", err)
	}

	// Resolve leaf directories into a flat list of tile entries.
	var allEntries []Entry
	for _, e := range rootEntries {
		if e.RunLength == 0 {
			// Leaf directory pointer: offset/length are relative to leaf dir section.
			leafData := make([]byte, e.Length)
			absOffset := int64(header.LeafDirOffset + e.Offset)
			if _, err := f.ReadAt(leafData, absOffset); err != nil {
				f.Close()
				return nil, fmt.Errorf("reading leaf directory at offset %d: %w", absOffset, err)
			}
			leafEntries, err := DeserializeDirectory(leafData)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("parsing leaf directory: %w", err)
			}
			allEntries = append(allEntries, leafEntries...)
		} else {
			allEntries = append(allEntries, e)
		}
	}

	// Expand run-length entries and build index.
	tileIdx := make(map[uint64]tileRef, len(allEntries)*2)
	var expanded []Entry
	for _, e := range allEntries {
		for r := uint32(0); r < e.RunLength; r++ {
			tileID := e.TileID + uint64(r)
			ref := tileRef{
				offset: header.TileDataOffset + e.Offset + uint64(r)*uint64(e.Length),
				length: e.Length,
			}
			tileIdx[tileID] = ref
			expanded = append(expanded, Entry{
				TileID:    tileID,
				Offset:    ref.offset,
				Length:    ref.length,
				RunLength: 1,
			})
		}
	}

	sort.Slice(expanded, func(i, j int) bool {
		return expanded[i].TileID < expanded[j].TileID
	})

	return &Reader{
		file:    f,
		header:  header,
		entries: expanded,
		tileIdx: tileIdx,
	}, nil
}

// Header returns the parsed PMTiles header.
func (r *Reader) Header() Header {
	return r.header
}

// ReadTile returns the raw encoded bytes for a tile at z/x/y.
// Returns nil, nil if the tile does not exist.
func (r *Reader) ReadTile(z, x, y int) ([]byte, error) {
	tileID := ZXYToTileID(z, x, y)
	ref, ok := r.tileIdx[tileID]
	if !ok {
		return nil, nil
	}

	data := make([]byte, ref.length)
	if _, err := r.file.ReadAt(data, int64(ref.offset)); err != nil {
		return nil, fmt.Errorf("reading tile z%d/%d/%d: %w", z, x, y, err)
	}
	return data, nil
}

// TilesAtZoom returns all [z, x, y] coordinates that have tiles at the given zoom level.
func (r *Reader) TilesAtZoom(z int) [][3]int {
	// Compute the tile ID range for this zoom level.
	var minID uint64
	for i := 0; i < z; i++ {
		n := uint64(1) << uint(i)
		minID += n * n
	}
	n := uint64(1) << uint(z)
	maxID := minID + n*n // exclusive

	// Binary search for the first entry >= minID.
	start := sort.Search(len(r.entries), func(i int) bool {
		return r.entries[i].TileID >= minID
	})

	var tiles [][3]int
	for i := start; i < len(r.entries); i++ {
		e := r.entries[i]
		if e.TileID >= maxID {
			break
		}
		_, x, y := TileIDToZXY(e.TileID)
		tiles = append(tiles, [3]int{z, x, y})
	}
	return tiles
}

// NumTiles returns the total number of tiles in the archive.
func (r *Reader) NumTiles() int {
	return len(r.entries)
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

	gz, err := gzip.NewReader(bytes.NewReader(metaRaw))
	if err != nil {
		return nil, fmt.Errorf("decompressing metadata: %w", err)
	}
	defer gz.Close()

	jsonData, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("reading decompressed metadata: %w", err)
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
