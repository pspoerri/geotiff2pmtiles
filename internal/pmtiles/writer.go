package pmtiles

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// dedupEntry records the location of a previously written tile in the temp file.
type dedupEntry struct {
	offset uint64
	length uint32
}

// Writer writes tiles to a PMTiles v3 archive using a two-pass approach.
// Pass 1: tiles are appended to a temporary file, entries are collected in memory.
// Pass 2: directories are built and the final PMTiles file is assembled.
//
// Identical small tiles are automatically deduplicated: when multiple tiles
// produce the same encoded bytes (e.g. uniform single-color tiles), the data
// is written to disk only once and all entries share the same offset.
type Writer struct {
	tmpFile    *os.File
	dedup      map[uint64]dedupEntry // FNV-64a hash → first occurrence (tiles up to dedupMax bytes)
	dedupMax   int                   // largest tile considered for dedup
	cmpBuf     []byte                // scratch for comparing dedup candidates
	outputPath string
	entries    []Entry
	opts       WriterOptions
	header     Header

	tmpOffset uint64
	dedupHits int64 // number of tiles that reused existing data
	mu        sync.Mutex
	finalized bool
}

// NewWriter creates a new PMTiles writer.
func NewWriter(outputPath string, opts WriterOptions) (*Writer, error) {
	// Fail now rather than in Finalize, after every tile has been written.
	if _, err := json.Marshal(opts.Extra); err != nil {
		return nil, fmt.Errorf("metadata Extra is not JSON-encodable: %w", err)
	}
	tmpDir := opts.TempDir
	if tmpDir == "" {
		tmpDir = filepath.Dir(outputPath)
	}

	tmpFile, err := os.CreateTemp(tmpDir, "pmtiles-tiles-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}

	// Repeated tiles are in practice uniform or fill tiles, whose encodings
	// are small (a uniform 256 px PNG is ~1.1 KB, JPEG ~1.6 KB; ~19 KB and
	// ~17 KB at 1024 px). Only tiles up to tileSize²/16 bytes (4 KiB at
	// 256 px, 64 KiB at 1024 px) are hashed, so the dedup map holds those
	// rather than one entry for every tile of a large run.
	tileSize := opts.TileSize
	if tileSize <= 0 {
		tileSize = 256
	}

	return &Writer{
		outputPath: outputPath,
		opts:       opts,
		header:     NewHeader(opts),
		tmpFile:    tmpFile,
		entries:    make([]Entry, 0, 65536),
		dedup:      make(map[uint64]dedupEntry),
		dedupMax:   tileSize * tileSize / 16,
	}, nil
}

// tileHash computes a FNV-64a hash of tile data for deduplication.
// A variable so that tests can force collisions.
var tileHash = func(data []byte) uint64 {
	h := fnv.New64a()
	h.Write(data)
	return h.Sum64()
}

// WriteTile writes a single tile. Safe for concurrent use.
//
// Identical small tiles are deduplicated: if a tile with the same content
// has already been written, the new entry reuses the existing offset on disk.
// This dramatically reduces temp file size for datasets with many uniform tiles.
func (w *Writer) WriteTile(z, x, y int, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	tileID := ZXYToTileID(z, x, y)
	dedup := len(data) <= w.dedupMax
	var hash uint64
	if dedup {
		hash = tileHash(data)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.tmpFile == nil {
		return fmt.Errorf("writing tile data: writer already finalized or aborted")
	}

	// Check for a dedup hit: reuse the existing data on disk. The bytes are
	// compared as well, so that a hash collision cannot make two different
	// tiles share one image.
	if de, ok := w.dedup[hash]; dedup && ok && de.length == uint32(len(data)) {
		same, err := w.tmpHolds(de.offset, data)
		if err != nil {
			return err
		}
		if same {
			w.entries = append(w.entries, Entry{
				TileID:    tileID,
				Offset:    de.offset,
				Length:    de.length,
				RunLength: 1,
			})
			w.dedupHits++
			return nil
		}
	}

	// New unique tile: write to temp file.
	offset := w.tmpOffset
	n, err := w.tmpFile.Write(data)
	if err != nil {
		return fmt.Errorf("writing tile data: %w", err)
	}
	w.tmpOffset += uint64(n)

	if dedup {
		w.dedup[hash] = dedupEntry{offset: offset, length: uint32(n)}
	}

	w.entries = append(w.entries, Entry{
		TileID:    tileID,
		Offset:    offset,
		Length:    uint32(len(data)),
		RunLength: 1,
	})

	return nil
}

// tmpHolds reports whether the temp file holds data at offset. The caller
// holds w.mu and len(data) <= w.dedupMax.
func (w *Writer) tmpHolds(offset uint64, data []byte) (bool, error) {
	if w.cmpBuf == nil {
		w.cmpBuf = make([]byte, w.dedupMax)
	}
	buf := w.cmpBuf[:len(data)]
	if _, err := w.tmpFile.ReadAt(buf, int64(offset)); err != nil {
		return false, fmt.Errorf("reading tile data for dedup: %w", err)
	}
	return bytes.Equal(buf, data), nil
}

// Finalize builds the directory, metadata, and writes the final PMTiles file.
//
// The archive is written to outputPath+".partial" and renamed over outputPath
// only once it is complete and synced, so a failed Finalize never leaves a
// truncated archive behind or destroys an existing one. The temp tile file
// is removed whether or not Finalize succeeds.
func (w *Writer) Finalize() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.finalized {
		return fmt.Errorf("already finalized")
	}
	w.finalized = true
	defer w.removeTmp()

	// The dedup map is not needed any more; free it before clustering.
	w.dedup = nil

	// Sort entries by tile ID for the directory.
	sort.Slice(w.entries, func(i, j int) bool {
		return w.entries[i].TileID < w.entries[j].TileID
	})

	// Lay tile data out in tile-ID order so the archive is properly clustered.
	// This ensures tile data on disk follows the same Hilbert order as the directory,
	// which enables readers to optimize range requests.
	src, tileDataLength := w.clusterOffsets()

	// Build the directory.
	rootDir, leafDirs, numTileEntries, err := BuildDirectory(w.entries)
	if err != nil {
		return fmt.Errorf("building directory: %w", err)
	}

	// Build metadata JSON.
	metadata, err := w.buildMetadata()
	if err != nil {
		return fmt.Errorf("encoding metadata: %w", err)
	}
	metadataBytes, err := compressGzip(metadata)
	if err != nil {
		return fmt.Errorf("compressing metadata: %w", err)
	}

	// Compute offsets.
	// Layout: [Header (127)] [Root Dir] [Metadata] [Leaf Dirs] [Tile Data]
	rootDirOffset := uint64(HeaderSize)
	rootDirLength := uint64(len(rootDir))
	metadataOffset := rootDirOffset + rootDirLength
	metadataLength := uint64(len(metadataBytes))
	leafDirOffset := metadataOffset + metadataLength
	leafDirLength := uint64(len(leafDirs))
	tileDataOffset := leafDirOffset + leafDirLength

	// Update header.
	w.header.RootDirOffset = rootDirOffset
	w.header.RootDirLength = rootDirLength
	w.header.MetadataOffset = metadataOffset
	w.header.MetadataLength = metadataLength
	w.header.LeafDirOffset = leafDirOffset
	w.header.LeafDirLength = leafDirLength
	w.header.TileDataOffset = tileDataOffset
	w.header.TileDataLength = tileDataLength
	w.header.NumAddressedTiles = uint64(len(w.entries))
	w.header.NumTileEntries = uint64(numTileEntries)
	w.header.NumTileContents = uint64(len(src))

	return w.writeArchive(rootDir, metadataBytes, leafDirs, src)
}

// clusterOffsets assigns each entry the offset its data will have in the
// archive, so that tile data follows the sorted entries (Hilbert tile-ID
// order) and the archive is "clustered" per the PMTiles v3 spec. Entries that
// share data (deduplicated tiles) keep sharing one new offset.
//
// It returns the temp file location of every unique tile in archive order,
// and the total length of the tile data.
func (w *Writer) clusterOffsets() (src []dedupEntry, length uint64) {
	// Only tiles small enough to be deduplicated can share an offset, so
	// only those need to be remembered.
	seen := make(map[uint64]uint64) // old offset → new offset
	src = make([]dedupEntry, 0, len(w.entries)-int(w.dedupHits))
	for i := range w.entries {
		e := &w.entries[i]
		if int(e.Length) <= w.dedupMax {
			if off, ok := seen[e.Offset]; ok {
				e.Offset = off
				continue
			}
			seen[e.Offset] = length
		}
		src = append(src, dedupEntry{offset: e.Offset, length: e.Length})
		e.Offset = length
		length += uint64(e.Length)
	}
	return src, length
}

// writeArchive writes the header, directories, metadata and tile data to
// outputPath+".partial", then renames it over outputPath. On any error the
// partial file is removed and an existing archive stays untouched.
//
// The partial file lives next to the output rather than in the temp
// directory, which may be on another file system, where a rename fails.
func (w *Writer) writeArchive(rootDir, metadata, leafDirs []byte, src []dedupEntry) (err error) {
	partial := w.outputPath + ".partial"
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(partial)
		}
	}()

	out := bufio.NewWriterSize(f, 1<<20)

	// Write header.
	if _, err := out.Write(w.header.Serialize()); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}

	// Write root directory.
	if _, err := out.Write(rootDir); err != nil {
		return fmt.Errorf("writing root directory: %w", err)
	}

	// Write metadata.
	if _, err := out.Write(metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	// Write leaf directories.
	if _, err := out.Write(leafDirs); err != nil {
		return fmt.Errorf("writing leaf directories: %w", err)
	}

	// Copy tile data from the temp file straight into the archive, in
	// clustered order.
	if err := w.copyTileData(out, src); err != nil {
		return err
	}

	if err := out.Flush(); err != nil {
		return fmt.Errorf("writing tile data: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing output file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing output file: %w", err)
	}
	if err := os.Rename(partial, w.outputPath); err != nil {
		return fmt.Errorf("renaming output file: %w", err)
	}
	return nil
}

