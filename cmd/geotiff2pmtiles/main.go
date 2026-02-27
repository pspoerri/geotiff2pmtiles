package main

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
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
		format       string
		quality      int
		minZoom      int
		maxZoom      int
		showVersion  bool
		tileSize     int
		concurrency  int
		verbose      bool
		resampling   string
		cpuProfile   string
		memProfile   string
		memLimitMB   int
		noSpill      bool
		fillColor    string
		attribution  string
		layerType    string
		bandsStr     string
		alphaBandStr string
		rescaleStr   string
		rescaleRange string
	)

	flag.StringVar(&format, "format", "jpeg", "Tile encoding: jpeg, png, webp, terrarium")
	flag.IntVar(&quality, "quality", 85, "JPEG/WebP quality 1-100")
	flag.IntVar(&minZoom, "min-zoom", -1, "Minimum zoom level (default: auto)")
	flag.IntVar(&maxZoom, "max-zoom", -1, "Maximum zoom level (default: auto from resolution)")
	flag.IntVar(&tileSize, "tile-size", 256, "Output tile size in pixels")
	flag.IntVar(&concurrency, "concurrency", runtime.NumCPU(), "Number of parallel workers")
	flag.StringVar(&resampling, "resampling", "bicubic", "Interpolation method: lanczos, bicubic, bilinear, nearest, mode")
	flag.BoolVar(&verbose, "verbose", false, "Verbose progress output")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.StringVar(&cpuProfile, "cpuprofile", "", "Write CPU profile to file")
	flag.StringVar(&memProfile, "memprofile", "", "Write memory profile to file")
	flag.IntVar(&memLimitMB, "mem-limit", 0, "Tile store memory limit in MB before disk spilling (0 = auto ~90% of RAM)")
	flag.BoolVar(&noSpill, "no-spill", false, "Disable disk spilling (keep all tiles in memory)")
	flag.StringVar(&fillColor, "fill-color", "", "Substitute transparent/nodata with RGBA (color transform); also fill missing tile positions, e.g. \"0,0,0,255\" or \"#000000ff\"")
	flag.StringVar(&attribution, "attribution", "", "Attribution string for data sources (stored in metadata)")
	flag.StringVar(&layerType, "type", "baselayer", "Layer type: baselayer, overlay")
	flag.StringVar(&bandsStr, "bands", "1,2,3", "1-indexed band numbers for R,G,B output (e.g. \"4,1,2\" for NIR-R-G)")
	flag.StringVar(&alphaBandStr, "alpha-band", "auto", "1-indexed band for alpha (0=auto: band 4 for 8-bit spp>=4; -1=force no alpha)")
	flag.StringVar(&rescaleStr, "rescale", "auto", "Rescale mode: auto, log, linear, none (auto: requires --rescale-range for 16-bit)")
	flag.StringVar(&rescaleRange, "rescale-range", "", "Input value range for rescaling: min,max (required for 16-bit data)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>\n\n")
		fmt.Fprintf(os.Stderr, "Convert GeoTIFF/COG files to a PMTiles v3 archive.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if showVersion {
		fmt.Printf("geotiff2pmtiles %s (commit %s, built %s)\n", version, commit, buildDate)
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
		if verbose {
			log.Printf("CPU profiling enabled → %s", cpuProfile)
		}
	}

	// Memory profile (written at exit).
	if memProfile != "" {
		defer func() {
			f, err := os.Create(memProfile)
			if err != nil {
				log.Fatalf("Creating memory profile: %v", err)
			}
			defer f.Close()
			runtime.GC() // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatalf("Writing memory profile: %v", err)
			}
			if verbose {
				log.Printf("Memory profile written → %s", memProfile)
			}
		}()
	}

	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	outputPath := args[len(args)-1]
	inputPaths := args[:len(args)-1]

	if !strings.HasSuffix(outputPath, ".pmtiles") {
		log.Fatal("Output file must have .pmtiles extension")
	}

	// Resolve tile encoder.
	enc, err := encode.NewEncoder(format, quality)
	if err != nil {
		log.Fatalf("Encoder: %v", err)
	}

	// Resolve resampling method.
	resamplingMode, err := tile.ParseResampling(resampling)
	if err != nil {
		log.Fatalf("Resampling: %v", err)
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

	// Collect GeoTIFF files.
	tiffFiles, err := collectTIFFs(inputPaths)
	if err != nil {
		log.Fatalf("Collecting input files: %v", err)
	}
	if len(tiffFiles) == 0 {
		log.Fatal("No GeoTIFF files found in the specified inputs")
	}
	log.Printf("Found %d GeoTIFF file(s)", len(tiffFiles))

	// Open all COG readers and gather metadata.
	// OpenAll validates that all files exist before opening any,
	// so we get a complete error report for missing files.
	start := time.Now()
	sources, err := cog.OpenAll(tiffFiles)
	if err != nil {
		log.Fatalf("Opening GeoTIFFs:\n%v", err)
	}
	defer func() {
		for _, s := range sources {
			s.Close()
		}
	}()

	if verbose {
		log.Printf("Opened %d COG(s) in %v", len(sources), time.Since(start).Round(time.Millisecond))
	}

	// Check for geographic holes in coverage.
	gaps := cog.CheckCoverageGaps(sources)
	if len(gaps) > 0 {
		log.Printf("WARNING: Detected %d geographic hole(s) in the input coverage:", len(gaps))
		for i, g := range gaps {
			log.Printf("  Hole %d: X [%.1f, %.1f], Y [%.1f, %.1f] (source CRS)",
				i+1, g.MinX, g.MaxX, g.MinY, g.MaxY)
		}
	}

	// Auto-detect preset from GeoTIFF structure and GDAL metadata.
	// Apply format override (e.g. terrarium for float data) before band config
	// parsing so that the format is settled before we proceed.
	if preset, ok := sources[0].DetectPreset(); ok {
		if preset.Format != "" && format == "jpeg" {
			format = preset.Format
			log.Printf("Auto-detected: %s (format: %s)", preset.Name, format)
			enc, err = encode.NewEncoder(format, quality)
			if err != nil {
				log.Fatalf("Encoder: %v", err)
			}
		}
	}

	// Validate terrarium requires float input.
	if format == "terrarium" && !sources[0].IsFloat() {
		log.Fatal("Terrarium format requires float GeoTIFF input (elevation data)")
	}

	// Parse band config.
	bandCfg, err := parseBandConfig(bandsStr, alphaBandStr, rescaleStr, rescaleRange, sources[0])
	if err != nil {
		log.Fatalf("Band config: %v", err)
	}
	for _, src := range sources {
		src.SetBandConfig(bandCfg)
	}

	// Compute merged bounds in WGS84.
	mergedBounds := cog.MergedBoundsWGS84(sources)
	if verbose {
		log.Printf("Merged bounds (WGS84): lon [%.6f, %.6f], lat [%.6f, %.6f]",
			mergedBounds.MinLon, mergedBounds.MaxLon, mergedBounds.MinLat, mergedBounds.MaxLat)
	}

	// Determine zoom levels.
	pixelSizeMeters := coord.PixelSizeInGroundMeters(sources[0].PixelSize(), sources[0].EPSG(), mergedBounds.CenterLat())
	autoMax := coord.MaxZoomForResolution(pixelSizeMeters, mergedBounds.CenterLat(), tileSize)
	if maxZoom < 0 {
		maxZoom = autoMax
	}
	if minZoom < 0 {
		minZoom = maxZoom - 6
		if minZoom < 0 {
			minZoom = 0
		}
	}
	if verbose {
		log.Printf("Zoom range: %d - %d (auto-detected max: %d)", minZoom, maxZoom, autoMax)
	}

	// Compute memory limit for disk spilling.
	var memoryLimitBytes int64
	if noSpill {
		memoryLimitBytes = -1 // sentinel: disable spilling
	} else if memLimitMB > 0 {
		memoryLimitBytes = int64(memLimitMB) * 1024 * 1024
	}
	// 0 = auto-detect from system RAM (handled inside Generate).

	// Print settings summary.
	fmt.Printf("geotiff2pmtiles %s (commit %s, built %s)\n", version, commit, buildDate)
	switch format {
	case "jpeg", "webp":
		fmt.Printf("  %-14s %s (quality: %d)\n", "Format:", format, quality)
	default:
		fmt.Printf("  %-14s %s\n", "Format:", format)
	}
	fmt.Printf("  %-14s %dpx\n", "Tile size:", tileSize)
	fmt.Printf("  %-14s %d – %d (auto-max: %d)\n", "Zoom:", minZoom, maxZoom, autoMax)
	fmt.Printf("  %-14s %s\n", "Resampling:", resampling)
	fmt.Printf("  %-14s %d\n", "Concurrency:", concurrency)
	if fc != nil {
		fmt.Printf("  %-14s rgba(%d,%d,%d,%d)\n", "Fill color:", fc.R, fc.G, fc.B, fc.A)
	}
	if noSpill {
		fmt.Printf("  %-14s disabled (all in memory)\n", "Disk spill:")
	} else if memLimitMB > 0 {
		fmt.Printf("  %-14s %d MB\n", "Mem limit:", memLimitMB)
	} else {
		fmt.Printf("  %-14s auto (~90%% of RAM)\n", "Mem limit:")
	}
	if bandCfg.Bands != ([3]int{1, 2, 3}) || bandCfg.AlphaBand != 0 || bandCfg.Rescale != cog.RescaleNone {
		fmt.Printf("  %-14s %d,%d,%d\n", "Bands:", bandCfg.Bands[0], bandCfg.Bands[1], bandCfg.Bands[2])
		switch bandCfg.AlphaBand {
		case 0:
			fmt.Printf("  %-14s auto\n", "Alpha band:")
		case -1:
			fmt.Printf("  %-14s none\n", "Alpha band:")
		default:
			fmt.Printf("  %-14s %d\n", "Alpha band:", bandCfg.AlphaBand)
		}
		switch bandCfg.Rescale {
		case cog.RescaleLinear:
			fmt.Printf("  %-14s linear [%.0f, %.0f]\n", "Rescale:", bandCfg.RescaleMin, bandCfg.RescaleMax)
		case cog.RescaleLog:
			fmt.Printf("  %-14s log [%.0f, %.0f]\n", "Rescale:", bandCfg.RescaleMin, bandCfg.RescaleMax)
		}
	}
	fmt.Printf("  %-14s %d file(s)\n", "Input:", len(tiffFiles))
	fmt.Printf("  %-14s %s\n", "Output:", outputPath)

	// Build tile generation config.
	outputDir := filepath.Dir(outputPath)
	cfg := tile.Config{
		MinZoom:          minZoom,
		MaxZoom:          maxZoom,
		TileSize:         tileSize,
		Concurrency:      concurrency,
		Verbose:          verbose,
		Encoder:          enc,
		Bounds:           mergedBounds,
		Resampling:       resamplingMode,
		IsTerrarium:      format == "terrarium",
		FillColor:        fc,
		MemoryLimitBytes: memoryLimitBytes,
		OutputDir:        outputDir,
	}

	// Build description for PMTiles metadata.
	description := buildDescription(sources, mergedBounds, gaps, format, quality, tileSize, minZoom, maxZoom, resampling, fc, bandCfg)

	// Create PMTiles writer.
	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      mergedBounds,
		TileFormat:  enc.PMTileType(),
		TileSize:    tileSize,
		TempDir:     outputDir,
		Description: description,
		Attribution: attribution,
		Type:        layerType,
	})
	if err != nil {
		log.Fatalf("Creating PMTiles writer: %v", err)
	}

	// Generate tiles.
	genStart := time.Now()
	stats, err := tile.Generate(cfg, sources, writer)
	if err != nil {
		writer.Abort()
		log.Fatalf("Tile generation: %v", err)
	}

	if verbose {
		log.Printf("Generated %d tiles (%d uniform, %d empty) in %v",
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

// collectTIFFs resolves input paths to a list of .tif files.
func collectTIFFs(paths []string) ([]string, error) {
	var result []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if info.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, fmt.Errorf("readdir %s: %w", p, err)
			}
			for _, e := range entries {
				if !e.IsDir() && isTIFF(e.Name()) {
					result = append(result, filepath.Join(p, e.Name()))
				}
			}
		} else if isTIFF(p) {
			result = append(result, p)
		}
	}
	return result, nil
}

