package pmtiles

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// StreamTileDataOffset is where a StreamWriter's tile data starts: right
// after the space reserved for the header and root directory, which the
// spec requires within the first 16 KiB.
const StreamTileDataOffset = 16384

// StreamWriter writes an archive whose tiles arrive in increasing tile-ID
// order straight into <output>.partial, with no temp copy: the peak disk
// use is the archive itself rather than twice it. Sections are laid out as
//
//	[header][root dir, zero padded to 16 KiB][tile data][metadata][leaf dirs]
//
// which the spec allows: every section but the header may be relocated,
// and the root directory only has to end within the first 16 KiB, which
// BuildDirectory guarantees. The header and root directory are written in
// place by Finalize. Small repeated tiles are deduplicated as in Writer, and
// consecutive tile IDs sharing one blob are merged into runs as they come.
type StreamWriter struct {
	f          *os.File
	outputPath string
	opts       WriterOptions
	header     Header
	entries    []Entry
	dedup      map[uint64]dedupEntry
	dedupMax   int
	cmpBuf     []byte
	dataLen    uint64 // tile data written so far
	lastID     uint64
	addressed  int64
	contents   int64
	mu         sync.Mutex
	done       bool
}

// NewStreamWriter creates <outputPath>.partial; Finalize renames it over
// outputPath.
func NewStreamWriter(outputPath string, opts WriterOptions) (*StreamWriter, error) {
	if _, err := json.Marshal(opts.Extra); err != nil {
		return nil, fmt.Errorf("metadata Extra is not JSON-encodable: %w", err)
	}
	f, err := os.OpenFile(outputPath+".partial", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, fmt.Errorf("creating output file: %w", err)
	}
	if _, err := f.Seek(StreamTileDataOffset, 0); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("seeking to tile data: %w", err)
	}
	tileSize := opts.TileSize
	if tileSize <= 0 {
		tileSize = 256
	}
	return &StreamWriter{
		f:          f,
		outputPath: outputPath,
		opts:       opts,
		header:     NewHeader(opts),
		entries:    make([]Entry, 0, 65536),
		dedup:      make(map[uint64]dedupEntry),
		dedupMax:   tileSize * tileSize / 16, // see NewWriter
	}, nil
}

// WriteTile appends a tile. Tile IDs must increase from call to call; the
// caller orders its writes (see tile.Merge). Safe for concurrent use, but
// concurrent callers cannot keep the order.
func (w *StreamWriter) WriteTile(z, x, y int, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	id := ZXYToTileID(z, x, y)
	dedup := len(data) <= w.dedupMax
	var hash uint64
	if dedup {
		hash = tileHash(data)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return fmt.Errorf("writing tile data: writer already finalized or aborted")
	}
	if w.addressed > 0 && id <= w.lastID {
		return fmt.Errorf("tile z%d/%d/%d (ID %d) written after ID %d; a StreamWriter needs increasing tile IDs", z, x, y, id, w.lastID)
	}
	w.lastID = id
	w.addressed++

	if de, ok := w.dedup[hash]; dedup && ok && de.length == uint32(len(data)) {
		if w.cmpBuf == nil {
			w.cmpBuf = make([]byte, w.dedupMax)
		}
		buf := w.cmpBuf[:len(data)]
		if _, err := w.f.ReadAt(buf, int64(StreamTileDataOffset+de.offset)); err != nil {
			return fmt.Errorf("reading tile data for dedup: %w", err)
		}
		if string(buf) == string(data) {
			w.addEntry(id, de)
			return nil
		}
	}

	n, err := w.f.Write(data)
	if err != nil {
		return fmt.Errorf("writing tile data: %w", err)
	}
	de := dedupEntry{offset: w.dataLen, length: uint32(n)}
	w.dataLen += uint64(n)
	w.contents++
	if dedup {
		w.dedup[hash] = de
	}
	w.addEntry(id, de)
	return nil
}

// addEntry records tile id at blob de, extending the last entry's run when
// id follows it and shares its blob. The caller holds w.mu.
func (w *StreamWriter) addEntry(id uint64, de dedupEntry) {
	if n := len(w.entries); n > 0 {
		if last := &w.entries[n-1]; last.Offset == de.offset && last.Length == de.length &&
			last.TileID+uint64(last.RunLength) == id {
			last.RunLength++
			return
		}
	}
	w.entries = append(w.entries, Entry{TileID: id, Offset: de.offset, Length: de.length, RunLength: 1})
}

// Finalize writes the directories, metadata and header, and renames the
// archive over outputPath. On error the partial file is removed.
func (w *StreamWriter) Finalize() (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return fmt.Errorf("already finalized")
	}
	w.done = true
	w.dedup = nil
	partial := w.f.Name()
	defer func() {
		if err != nil {
			w.f.Close()
			os.Remove(partial)
		}
	}()

	rootDir, leafDirs, numEntries, err := BuildDirectory(w.entries)
	if err != nil {
		return fmt.Errorf("building directory: %w", err)
	}
	if HeaderSize+len(rootDir) > StreamTileDataOffset {
		return fmt.Errorf("root directory of %d bytes does not fit before the tile data", len(rootDir))
	}
	metadata, err := buildMetadata(w.opts)
	if err != nil {
		return fmt.Errorf("encoding metadata: %w", err)
	}
	if metadata, err = compressGzip(metadata); err != nil {
		return fmt.Errorf("compressing metadata: %w", err)
	}

	h := &w.header
	h.RootDirOffset, h.RootDirLength = HeaderSize, uint64(len(rootDir))
	h.TileDataOffset, h.TileDataLength = StreamTileDataOffset, w.dataLen
	h.MetadataOffset, h.MetadataLength = StreamTileDataOffset+w.dataLen, uint64(len(metadata))
	h.LeafDirOffset, h.LeafDirLength = h.MetadataOffset+h.MetadataLength, uint64(len(leafDirs))
	h.NumAddressedTiles = uint64(w.addressed)
	h.NumTileEntries = uint64(numEntries)
	h.NumTileContents = uint64(w.contents)

	// The file position is at the end of the tile data.
	for _, b := range [][]byte{metadata, leafDirs} {
		if _, err := w.f.Write(b); err != nil {
			return fmt.Errorf("writing metadata and leaf directories: %w", err)
		}
	}
	if _, err := w.f.WriteAt(rootDir, HeaderSize); err != nil {
		return fmt.Errorf("writing root directory: %w", err)
	}
	if _, err := w.f.WriteAt(h.Serialize(), 0); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("syncing output file: %w", err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("closing output file: %w", err)
	}
	if err := os.Rename(partial, w.outputPath); err != nil {
		return fmt.Errorf("renaming output file: %w", err)
	}
	return nil
}

// Abort removes the partial archive.
func (w *StreamWriter) Abort() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.f.Name())
}
