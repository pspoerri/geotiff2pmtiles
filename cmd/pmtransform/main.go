package main

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
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
		cpuProfile  string
		memProfile  string
		memLimitMB  int
		noSpill     bool
		fillColor   string
		rebuild     bool
	)

	flag.StringVar(&format, "format", "", "Target tile encoding: jpeg, png, webp (default: keep source format)")
	flag.IntVar(&quality, "quality", 85, "JPEG/WebP quality 1-100")
	flag.IntVar(&minZoom, "min-zoom", -1, "Minimum zoom level (default: keep source)")
	flag.IntVar(&maxZoom, "max-zoom", -1, "Maximum zoom level (default: keep source)")
	flag.IntVar(&tileSize, "tile-size", -1, "Output tile size in pixels (default: keep source)")
	flag.IntVar(&concurrency, "concurrency", runtime.NumCPU(), "Number of parallel workers")
	flag.StringVar(&resampling, "resampling", "bicubic", "Interpolation method: lanczos, bicubic, bilinear, nearest, mode")
	flag.BoolVar(&verbose, "verbose", false, "Verbose progress output")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.StringVar(&cpuProfile, "cpuprofile", "", "Write CPU profile to file")
	flag.StringVar(&memProfile, "memprofile", "", "Write memory profile to file")
	flag.IntVar(&memLimitMB, "mem-limit", 0, "Tile store memory limit in MB before disk spilling (0 = auto ~90% of RAM)")
	flag.BoolVar(&noSpill, "no-spill", false, "Disable disk spilling (keep all tiles in memory)")
	flag.StringVar(&fillColor, "fill-color", "", "Substitute transparent/nodata with RGBA (color transform); also fill missing tile positions, e.g. \"0,0,0,255\" or \"#000000ff\"")
	flag.BoolVar(&rebuild, "rebuild", false, "Force full pyramid rebuild (required for resampling changes)")

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
		os.Exit(0)
	}

	// CPU profiling.
	if cpuProfile != "" {
		f, err := os.Create(cpuProfile)
		if err != nil {
			log.Fatalf("Creating CPU profile: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("Starting CPU profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	// Memory profile (written at exit).
	if memProfile != "" {
		defer func() {
			f, err := os.Create(memProfile)
			if err != nil {
				log.Fatalf("Creating memory profile: %v", err)
			}
			defer f.Close()
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatalf("Writing memory profile: %v", err)
			}
		}()
	}

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}

	inputPath := args[0]
	outputPath := args[1]

	if !strings.HasSuffix(inputPath, ".pmtiles") {
		log.Fatal("Input file must have .pmtiles extension")
	}
	if !strings.HasSuffix(outputPath, ".pmtiles") {
		log.Fatal("Output file must have .pmtiles extension")
	}
	if inputPath == outputPath {
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
	if minZoom < 0 {
		minZoom = int(srcHeader.MinZoom)
	}
	if maxZoom < 0 {
		maxZoom = int(srcHeader.MaxZoom)
	}
	if tileSize < 0 {
		tileSize = discoverSourceTileSize(reader, srcFormat)
	}

	// Resolve resampling method.
	resamplingMode, err := tile.ParseResampling(resampling)
	if err != nil {
		log.Fatalf("Resampling: %v", err)
	}

	// Resolve tile encoder.
	enc, err := encode.NewEncoder(format, quality)
	if err != nil {
		log.Fatalf("Encoder: %v", err)
	}

	// Parse fill color.
	var fc *color.RGBA
	if fillColor != "" {
		c, err := parseColor(fillColor)
		if err != nil {
			log.Fatalf("Fill color: %v", err)
		}
		fc = &c
	}

	// Determine transform mode.
	formatChanged := format != srcFormat
	zoomChanged := minZoom < int(srcHeader.MinZoom) // adding lower zoom levels
	mode := tile.TransformPassthrough

	if rebuild || zoomChanged {
		mode = tile.TransformRebuild
	} else if formatChanged {
		mode = tile.TransformReencode
	} else if fc != nil {
		// Fill-only: use re-encode mode since we need the encoder.
		mode = tile.TransformReencode
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

	fmt.Printf("pmtransform %s (commit %s, built %s)\n", version, commit, buildDate)
	fmt.Printf("  %-14s %s\n", "Mode:", modeStr)
	fmt.Printf("  %-14s %s → %s\n", "Format:", srcFormat, format)
	if format == "jpeg" || format == "webp" {
		fmt.Printf("  %-14s %d\n", "Quality:", quality)
	}
	fmt.Printf("  %-14s %dpx\n", "Tile size:", tileSize)
	fmt.Printf("  %-14s %d – %d (source: %d – %d)\n", "Zoom:",
		minZoom, maxZoom, srcHeader.MinZoom, srcHeader.MaxZoom)
	if mode == tile.TransformRebuild {
		fmt.Printf("  %-14s %s\n", "Resampling:", resampling)
	}
	fmt.Printf("  %-14s %d\n", "Concurrency:", concurrency)
	if fc != nil {
		fmt.Printf("  %-14s rgba(%d,%d,%d,%d)\n", "Fill color:", fc.R, fc.G, fc.B, fc.A)
	}
	if noSpill {
		fmt.Printf("  %-14s disabled (all in memory)\n", "Disk spill:")
	} else if memLimitMB > 0 {
		fmt.Printf("  %-14s %d MB\n", "Mem limit:", memLimitMB)
	} else if mode == tile.TransformRebuild {
		fmt.Printf("  %-14s auto (~90%% of RAM)\n", "Mem limit:")
	}
	fmt.Printf("  %-14s %s (%d tiles)\n", "Input:", inputPath, reader.NumTiles())
	fmt.Printf("  %-14s %s\n", "Output:", outputPath)

	// Build config.
	outputDir := filepath.Dir(outputPath)
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
		FillColor:        fc,
		Bounds:           bounds,
		MemoryLimitBytes: memoryLimitBytes,
		OutputDir:        outputDir,
	}

	// Create PMTiles writer.
	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:    minZoom,
		MaxZoom:    maxZoom,
		Bounds:     cog.Bounds{MinLon: float64(bounds[0]), MinLat: float64(bounds[1]), MaxLon: float64(bounds[2]), MaxLat: float64(bounds[3])},
		TileFormat: enc.PMTileType(),
		TileSize:   tileSize,
		TempDir:    outputDir,
	})
	if err != nil {
		log.Fatalf("Creating PMTiles writer: %v", err)
	}

	// Run transform.
	genStart := time.Now()
	stats, err := tile.Transform(cfg, reader, writer)
	if err != nil {
		writer.Abort()
		log.Fatalf("Transform: %v", err)
	}

	if verbose {
		log.Printf("Processed %d tiles (%d uniform, %d empty) in %v",
			stats.TileCount, stats.UniformTiles, stats.EmptyTiles,
			time.Since(genStart).Round(time.Millisecond))
	}

	// Finalize PMTiles file.
	if err := writer.Finalize(); err != nil {
		log.Fatalf("Finalizing PMTiles: %v", err)
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	fi, _ := os.Stat(outputPath)
	fmt.Printf("Done: %d tiles, %s, %v → %s\n", stats.TileCount, humanSize(fi.Size()), elapsed, outputPath)
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

// parseColor parses an RGBA color from "R,G,B,A" or "#RRGGBBAA" format.
func parseColor(s string) (color.RGBA, error) {
	if strings.HasPrefix(s, "#") {
		return parseHexColor(s)
	}

	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return color.RGBA{}, fmt.Errorf("expected R,G,B,A format (e.g. \"0,0,0,255\"), got %q", s)
	}

	vals := make([]uint8, 4)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 || v > 255 {
			return color.RGBA{}, fmt.Errorf("invalid color component %q (must be 0-255)", p)
		}
		vals[i] = uint8(v)
	}
	return color.RGBA{R: vals[0], G: vals[1], B: vals[2], A: vals[3]}, nil
}

func parseHexColor(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(s, "#")
	switch len(s) {
	case 6:
		s += "ff" // default alpha
	case 8:
		// full RRGGBBAA
	default:
		return color.RGBA{}, fmt.Errorf("hex color must be #RRGGBB or #RRGGBBAA, got %q", "#"+s)
	}

	r, err := strconv.ParseUint(s[0:2], 16, 8)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("invalid hex color: %w", err)
	}
	g, err := strconv.ParseUint(s[2:4], 16, 8)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("invalid hex color: %w", err)
	}
	b, err := strconv.ParseUint(s[4:6], 16, 8)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("invalid hex color: %w", err)
	}
	a, err := strconv.ParseUint(s[6:8], 16, 8)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("invalid hex color: %w", err)
	}

	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}, nil
}

func humanSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
