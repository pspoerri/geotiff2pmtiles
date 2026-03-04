// pmheader patches PMTiles v3 header fields and metadata without touching tile data.
//
// Usage:
//
//	pmheader [flags] input.pmtiles [output.pmtiles]
//
// When output is omitted, changes are applied in-place.
// Header-only patches (no metadata changes) are applied with a 127-byte seek-and-write.
// Metadata changes rewrite the pre-tile sections and stream the tile data verbatim.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// optInt is an optional integer flag (distinguishes "not set" from zero).
type optInt struct {
	val int
	set bool
}

func (o *optInt) String() string { return strconv.Itoa(o.val) }
func (o *optInt) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	o.val = v
	o.set = true
	return nil
}

// optFloat32 is an optional float32 flag.
type optFloat32 struct {
	val float32
	set bool
}

func (o *optFloat32) String() string { return strconv.FormatFloat(float64(o.val), 'f', 7, 32) }
func (o *optFloat32) Set(s string) error {
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return err
	}
	o.val = float32(v)
	o.set = true
	return nil
}

// repeatFlag collects multiple values for a repeatable flag (e.g. --set k=v --set k2=v2).
type repeatFlag []string

func (r *repeatFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func main() {
	showFlag := flag.Bool("show", false, "Print header and metadata, then exit")
	verboseFlag := flag.Bool("verbose", false, "Print a summary of changes")

	var (
		minZoom    optInt
		maxZoom    optInt
		centerZoom optInt
		minLon     optFloat32
		minLat     optFloat32
		maxLon     optFloat32
		maxLat     optFloat32
		centerLon  optFloat32
		centerLat  optFloat32
	)

	var tileTypeStr string
	var setKV repeatFlag
	var unsetKeys repeatFlag
	var metadataFile string

	flag.Var(&minZoom, "min-zoom", "Override MinZoom (0–30)")
	flag.Var(&maxZoom, "max-zoom", "Override MaxZoom (0–30)")
	flag.Var(&centerZoom, "center-zoom", "Override CenterZoom (0–30)")
	flag.Var(&minLon, "min-lon", "Override MinLon in decimal degrees")
	flag.Var(&minLat, "min-lat", "Override MinLat in decimal degrees")
	flag.Var(&maxLon, "max-lon", "Override MaxLon in decimal degrees")
	flag.Var(&maxLat, "max-lat", "Override MaxLat in decimal degrees")
	flag.Var(&centerLon, "center-lon", "Override CenterLon in decimal degrees")
	flag.Var(&centerLat, "center-lat", "Override CenterLat in decimal degrees")
	flag.StringVar(&tileTypeStr, "tile-type", "", "Override tile type: png, jpeg, webp, mvt")
	rebuildDirsFlag := flag.Bool("rebuild-dirs", false, "Rebuild directory structure to fit 16 KiB budget (fixes oversized root)")
	flag.Var(&setKV, "set", "Set metadata JSON key=value (repeatable)")
	flag.Var(&unsetKeys, "unset", "Remove metadata JSON key (repeatable)")
	flag.StringVar(&metadataFile, "metadata-file", "", "Replace entire metadata with JSON file content")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "pmheader %s (%s, %s)\n\n", version, commit, buildDate)
		fmt.Fprintf(os.Stderr, "Usage: pmheader [flags] input.pmtiles [output.pmtiles]\n\n")
		fmt.Fprintf(os.Stderr, "Patch PMTiles v3 header fields and metadata without re-encoding tile data.\n")
		fmt.Fprintf(os.Stderr, "When output is omitted, changes are applied in-place.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  pmheader --show map.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --min-zoom 5 --max-zoom 14 map.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --center-zoom 8 --center-lon 8.3 --center-lat 46.9 map.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --set attribution='© OpenStreetMap' map.pmtiles patched.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --tile-type webp map.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --rebuild-dirs map.pmtiles fixed.pmtiles\n")
		fmt.Fprintf(os.Stderr, "  pmheader --unset description --set name=MyMap map.pmtiles\n")
	}

	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	inputPath := args[0]
	outputPath := ""
	if len(args) >= 2 {
		outputPath = args[1]
	}

	opts := patchOptions{
		showOnly:     *showFlag,
		verbose:      *verboseFlag,
		rebuildDirs:  *rebuildDirsFlag,
		minZoom:      minZoom,
		maxZoom:      maxZoom,
		centerZoom:   centerZoom,
		minLon:       minLon,
		minLat:       minLat,
		maxLon:       maxLon,
		maxLat:       maxLat,
		centerLon:    centerLon,
		centerLat:    centerLat,
		tileTypeStr:  tileTypeStr,
		setKV:        []string(setKV),
		unsetKeys:    []string(unsetKeys),
		metadataFile: metadataFile,
	}

	if err := patch(inputPath, outputPath, opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type patchOptions struct {
	showOnly    bool
	verbose     bool
	rebuildDirs bool
	minZoom     optInt
	maxZoom      optInt
	centerZoom   optInt
	minLon       optFloat32
	minLat       optFloat32
	maxLon       optFloat32
	maxLat       optFloat32
	centerLon    optFloat32
	centerLat    optFloat32
	tileTypeStr  string
	setKV        []string
	unsetKeys    []string
	metadataFile string
}

func patch(inputPath, outputPath string, opts patchOptions) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", inputPath, err)
	}
	defer f.Close()

	// Read 127-byte header.
	headerBuf := make([]byte, pmtiles.HeaderSize)
	if _, err := io.ReadFull(f, headerBuf); err != nil {
		return fmt.Errorf("reading header: %w", err)
	}
	srcHdr, err := pmtiles.DeserializeHeader(headerBuf)
	if err != nil {
		return err
	}

	if opts.showOnly {
		return showHeader(f, srcHdr)
	}

	hasHeaderChanges := opts.minZoom.set || opts.maxZoom.set || opts.centerZoom.set ||
		opts.minLon.set || opts.minLat.set || opts.maxLon.set || opts.maxLat.set ||
		opts.centerLon.set || opts.centerLat.set || opts.tileTypeStr != ""
	hasMetaChanges := len(opts.setKV) > 0 || len(opts.unsetKeys) > 0 || opts.metadataFile != ""

	if !hasHeaderChanges && !hasMetaChanges && !opts.rebuildDirs {
		return fmt.Errorf("no changes specified; use --show to inspect the file")
	}

	// Build patched header.
	dstHdr := srcHdr
	if opts.minZoom.set {
		dstHdr.MinZoom = uint8(opts.minZoom.val)
	}
	if opts.maxZoom.set {
		dstHdr.MaxZoom = uint8(opts.maxZoom.val)
	}
	if opts.centerZoom.set {
		dstHdr.CenterZoom = uint8(opts.centerZoom.val)
	}
	if opts.minLon.set {
		dstHdr.MinLon = opts.minLon.val
	}
	if opts.minLat.set {
		dstHdr.MinLat = opts.minLat.val
	}
	if opts.maxLon.set {
		dstHdr.MaxLon = opts.maxLon.val
	}
	if opts.maxLat.set {
		dstHdr.MaxLat = opts.maxLat.val
	}
	if opts.centerLon.set {
		dstHdr.CenterLon = opts.centerLon.val
	}
	if opts.centerLat.set {
		dstHdr.CenterLat = opts.centerLat.val
	}
	if opts.tileTypeStr != "" {
		tt, err := parseTileType(opts.tileTypeStr)
		if err != nil {
			return err
		}
		dstHdr.TileType = tt
	}

	// Fast path: header-only in-place patch — seek to byte 0 and write 127 bytes.
	if !hasMetaChanges && !opts.rebuildDirs && outputPath == "" {
		fw, err := os.OpenFile(inputPath, os.O_RDWR, 0o666)
		if err != nil {
			return fmt.Errorf("opening for in-place write: %w", err)
		}
		defer fw.Close()
		if _, err := fw.WriteAt(dstHdr.Serialize(), 0); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		if opts.verbose {
			fmt.Printf("patched in place: %s\n", inputPath)
			printDiff(srcHdr, dstHdr, false, 0, 0)
		}
		return nil
	}

	// Full rewrite: read directory and metadata sections, update metadata if needed,
	// recalculate offsets, write new header+dirs+metadata, then stream tile data.
	srcMetaBytes, err := readSection(f, int64(srcHdr.MetadataOffset), srcHdr.MetadataLength)
	if err != nil {
		return fmt.Errorf("reading metadata section: %w", err)
	}

	dstMetaBytes := srcMetaBytes
	if hasMetaChanges {
		dstMetaBytes, err = patchMetadata(srcMetaBytes, opts.setKV, opts.unsetKeys, opts.metadataFile)
		if err != nil {
			return fmt.Errorf("patching metadata: %w", err)
		}
	}

	var rootDirBytes, leafDirBytes []byte

	if opts.rebuildDirs {
		// Resolve all directory entries (root + leaves) and rebuild to fit 16 KiB budget.
		allEntries, err := readAllEntries(f, srcHdr)
		if err != nil {
			return fmt.Errorf("reading directory entries: %w", err)
		}
		if opts.verbose {
			fmt.Printf("rebuilding directories: %d tile entries\n", len(allEntries))
		}
		var numOptimized int
		rootDirBytes, leafDirBytes, numOptimized, err = pmtiles.BuildDirectory(allEntries)
		if err != nil {
			return fmt.Errorf("rebuilding directories: %w", err)
		}
		dstHdr.NumTileEntries = uint64(numOptimized)
	} else {
		rootDirBytes, err = readSection(f, int64(srcHdr.RootDirOffset), srcHdr.RootDirLength)
		if err != nil {
			return fmt.Errorf("reading root directory: %w", err)
		}
		leafDirBytes, err = readSection(f, int64(srcHdr.LeafDirOffset), srcHdr.LeafDirLength)
		if err != nil {
			return fmt.Errorf("reading leaf directories: %w", err)
		}
	}

	// Recalculate offsets for canonical layout.
	dstHdr.RootDirOffset = uint64(pmtiles.HeaderSize)
	dstHdr.RootDirLength = uint64(len(rootDirBytes))
	dstHdr.MetadataOffset = dstHdr.RootDirOffset + dstHdr.RootDirLength
	dstHdr.MetadataLength = uint64(len(dstMetaBytes))
	dstHdr.LeafDirOffset = dstHdr.MetadataOffset + dstHdr.MetadataLength
	dstHdr.LeafDirLength = uint64(len(leafDirBytes))
	dstHdr.TileDataOffset = dstHdr.LeafDirOffset + dstHdr.LeafDirLength
	// TileDataLength and tile counts remain unchanged.

	// Determine where to write.
	inPlace := outputPath == "" || outputPath == inputPath
	writePath := outputPath
	if inPlace {
		tmp, err := os.CreateTemp(filepath.Dir(inputPath), ".pmheader-*.pmtiles")
		if err != nil {
			return fmt.Errorf("creating temp file: %w", err)
		}
		writePath = tmp.Name()
		tmp.Close()
	}

	if err := writeOutput(writePath, dstHdr, rootDirBytes, dstMetaBytes, leafDirBytes, f, srcHdr.TileDataOffset, srcHdr.TileDataLength); err != nil {
		if inPlace {
			os.Remove(writePath)
		}
		return err
	}

	if inPlace {
		if err := os.Rename(writePath, inputPath); err != nil {
			os.Remove(writePath)
			return fmt.Errorf("replacing input file: %w", err)
		}
	}

	if opts.verbose {
		dest := outputPath
		if dest == "" {
			dest = inputPath + " (in place)"
		}
		fmt.Printf("wrote %s\n", dest)
		printDiff(srcHdr, dstHdr, hasMetaChanges, len(srcMetaBytes), len(dstMetaBytes))
	}
	return nil
}

