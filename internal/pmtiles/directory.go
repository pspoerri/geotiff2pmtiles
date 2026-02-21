package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Entry represents a single entry in the PMTiles directory.
type Entry struct {
	TileID    uint64
	Offset    uint64
	Length    uint32
	RunLength uint32
}

// ZXYToTileID converts z/x/y coordinates to a PMTiles v3 tile ID using Hilbert curve ordering.
func ZXYToTileID(z, x, y int) uint64 {
	if z == 0 {
		return 0
	}

	// The tile ID is the sum of all tiles at lower zoom levels plus the Hilbert index at this level.
	var acc uint64
	for i := 0; i < z; i++ {
		n := uint64(1) << uint(i)
		acc += n * n
	}

	// Hilbert curve index for (x, y) within a 2^z grid.
	n := uint64(1) << uint(z)
	return acc + xyToHilbert(uint64(x), uint64(y), n)
}

// xyToHilbert converts (x, y) to a Hilbert curve index for an n x n grid.
func xyToHilbert(x, y, n uint64) uint64 {
	var d uint64
	s := n / 2
	for s > 0 {
		var rx, ry uint64
		if (x & s) > 0 {
			rx = 1
		}
		if (y & s) > 0 {
			ry = 1
		}
		d += s * s * ((3 * rx) ^ ry)
		// Rotate
		if ry == 0 {
			if rx == 1 {
				x = s*2 - 1 - x
				y = s*2 - 1 - y
			}
			x, y = y, x
		}
		s /= 2
	}
	return d
}

// buildDirectory takes a sorted list of entries and produces a serialized, gzip-compressed directory.
func buildDirectory(entries []Entry) (rootDir []byte, leafDirs []byte, err error) {
	// Sort entries by tile ID.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TileID < entries[j].TileID
	})

	// Optimize run lengths: consecutive tile IDs with contiguous data can share an entry.
	optimized := optimizeRunLengths(entries)

	// If entries fit in a single root directory, no leaf directories needed.
	const maxRootEntries = 16384

	if len(optimized) <= maxRootEntries {
		rootDir, err = serializeDirectory(optimized)
		return rootDir, nil, err
	}

	// Otherwise, split into leaf directories.
	leafSize := 4096
	numLeaves := (len(optimized) + leafSize - 1) / leafSize

	type leafInfo struct {
		firstTileID uint64
		offset      uint64
		length      uint64
	}

	var leafBuf bytes.Buffer
	leaves := make([]leafInfo, 0, numLeaves)

	for i := 0; i < len(optimized); i += leafSize {
		end := i + leafSize
		if end > len(optimized) {
			end = len(optimized)
		}
		chunk := optimized[i:end]

		leafData, serErr := serializeDirectory(chunk)
		if serErr != nil {
			return nil, nil, serErr
		}

		leaves = append(leaves, leafInfo{
			firstTileID: chunk[0].TileID,
			offset:      uint64(leafBuf.Len()),
			length:      uint64(len(leafData)),
		})
		leafBuf.Write(leafData)
	}

	// Build root directory with leaf pointers.
	// In PMTiles v3, leaf directory entries have RunLength = 0, and Offset/Length point
	// into the leaf directories section.
	rootEntries := make([]Entry, len(leaves))
	for i, l := range leaves {
		rootEntries[i] = Entry{
			TileID:    l.firstTileID,
			Offset:    l.offset,
			Length:    uint32(l.length),
			RunLength: 0, // 0 indicates this is a leaf directory pointer
		}
	}

	rootDir, err = serializeDirectory(rootEntries)
	return rootDir, leafBuf.Bytes(), err
}

