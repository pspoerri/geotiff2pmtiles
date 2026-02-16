package tile

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// diskEntry records the location and format of a tile spilled to disk.
type diskEntry struct {
	offset int64
	length int32
	typ    tileDataType
}

// DiskTileStore is a concurrent-safe tile store that keeps tiles in memory
// and spills them to disk when memory pressure exceeds a threshold.
//
// Tiles are primarily stored in an in-memory map (fast path). When the
// estimated memory usage crosses a configurable limit, all in-memory tiles
// are flushed to a temporary file. Future reads check memory first, then
// fall back to disk. The disk file stores raw pixel data sequentially;
// a small in-memory index maps each tile key to its (offset, length, type).
//
// This design means:
//   - At most one zoom level's tiles are on disk (the store is swapped per level).
//   - Tiles arrive roughly in Hilbert order (from the generator), so the disk
//     file has good spatial locality for the downsampling read-back pass.
//   - The per-tile index entry is ~30 bytes, far smaller than the tile data
//     (64–256 KB), so even millions of tiles have a manageable index.
type DiskTileStore struct {
	mu       sync.RWMutex
	tiles    map[[3]int]*TileData // in-memory tiles
	index    map[[3]int]diskEntry // disk index (populated after flush)
	tileSize int

	// Disk backing.
	file    *os.File // temp file for spilled tiles
	fileOff int64    // current write offset in the temp file
	dir     string   // directory for temp files

	// Memory tracking.
	memBytes       atomic.Int64 // estimated bytes of in-memory tile data
	memLimit       int64        // flush threshold in bytes (0 = never flush)
	flushCount     int          // number of flushes performed
	totalFlushed   int64        // total tiles flushed to disk
	checkInterval  int64        // check memory every N puts
	putsSinceCheck atomic.Int64

	verbose bool
}

// DiskTileStoreConfig configures the disk-backed tile store.
type DiskTileStoreConfig struct {
	// InitialCapacity is the estimated number of tiles for map pre-allocation.
	InitialCapacity int
	// TileSize is the tile dimension in pixels (e.g. 256).
	TileSize int
	// TempDir is the directory for spill files. Defaults to the OS temp dir.
	TempDir string
	// MemoryLimitBytes is the threshold at which in-memory tiles are flushed
	// to disk. Set to 0 to disable disk spilling (pure in-memory mode).
	MemoryLimitBytes int64
	// Verbose enables logging of flush events.
	Verbose bool
}

// NewDiskTileStore creates a new disk-backed tile store.
func NewDiskTileStore(cfg DiskTileStoreConfig) *DiskTileStore {
	cap := cfg.InitialCapacity
	if cap < 64 {
		cap = 64
	}
	dir := cfg.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	checkInterval := int64(1024) // check memory pressure every 1024 puts
	return &DiskTileStore{
		tiles:         make(map[[3]int]*TileData, cap),
		index:         make(map[[3]int]diskEntry),
		tileSize:      cfg.TileSize,
		dir:           dir,
		memLimit:      cfg.MemoryLimitBytes,
		checkInterval: checkInterval,
		verbose:       cfg.Verbose,
	}
}

// Get retrieves tile data. Checks in-memory store first, then disk.
// Returns nil if the tile is not present anywhere.
func (s *DiskTileStore) Get(z, x, y int) *TileData {
	key := [3]int{z, x, y}

	// Fast path: in-memory.
	s.mu.RLock()
	td := s.tiles[key]
	s.mu.RUnlock()
	if td != nil {
		return td
	}

	// Slow path: check disk index.
	s.mu.RLock()
	de, onDisk := s.index[key]
	f := s.file
	s.mu.RUnlock()
	if !onDisk || f == nil {
		return nil
	}

	// Read from disk.
	buf := make([]byte, de.length)
	_, err := f.ReadAt(buf, de.offset)
	if err != nil {
		return nil
	}
	return DeserializeTileData(buf, de.typ, s.tileSize)
}

// Put stores tile data. May trigger a flush to disk if memory is over the limit.
func (s *DiskTileStore) Put(z, x, y int, td *TileData) {
	key := [3]int{z, x, y}
	mem := td.MemoryBytes()

	s.mu.Lock()
	s.tiles[key] = td
	s.mu.Unlock()

	s.memBytes.Add(mem)

	// Periodically check if we should flush.
	if s.memLimit > 0 {
		n := s.putsSinceCheck.Add(1)
		if n >= s.checkInterval {
			s.putsSinceCheck.Store(0)
			if s.memBytes.Load() > s.memLimit {
				s.flush()
			}
		}
	}
}

