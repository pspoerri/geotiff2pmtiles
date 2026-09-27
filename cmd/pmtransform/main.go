// pmtransform writes a new PMTiles archive from an existing one: it changes
// the tile format or zoom range, recolours nodata, fills missing tiles or
// rebuilds the lower levels. Tiles are copied byte for byte unless a flag
// needs them decoded.
//
// Usage:
//
//	pmtransform [flags] <input.pmtiles> <output.pmtiles>
package main

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/pspoerri/geotiff2pmtiles/internal/cli"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
	"github.com/pspoerri/geotiff2pmtiles/internal/tile"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	var (
		format      string
		quality     int
		minZoom     int
		maxZoom     int
		showVersion bool
		tileSize    int
		concurrency int
		verbose     bool
		resampling  string
		memLimitMB  int
		noSpill     bool
		nodataColor string
		fillMissing string
		fillColor   string
		rebuild     bool
		terrarium   bool
		attribution string
		layerType   string
		tmpDirFlag  string
	)

	flag.StringVar(&format, "format", "", "Target tile encoding: jpeg, png, webp (default: keep source format)")
	flag.IntVar(&quality, "quality", 85, "JPEG/WebP quality 1-100 (ignored for png)")
	flag.IntVar(&minZoom, "min-zoom", -1, "Minimum zoom level; -1 = source min zoom, extended down to the zoom where all data fits in one tile (added levels are downsampled from the source's lowest level)")
	flag.IntVar(&maxZoom, "max-zoom", -1, "Maximum zoom level, at most the source max zoom; -1 = keep source")
	flag.IntVar(&tileSize, "tile-size", -1, "Output tile size in pixels; must equal the source tile size (resizing is not supported); -1 = keep source")
	flag.IntVar(&concurrency, "concurrency", runtime.NumCPU(), "Number of parallel workers (>= 1)")
	flag.StringVar(&resampling, "resampling", "bicubic", "Downsampling method for rebuilt or added zoom levels: lanczos, bicubic, bilinear, nearest, mode")
	flag.BoolVar(&verbose, "verbose", false, "Verbose progress output")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	profiles := cli.RegisterProfileFlags(flag.CommandLine)
	flag.IntVar(&memLimitMB, "mem-limit", 0, "MB of encoded tiles allowed to queue for the spill file before workers pause while levels are rebuilt or added (0 = auto: 90% of RAM, or of the cgroup limit on Linux, minus current usage minus 2 GB, never below 256 MB; spilling is always on unless --no-spill)")
	flag.BoolVar(&noSpill, "no-spill", false, "Disable disk spilling (keep all tiles in memory)")
	flag.StringVar(&tmpDirFlag, "tmp-dir", "", "Directory for temporary files, about 2x the output size at peak (default: the output file's directory)")
	flag.StringVar(&nodataColor, "nodata-color", "none", "RGBA color, e.g. \"0,0,0,255\" or \"#000000ff\", that replaces transparent pixels; forces re-encoding. none = keep them")
	flag.StringVar(&fillMissing, "fill-missing", "none", "RGBA color of the solid tiles written at tile positions inside the bounds that the source lacks; does not force re-encoding. none = leave them absent")
	flag.StringVar(&fillColor, "fill-color", "", "deprecated: sets both --nodata-color and --fill-missing")
	flag.BoolVar(&rebuild, "rebuild", false, "Rebuild every level below max zoom by downsampling (needed to apply --resampling to existing levels)")
	flag.BoolVar(&terrarium, "terrarium", false, "Treat tiles as terrarium-encoded elevations so rebuild downsamples in elevation space (auto-detected from archive metadata)")
	flag.StringVar(&attribution, "attribution", "", "Attribution string for data sources (default: keep source)")
	flag.StringVar(&layerType, "type", "", "Layer type: baselayer, overlay (default: keep source)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: pmtransform [flags] <input.pmtiles> <output.pmtiles>\n\n")
		fmt.Fprintf(os.Stderr, "Transform an existing PMTiles archive: change format, zoom levels,\n")
		fmt.Fprintf(os.Stderr, "resampling, or fill empty tiles. Always creates a new file.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if showVersion {
		fmt.Printf("pmtransform %s (commit %s, built %s)\n", version, commit, buildDate)
		fmt.Printf("formats: %s\n", encode.Formats())
		os.Exit(0)
	}

	stopProfiles, err := profiles.Start()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := stopProfiles(); err != nil {
			log.Print(err)
		}
	}()

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}

	inputPath := args[0]
	outputPath := args[1]

	format = strings.ToLower(format)
	if format == "jpg" {
		format = "jpeg"
	}
	if quality < 1 || quality > 100 {
		log.Fatalf("--quality must be 1-100, got %d", quality)
	}
	if concurrency < 1 {
		log.Fatalf("--concurrency must be >= 1, got %d", concurrency)
	}

	if !strings.EqualFold(filepath.Ext(inputPath), ".pmtiles") {
		log.Fatal("Input file must have .pmtiles extension")
	}
	if !strings.EqualFold(filepath.Ext(outputPath), ".pmtiles") {
		log.Fatal("Output file must have .pmtiles extension")
	}
	// Compare file identity, not path strings: on Windows "map.pmtiles" and
	// "MAP.PMTILES" name the same file, and overwriting the source mid-read
	// would corrupt it.
	if inputPath == outputPath || sameFile(inputPath, outputPath) {
		log.Fatal("Input and output paths must be different")
	}

	// Open source PMTiles.
	start := time.Now()
	reader, err := pmtiles.OpenReader(inputPath)
	if err != nil {
		log.Fatalf("Opening input: %v", err)
	}
	defer reader.Close()

	srcHeader := reader.Header()
	srcFormat := pmtiles.TileTypeString(srcHeader.TileType)

	// Read source metadata for description/attribution/type propagation.
	var srcDescription, srcAttribution, srcType, srcEncoding string
	var srcExtra map[string]any
	if srcMeta, err := reader.ReadMetadata(); err != nil {
		// Metadata controls pixel semantics (terrarium detection), so a read
		// failure must be visible: a silent fallback to RGBA downsampling
		// would corrupt elevation archives.
		log.Printf("Warning: could not read source metadata: %v", err)
		log.Printf("If this archive contains terrarium elevation tiles, pass --terrarium")
	} else if srcMeta != nil {
		if v, ok := srcMeta["description"].(string); ok {
			srcDescription = v
		}
		if v, ok := srcMeta["attribution"].(string); ok {
			srcAttribution = v
		}
		if v, ok := srcMeta["type"].(string); ok {
			srcType = v
		}
		if v, ok := srcMeta["encoding"].(string); ok {
			srcEncoding = v
			terrarium = terrarium || v == "terrarium"
		}
		srcExtra = passthroughMetadata(srcMeta)
	}

	// Carry forward source attribution and type when not explicitly overridden.
	if attribution == "" {
		attribution = srcAttribution
	}
	if layerType == "" {
		layerType = srcType
	}

	if verbose {
		log.Printf("Opened %s: %d tiles, zoom %d-%d, format %s, bounds [%.4f,%.4f,%.4f,%.4f]",
			inputPath, reader.NumTiles(),
			srcHeader.MinZoom, srcHeader.MaxZoom, srcFormat,
			srcHeader.MinLon, srcHeader.MinLat, srcHeader.MaxLon, srcHeader.MaxLat)
	}

	// Resolve defaults from source.
	if format == "" {
		format = srcFormat
	}
	if maxZoom < 0 {
		maxZoom = int(srcHeader.MaxZoom)
	}
	if minZoom < 0 {
		// Extend the pyramid down to the zoom where all data fits in one tile
		// (same rule as geotiff2pmtiles), never dropping source levels.
		b := srcHeader.Bounds()
		minZoom = min(int(srcHeader.MinZoom), coord.MinZoomForSingleTile(b.MinLon, b.MinLat, b.MaxLon, b.MaxLat), maxZoom)
	}
	if maxZoom > int(srcHeader.MaxZoom) {
		log.Fatalf("--max-zoom %d exceeds the source max zoom %d; pmtransform cannot add detail (let the viewer overzoom)", maxZoom, srcHeader.MaxZoom)
	}
	if minZoom > maxZoom {
		log.Fatalf("invalid zoom range %d-%d: --min-zoom must be <= --max-zoom", minZoom, maxZoom)
	}
	srcTileSize := discoverSourceTileSize(reader, srcFormat)
	if tileSize < 0 {
		tileSize = srcTileSize
	} else if tileSize != srcTileSize {
		log.Fatalf("--tile-size %d differs from the source tile size %d; resizing tiles is not supported", tileSize, srcTileSize)
	}

	// Resolve resampling method.
	resamplingMode, err := tile.ParseResampling(resampling)
	if err != nil {
		log.Fatalf("Resampling: %v", err)
	}

	nodataFill, missingFill, err := cli.FillColors(flag.CommandLine, nodataColor, fillMissing, fillColor)
	if err != nil {
		log.Fatal(err)
	}

	srcMinZoom := int(srcHeader.MinZoom)
	mode, extendDown := tile.SelectTransformMode(tile.TransformModeOptions{
		Rebuild:       rebuild,
		FormatChanged: format != srcFormat,
		NodataColor:   nodataFill != nil,
		MinZoom:       minZoom,
		SourceMinZoom: srcMinZoom,
	})

	// Resolve the tile encoder — only re-encode, rebuild and fill tiles need
	// one, so a passthrough of e.g. a WebP archive still works in a build
	// without CGo.
	var enc encode.Encoder
	tileFormat := srcHeader.TileType
	if mode != tile.TransformPassthrough || extendDown || missingFill != nil {
		enc, err = encode.NewEncoder(format, quality)
		if err != nil {
			log.Fatalf("Encoder: %v", err)
		}
	}
	if mode != tile.TransformPassthrough || extendDown {
		tileFormat = enc.PMTileType()
	}
	if format == "jpeg" {
		for _, f := range []struct {
			name string
			c    *color.RGBA
		}{{"--nodata-color", nodataFill}, {"--fill-missing", missingFill}} {
			if f.c != nil && f.c.A < 255 {
				log.Print(jpegAlphaWarning(f.name, f.c))
			}
		}
	}

	if cli.IsFlagSet(flag.CommandLine, "resampling") && mode != tile.TransformRebuild && !extendDown {
		log.Printf("WARNING: --resampling only applies to rebuilt or added zoom levels; pass --rebuild to apply it to existing levels")
	}

	if terrarium && (format == "jpeg" || format == "webp") {
		log.Printf("Warning: source is terrarium-encoded elevation data; lossy %s encoding will corrupt elevations", format)
	}

	// Compute memory limit.
	var memoryLimitBytes int64
	if noSpill {
		memoryLimitBytes = -1
	} else if memLimitMB > 0 {
		memoryLimitBytes = int64(memLimitMB) * 1024 * 1024
	}

	bounds := [4]float32{srcHeader.MinLon, srcHeader.MinLat, srcHeader.MaxLon, srcHeader.MaxLat}

	// Print settings summary.
	modeStr := "passthrough"
	switch mode {
	case tile.TransformReencode:
		modeStr = "re-encode"
	case tile.TransformRebuild:
		modeStr = "rebuild pyramid"
	}
	if extendDown {
		modeStr += fmt.Sprintf(" + add zoom %d–%d", minZoom, min(srcMinZoom-1, maxZoom))
	}

	fmt.Printf("pmtransform %s (commit %s, built %s)\n", version, commit, buildDate)
	fmt.Printf("  %-14s %s\n", "Mode:", modeStr)
	fmt.Printf("  %-14s %s → %s\n", "Format:", srcFormat, format)
	if format == "jpeg" || format == "webp" {
		fmt.Printf("  %-14s %d\n", "Quality:", quality)
	}
	fmt.Printf("  %-14s %dpx\n", "Tile size:", tileSize)
	fmt.Printf("  %-14s %d – %d (source: %d – %d)\n", "Zoom:",
		minZoom, maxZoom, srcHeader.MinZoom, srcHeader.MaxZoom)
	if mode == tile.TransformRebuild || extendDown {
		fmt.Printf("  %-14s %s\n", "Resampling:", resampling)
	}
	if terrarium {
		fmt.Printf("  %-14s terrarium (elevation-space downsampling)\n", "Encoding:")
	}
	fmt.Printf("  %-14s %d\n", "Concurrency:", concurrency)
	if nodataFill != nil {
		fmt.Printf("  %-14s %s\n", "Nodata color:", cli.FormatColor(nodataFill))
	}
	if missingFill != nil {
		fmt.Printf("  %-14s %s\n", "Fill missing:", cli.FormatColor(missingFill))
	}
	if noSpill {
		fmt.Printf("  %-14s disabled (all in memory)\n", "Disk spill:")
	} else if memLimitMB > 0 {
		fmt.Printf("  %-14s %d MB\n", "Spill queue:", memLimitMB)
	} else if mode == tile.TransformRebuild || extendDown {
		fmt.Printf("  %-14s %d MB (auto)\n", "Spill queue:", tile.ComputeMemoryLimit(tile.DefaultMemoryPressurePercent, false)>>20)
	}
	fmt.Printf("  %-14s %s (%d tiles)\n", "Input:", inputPath, reader.NumTiles())
	fmt.Printf("  %-14s %s\n", "Output:", outputPath)

	tmpDir, cleanup, err := cli.MakeTmpDir(tmpDirFlag, outputPath, ".pmtransform-tmp-")
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	// Build config.
	cfg := tile.TransformConfig{
		MinZoom:          minZoom,
		MaxZoom:          maxZoom,
		TileSize:         tileSize,
		Concurrency:      concurrency,
		Verbose:          verbose,
		Encoder:          enc,
		SourceFormat:     srcFormat,
		Resampling:       resamplingMode,
		Mode:             mode,
		NodataColor:      nodataFill,
		FillMissing:      missingFill,
		Bounds:           bounds,
		MemoryLimitBytes: memoryLimitBytes,
		OutputDir:        tmpDir,
		IsTerrarium:      terrarium,
	}

	// Build description with processing steps prepended to source description.
	description := buildTransformDescription(srcDescription, srcHeader, mode, extendDown, srcFormat, format, quality,
		tileSize, minZoom, maxZoom, resampling, nodataFill, missingFill)

	// Create PMTiles writer.
	encoding := srcEncoding
	if terrarium {
		encoding = "terrarium"
	}
	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      srcHeader.Bounds(),
		TileFormat:  tileFormat,
		TileSize:    tileSize,
		TempDir:     tmpDir,
		Name:        "pmtransform",
		Description: description,
		Attribution: attribution,
		Type:        layerType,
		Encoding:    encoding,
		Extra:       srcExtra,
	})
	if err != nil {
		cleanup()
		log.Fatalf("Creating PMTiles writer: %v", err)
	}

	// Run transform.
	genStart := time.Now()
	stats, err := tile.Transform(cfg, reader, writer)
	if err != nil {
		writer.Abort()
		cleanup()
		log.Fatalf("Transform: %v", err)
	}

	if verbose {
		log.Printf("Processed %d tiles (%d uniform, %d empty) in %v",
			stats.TileCount, stats.UniformTiles, stats.EmptyTiles,
			time.Since(genStart).Round(time.Millisecond))
	}

	// Finalize PMTiles file.
	if err := writer.Finalize(); err != nil {
		cleanup()
		log.Fatalf("Finalizing PMTiles: %v", err)
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	fi, _ := os.Stat(outputPath)
	fmt.Printf("Done: %d tiles, %s, %v → %s\n", stats.TileCount, cli.HumanSize(fi.Size()), elapsed, outputPath)
}