// serializeDirectory serializes entries into a gzip-compressed binary format.
func serializeDirectory(entries []Entry) ([]byte, error) {
	var raw bytes.Buffer

	// Write number of entries.
	buf := make([]byte, binary.MaxVarintLen64)

	n := binary.PutUvarint(buf, uint64(len(entries)))
	raw.Write(buf[:n])

	// Write tile IDs as deltas.
	var lastID uint64
	for _, e := range entries {
		delta := e.TileID - lastID
		n = binary.PutUvarint(buf, delta)
		raw.Write(buf[:n])
		lastID = e.TileID
	}

	// Write run lengths.
	for _, e := range entries {
		n = binary.PutUvarint(buf, uint64(e.RunLength))
		raw.Write(buf[:n])
	}

	// Write lengths.
	for _, e := range entries {
		n = binary.PutUvarint(buf, uint64(e.Length))
		raw.Write(buf[:n])
	}

	// Write offsets as deltas (with special encoding for tile data vs leaf dir offsets).
	var lastOffset uint64
	for i, e := range entries {
		var val uint64
		if i > 0 && e.Offset == lastOffset+uint64(entries[i-1].Length) {
			// Offset is exactly contiguous with previous entry — encode as 0.
			val = 0
		} else {
			val = e.Offset + 1 // +1 so that 0 can mean "contiguous"
		}
		n = binary.PutUvarint(buf, val)
		raw.Write(buf[:n])
		lastOffset = e.Offset
	}

	// Gzip compress.
	var compressed bytes.Buffer
	gw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return compressed.Bytes(), nil
}

// TileIDToZXY converts a PMTiles v3 tile ID back to z/x/y coordinates.
func TileIDToZXY(tileID uint64) (z, x, y int) {
	if tileID == 0 {
		return 0, 0, 0
	}

	// Find the zoom level: tile IDs for zoom z start at sum of 4^i for i in [0, z-1].
	var acc uint64
	z = 0
	for {
		n := uint64(1) << uint(z)
		count := n * n // 4^z tiles at this zoom
		if acc+count > tileID {
			break
		}
		acc += count
		z++
	}

	// The Hilbert index within this zoom level.
	hilbertIdx := tileID - acc
	n := uint64(1) << uint(z)
	hx, hy := hilbertToXY(hilbertIdx, n)
	return z, int(hx), int(hy)
}

// hilbertToXY converts a Hilbert curve index to (x, y) for an n x n grid.
func hilbertToXY(d, n uint64) (x, y uint64) {
	var rx, ry uint64
	s := uint64(1)
	for s < n {
		rx = 1 & (d / 2)
		ry = 1 & (d ^ rx)
		if ry == 0 {
			if rx == 1 {
				x = s - 1 - x
				y = s - 1 - y
			}
			x, y = y, x
		}
		x += s * rx
		y += s * ry
		d /= 4
		s *= 2
	}
	return x, y
}

// DeserializeDirectory decompresses and parses a gzip-compressed PMTiles v3 directory.
func DeserializeDirectory(data []byte) ([]Entry, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	raw, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("decompressing directory: %w", err)
	}

	r := bytes.NewReader(raw)

	numEntries, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, fmt.Errorf("reading entry count: %w", err)
	}

	entries := make([]Entry, numEntries)

	// Read tile IDs (delta-encoded).
	var lastID uint64
	for i := uint64(0); i < numEntries; i++ {
		delta, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("reading tile ID delta %d: %w", i, err)
		}
		lastID += delta
		entries[i].TileID = lastID
	}

	// Read run lengths.
	for i := uint64(0); i < numEntries; i++ {
		rl, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("reading run length %d: %w", i, err)
		}
		entries[i].RunLength = uint32(rl)
	}

	// Read lengths.
	for i := uint64(0); i < numEntries; i++ {
		length, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("reading length %d: %w", i, err)
		}
		entries[i].Length = uint32(length)
	}

	// Read offsets (special encoding: 0 means contiguous with previous).
	var lastOffset uint64
	for i := uint64(0); i < numEntries; i++ {
		val, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("reading offset %d: %w", i, err)
		}
		if val == 0 && i > 0 {
			entries[i].Offset = lastOffset + uint64(entries[i-1].Length)
		} else {
			entries[i].Offset = val - 1
		}
		lastOffset = entries[i].Offset
	}

	return entries, nil
}

// optimizeRunLengths merges consecutive entries with contiguous tile IDs and offsets.
func optimizeRunLengths(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}

	result := make([]Entry, 0, len(entries))
	current := entries[0]
	current.RunLength = 1

	for i := 1; i < len(entries); i++ {
		e := entries[i]
		// Check if this entry is contiguous with the current run.
		expectedTileID := current.TileID + uint64(current.RunLength)
		expectedOffset := current.Offset + uint64(current.Length)*uint64(current.RunLength)

		if e.TileID == expectedTileID &&
			e.Offset == expectedOffset &&
			e.Length == current.Length {
			current.RunLength++
		} else {
			result = append(result, current)
			current = e
			current.RunLength = 1
		}
	}
	result = append(result, current)

	return result
}
