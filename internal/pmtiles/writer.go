package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

// Writer writes tiles to a PMTiles v3 archive using a two-pass approach.
// Pass 1: tiles are appended to a temporary file, entries are collected in memory.
// Pass 2: directories are built and the final PMTiles file is assembled.
type Writer struct {
	outputPath string
	opts       WriterOptions
	header     Header

	tmpFile   *os.File
	tmpOffset uint64
	entries   []Entry
	mu        sync.Mutex
	finalized bool
}

// NewWriter creates a new PMTiles writer.
func NewWriter(outputPath string, opts WriterOptions) (*Writer, error) {
	tmpFile, err := os.CreateTemp("", "pmtiles-tiles-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}

	return &Writer{
		outputPath: outputPath,
		opts:       opts,
		header:     NewHeader(opts),
		tmpFile:    tmpFile,
		entries:    make([]Entry, 0, 65536),
	}, nil
}

// WriteTile writes a single tile. Safe for concurrent use.
func (w *Writer) WriteTile(z, x, y int, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	tileID := ZXYToTileID(z, x, y)

	w.mu.Lock()
	defer w.mu.Unlock()

	offset := w.tmpOffset
	n, err := w.tmpFile.Write(data)
	if err != nil {
		return fmt.Errorf("writing tile data: %w", err)
	}
	w.tmpOffset += uint64(n)

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
	w.header.NumTileContents = uint64(len(w.entries))

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
func (w *Writer) clusterTileData() error {
	// Create a new temp file for the reordered data.
	newTmp, err := os.CreateTemp("", "pmtiles-clustered-*.tmp")
	if err != nil {
		return fmt.Errorf("creating clustered temp file: %w", err)
	}

	buf := make([]byte, 256*1024) // 256 KiB read buffer
	var newOffset uint64

	for i := range w.entries {
		e := &w.entries[i]
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

		// Update entry offset to the new position.
		e.Offset = newOffset
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

	meta := map[string]interface{}{
		"name":        "geotiff2pmtiles",
		"description": "Generated from GeoTIFF files",
		"format":      tileFormatStr,
		"type":        "baselayer",
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