// writeOutput writes header + directories + metadata + tile data (streamed) to path.
func writeOutput(path string, hdr pmtiles.Header, rootDir, metadata, leafDirs []byte, src *os.File, tileDataOffset, tileDataLength uint64) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating output %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(hdr.Serialize()); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := f.Write(rootDir); err != nil {
		return fmt.Errorf("writing root directory: %w", err)
	}
	if _, err := f.Write(metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	if _, err := f.Write(leafDirs); err != nil {
		return fmt.Errorf("writing leaf directories: %w", err)
	}
	if tileDataLength > 0 {
		if _, err := src.Seek(int64(tileDataOffset), io.SeekStart); err != nil {
			return fmt.Errorf("seeking to tile data: %w", err)
		}
		if n, err := io.CopyN(f, src, int64(tileDataLength)); err != nil {
			return fmt.Errorf("copying tile data (copied %d of %d bytes): %w", n, tileDataLength, err)
		}
	}
	return nil
}

// readAllEntries reads the root directory and all leaf directories, returning
// a flat slice of tile entries (with offsets relative to tile data section).
func readAllEntries(f *os.File, h pmtiles.Header) ([]pmtiles.Entry, error) {
	rootDirData, err := readSection(f, int64(h.RootDirOffset), h.RootDirLength)
	if err != nil {
		return nil, fmt.Errorf("reading root directory: %w", err)
	}
	rootEntries, err := pmtiles.DeserializeDirectory(rootDirData)
	if err != nil {
		return nil, fmt.Errorf("parsing root directory: %w", err)
	}

	var allEntries []pmtiles.Entry
	for _, e := range rootEntries {
		if e.RunLength == 0 {
			// Leaf directory pointer: offset/length relative to leaf dir section.
			leafData, err := readSection(f, int64(h.LeafDirOffset+e.Offset), uint64(e.Length))
			if err != nil {
				return nil, fmt.Errorf("reading leaf directory at offset %d: %w", e.Offset, err)
			}
			leafEntries, err := pmtiles.DeserializeDirectory(leafData)
			if err != nil {
				return nil, fmt.Errorf("parsing leaf directory: %w", err)
			}
			allEntries = append(allEntries, leafEntries...)
		} else {
			allEntries = append(allEntries, e)
		}
	}
	return allEntries, nil
}

