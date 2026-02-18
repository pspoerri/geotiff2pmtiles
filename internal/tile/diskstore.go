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

// Estimated per-entry Go map overhead including bucket metadata, hash table
// load factor (~6.5 entries/bucket), and key/value storage. These are
// conservative estimates to keep the memory limit honest.
const (
	mapOverheadUniform = 128 // map[[3]int]*TileData entry + TileData struct
	mapOverheadIndex   = 64  // map[[3]int]diskEntry entry
)

// ioRequest is sent from Put() to the I/O goroutine for async disk writes.
type ioRequest struct {
	key      [3]int
	encoded  []byte // pre-encoded tile bytes (PNG/WebP/JPEG)
	memBytes int64  // memory to reclaim when evicted from in-memory store
}

// DiskTileStore is a concurrent-safe tile store that keeps tiles in memory
// in their encoded form and continuously spills them to disk via a dedicated
// I/O goroutine.
//
// Tiles are stored in memory as encoded bytes (PNG/WebP/JPEG) rather than
// raw pixels. This reduces the in-memory footprint by 5-25× (e.g., 10-50 KB
// encoded vs 64-256 KB raw), at the cost of a decode step on Get().
//
// When disk spilling is enabled, tiles are handled as follows:
//   - Put() stores encoded bytes in an in-memory map and sends them to a
//     buffered channel for the I/O goroutine.
//   - The I/O goroutine writes encoded bytes to a temporary file, adds an
//     index entry, and evicts the tile from memory — all without blocking
//     compute workers.
//   - Uniform tiles (single-color, 4 bytes each) are kept in memory as
//     compact TileData and never spilled.
//   - Get() checks uniform tiles first, then decodes in-memory encoded
//     bytes, then falls back to reading from disk.
//
// The continuous I/O design means disk writes are spread evenly over the
// processing time rather than occurring in large blocking flushes.
//
// The temp file is owned exclusively by the I/O goroutine for writing.
// Readers access it via an atomic pointer (lock-free ReadAt), so file I/O
// never contends with the map mutex.
type DiskTileStore struct {
	mu       sync.RWMutex
	uniforms map[[3]int]*TileData // uniform tiles (tiny, never spilled)
	encoded  map[[3]int][]byte    // encoded non-uniform tiles in memory
	index    map[[3]int]diskEntry // disk index (populated by I/O goroutine)
	tileSize int
	format   string // encoder format for decode path ("png", "jpeg", "webp", "terrarium")

	// Read-only file handle for Get(). Set once by ioLoop on first write,
	// never reassigned. Readers use atomic load + ReadAt (pread, no locking).
	readFile atomic.Pointer[os.File]
	dir      string // directory for temp files

	// Memory tracking.
	memBytes    atomic.Int64 // estimated bytes of in-memory encoded tile data
	mapOverhead atomic.Int64 // estimated bytes for map entry overhead (uniforms + index)
	memoryLimit int64        // max total memory before blocking Put(); 0 = no limit
	spillMu     sync.Mutex   // protects memCond waits (separate from mu to avoid contention)
	memCond     *sync.Cond   // signaled by ioLoop when memBytes decreases; nil when spilling is off

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

	// When disk spilling is enabled, the encoded map holds only a small
	// working set (bounded by MemoryLimitBytes). Pre-allocating for the
	// full tile count wastes enormous amounts of memory on empty hash
	// buckets (e.g. 112M entries × ~400 bytes/bucket ≈ 13 GB).
	encodedCap := cap
	uniformCap := cap / 4
	if cfg.MemoryLimitBytes > 0 {
		// Estimate: average encoded tile is ~20 KB (JPEG). Pre-allocate
		// for roughly the number of tiles that fit in the memory limit,
		// capped at a reasonable size to avoid huge upfront allocations.
		encodedCap = int(cfg.MemoryLimitBytes / (20 * 1024))
		if encodedCap > 1_000_000 {
			encodedCap = 1_000_000
		}
		if encodedCap < 1024 {
			encodedCap = 1024
		}
		uniformCap = 1024
	}

	s := &DiskTileStore{
		uniforms: make(map[[3]int]*TileData, uniformCap),
		encoded:  make(map[[3]int][]byte, encodedCap),
		index:    make(map[[3]int]diskEntry),
		tileSize: cfg.TileSize,
		format:   cfg.Format,
		dir:      dir,
		verbose:  cfg.Verbose,
	}

	// Start the dedicated I/O goroutine when disk spilling is enabled.
	if cfg.MemoryLimitBytes > 0 && cfg.Format != "" {
		s.memoryLimit = cfg.MemoryLimitBytes
		s.memCond = sync.NewCond(&s.spillMu)
		s.ioCh = make(chan ioRequest, 256)
		s.ioWg.Add(1)
		go s.ioLoop()
	}

	return s
}