// Len returns the total number of stored tiles (memory + disk).
func (s *DiskTileStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tiles) + len(s.index)
}

// MemoryBytes returns the estimated in-memory tile data size.
func (s *DiskTileStore) MemoryBytes() int64 {
	return s.memBytes.Load()
}

// flush writes all in-memory tiles to the disk file and clears the in-memory map.
func (s *DiskTileStore) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tiles) == 0 {
		return
	}

	// Lazily create the temp file on first flush.
	if s.file == nil {
		f, err := os.CreateTemp(s.dir, "pmtiles-tilestore-*.tmp")
		if err != nil {
			log.Printf("WARNING: disk tile store: failed to create temp file: %v", err)
			return
		}
		s.file = f
	}

	tileCount := len(s.tiles)
	var flushedBytes int64

	// Use a write buffer to reduce syscalls. Pre-allocate for the common case
	// of gray tiles (64 KB each).
	writeBuf := make([]byte, 0, 128*1024)

	for key, td := range s.tiles {
		writeBuf = writeBuf[:0]
		writeBuf, typ := td.SerializeAppend(writeBuf)

		n, err := s.file.Write(writeBuf)
		if err != nil {
			log.Printf("WARNING: disk tile store: write error: %v", err)
			return
		}

		s.index[key] = diskEntry{
			offset: s.fileOff,
			length: int32(n),
			typ:    typ,
		}
		s.fileOff += int64(n)
		flushedBytes += td.MemoryBytes()
	}

	// Clear the in-memory map.
	s.tiles = make(map[[3]int]*TileData, 1024)
	s.memBytes.Store(0)
	s.flushCount++
	s.totalFlushed += int64(tileCount)

	if s.verbose {
		log.Printf("Disk tile store: flushed %d tiles (%.1f MB) to disk (total on disk: %d tiles, %.1f MB file)",
			tileCount, float64(flushedBytes)/(1024*1024),
			len(s.index), float64(s.fileOff)/(1024*1024))
	}
}

// Close removes the temporary file. Call when the store is no longer needed.
func (s *DiskTileStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		name := s.file.Name()
		s.file.Close()
		os.Remove(name)
		s.file = nil
	}
}

// Stats returns a human-readable summary of the store's usage.
func (s *DiskTileStore) Stats() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("in-memory: %d tiles (%.1f MB), on-disk: %d tiles (%.1f MB file), flushes: %d",
		len(s.tiles), float64(s.memBytes.Load())/(1024*1024),
		len(s.index), float64(s.fileOff)/(1024*1024),
		s.flushCount)
}

// WriteTo writes the disk index to a writer for debugging/checkpointing.
// Format: count(uint32) + [key_z(int32) key_x(int32) key_y(int32) offset(int64) length(int32) type(uint8)] × count.
func (s *DiskTileStore) WriteIndexTo(w io.Writer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(s.index)))
	if _, err := w.Write(buf); err != nil {
		return err
	}

	entry := make([]byte, 4+4+4+8+4+1) // 25 bytes per entry
	for key, de := range s.index {
		binary.LittleEndian.PutUint32(entry[0:4], uint32(key[0]))   // z
		binary.LittleEndian.PutUint32(entry[4:8], uint32(key[1]))   // x
		binary.LittleEndian.PutUint32(entry[8:12], uint32(key[2]))  // y
		binary.LittleEndian.PutUint64(entry[12:20], uint64(de.offset))
		binary.LittleEndian.PutUint32(entry[20:24], uint32(de.length))
		entry[24] = uint8(de.typ)
		if _, err := w.Write(entry); err != nil {
			return err
		}
	}
	return nil
}

// TempFilePath returns the path to the temporary spill file, or "" if none exists.
func (s *DiskTileStore) TempFilePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.file != nil {
		return s.file.Name()
	}
	return ""
}

// StoreInTargetDir creates the temp file in the same directory as the output
// file, which avoids cross-filesystem moves and keeps spill data near the output.
func StoreInTargetDir(outputPath string) string {
	return filepath.Dir(outputPath)
}