// jpegAlphaWarning is the warning for a colour flag whose alpha jpeg cannot
// store: the encoder drops the alpha and writes the colour's RGB, opaque.
func jpegAlphaWarning(flagName string, c *color.RGBA) string {
	return fmt.Sprintf("WARNING: %s alpha %d cannot be stored in jpeg; those areas will be opaque rgb(%d,%d,%d)",
		flagName, c.A, c.R, c.G, c.B)
}

// discoverSourceTileSize reads and decodes one tile to infer the source tile size.
// PMTiles v3 header does not store tile size, so we must decode to discover it.
// Returns 256 if no tile could be decoded (e.g. all empty).
func discoverSourceTileSize(reader *pmtiles.Reader, format string) int {
	tiles := reader.TilesAtZoom(int(reader.Header().MaxZoom))
	for _, t := range tiles {
		z, x, y := t[0], t[1], t[2]
		data, err := reader.ReadTile(z, x, y)
		if err != nil || data == nil {
			continue
		}
		img, err := encode.DecodeImage(data, format)
		if err != nil {
			continue
		}
		b := img.Bounds()
		if b.Dx() > 0 && b.Dy() > 0 {
			return b.Dx()
		}
	}
	return 256
}

func buildTransformDescription(srcDescription string, srcHeader pmtiles.Header,
	mode tile.TransformMode, extendDown bool, srcFormat, targetFormat string, quality int,
	tileSize, minZoom, maxZoom int, resampling string, nodataFill, missingFill *color.RGBA) string {

	var b strings.Builder

	b.WriteString(fmt.Sprintf("Processing: pmtransform %s\n", version))

	modeStr := "passthrough"
	switch mode {
	case tile.TransformReencode:
		modeStr = "re-encode"
	case tile.TransformRebuild:
		modeStr = "rebuild"
	}
	if extendDown {
		modeStr += fmt.Sprintf(" + added zoom %d-%d", minZoom, min(int(srcHeader.MinZoom)-1, maxZoom))
	}
	b.WriteString(fmt.Sprintf("  Mode: %s\n", modeStr))

	if srcFormat != targetFormat {
		b.WriteString(fmt.Sprintf("  Format: %s -> %s", srcFormat, targetFormat))
	} else {
		b.WriteString(fmt.Sprintf("  Format: %s", targetFormat))
	}
	if targetFormat == "jpeg" || targetFormat == "webp" {
		b.WriteString(fmt.Sprintf(" (quality: %d)", quality))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("  Tile size: %dpx\n", tileSize))

	if minZoom != int(srcHeader.MinZoom) || maxZoom != int(srcHeader.MaxZoom) {
		b.WriteString(fmt.Sprintf("  Zoom: %d - %d (source: %d - %d)\n",
			minZoom, maxZoom, srcHeader.MinZoom, srcHeader.MaxZoom))
	} else {
		b.WriteString(fmt.Sprintf("  Zoom: %d - %d\n", minZoom, maxZoom))
	}

	if mode == tile.TransformRebuild || extendDown {
		b.WriteString(fmt.Sprintf("  Resampling: %s\n", resampling))
	}

	if nodataFill != nil {
		b.WriteString(fmt.Sprintf("  Nodata color: %s\n", cli.FormatColor(nodataFill)))
	}
	if missingFill != nil {
		b.WriteString(fmt.Sprintf("  Fill missing: %s\n", cli.FormatColor(missingFill)))
	}

	if srcDescription != "" {
		b.WriteString("\n")
		b.WriteString(srcDescription)
	}

	return b.String()
}

// derivedMetadataKeys are the metadata keys pmtiles.Writer derives from its
// options, which pmtransform sets from the source or its flags.
var derivedMetadataKeys = []string{"name", "description", "format", "type", "minzoom", "maxzoom", "bounds", "center", "attribution", "encoding"}

// passthroughMetadata returns the source metadata keys that the writer does
// not derive, so that provenance such as a composite's scene list survives
// the transform. They go to WriterOptions.Extra, which overrides derived
// keys, so the derived ones must stay out.
func passthroughMetadata(meta map[string]any) map[string]any {
	extra := map[string]any{}
	for k, v := range meta {
		if !slices.Contains(derivedMetadataKeys, k) {
			extra[k] = v
		}
	}
	return extra
}

// sameFile reports whether two paths name the same existing file.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