func isTIFF(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tif") || strings.HasSuffix(lower, ".tiff")
}

func buildDescription(sources []*cog.Reader, mergedBounds cog.Bounds, gaps []cog.CoverageGap,
	format string, quality int, tileSize int, minZoom, maxZoom int, resampling string, fc *color.RGBA, bandCfg cog.BandConfig) string {

	var b strings.Builder

	b.WriteString(fmt.Sprintf("Processing: geotiff2pmtiles %s\n", version))
	switch format {
	case "jpeg", "webp":
		b.WriteString(fmt.Sprintf("  Format: %s (quality: %d)\n", format, quality))
	default:
		b.WriteString(fmt.Sprintf("  Format: %s\n", format))
	}
	b.WriteString(fmt.Sprintf("  Tile size: %dpx\n", tileSize))
	b.WriteString(fmt.Sprintf("  Zoom: %d - %d\n", minZoom, maxZoom))
	b.WriteString(fmt.Sprintf("  Resampling: %s\n", resampling))
	if fc != nil {
		b.WriteString(fmt.Sprintf("  Fill color: rgba(%d,%d,%d,%d)\n", fc.R, fc.G, fc.B, fc.A))
	}
	if bandCfg.Bands != ([3]int{1, 2, 3}) || bandCfg.AlphaBand != 0 || bandCfg.Rescale != cog.RescaleNone {
		b.WriteString(fmt.Sprintf("  Bands: %d,%d,%d\n", bandCfg.Bands[0], bandCfg.Bands[1], bandCfg.Bands[2]))
		switch bandCfg.AlphaBand {
		case 0:
			b.WriteString("  Alpha band: auto\n")
		case -1:
			b.WriteString("  Alpha band: none\n")
		default:
			b.WriteString(fmt.Sprintf("  Alpha band: %d\n", bandCfg.AlphaBand))
		}
		switch bandCfg.Rescale {
		case cog.RescaleLinear:
			b.WriteString(fmt.Sprintf("  Rescale: linear [%.0f, %.0f]\n", bandCfg.RescaleMin, bandCfg.RescaleMax))
		case cog.RescaleLog:
			b.WriteString(fmt.Sprintf("  Rescale: log [%.0f, %.0f]\n", bandCfg.RescaleMin, bandCfg.RescaleMax))
		}
	}

	b.WriteString("\n")

	epsg := sources[0].EPSG()
	b.WriteString(fmt.Sprintf("Source: %d GeoTIFF file(s), EPSG:%d\n", len(sources), epsg))

	mergedMinX, mergedMinY := math.MaxFloat64, math.MaxFloat64
	mergedMaxX, mergedMaxY := -math.MaxFloat64, -math.MaxFloat64
	for _, src := range sources {
		minX, minY, maxX, maxY := src.BoundsInCRS()
		if minX < mergedMinX {
			mergedMinX = minX
		}
		if minY < mergedMinY {
			mergedMinY = minY
		}
		if maxX > mergedMaxX {
			mergedMaxX = maxX
		}
		if maxY > mergedMaxY {
			mergedMaxY = maxY
		}
	}
	b.WriteString(fmt.Sprintf("  Extent (CRS): [%.2f, %.2f] - [%.2f, %.2f]\n",
		mergedMinX, mergedMinY, mergedMaxX, mergedMaxY))

	b.WriteString(fmt.Sprintf("  Extent (WGS84): [%.6f, %.6f] - [%.6f, %.6f]\n",
		mergedBounds.MinLon, mergedBounds.MinLat, mergedBounds.MaxLon, mergedBounds.MaxLat))

	b.WriteString(fmt.Sprintf("  Pixel size: %g\n", sources[0].PixelSize()))

	b.WriteString(fmt.Sprintf("  Data: %s\n", sources[0].FormatDescription()))

	if len(gaps) == 0 {
		b.WriteString("  Holes: none")
	} else {
		b.WriteString(fmt.Sprintf("  Holes: %d gap(s)", len(gaps)))
	}

	return b.String()
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
		s += "ff"
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

// parseBandConfig parses CLI flags into a cog.BandConfig.
func parseBandConfig(bandsStr, alphaBandStr, rescaleStr, rescaleRange string, firstSrc *cog.Reader) (cog.BandConfig, error) {
	var cfg cog.BandConfig

	// Parse --bands.
	parts := strings.Split(bandsStr, ",")
	if len(parts) != 3 {
		return cfg, fmt.Errorf("--bands must be 3 comma-separated band numbers (e.g. \"1,2,3\"), got %q", bandsStr)
	}
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 1 {
			return cfg, fmt.Errorf("invalid band number %q (must be >= 1)", p)
		}
		cfg.Bands[i] = v
	}

	// Parse --alpha-band.
	switch alphaBandStr {
	case "auto":
		cfg.AlphaBand = 0
	default:
		v, err := strconv.Atoi(strings.TrimSpace(alphaBandStr))
		if err != nil {
			return cfg, fmt.Errorf("--alpha-band must be \"auto\" or an integer, got %q", alphaBandStr)
		}
		cfg.AlphaBand = v
	}

	// Parse --rescale and --rescale-range.
	is16 := firstSrc.BitsPerSample() == 16
	bandsExplicit := bandsStr != "1,2,3"
	switch rescaleStr {
	case "auto":
		if is16 {
			if rescaleRange == "" {
				// Try auto-detection from GDAL metadata before erroring.
				if preset, ok := firstSrc.DetectPreset(); ok && !bandsExplicit {
					log.Printf("Auto-detected: %s (bands %d,%d,%d, rescale linear [%.0f, %.0f])",
						preset.Name,
						preset.BandCfg.Bands[0], preset.BandCfg.Bands[1], preset.BandCfg.Bands[2],
						preset.BandCfg.RescaleMin, preset.BandCfg.RescaleMax)
					return preset.BandCfg, nil
				}
				return cfg, fmt.Errorf("16-bit GeoTIFF detected: --rescale-range min,max is required\n" +
					"  Hint: use gdalinfo or inspect the data to find the value range.\n" +
					"  Example: --rescale linear --rescale-range 0,5000")
			}
			cfg.Rescale = cog.RescaleLinear
			minV, maxV, err := parseRange(rescaleRange)
			if err != nil {
				return cfg, fmt.Errorf("--rescale-range: %w", err)
			}
			cfg.RescaleMin = minV
			cfg.RescaleMax = maxV
		} else {
			cfg.Rescale = cog.RescaleNone
		}
	case "linear":
		if rescaleRange == "" {
			return cfg, fmt.Errorf("--rescale-range is required when --rescale is set to %q", rescaleStr)
		}
		minV, maxV, err := parseRange(rescaleRange)
		if err != nil {
			return cfg, fmt.Errorf("--rescale-range: %w", err)
		}
		cfg.Rescale = cog.RescaleLinear
		cfg.RescaleMin = minV
		cfg.RescaleMax = maxV
	case "log":
		if rescaleRange == "" {
			return cfg, fmt.Errorf("--rescale-range is required when --rescale is set to %q", rescaleStr)
		}
		minV, maxV, err := parseRange(rescaleRange)
		if err != nil {
			return cfg, fmt.Errorf("--rescale-range: %w", err)
		}
		cfg.Rescale = cog.RescaleLog
		cfg.RescaleMin = minV
		cfg.RescaleMax = maxV
	case "none":
		cfg.Rescale = cog.RescaleNone
	default:
		return cfg, fmt.Errorf("--rescale must be auto, linear, log, or none, got %q", rescaleStr)
	}

	return cfg, nil
}

// parseRange parses a "min,max" string into two float64 values.
func parseRange(s string) (float64, float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected min,max format, got %q", s)
	}
	minV, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid min value %q: %w", parts[0], err)
	}
	maxV, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid max value %q: %w", parts[1], err)
	}
	return minV, maxV, nil
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
