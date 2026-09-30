// checkpmtiles validates a PMTiles v3 archive for structural correctness.
//
// Usage:
//
//	checkpmtiles [flags] <file.pmtiles | https://...>
//
// It checks header consistency, the 16 KiB root directory budget, every
// directory (root and leaves, at any depth), the absence of trailing bytes,
// the zoom range (MinZoom <= MaxZoom, and every addressed tile inside it) and
// that the archive addresses tiles. Exits with code 1 on
// any error and 2 on a usage error.
package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: checkpmtiles [flags] <file.pmtiles | https://...>\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Validate a PMTiles v3 archive: header, directories and zoom range.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Exits 1 if a check fails.\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Printf("checkpmtiles %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	target := flag.Arg(0)

	var src dataSource
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		src = &httpSource{url: target}
	} else {
		f, err := os.Open(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", target, err)
			os.Exit(1)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			fmt.Fprintf(os.Stderr, "stat %s: %v\n", target, err)
			os.Exit(1)
		}
		src = &fileSource{f: f, size: fi.Size()}
	}
	if !check(src) {
		fmt.Fprintf(os.Stderr, "\nValidation FAILED\n")
		os.Exit(1)
	}
	fmt.Printf("\nAll checks passed.\n")
}

// check prints the header and the results of every check, and reports
// whether all of them passed.
func check(src dataSource) bool {
	var failed bool
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
		failed = true
	}

	// Read header.
	headerBuf := src.readRange(0, pmtiles.HeaderSize)
	h, err := pmtiles.DeserializeHeader(headerBuf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse header: %v\n", err)
		return false
	}

	fmt.Printf("Header:\n")
	fmt.Printf("  RootDirOffset:     %d\n", h.RootDirOffset)
	fmt.Printf("  RootDirLength:     %d\n", h.RootDirLength)
	fmt.Printf("  MetadataOffset:    %d\n", h.MetadataOffset)
	fmt.Printf("  MetadataLength:    %d\n", h.MetadataLength)
	fmt.Printf("  LeafDirOffset:     %d\n", h.LeafDirOffset)
	fmt.Printf("  LeafDirLength:     %d\n", h.LeafDirLength)
	fmt.Printf("  TileDataOffset:    %d\n", h.TileDataOffset)
	fmt.Printf("  TileDataLength:    %d\n", h.TileDataLength)
	fmt.Printf("  NumAddressedTiles: %d\n", h.NumAddressedTiles)
	fmt.Printf("  NumTileEntries:    %d\n", h.NumTileEntries)
	fmt.Printf("  NumTileContents:   %d\n", h.NumTileContents)
	fmt.Printf("  Clustered:         %v\n", h.Clustered)
	fmt.Printf("  InternalCompr:     %d\n", h.InternalCompression)
	fmt.Printf("  TileCompr:         %d\n", h.TileCompression)
	fmt.Printf("  TileType:          %d (%s)\n", h.TileType, pmtiles.TileTypeString(h.TileType))
	fmt.Printf("  MinZoom:           %d\n", h.MinZoom)
	fmt.Printf("  MaxZoom:           %d\n", h.MaxZoom)
	fmt.Printf("  Bounds:            %.6f,%.6f,%.6f,%.6f\n", h.MinLon, h.MinLat, h.MaxLon, h.MaxLat)
	fmt.Printf("  Center:            %.6f,%.6f z%d\n", h.CenterLon, h.CenterLat, h.CenterZoom)

	// Consistency checks.
	fmt.Printf("\nConsistency checks:\n")
	// The spec lets every section but the header sit anywhere (pmmerge puts
	// tile data first), so check that they follow the header, do not
	// overlap, and that the last one ends the file.
	sections := layoutSections(h)
	expectedSize := uint64(pmtiles.HeaderSize)
	if overlap := sectionOverlap(sections); overlap != "" {
		fail("%s", overlap)
	} else {
		fmt.Printf("  Sections (%s): OK\n", sectionOrder(sections))
	}
	for _, s := range sections {
		expectedSize = max(expectedSize, s.off+s.len)
	}
	if h.MinZoom > h.MaxZoom {
		fail("min zoom %d is greater than max zoom %d", h.MinZoom, h.MaxZoom)
	} else {
		fmt.Printf("  Zoom range %d-%d: OK\n", h.MinZoom, h.MaxZoom)
	}

	// File size check (local files only).
	if fs, ok := src.(*fileSource); ok {
		if uint64(fs.size) != expectedSize {
			fail("file size %d != expected %d", fs.size, expectedSize)
		} else {
			fmt.Printf("  File size: OK (%d bytes)\n", fs.size)
		}
	} else {
		fmt.Printf("  Expected file size: %d\n", expectedSize)
	}

	// 16 KiB budget.
	initialFetch := h.RootDirOffset + h.RootDirLength
	if initialFetch > 16384 {
		fail("header + root directory = %d bytes, exceeds 16384-byte initial fetch budget", initialFetch)
	} else {
		fmt.Printf("  Header + RootDir: %d bytes (budget: 16384): OK\n", initialFetch)
	}

	// Directories. The leaf section is read in one range request and every
	// leaf pointer followed, to any depth.
	w := &dirWalk{h: h, fail: fail, seen: map[uint64]bool{}}
	if h.LeafDirLength > 0 {
		w.leaves = src.readRange(h.LeafDirOffset, h.LeafDirLength)
	}
	rootDirBuf := src.readRange(h.RootDirOffset, h.RootDirLength)
	fmt.Printf("\nRoot directory: %d bytes\n", len(rootDirBuf))
	if !w.dir("root directory", rootDirBuf, 0) {
		return false
	}
	fmt.Printf("\nDirectories: root + %d leaves (depth %d)\n", w.leafDirs, w.depth)
	fmt.Printf("  Tile entries:    %d\n", w.entries)
	fmt.Printf("  Addressed tiles: %d\n", w.addressed)
	if w.trailing == 0 {
		fmt.Printf("  Trailing bytes:  none\n")
	}
	if w.addressed == 0 {
		fail("the archive addresses no tiles")
	} else if h.NumAddressedTiles != 0 && w.addressed != h.NumAddressedTiles {
		fail("directories address %d tiles, header says %d", w.addressed, h.NumAddressedTiles)
	}
	// Clients only request zooms in the header's range, so tiles outside it
	// are unreachable.
	if w.addressed > 0 && h.MinZoom <= h.MaxZoom && h.MaxZoom < 31 {
		lo := pmtiles.ZXYToTileID(int(h.MinZoom), 0, 0)
		hi := pmtiles.ZXYToTileID(int(h.MaxZoom)+1, 0, 0)
		if w.minID < lo || w.maxID >= hi {
			minZ, _, _ := pmtiles.TileIDToZXY(w.minID)
			maxZ, _, _ := pmtiles.TileIDToZXY(w.maxID)
			fail("tiles span zoom %d-%d, outside the header's %d-%d", minZ, maxZ, h.MinZoom, h.MaxZoom)
		} else {
			fmt.Printf("  Tiles within zoom range: OK\n")
		}
	}

	return !failed
}

