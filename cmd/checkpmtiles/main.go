// checkpmtiles validates a PMTiles v3 archive for structural correctness.
//
// Usage:
//
//	checkpmtiles <file.pmtiles | https://...>
//
// It checks header consistency, the 16 KiB root directory budget, directory
// deserialization, and absence of trailing bytes. Exits with code 1 on any error.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: checkpmtiles <file.pmtiles | https://...>\n")
		os.Exit(2)
	}
	target := os.Args[1]

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

	var failed bool
	fail := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
		failed = true
	}

	// Read header.
	headerBuf := src.readRange(0, pmtiles.HeaderSize)
	h, err := pmtiles.DeserializeHeader(headerBuf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse header: %v\n", err)
		os.Exit(1)
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
	if end := h.RootDirOffset + h.RootDirLength; end != h.MetadataOffset {
		fail("root dir end (%d) != metadata offset (%d)", end, h.MetadataOffset)
	} else {
		fmt.Printf("  RootDir end -> MetadataOffset: OK\n")
	}
	if end := h.MetadataOffset + h.MetadataLength; end != h.LeafDirOffset {
		fail("metadata end (%d) != leaf dir offset (%d)", end, h.LeafDirOffset)
	} else {
		fmt.Printf("  Metadata end -> LeafDirOffset: OK\n")
	}
	if end := h.LeafDirOffset + h.LeafDirLength; end != h.TileDataOffset {
		fail("leaf dir end (%d) != tile data offset (%d)", end, h.TileDataOffset)
	} else {
		fmt.Printf("  LeafDir end -> TileDataOffset: OK\n")
	}

	// File size check (local files only).
	expectedSize := h.TileDataOffset + h.TileDataLength
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

	// Root directory.
	rootDirBuf := src.readRange(h.RootDirOffset, h.RootDirLength)
	fmt.Printf("\nRoot directory: %d bytes compressed\n", len(rootDirBuf))

	entries, err := pmtiles.DeserializeDirectory(rootDirBuf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse root dir: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Entries: %d\n", len(entries))
	if len(entries) > 0 {
		fmt.Printf("  First: TileID=%d Offset=%d Length=%d RL=%d\n",
			entries[0].TileID, entries[0].Offset, entries[0].Length, entries[0].RunLength)
		fmt.Printf("  Last:  TileID=%d Offset=%d Length=%d RL=%d\n",
			entries[len(entries)-1].TileID, entries[len(entries)-1].Offset, entries[len(entries)-1].Length, entries[len(entries)-1].RunLength)
	}

	allLeaf := true
	for _, e := range entries {
		if e.RunLength != 0 {
			allLeaf = false
			break
		}
	}
	if allLeaf && len(entries) > 0 {
		fmt.Printf("  Type: leaf pointers (%d leaves)\n", len(entries))
	} else {
		fmt.Printf("  Type: tile entries\n")
	}

	trailing := checkTrailingBytes(rootDirBuf)
	if trailing > 0 {
		fail("root directory has %d trailing bytes", trailing)
	}

	// First leaf directory (if applicable).
	if allLeaf && len(entries) > 0 {
		leafStart := h.LeafDirOffset + entries[0].Offset
		leafBuf := src.readRange(leafStart, uint64(entries[0].Length))

		leafEntries, err := pmtiles.DeserializeDirectory(leafBuf)
		if err != nil {
			fail("parse first leaf: %v", err)
		} else {
			fmt.Printf("\nFirst leaf directory:\n")
			fmt.Printf("  Entries: %d\n", len(leafEntries))
			if len(leafEntries) > 0 {
				fmt.Printf("  First: TileID=%d Offset=%d Length=%d RL=%d\n",
					leafEntries[0].TileID, leafEntries[0].Offset, leafEntries[0].Length, leafEntries[0].RunLength)
				fmt.Printf("  Last:  TileID=%d Offset=%d Length=%d RL=%d\n",
					leafEntries[len(leafEntries)-1].TileID, leafEntries[len(leafEntries)-1].Offset, leafEntries[len(leafEntries)-1].Length, leafEntries[len(leafEntries)-1].RunLength)
			}
			trailing = checkTrailingBytes(leafBuf)
			if trailing > 0 {
				fail("first leaf directory has %d trailing bytes", trailing)
			}
		}
	}

	if failed {
		fmt.Fprintf(os.Stderr, "\nValidation FAILED\n")
		os.Exit(1)
	}
	fmt.Printf("\nAll checks passed.\n")
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

// checkTrailingBytes decompresses a gzip directory and returns the number
// of bytes remaining after parsing all declared entries. Returns 0 if clean.
func checkTrailingBytes(gzipData []byte) int {
	gr, err := gzip.NewReader(bytes.NewReader(gzipData))
	if err != nil {
		fmt.Printf("  gzip error: %v\n", err)
		return -1
	}
	raw, err := io.ReadAll(gr)
	gr.Close()
	if err != nil {
		fmt.Printf("  decompress error: %v\n", err)
		return -1
	}

	r := bytes.NewReader(raw)
	n, err := binary.ReadUvarint(r)
	if err != nil {
		fmt.Printf("  error reading entry count: %v\n", err)
		return -1
	}

	for i := uint64(0); i < n; i++ {
		if _, err := binary.ReadUvarint(r); err != nil {
			fmt.Printf("  error reading tile ID delta %d: %v\n", i, err)
			return -1
		}
	}
	for i := uint64(0); i < n; i++ {
		if _, err := binary.ReadUvarint(r); err != nil {
			fmt.Printf("  error reading run length %d: %v\n", i, err)
			return -1
		}
	}
	for i := uint64(0); i < n; i++ {
		if _, err := binary.ReadUvarint(r); err != nil {
			fmt.Printf("  error reading length %d: %v\n", i, err)
			return -1
		}
	}
	for i := uint64(0); i < n; i++ {
		if _, err := binary.ReadUvarint(r); err != nil {
			fmt.Printf("  error reading offset %d: %v\n", i, err)
			return -1
		}
	}

	remaining := r.Len()
	if remaining > 0 {
		fmt.Printf("  Trailing bytes: %d\n", remaining)
	} else {
		fmt.Printf("  Directory: clean (no trailing bytes)\n")
	}
	return remaining
}
