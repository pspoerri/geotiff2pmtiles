package tile

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// diskEntry records the location of an encoded tile on disk.
type diskEntry struct {
	offset int64
	length int32
}

// ioRequest is sent from Put() to the I/O goroutine for async disk writes.
type ioRequest struct {
	key      [3]int
	encoded  []byte // pre-encoded tile bytes (PNG/WebP/JPEG)
	memBytes int64  // memory to reclaim when evicted from in-memory store
}

// DiskTileStore is a concurrent-safe tile store that keeps tiles in memory
// and continuously spills them to disk via a dedicated I/O goroutine.
//
// When disk spilling is enabled, tiles are handled as follows:
//   - Put() stores the tile in an in-memory map (fast access for recently
//     stored tiles) and sends the pre-encoded bytes to a buffered channel.
//   - A dedicated background I/O goroutine reads from the channel, writes
//     encoded bytes to a temporary file, adds an index entry, and evicts
//     the tile from memory — all without blocking compute workers.
//   - Uniform tiles (single-color, 4 bytes each) are kept in memory and
//     never spilled, since they're already extremely compact.
//   - Get() checks in-memory tiles first, then falls back to reading from
//     disk and decoding the encoded bytes back to pixel data.
//
// Storing tiles in their encoded format (PNG/WebP/JPEG) instead of raw
// pixels reduces disk usage by 5-50× (e.g., 10 KB encoded vs 64-256 KB raw),
// at the cost of a decode step during the downsampling read-back pass.
//
// The continuous I/O design means disk writes are spread evenly over the
// processing time rather than occurring in large blocking flushes.
//
// The temp file is owned exclusively by the I/O goroutine for writing.
// Readers access it via an atomic pointer (lock-free ReadAt), so file I/O
// never contends with the map mutex.
type DiskTileStore struct {
	mu       sync.RWMutex
	tiles    map[[3]int]*TileData // in-memory tiles (evicted once on disk)
	index    map[[3]int]diskEntry // disk index (populated by I/O goroutine)
	tileSize int
	format   string // encoder format for decode path ("png", "jpeg", "webp", "terrarium")

	// Read-only file handle for Get(). Set once by ioLoop on first write,
	// never reassigned. Readers use atomic load + ReadAt (pread, no locking).
	readFile atomic.Pointer[os.File]
	dir      string // directory for temp files

	// Memory tracking.
	memBytes atomic.Int64 // estimated bytes of in-memory tile data

	// Dedicated I/O goroutine.
	ioCh      chan ioRequest // tiles to write to disk
	ioWg      sync.WaitGroup // for Drain()
	drainOnce sync.Once      // ensures Drain() is idempotent

	// Stats (updated by I/O goroutine only, read after Drain).
	totalDiskTiles int64 // tiles written to disk
	totalDiskBytes int64 // total encoded bytes on disk

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
	// MemoryLimitBytes enables continuous disk spilling when > 0. Tiles are
	// written to disk by a dedicated I/O goroutine, encoded in the target
	// format for reduced disk usage. Set to 0 to disable (pure in-memory mode).
	MemoryLimitBytes int64
	// Format is the encoder format name (e.g. "png", "jpeg", "webp", "terrarium").
	// Required when MemoryLimitBytes > 0 so that tiles can be decoded on read-back.
	Format string
	// Verbose enables logging of I/O events.
	Verbose bool
}

// NewDiskTileStore creates a new disk-backed tile store.
// When MemoryLimitBytes > 0, a dedicated I/O goroutine is started that
// continuously writes encoded tiles to disk.
func NewDiskTileStore(cfg DiskTileStoreConfig) *DiskTileStore {
	cap := cfg.InitialCapacity
	if cap < 64 {
		cap = 64
	}
	dir := cfg.TempDir
	if dir == "" {
		dir = os.TempDir()
	}

	s := &DiskTileStore{
		tiles:    make(map[[3]int]*TileData, cap),
		index:    make(map[[3]int]diskEntry),
		tileSize: cfg.TileSize,
		format:   cfg.Format,
		dir:      dir,
		verbose:  cfg.Verbose,
	}

	// Start the dedicated I/O goroutine when disk spilling is enabled.
	if cfg.MemoryLimitBytes >= 0 && cfg.Format != "" {
		s.ioCh = make(chan ioRequest, 256)
		s.ioWg.Add(1)
		go s.ioLoop()
	}

	return s
}

// Put stores tile data. If disk spilling is enabled, non-uniform tiles are
// sent to the dedicated I/O goroutine for asynchronous disk storage; uniform
// tiles (single-color, 4 bytes) stay in memory permanently.
//
// The encoded parameter should contain the pre-encoded tile bytes (e.g., from
// the output encoder). When disk spilling is disabled, encoded is ignored and
// may be nil.
func (s *DiskTileStore) Put(z, x, y int, td *TileData, encoded []byte) {
	key := [3]int{z, x, y}
	mem := td.MemoryBytes()

	s.mu.Lock()
	s.tiles[key] = td
	s.mu.Unlock()

	s.memBytes.Add(mem)

	// Send to I/O goroutine for disk storage (skip uniform tiles — they're tiny).
	if s.ioCh != nil && !td.IsUniform() && len(encoded) > 0 {
		s.ioCh <- ioRequest{key: key, encoded: encoded, memBytes: mem}
	}
}