// readSection reads length bytes from f at offset. Returns nil if length == 0.
func readSection(f *os.File, offset int64, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, err
	}
	return buf, nil
}

// patchMetadata decompresses the gzip metadata, applies key-value mutations, and re-gzips.
func patchMetadata(srcGzip []byte, setKV, unsetKeys []string, metadataFile string) ([]byte, error) {
	var meta map[string]interface{}

	if metadataFile != "" {
		data, err := os.ReadFile(metadataFile)
		if err != nil {
			return nil, fmt.Errorf("reading metadata file: %w", err)
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parsing metadata file: %w", err)
		}
	} else {
		if len(srcGzip) > 0 {
			gz, err := gzip.NewReader(bytes.NewReader(srcGzip))
			if err != nil {
				return nil, fmt.Errorf("decompressing metadata: %w", err)
			}
			jsonData, err := io.ReadAll(gz)
			gz.Close()
			if err != nil {
				return nil, fmt.Errorf("reading decompressed metadata: %w", err)
			}
			if err := json.Unmarshal(jsonData, &meta); err != nil {
				return nil, fmt.Errorf("parsing metadata JSON: %w", err)
			}
		} else {
			meta = make(map[string]interface{})
		}
	}

	for _, kv := range setKV {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			return nil, fmt.Errorf("--set %q: missing '=' separator", kv)
		}
		key, raw := kv[:idx], kv[idx+1:]
		// Try JSON decode first (handles numbers, booleans, objects, arrays).
		// Fall back to plain string so --set name=My Map works without quoting.
		var val interface{}
		if err := json.Unmarshal([]byte(raw), &val); err != nil {
			val = raw
		}
		meta[key] = val
	}

	for _, key := range unsetKeys {
		delete(meta, key)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := json.NewEncoder(gz)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(meta); err != nil {
		return nil, fmt.Errorf("encoding metadata JSON: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzipping metadata: %w", err)
	}
	return buf.Bytes(), nil
}

