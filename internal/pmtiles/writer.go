package pmtiles

import (
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
// Identical tile data is automatically deduplicated: when multiple tiles produce
// the same encoded bytes (e.g. uniform single-color tiles), the data is written
// to disk only once and all entries share the same offset.
type Writer struct {
	outputPath string
	opts       WriterOptions
	header     Header

	tmpFile   *os.File
	tmpDir    string // directory for temp files
	tmpOffset uint64
	entries   []Entry
	dedup     map[uint64]dedupEntry // FNV-64a hash → first occurrence (for dedup)
	mu        sync.Mutex
	finalized bool

	dedupHits int64 // number of tiles that reused existing data
}

// NewWriter creates a new PMTiles writer.
func NewWriter(outputPath string, opts WriterOptions) (*Writer, error) {
	tmpDir := opts.TempDir
	if tmpDir == "" {
		tmpDir = filepath.Dir(outputPath)
	}

	tmpFile, err := os.CreateTemp(tmpDir, "pmtiles-tiles-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}

	return &Writer{
		outputPath: outputPath,
		opts:       opts,
		header:     NewHeader(opts),
		tmpFile:    tmpFile,
		tmpDir:     tmpDir,
		entries:    make([]Entry, 0, 65536),
		dedup:      make(map[uint64]dedupEntry),
	}, nil
}

// tileHash computes a FNV-64a hash of tile data for deduplication.
func tileHash(data []byte) uint64 {
	h := fnv.New64a()
	h.Write(data)
	return h.Sum64()
}

// WriteTile writes a single tile. Safe for concurrent use.
//
// Identical tile data is deduplicated: if a tile with the same content has
// already been written, the new entry reuses the existing offset on disk.
// This dramatically reduces temp file size for datasets with many uniform tiles.
func (w *Writer) WriteTile(z, x, y int, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	tileID := ZXYToTileID(z, x, y)
	hash := tileHash(data)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Check for a dedup hit: reuse the existing data on disk.
	if de, ok := w.dedup[hash]; ok && de.length == uint32(len(data)) {
		w.entries = append(w.entries, Entry{
			TileID:    tileID,
			Offset:    de.offset,
			Length:    de.length,
			RunLength: 1,
		})
		w.dedupHits++
		return nil
	}

	// New unique tile: write to temp file.
	offset := w.tmpOffset
	n, err := w.tmpFile.Write(data)
	if err != nil {
		return fmt.Errorf("writing tile data: %w", err)
	}
	w.tmpOffset += uint64(n)

	w.dedup[hash] = dedupEntry{offset: offset, length: uint32(n)}

	w.entries = append(w.entries, Entry{
		TileID:    tileID,
		Offset:    offset,
		Length:    uint32(len(data)),
		RunLength: 1,
	})

	return nil
}

// Finalize builds the directory, metadata, and writes the final PMTiles file.
func (w *Writer) Finalize() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.finalized {
		return fmt.Errorf("already finalized")
	}
	w.finalized = true

	// Sort entries by tile ID for the directory.
	sort.Slice(w.entries, func(i, j int) bool {
		return w.entries[i].TileID < w.entries[j].TileID
	})

	// Rewrite tile data in tile-ID order so the archive is properly clustered.
	// This ensures tile data on disk follows the same Hilbert order as the directory,
	// which enables readers to optimize range requests.
	if err := w.clusterTileData(); err != nil {
		return fmt.Errorf("clustering tile data: %w", err)
	}

	// Build the directory.
	rootDir, leafDirs, err := buildDirectory(w.entries)
	if err != nil {
		return fmt.Errorf("building directory: %w", err)
	}

	// Build metadata JSON.
	metadata := w.buildMetadata()
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
	w.header.TileDataLength = w.tmpOffset
	w.header.NumAddressedTiles = uint64(len(w.entries))
	w.header.NumTileEntries = uint64(len(w.entries))
	w.header.NumTileContents = uint64(len(w.entries) - int(w.dedupHits))

	// Write the final file.
	outFile, err := os.Create(w.outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	// Write header.
	if _, err := outFile.Write(w.header.Serialize()); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}

	// Write root directory.
	if _, err := outFile.Write(rootDir); err != nil {
		return fmt.Errorf("writing root directory: %w", err)
	}

	// Write metadata.
	if _, err := outFile.Write(metadataBytes); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	// Write leaf directories.
	if len(leafDirs) > 0 {
		if _, err := outFile.Write(leafDirs); err != nil {
			return fmt.Errorf("writing leaf directories: %w", err)
		}
	}

	// Copy tile data from temp file (now in clustered order).
	if _, err := w.tmpFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seeking temp file: %w", err)
	}

	if _, err := io.Copy(outFile, w.tmpFile); err != nil {
		return fmt.Errorf("copying tile data: %w", err)
	}

	// Cleanup temp file.
	tmpPath := w.tmpFile.Name()
	w.tmpFile.Close()
	os.Remove(tmpPath)

	return nil
}