// Get retrieves tile data. Checks the in-memory store first, then falls back
// to reading encoded bytes from disk and decoding them.
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
	s.mu.RUnlock()
	if !onDisk {
		return nil
	}

	// Load the file handle (lock-free). ReadAt uses pread under the hood,
	// so concurrent reads are safe without any mutex.
	f := s.readFile.Load()
	if f == nil {
		return nil
	}

	// Read encoded bytes from disk.
	buf := make([]byte, de.length)
	_, err := f.ReadAt(buf, de.offset)
	if err != nil {
		return nil
	}

	// Decode encoded tile back to pixel data.
	return s.decodeFromDisk(buf)
}

// decodeFromDisk decodes encoded image bytes back to a TileData.
func (s *DiskTileStore) decodeFromDisk(data []byte) *TileData {
	img, err := encode.DecodeImage(data, s.format)
	if err != nil {
		return nil
	}

	// Fast path: already RGBA.
	if rgba, ok := img.(*image.RGBA); ok {
		return newTileData(rgba, s.tileSize)
	}

	// Fast path: grayscale image.
	if g, ok := img.(*image.Gray); ok {
		return &TileData{gray: g, tileSize: s.tileSize}
	}

	// General case: convert to RGBA (handles NRGBA from PNG, YCbCr from JPEG, etc.).
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return newTileData(rgba, s.tileSize)
}

// ioLoop is the dedicated I/O goroutine that continuously writes encoded
// tiles to the temp file and evicts them from memory.
//
// The file and write offset are local variables — only this goroutine writes.
// The file handle is published once via s.readFile so that concurrent Get()
// callers can issue ReadAt (pread) without any mutex involvement.
//
// Invariant: the tile is always in either s.tiles or s.index (or both during
// the brief window inside the critical section). A Get() will always find it.
func (s *DiskTileStore) ioLoop() {
	defer s.ioWg.Done()

	var file *os.File // owned by this goroutine for sequential writes
	var fileOff int64 // current write position (local, no sharing)

	for req := range s.ioCh {
		// Lazily create the temp file on first write.
		if file == nil {
			f, err := os.CreateTemp(s.dir, "pmtiles-tilestore-*.tmp")
			if err != nil {
				log.Printf("WARNING: disk tile store: failed to create temp file: %v (tile stays in memory)", err)
				continue
			}
			file = f
			s.readFile.Store(f) // publish for concurrent readers (lock-free)
			if s.verbose {
				log.Printf("Disk tile store: created temp file %s", f.Name())
			}
		}

		n, err := file.Write(req.encoded)
		if err != nil {
			log.Printf("WARNING: disk tile store: write error: %v (tile stays in memory)", err)
			continue
		}

		// Add to disk index and evict from memory atomically.
		// This ensures Get() always finds the tile in one place or the other.
		s.mu.Lock()
		s.index[req.key] = diskEntry{
			offset: fileOff,
			length: int32(n),
		}
		delete(s.tiles, req.key)
		s.mu.Unlock()

		fileOff += int64(n)
		s.memBytes.Add(-req.memBytes)
		s.totalDiskTiles++
		s.totalDiskBytes += int64(n)
	}
}

// Drain blocks until all pending I/O operations are complete.
// Must be called after all Put() calls are done and before any subsequent
// Get() calls on tiles that may have been spilled to disk (typically between
// zoom levels in the generator).
func (s *DiskTileStore) Drain() {
	if s.ioCh == nil {
		return
	}
	s.drainOnce.Do(func() {
		close(s.ioCh)
		s.ioWg.Wait()
		if s.verbose {
			log.Printf("Disk tile store: drained (%d tiles, %.1f MB encoded on disk)",
				s.totalDiskTiles, float64(s.totalDiskBytes)/(1024*1024))
		}
	})
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

// Close drains pending I/O and removes the temporary file.
// Call when the store is no longer needed.
// Safe to call after Drain() — the I/O goroutine has exited so the file
// is no longer being written to.
func (s *DiskTileStore) Close() {
	s.Drain()
	if f := s.readFile.Swap(nil); f != nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
}

// Stats returns a human-readable summary of the store's usage.
func (s *DiskTileStore) Stats() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("in-memory: %d tiles (%.1f MB), on-disk: %d tiles (%.1f MB encoded)",
		len(s.tiles), float64(s.memBytes.Load())/(1024*1024),
		len(s.index), float64(s.totalDiskBytes)/(1024*1024))
}

// WriteIndexTo writes the disk index to a writer for debugging/checkpointing.
// Format: count(uint32) + [key_z(int32) key_x(int32) key_y(int32) offset(int64) length(int32)] × count.
func (s *DiskTileStore) WriteIndexTo(w io.Writer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(s.index)))
	if _, err := w.Write(buf); err != nil {
		return err
	}

	entry := make([]byte, 4+4+4+8+4) // 24 bytes per entry
	for key, de := range s.index {
		binary.LittleEndian.PutUint32(entry[0:4], uint32(key[0]))  // z
		binary.LittleEndian.PutUint32(entry[4:8], uint32(key[1]))  // x
		binary.LittleEndian.PutUint32(entry[8:12], uint32(key[2])) // y
		binary.LittleEndian.PutUint64(entry[12:20], uint64(de.offset))
		binary.LittleEndian.PutUint32(entry[20:24], uint32(de.length))
		if _, err := w.Write(entry); err != nil {
			return err
		}
	}
	return nil
}

// TempFilePath returns the path to the temporary spill file, or "" if none exists.
func (s *DiskTileStore) TempFilePath() string {
	if f := s.readFile.Load(); f != nil {
		return f.Name()
	}
	return ""
}

// StoreInTargetDir creates the temp file in the same directory as the output
// file, which avoids cross-filesystem moves and keeps spill data near the output.
func StoreInTargetDir(outputPath string) string {
	return filepath.Dir(outputPath)
}