// Put stores tile data. Non-uniform tiles are stored in their encoded form
// (PNG/WebP/JPEG) to reduce memory footprint. Uniform tiles (single-color,
// 4 bytes) are stored as compact TileData.
//
// The encoded parameter must contain the pre-encoded tile bytes for
// non-uniform tiles (e.g., from the output encoder).
// If disk spilling is enabled, non-uniform tiles are also sent to the
// dedicated I/O goroutine for eventual eviction from memory.
func (s *DiskTileStore) Put(z, x, y int, td *TileData, encoded []byte) {
	key := [3]int{z, x, y}

	// Uniform tiles are tiny (4 bytes) — keep as TileData, never spill.
	if td.IsUniform() {
		s.mu.Lock()
		s.uniforms[key] = td
		s.mu.Unlock()
		s.mapOverhead.Add(mapOverheadUniform)
		return
	}

	// Store encoded bytes in memory (much smaller than raw pixels:
	// ~10-50 KB encoded vs 64-256 KB raw for a 256×256 tile).
	mem := int64(len(encoded))
	s.mu.Lock()
	s.encoded[key] = encoded
	s.mu.Unlock()
	s.memBytes.Add(mem)

	// Send to I/O goroutine for eventual disk eviction (when enabled).
	if s.ioCh != nil && len(encoded) > 0 {
		s.ioCh <- ioRequest{key: key, encoded: encoded, memBytes: mem}
	}

	// Block if the memory limit is exceeded, providing backpressure to
	// workers so the I/O goroutine can catch up with disk eviction.
	// This must happen AFTER the ioCh send so the ioLoop always has work
	// to process — otherwise a single blocked worker with no pending I/O
	// would deadlock.
	if s.memCond != nil {
		s.spillMu.Lock()
		for s.totalMemory() > s.memoryLimit {
			s.memCond.Wait()
		}
		s.spillMu.Unlock()
	}
}

// Get retrieves tile data. Checks uniform tiles first, then decodes in-memory
// encoded bytes, then falls back to reading from disk.
// Returns nil if the tile is not present anywhere.
func (s *DiskTileStore) Get(z, x, y int) *TileData {
	key := [3]int{z, x, y}

	// Single lock acquisition for all in-memory lookups.
	s.mu.RLock()
	td := s.uniforms[key]
	enc := s.encoded[key]
	de, onDisk := s.index[key]
	s.mu.RUnlock()

	// Fast path: uniform tile (no decode needed).
	if td != nil {
		return td
	}

	// In-memory encoded tile: decode back to pixel data.
	if enc != nil {
		return s.decodeEncoded(enc)
	}

	// Slow path: read encoded bytes from disk.
	if !onDisk {
		return nil
	}

	// Load the file handle (lock-free). ReadAt uses pread under the hood,
	// so concurrent reads are safe without any mutex.
	f := s.readFile.Load()
	if f == nil {
		return nil
	}

	buf := make([]byte, de.length)
	_, err := f.ReadAt(buf, de.offset)
	if err != nil {
		return nil
	}

	return s.decodeEncoded(buf)
}

// decodeEncoded decodes encoded image bytes (from memory or disk) back to a TileData.
func (s *DiskTileStore) decodeEncoded(data []byte) *TileData {
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
	rgba := GetRGBA(bounds.Dx(), bounds.Dy())
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
// Invariant: a non-uniform tile is always in either s.encoded or s.index
// (or both during the brief window inside the critical section).
// A Get() will always find it.
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

		// Add to disk index and evict from in-memory encoded map atomically.
		// This ensures Get() always finds the tile in one place or the other.
		s.mu.Lock()
		s.index[req.key] = diskEntry{
			offset: fileOff,
			length: int32(n),
		}
		delete(s.encoded, req.key)
		s.mu.Unlock()

		fileOff += int64(n)
		s.memBytes.Add(-req.memBytes)
		s.mapOverhead.Add(mapOverheadIndex)
		s.totalDiskTiles++
		s.totalDiskBytes += int64(n)

		// Wake blocked Put() calls now that memory has been freed.
		if s.memCond != nil {
			s.memCond.Broadcast()
		}
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

// Len returns the total number of stored tiles (uniform + encoded in-memory + disk).
func (s *DiskTileStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.uniforms) + len(s.encoded) + len(s.index)
}

// totalMemory returns the estimated total memory usage: encoded tile data
// plus map entry overhead for uniforms and disk index.
func (s *DiskTileStore) totalMemory() int64 {
	return s.memBytes.Load() + s.mapOverhead.Load()
}

// MemoryBytes returns the estimated total in-memory usage.
func (s *DiskTileStore) MemoryBytes() int64 {
	return s.totalMemory()
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
	return fmt.Sprintf("in-memory: %d tiles (%d uniform, %d encoded, %.1f MB data + %.1f MB overhead), on-disk: %d tiles (%.1f MB)",
		len(s.uniforms)+len(s.encoded), len(s.uniforms), len(s.encoded),
		float64(s.memBytes.Load())/(1024*1024),
		float64(s.mapOverhead.Load())/(1024*1024),
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