// clusterTileData rewrites the temp file so tile data is in the same order
// as the sorted entries (Hilbert tile-ID order). This makes the archive
// "clustered" per the PMTiles v3 spec, enabling read-time optimizations.
//
// Deduplicated tiles (multiple entries sharing the same offset) are written
// once and all entries are remapped to the new shared offset.
func (w *Writer) clusterTileData() error {
	// Create a new temp file for the reordered data.
	newTmp, err := os.CreateTemp(w.tmpDir, "pmtiles-clustered-*.tmp")
	if err != nil {
		return fmt.Errorf("creating clustered temp file: %w", err)
	}

	buf := make([]byte, 256*1024) // 256 KiB read buffer
	var newOffset uint64

	// Track remapped offsets so deduplicated tiles (which share the same
	// old offset) are written only once and all entries point to the same
	// new offset.
	type remap struct {
		newOffset uint64
		length    uint32
	}
	seen := make(map[uint64]remap) // old offset → new location

	for i := range w.entries {
		e := &w.entries[i]

		// If we already wrote data from this old offset, reuse it.
		if m, ok := seen[e.Offset]; ok && m.length == e.Length {
			e.Offset = m.newOffset
			continue
		}

		tileLen := int64(e.Length)

		// Read tile data from old position.
		if tileLen > int64(len(buf)) {
			buf = make([]byte, tileLen)
		}
		if _, err := w.tmpFile.ReadAt(buf[:tileLen], int64(e.Offset)); err != nil {
			return fmt.Errorf("reading tile at offset %d: %w", e.Offset, err)
		}

		// Write to new position.
		if _, err := newTmp.Write(buf[:tileLen]); err != nil {
			return fmt.Errorf("writing tile at new offset %d: %w", newOffset, err)
		}

		// Record the remapping and update the entry.
		oldOffset := e.Offset
		e.Offset = newOffset
		seen[oldOffset] = remap{newOffset: newOffset, length: e.Length}
		newOffset += uint64(tileLen)
	}

	// Replace old temp file with the new clustered one.
	oldPath := w.tmpFile.Name()
	w.tmpFile.Close()
	os.Remove(oldPath)

	w.tmpFile = newTmp
	w.tmpOffset = newOffset

	return nil
}

// Abort cleans up resources without writing the output file.
func (w *Writer) Abort() {
	if w.tmpFile != nil {
		tmpPath := w.tmpFile.Name()
		w.tmpFile.Close()
		os.Remove(tmpPath)
	}
}

// buildMetadata creates the JSON metadata for the PMTiles archive.
func (w *Writer) buildMetadata() []byte {
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

	meta := map[string]interface{}{
		"name":        name,
		"description": description,
		"format":      tileFormatStr,
		"type":        layerType,
		"minzoom":     fmt.Sprintf("%d", w.opts.MinZoom),
		"maxzoom":     fmt.Sprintf("%d", w.opts.MaxZoom),
		"bounds": fmt.Sprintf("%.6f,%.6f,%.6f,%.6f",
			w.opts.Bounds.MinLon, w.opts.Bounds.MinLat,
			w.opts.Bounds.MaxLon, w.opts.Bounds.MaxLat),
		"center": fmt.Sprintf("%.6f,%.6f,%d",
			(w.opts.Bounds.MinLon+w.opts.Bounds.MaxLon)/2,
			(w.opts.Bounds.MinLat+w.opts.Bounds.MaxLat)/2,
			(w.opts.MinZoom+w.opts.MaxZoom)/2),
	}

	if w.opts.Attribution != "" {
		meta["attribution"] = w.opts.Attribution
	}

	data, _ := json.Marshal(meta)
	return data
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