// dirWalk parses a directory and the leaf directories it points to.
type dirWalk struct {
	h         pmtiles.Header
	leaves    []byte          // the leaf directory section
	seen      map[uint64]bool // leaf offsets visited, against pointer loops
	fail      func(format string, args ...any)
	leafDirs  int
	depth     int
	entries   int
	trailing  int // directories with trailing bytes
	addressed uint64
	minID     uint64 // smallest and largest addressed tile ID
	maxID     uint64
}

// dir parses one directory and recurses into its leaves. It returns false
// when the directory cannot be parsed at all.
func (w *dirWalk) dir(name string, data []byte, depth int) bool {
	w.depth = max(w.depth, depth)
	entries, err := pmtiles.DeserializeDirectoryCompressed(data, w.h.InternalCompression)
	if err != nil {
		w.fail("parse %s: %v", name, err)
		return false
	}
	if depth == 0 && len(entries) > 0 {
		fmt.Printf("  Entries: %d\n", len(entries))
		fmt.Printf("  First: TileID=%d Offset=%d Length=%d RL=%d\n",
			entries[0].TileID, entries[0].Offset, entries[0].Length, entries[0].RunLength)
		last := entries[len(entries)-1]
		fmt.Printf("  Last:  TileID=%d Offset=%d Length=%d RL=%d\n", last.TileID, last.Offset, last.Length, last.RunLength)
	}
	if n, err := trailingBytes(data, w.h.InternalCompression); err != nil {
		w.fail("%s: %v", name, err)
	} else if n > 0 {
		w.fail("%s has %d trailing bytes", name, n)
		w.trailing++
	}
	for _, e := range entries {
		if e.RunLength > 0 {
			if w.entries == 0 || e.TileID < w.minID {
				w.minID = e.TileID
			}
			w.maxID = max(w.maxID, e.TileID+uint64(e.RunLength)-1)
			w.entries++
			w.addressed += uint64(e.RunLength)
			continue
		}
		// Leaf directory pointer: offset/length within the leaf section.
		leafName := fmt.Sprintf("leaf directory at offset %d", e.Offset)
		end := e.Offset + uint64(e.Length)
		switch {
		case w.seen[e.Offset]:
			w.fail("%s is referenced more than once", leafName)
		case end < e.Offset || end > uint64(len(w.leaves)):
			w.fail("%s (%d bytes) lies outside the %d-byte leaf section", leafName, e.Length, len(w.leaves))
		default:
			w.seen[e.Offset] = true
			w.leafDirs++
			w.dir(leafName, w.leaves[e.Offset:end], depth+1)
		}
	}
	return true
}