// copyTileData streams the unique tiles at src from the temp file into out.
func (w *Writer) copyTileData(out io.Writer, src []dedupEntry) error {
	buf := make([]byte, 256*1024)
	for _, t := range src {
		tileLen := int(t.length)
		if tileLen > len(buf) {
			buf = make([]byte, tileLen)
		}
		if _, err := w.tmpFile.ReadAt(buf[:tileLen], int64(t.offset)); err != nil {
			return fmt.Errorf("reading tile at offset %d: %w", t.offset, err)
		}
		if _, err := out.Write(buf[:tileLen]); err != nil {
			return fmt.Errorf("writing tile data: %w", err)
		}
	}
	return nil
}

// removeTmp closes and deletes the temp tile file. Safe to call repeatedly.
func (w *Writer) removeTmp() {
	if w.tmpFile == nil {
		return
	}
	tmpPath := w.tmpFile.Name()
	w.tmpFile.Close()
	os.Remove(tmpPath)
	w.tmpFile = nil
}

// Abort cleans up resources without writing the output file.
func (w *Writer) Abort() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removeTmp()
}

// buildMetadata creates the JSON metadata for the PMTiles archive.
func (w *Writer) buildMetadata() ([]byte, error) {
	tileFormatStr := "unknown"
	switch w.opts.TileFormat {
	case TileTypeJPEG:
		tileFormatStr = "jpeg"
	case TileTypePNG:
		tileFormatStr = "png"
	case TileTypeWebP:
		tileFormatStr = "webp"
	}

	name := w.opts.Name
	if name == "" {
		name = "geotiff2pmtiles"
	}
	description := w.opts.Description
	if description == "" {
		description = "Generated from GeoTIFF files"
	}

	layerType := w.opts.Type
	if layerType == "" {
		layerType = "baselayer"
	}

	b, centerLon := archiveBounds(w.opts.Bounds)
	meta := map[string]interface{}{
		"name":        name,
		"description": description,
		"format":      tileFormatStr,
		"type":        layerType,
		"minzoom":     fmt.Sprintf("%d", w.opts.MinZoom),
		"maxzoom":     fmt.Sprintf("%d", w.opts.MaxZoom),
		"bounds":      fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", b.MinLon, b.MinLat, b.MaxLon, b.MaxLat),
		"center": fmt.Sprintf("%.6f,%.6f,%d",
			centerLon, (b.MinLat+b.MaxLat)/2, (w.opts.MinZoom+w.opts.MaxZoom)/2),
	}

	if w.opts.Attribution != "" {
		meta["attribution"] = w.opts.Attribution
	}
	if w.opts.Encoding != "" {
		meta["encoding"] = w.opts.Encoding
	}

	// Caller-supplied keys, merged last so they win over the derived ones.
	for k, v := range w.opts.Extra {
		meta[k] = v
	}

	return json.Marshal(meta)
}

func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