// showHeader prints the header and metadata in human-readable form.
func showHeader(f *os.File, h pmtiles.Header) error {
	fi, _ := f.Stat()
	fmt.Printf("PMTiles v3\n")
	fmt.Printf("  File size:    %s\n", fmtBytes(fi.Size()))
	fmt.Printf("  Tile type:    %s\n", pmtiles.TileTypeString(h.TileType))
	fmt.Printf("  Zoom:         %d – %d  (center zoom %d)\n", h.MinZoom, h.MaxZoom, h.CenterZoom)
	fmt.Printf("  Bounds:       %.7f, %.7f, %.7f, %.7f\n", h.MinLon, h.MinLat, h.MaxLon, h.MaxLat)
	fmt.Printf("  Center:       %.7f, %.7f\n", h.CenterLon, h.CenterLat)
	fmt.Printf("  Tiles:        %d addressed  %d entries  %d unique\n",
		h.NumAddressedTiles, h.NumTileEntries, h.NumTileContents)
	fmt.Printf("  Clustered:    %v\n", h.Clustered)
	fmt.Printf("  Int compress: %s\n", fmtCompression(h.InternalCompression))
	fmt.Printf("  Tile data:    offset=%d  size=%s\n", h.TileDataOffset, fmtBytes(int64(h.TileDataLength)))

	if h.MetadataLength > 0 {
		metaBytes, err := readSection(f, int64(h.MetadataOffset), h.MetadataLength)
		if err != nil {
			return fmt.Errorf("reading metadata: %w", err)
		}
		gz, err := gzip.NewReader(bytes.NewReader(metaBytes))
		if err != nil {
			return fmt.Errorf("decompressing metadata: %w", err)
		}
		jsonData, err := io.ReadAll(gz)
		gz.Close()
		if err != nil {
			return fmt.Errorf("reading metadata: %w", err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(jsonData, &meta); err != nil {
			return fmt.Errorf("parsing metadata: %w", err)
		}
		pretty, _ := json.MarshalIndent(meta, "  ", "  ")
		fmt.Printf("\nMetadata:\n  %s\n", pretty)
	}
	return nil
}

// printDiff logs what changed between src and dst headers.
func printDiff(src, dst pmtiles.Header, metaChanged bool, srcMetaLen, dstMetaLen int) {
	if src.MinZoom != dst.MinZoom {
		fmt.Printf("  MinZoom:    %d → %d\n", src.MinZoom, dst.MinZoom)
	}
	if src.MaxZoom != dst.MaxZoom {
		fmt.Printf("  MaxZoom:    %d → %d\n", src.MaxZoom, dst.MaxZoom)
	}
	if src.CenterZoom != dst.CenterZoom {
		fmt.Printf("  CenterZoom: %d → %d\n", src.CenterZoom, dst.CenterZoom)
	}
	if src.MinLon != dst.MinLon {
		fmt.Printf("  MinLon:     %.7f → %.7f\n", src.MinLon, dst.MinLon)
	}
	if src.MinLat != dst.MinLat {
		fmt.Printf("  MinLat:     %.7f → %.7f\n", src.MinLat, dst.MinLat)
	}
	if src.MaxLon != dst.MaxLon {
		fmt.Printf("  MaxLon:     %.7f → %.7f\n", src.MaxLon, dst.MaxLon)
	}
	if src.MaxLat != dst.MaxLat {
		fmt.Printf("  MaxLat:     %.7f → %.7f\n", src.MaxLat, dst.MaxLat)
	}
	if src.CenterLon != dst.CenterLon {
		fmt.Printf("  CenterLon:  %.7f → %.7f\n", src.CenterLon, dst.CenterLon)
	}
	if src.CenterLat != dst.CenterLat {
		fmt.Printf("  CenterLat:  %.7f → %.7f\n", src.CenterLat, dst.CenterLat)
	}
	if src.TileType != dst.TileType {
		fmt.Printf("  TileType:   %s → %s\n", pmtiles.TileTypeString(src.TileType), pmtiles.TileTypeString(dst.TileType))
	}
	if metaChanged {
		fmt.Printf("  Metadata:   %s → %s\n", fmtBytes(int64(srcMetaLen)), fmtBytes(int64(dstMetaLen)))
	}
}

func parseTileType(s string) (uint8, error) {
	switch strings.ToLower(s) {
	case "png":
		return pmtiles.TileTypePNG, nil
	case "jpeg", "jpg":
		return pmtiles.TileTypeJPEG, nil
	case "webp":
		return pmtiles.TileTypeWebP, nil
	case "mvt":
		return pmtiles.TileTypeMVT, nil
	default:
		return 0, fmt.Errorf("unknown tile type %q (want: png, jpeg, webp, mvt)", s)
	}
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func fmtCompression(c uint8) string {
	switch c {
	case pmtiles.CompressionNone:
		return "none"
	case pmtiles.CompressionGzip:
		return "gzip"
	case pmtiles.CompressionBrotli:
		return "brotli"
	case pmtiles.CompressionZstd:
		return "zstd"
	default:
		return fmt.Sprintf("unknown(%d)", c)
	}
}