// dataSource abstracts reading byte ranges from a local file or HTTP URL.
type dataSource interface {
	readRange(offset, length uint64) []byte
}

type fileSource struct {
	f    *os.File
	size int64
}

func (fs *fileSource) readRange(offset, length uint64) []byte {
	buf := make([]byte, length)
	n, err := fs.f.ReadAt(buf, int64(offset))
	if err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "read at %d: %v\n", offset, err)
		os.Exit(1)
	}
	return buf[:n]
}

type httpSource struct {
	url string
}

func (hs *httpSource) readRange(offset, length uint64) []byte {
	end := offset + length - 1
	req, _ := http.NewRequest("GET", hs.url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch range %d-%d: %v\n", offset, end, err)
		os.Exit(1)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return data
}

// trailingBytes returns the number of bytes left in a directory after all
// the entries it declares.
func trailingBytes(data []byte, compression uint8) (int, error) {
	switch compression {
	case pmtiles.CompressionNone, pmtiles.CompressionUnknown:
	case pmtiles.CompressionGzip:
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return 0, err
		}
		data, err = io.ReadAll(gr)
		gr.Close()
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("internal compression %d is not supported", compression)
	}

	r := bytes.NewReader(data)
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, fmt.Errorf("reading entry count: %w", err)
	}
	// Tile ID deltas, run lengths, lengths and offsets.
	for i := uint64(0); i < 4*n; i++ {
		if _, err := binary.ReadUvarint(r); err != nil {
			return 0, fmt.Errorf("reading entry field %d: %w", i, err)
		}
	}
	return r.Len(), nil
}

// section is one of an archive's sections, by header offset and length.
type section struct {
	name     string
	off, len uint64
}

// layoutSections returns h's non-empty sections, sorted by offset.
func layoutSections(h pmtiles.Header) []section {
	var ss []section
	for _, s := range []section{
		{"root dir", h.RootDirOffset, h.RootDirLength},
		{"metadata", h.MetadataOffset, h.MetadataLength},
		{"leaf dirs", h.LeafDirOffset, h.LeafDirLength},
		{"tile data", h.TileDataOffset, h.TileDataLength},
	} {
		if s.len > 0 {
			ss = append(ss, s)
		}
	}
	slices.SortStableFunc(ss, func(a, b section) int { return cmp.Compare(a.off, b.off) })
	return ss
}

// sectionOverlap describes the first section that starts inside the header
// or the section before it, or returns "".
func sectionOverlap(ss []section) string {
	prev := section{"header", 0, pmtiles.HeaderSize}
	for _, s := range ss {
		if s.off < prev.off+prev.len {
			return fmt.Sprintf("%s at %d overlaps %s at %d-%d", s.name, s.off, prev.name, prev.off, prev.off+prev.len)
		}
		prev = s
	}
	return ""
}

func sectionOrder(ss []section) string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}
