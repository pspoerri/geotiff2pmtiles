// geotiff2pmtiles converts GeoTIFF and Cloud Optimized GeoTIFF files to a
// PMTiles v3 archive. Directories are scanned recursively; inputs may mix
// CRSs. Format, bands, rescale range and zoom range are detected from the
// input unless set by flags.
//
// Usage:
//
//	geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>
package main

import (
	"errors"
	"flag"
	"fmt"
	"image/color"
	"io/fs"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pspoerri/geotiff2pmtiles/internal/cli"
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
		format          string
		quality         int
		minZoom         int
		maxZoom         int
		showVersion     bool
		tileSize        int
		concurrency     int
		verbose         bool
		resampling      string
		memLimitMB      int
		noSpill         bool
		nodataColor     string
		fillMissing     string
		fillColor       string
		attribution     string
		layerType       string
		bandsStr        string
		alphaBandStr    string
		rescaleStr      string
		rescaleRange    string
		nodataStr       string
		nodataTolStr    string
		nodataFlood     bool
		resamplingGamma float64
		tmpDirFlag      string
		sourceEPSG      int
	)

	flag.StringVar(&format, "format", "auto", "Tile encoding: auto, jpeg, png, webp, terrarium (auto: terrarium for float/signed-int elevation data, webp when nodata or an internal mask is active, else jpeg)")
	flag.IntVar(&quality, "quality", 85, "JPEG/WebP quality 1-100 (ignored for png/terrarium; WebP is lossless-only in builds without libwebp)")
	flag.IntVar(&minZoom, "min-zoom", -1, "Minimum zoom level; -1 = auto: the highest zoom at which the whole extent fits in one tile")
	flag.IntVar(&maxZoom, "max-zoom", -1, "Maximum zoom level (0-30); -1 = auto from the source resolution")
	flag.IntVar(&tileSize, "tile-size", 256, "Output tile size in pixels: a power of two from 64 to 4096")
	flag.IntVar(&concurrency, "concurrency", runtime.NumCPU(), "Number of parallel workers (>= 1)")
	flag.StringVar(&resampling, "resampling", "bicubic", "Interpolation method: lanczos, bicubic, bilinear, nearest, mode")
	flag.Float64Var(&resamplingGamma, "resampling-gamma", 1.0, "Brighten interpolated output: out = 255*(v/255)^(1/gamma); 1.0 = off, must be > 0, typical 1.5-2.2 for dB-scaled SAR. Ignored for nearest/mode and terrarium")
	flag.BoolVar(&verbose, "verbose", false, "Verbose progress output")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	profiles := cli.RegisterProfileFlags(flag.CommandLine)
	flag.IntVar(&memLimitMB, "mem-limit", 0, "MB of encoded tiles allowed to queue for the spill file before workers pause (0 = auto: 90% of RAM, or of the cgroup limit on Linux, minus current usage minus 2 GB, never below 256 MB; spilling is always on unless --no-spill)")
	flag.BoolVar(&noSpill, "no-spill", false, "Disable disk spilling (keep all tiles in memory)")
	flag.StringVar(&tmpDirFlag, "tmp-dir", "", "Directory for temporary files, about 2x the output size at peak (default: the output file's directory)")
	flag.StringVar(&nodataColor, "nodata-color", "none", "RGBA color, e.g. \"0,0,0,255\" or \"#000000ff\", that replaces transparent/nodata pixels; none = keep them transparent")
	flag.StringVar(&fillMissing, "fill-missing", "0,0,0,0", "RGBA color of the solid tiles written at tile positions inside the bounds that have no data; none or \"\" = leave them absent. Not applied to jpeg output unless set explicitly")
	flag.StringVar(&fillColor, "fill-color", "", "deprecated: sets both --nodata-color and --fill-missing")
	flag.StringVar(&attribution, "attribution", "", "Attribution string for data sources (stored in metadata)")
	flag.StringVar(&layerType, "type", "baselayer", "Layer type: baselayer, overlay")
	flag.StringVar(&bandsStr, "bands", "auto", "1-indexed band numbers for R,G,B, e.g. \"4,1,2\" for NIR-R-G (auto: 1,2,3; gray from band 1 for 1-2 band input)")
	flag.StringVar(&alphaBandStr, "alpha-band", "auto", "Alpha band: auto (band 4 of 8-bit input with 4+ bands), none, or a 1-indexed band number")
	flag.StringVar(&rescaleStr, "rescale", "auto", "Rescale mode: auto, linear, log, none (auto: GDAL band-description preset if present, else linear over --rescale-range for 9-16-bit input, none for 8-bit; none maps the full range of the sample type, e.g. 0..65535 or -32768..32767 for Int16, to 0..255; ignored for terrarium)")
	flag.StringVar(&rescaleRange, "rescale-range", "", "Input value range min,max for rescaling, min < max (default: min/max over all sources of the bands selected by --bands, never the alpha band, from GDAL STATISTICS_* metadata when every selected band has them, else a scan of up to 64 tiles of the coarsest overview; sources without usable values, e.g. all-nodata ocean tiles, are skipped; the selected range is logged)")
	flag.StringVar(&nodataStr, "nodata", "", "Nodata value: pixels with all bands equal to this value are transparent (default: from the GDAL_NODATA tag). RGB outputs: an integer in [-32768, 65535]; terrarium: any float, overriding GDAL_NODATA of every source")
	flag.StringVar(&nodataTolStr, "nodata-tolerance", "", "Per-band tolerance applied to --nodata matching (default 0 = exact match). Useful for lossy-JPEG borders where strict 0 is smeared to 1..5; try 4–8. RGB outputs only")
	flag.IntVar(&sourceEPSG, "source-epsg", 0, "EPSG code of the input CRS for every input file, e.g. 32632, 25832 or 21781; overrides the GeoTIFF keys and the guess made for world-file (.tfw) inputs (0 = from the files)")
	flag.BoolVar(&nodataFlood, "nodata-flood", false, "Source-level flood-fill from the COG outer edges through near-nodata pixels. Only edge-reachable pixels are made transparent; interior dark pixels (text, shadows, canopy) stay opaque. Requires --nodata; pair with a widened --nodata-tolerance (e.g. 40) for scanned/JPEG sources. Costs ~W*H/8 bytes of RAM per source. Not for terrarium output.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>\n\n")
		fmt.Fprintf(os.Stderr, "Convert GeoTIFF/COG files to a PMTiles v3 archive.\n")
		fmt.Fprintf(os.Stderr, "Directories are scanned recursively for .tif/.tiff files.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if showVersion {
		fmt.Printf("geotiff2pmtiles %s (commit %s, built %s)\n", version, commit, buildDate)
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
	if len(args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	outputPath := args[len(args)-1]
	inputPaths := args[:len(args)-1]

	if !strings.EqualFold(filepath.Ext(outputPath), ".pmtiles") {
		log.Fatal("Output file must have .pmtiles extension")
	}

	format = strings.ToLower(format)
	if format == "jpg" {
		format = "jpeg"
	}
	switch format {
	case "auto", "jpeg", "png", "webp", "terrarium":
	default:
		log.Fatalf("--format must be auto, jpeg, png, webp or terrarium, got %q", format)
	}
	if quality < 1 || quality > 100 {
		log.Fatalf("--quality must be 1-100, got %d", quality)
	}
	if tileSize < 64 || tileSize > 4096 || tileSize&(tileSize-1) != 0 {
		log.Fatalf("--tile-size must be a power of two from 64 to 4096, got %d", tileSize)
	}
	if concurrency < 1 {
		log.Fatalf("--concurrency must be >= 1, got %d", concurrency)
	}
	if maxZoom > 30 {
		log.Fatalf("--max-zoom must be <= 30, got %d", maxZoom)
	}
	if resamplingGamma <= 0 {
		log.Fatalf("--resampling-gamma must be > 0, got %g", resamplingGamma)
	}
	if sourceEPSG < 0 {
		log.Fatalf("--source-epsg must be an EPSG code, got %d", sourceEPSG)
	}

	// Resolve resampling method.
	resamplingMode, err := tile.ParseResampling(resampling)
	if err != nil {
		log.Fatalf("Resampling: %v", err)
	}
	if resamplingGamma != 1.0 && (resamplingMode == tile.ResamplingNearest || resamplingMode == tile.ResamplingMode) {
		log.Printf("WARNING: --resampling-gamma has no effect with --resampling %s", resampling)
	}

	nodataFill, missingFill, err := cli.FillColors(flag.CommandLine, nodataColor, fillMissing, fillColor)
	if err != nil {
		log.Fatal(err)
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
	if sourceEPSG == 0 {
		var guessed []*cog.Reader
		for _, s := range sources {
			if s.EPSGGuessed() {
				guessed = append(guessed, s)
			}
		}
		if len(guessed) > 0 {
			log.Printf("WARNING: %d file(s) have no CRS in their GeoTIFF keys, so it was guessed from the coordinate ranges (%s: EPSG:%d). If that is wrong, set the source CRS (--source-epsg)",
				len(guessed), guessed[0].Path(), guessed[0].EPSG())
		}
	}

	if verbose {
		log.Printf("Opened %d COG(s) in %v", len(sources), time.Since(start).Round(time.Millisecond))
	}

	if sourceEPSG > 0 {
		for _, src := range sources {
			src.SetEPSG(sourceEPSG)
		}
		log.Printf("Source CRS: EPSG:%d (from --source-epsg)", sourceEPSG)
	}

	// Every source is reprojected with its own CRS, so inputs may mix CRSs
	// (e.g. Sentinel-2 tiles from several UTM zones). Reject unknown ones
	// before they turn into bounds and zoom levels.
	if err := checkSourceCRSs(sources); err != nil {
		log.Fatal(err)
	}

	// Check for geographic holes in coverage.
	gaps := coverageGaps(sources)
	if len(gaps) > 0 {
		log.Printf("WARNING: Detected %d geographic hole(s) in the input coverage:", len(gaps))
		for i, g := range gaps {
			log.Printf("  Hole %d: X [%.1f, %.1f], Y [%.1f, %.1f] (EPSG:%d)",
				i+1, g.MinX, g.MaxX, g.MinY, g.MaxY, g.epsg)
		}
	}

	// Auto-detect preset from GeoTIFF structure and GDAL metadata.
	// Apply format override (e.g. terrarium for float data) before band config
	// parsing so that the format is settled before we proceed.
	if preset, ok := sources[0].DetectPreset(); ok {
		if preset.Format != "" && format == "auto" {
			format = preset.Format
			log.Printf("Auto-detected: %s (format: %s)", preset.Name, format)
		}
	}

	// Validate terrarium requires float or signed-integer input.
	if format == "terrarium" && !sources[0].IsFloat() {
		log.Fatal("Terrarium format requires float or signed-integer GeoTIFF input (elevation data)")
	}
	for _, src := range sources {
		// The image path reads unsigned or signed 16-bit integers; float and
		// other signed samples would be decoded from their low byte into noise.
		if format != "terrarium" && src.IsFloat() && (src.IsIEEEFloat() || src.BitsPerSample() != 16) {
			log.Fatalf("--format %s cannot render %s (%s); use --format terrarium",
				format, src.Path(), src.FormatDescription())
		}
	}
	if format == "terrarium" && (nodataTolStr != "" || nodataFlood) {
		log.Printf("WARNING: --nodata-tolerance and --nodata-flood are ignored for terrarium output")
		nodataTolStr, nodataFlood = "", false
	}
	if format == "terrarium" && resamplingGamma != 1.0 {
		log.Printf("WARNING: --resampling-gamma has no effect for terrarium output")
	}

	// Parse band config. Terrarium reads elevation directly, so rescaling
	// (and its range detection) does not apply.
	if format == "terrarium" {
		rescaleStr = "none"
	}
	bandCfg, err := parseBandConfig(bandsStr, alphaBandStr, rescaleStr, rescaleRange, sources)
	if err != nil {
		log.Fatalf("Band config: %v", err)
	}

	// Apply nodata: CLI override takes precedence, then preset/IFD auto-detection.
	// Terrarium reads the float samples, where any value can mark nodata;
	// without --nodata each source's GDAL_NODATA tag applies.
	var floatNodata *float64
	if format == "terrarium" {
		if nodataStr != "" {
			v, err := strconv.ParseFloat(strings.TrimSpace(nodataStr), 64)
			if err != nil {
				log.Fatalf("--nodata: must be a number, got %q", nodataStr)
			}
			floatNodata = &v
		}
	} else if nodataStr != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(nodataStr), 64)
		if err != nil || v < -32768 || v > 65535 || v != math.Floor(v) {
			log.Fatalf("--nodata: must be an integer in [-32768, 65535], got %q", nodataStr)
		}
		bandCfg.HasNodata = true
		bandCfg.Nodata = v
	} else if !bandCfg.HasNodata {
		// Auto-detect from the first source file if not already set by preset.
		if nd := sources[0].NoData(); nd != "" {
			if v, err := strconv.ParseFloat(strings.TrimSpace(nd), 64); err == nil && v >= -32768 && v <= 65535 && v == math.Floor(v) {
				bandCfg.HasNodata = true
				bandCfg.Nodata = v
			}
		}
	}

	// --nodata-tolerance: only meaningful when nodata is active.
	if nodataTolStr != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(nodataTolStr), 64)
		if err != nil || v < 0 || v > 65535 || v != math.Floor(v) {
			log.Fatalf("--nodata-tolerance: must be a non-negative integer ≤ 65535, got %q", nodataTolStr)
		}
		if !bandCfg.HasNodata {
			log.Printf("WARNING: --nodata-tolerance set without --nodata; ignored")
		} else {
			bandCfg.NodataTolerance = v
		}
	}

	// If nodata is active but the output format can't carry transparency,
	// switch to WebP automatically (when the user didn't pick --format),
	// or warn if they explicitly chose jpeg.
	masked := slices.ContainsFunc(sources, (*cog.Reader).HasMask)
	if format == "auto" {
		format = "jpeg"
		if bandCfg.HasNodata || masked {
			format = "webp"
			log.Printf("Nodata or an internal mask is active; using webp so transparency is preserved (override with --format=jpeg).")
			if !encode.WebPLossy {
				log.Printf("WARNING: this build has no lossy WebP encoder (built without libwebp); tiles will be lossless WebP, which is much larger for imagery. Use a libwebp build or --format png.")
			}
		}
	} else if (bandCfg.HasNodata || masked) && format == "jpeg" {
		log.Printf("WARNING: nodata or an internal mask is active but --format=jpeg cannot carry transparency; those pixels will be encoded as black. Use --format=webp or --format=png for true transparency.")
	}
	enc, err := encode.NewEncoder(format, quality)
	if err != nil {
		log.Fatalf("Encoder: %v", err)
	}
	// JPEG cannot store transparency: the transparent default fill would
	// turn every tile position without data into a black tile.
	if format == "jpeg" {
		explicit := cli.IsFlagSet(flag.CommandLine, "fill-missing") || cli.IsFlagSet(flag.CommandLine, "fill-color")
		if missingFill != nil && missingFill.A < 255 {
			if explicit {
				log.Print(jpegAlphaWarning("--fill-missing", missingFill))
			} else {
				missingFill = nil
			}
		}
		if nodataFill != nil && nodataFill.A < 255 {
			log.Print(jpegAlphaWarning("--nodata-color", nodataFill))
		}
	}

	log.Printf("Band config: %s", bandCfg)
	for _, src := range sources {
		src.SetBandConfig(bandCfg)
	}

	// --nodata-flood: build a per-source transparency bitmap once, before any
	// concurrent ReadTile traffic begins. Pixels are transparent iff they fall
	// within tolerance AND are reachable through near-nodata neighbours from
	// the COG outer boundary, eliminating interior-speckle false positives.
	if nodataFlood {
		if !bandCfg.HasNodata {
			log.Fatal("--nodata-flood requires --nodata to be set (or auto-detected)")
		}
		if bandCfg.NodataTolerance == 0 {
			log.Print("note: --nodata-flood with --nodata-tolerance 0 only removes exactly-matching edge-connected pixels; consider --nodata-tolerance 20-40 for scanned/JPEG sources")
		}
		// Up to 4 sources build concurrently (each costs ~2×W*H/8 bytes while
		// under construction); pass 1 of each build is itself parallel, so the
		// per-source worker count divides the global concurrency budget.
		buildStart := time.Now()
		log.Printf("Building nodata flood masks for %d source(s)...", len(sources))
		parallelSources := max(min(len(sources), 4, concurrency), 1)
		workersPerSource := max(concurrency/parallelSources, 1)
		sem := make(chan struct{}, parallelSources)
		var floodWg sync.WaitGroup
		var floodErrMu sync.Mutex
		var floodErr error
		for i, src := range sources {
			floodWg.Add(1)
			go func(i int, src *cog.Reader) {
				defer floodWg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				t0 := time.Now()
				if err := src.BuildFloodMask(workersPerSource); err != nil {
					floodErrMu.Lock()
					if floodErr == nil {
						floodErr = fmt.Errorf("BuildFloodMask for %s: %w", src.Path(), err)
					}
					floodErrMu.Unlock()
					return
				}
				if verbose {
					log.Printf("Flood mask built for source %d/%d (%s) in %v",
						i+1, len(sources), filepath.Base(src.Path()), time.Since(t0).Round(time.Millisecond))
				}
			}(i, src)
		}
		floodWg.Wait()
		if floodErr != nil {
			log.Fatal(floodErr)
		}
		log.Printf("Flood masks built in %v", time.Since(buildStart).Round(time.Millisecond))
	}

	// Compute merged bounds in WGS84.
	mergedBounds, err := cog.MergedBoundsWGS84(sources)
	if err == nil {
		err = checkBoundsWGS84(mergedBounds, sources)
	}
	if err != nil {
		log.Fatalf("Bounds: %v", err)
	}
	if verbose {
		log.Printf("Merged bounds (WGS84): lon [%.6f, %.6f], lat [%.6f, %.6f]",
			mergedBounds.MinLon, mergedBounds.MaxLon, mergedBounds.MinLat, mergedBounds.MaxLat)
	}

	// Determine zoom levels: the auto max zoom keeps the finest source's
	// detail.
	pixelSizeMeters := finestPixelSize(sources, mergedBounds.CenterLat())
	autoMax := coord.MaxZoomForResolution(pixelSizeMeters, mergedBounds.CenterLat(), tileSize)
	if maxZoom < 0 {
		maxZoom = autoMax
	}
	if minZoom < 0 {
		// Use the highest zoom level at which the entire image fits in a single
		// tile, so the output always has a useful overview at the minimum zoom.
		minZoom = coord.MinZoomForSingleTile(
			mergedBounds.MinLon, mergedBounds.MinLat,
			mergedBounds.MaxLon, mergedBounds.MaxLat,
		)
		if minZoom > maxZoom {
			minZoom = maxZoom
		}
	}
	if minZoom > maxZoom {
		log.Fatalf("invalid zoom range %d-%d: --min-zoom must be <= --max-zoom (auto max zoom is %d)", minZoom, maxZoom, autoMax)
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
	switch {
	case format == "webp" && !encode.WebPLossy:
		fmt.Printf("  %-14s webp (lossless; --quality ignored in this build)\n", "Format:")
	case format == "jpeg" || format == "webp":
		fmt.Printf("  %-14s %s (quality: %d)\n", "Format:", format, quality)
	default:
		fmt.Printf("  %-14s %s\n", "Format:", format)
	}
	fmt.Printf("  %-14s %dpx\n", "Tile size:", tileSize)
	fmt.Printf("  %-14s %d – %d (auto-max: %d)\n", "Zoom:", minZoom, maxZoom, autoMax)
	if resamplingGamma != 1.0 {
		fmt.Printf("  %-14s %s (gamma %.2g)\n", "Resampling:", resampling, resamplingGamma)
	} else {
		fmt.Printf("  %-14s %s\n", "Resampling:", resampling)
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
	} else {
		fmt.Printf("  %-14s %d MB (auto)\n", "Spill queue:", tile.ComputeMemoryLimit(tile.DefaultMemoryPressurePercent, false)>>20)
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

	tmpDir, cleanup, err := cli.MakeTmpDir(tmpDirFlag, outputPath, ".geotiff2pmtiles-tmp-")
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	// Build tile generation config.
	cfg := tile.Config{
		MinZoom:          minZoom,
		MaxZoom:          maxZoom,
		TileSize:         tileSize,
		Concurrency:      concurrency,
		Verbose:          verbose,
		Encoder:          enc,
		Bounds:           mergedBounds,
		Resampling:       resamplingMode,
		ResamplingGamma:  resamplingGamma,
		IsTerrarium:      format == "terrarium",
		FloatNodata:      floatNodata,
		NodataColor:      nodataFill,
		FillMissing:      missingFill,
		MemoryLimitBytes: memoryLimitBytes,
		OutputDir:        tmpDir,
	}

	// Build description for PMTiles metadata.
	description := buildDescription(sources, mergedBounds, gaps, format, quality, tileSize, minZoom, maxZoom, resampling, resamplingGamma, nodataFill, missingFill, bandCfg)

	// Create PMTiles writer.
	encoding := ""
	if format == "terrarium" {
		encoding = "terrarium"
	}
	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      mergedBounds,
		TileFormat:  enc.PMTileType(),
		TileSize:    tileSize,
		TempDir:     tmpDir,
		Description: description,
		Attribution: attribution,
		Type:        layerType,
		Encoding:    encoding,
	})
	if err != nil {
		cleanup()
		log.Fatalf("Creating PMTiles writer: %v", err)
	}

	// Generate tiles.
	genStart := time.Now()
	stats, err := tile.Generate(cfg, sources, writer)
	if err != nil {
		writer.Abort()
		cleanup()
		log.Fatalf("Tile generation: %v", err)
	}

	if verbose {
		log.Printf("Generated %d tiles (%d uniform, %d empty) in %v",
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

// collectTIFFs resolves input paths to a list of .tif files.
// Directories are walked recursively to find TIFF files in subfolders.
func collectTIFFs(paths []string) ([]string, error) {
	var result []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			// cmd.exe and PowerShell do not expand wildcards, so patterns
			// reach us verbatim; expand them here.
			if strings.ContainsAny(p, "*?[") {
				matches, gerr := filepath.Glob(p)
				if gerr == nil && len(matches) > 0 {
					for _, m := range matches {
						if isTIFF(m) {
							result = append(result, m)
						}
					}
					continue
				}
			}
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if info.IsDir() {
			err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && isTIFF(d.Name()) {
					result = append(result, path)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("walk %s: %w", p, err)
			}
		} else if isTIFF(p) {
			result = append(result, p)
		}
	}
	return result, nil
}

// jpegAlphaWarning is the warning for a colour flag whose alpha jpeg cannot
// store: the encoder drops the alpha and writes the colour's RGB, opaque.
func jpegAlphaWarning(flagName string, c *color.RGBA) string {
	return fmt.Sprintf("WARNING: %s alpha %d cannot be stored in jpeg; those areas will be opaque rgb(%d,%d,%d)",
		flagName, c.A, c.R, c.G, c.B)
}

func isTIFF(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tif") || strings.HasSuffix(lower, ".tiff")
}

func buildDescription(sources []*cog.Reader, mergedBounds cog.Bounds, gaps []crsGap,
	format string, quality int, tileSize int, minZoom, maxZoom int, resampling string, resamplingGamma float64, nodataFill, missingFill *color.RGBA, bandCfg cog.BandConfig) string {

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
	if resamplingGamma != 1.0 {
		b.WriteString(fmt.Sprintf("  Resampling: %s (gamma %.2g)\n", resampling, resamplingGamma))
	} else {
		b.WriteString(fmt.Sprintf("  Resampling: %s\n", resampling))
	}
	if nodataFill != nil {
		b.WriteString(fmt.Sprintf("  Nodata color: %s\n", cli.FormatColor(nodataFill)))
	}
	if missingFill != nil {
		b.WriteString(fmt.Sprintf("  Fill missing: %s\n", cli.FormatColor(missingFill)))
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

	var epsgs []string
	for _, epsg := range sourceEPSGs(sources) {
		epsgs = append(epsgs, fmt.Sprintf("EPSG:%d", epsg))
	}
	b.WriteString(fmt.Sprintf("Source: %d GeoTIFF file(s), %s\n", len(sources), strings.Join(epsgs, ", ")))

	// A merged extent and pixel size in CRS units only mean something
	// when all sources share the CRS.
	pixelSize := fmt.Sprintf("%.3g m (finest source, on the ground)", finestPixelSize(sources, mergedBounds.CenterLat()))
	if len(epsgs) == 1 {
		mergedMinX, mergedMinY := math.MaxFloat64, math.MaxFloat64
		mergedMaxX, mergedMaxY := -math.MaxFloat64, -math.MaxFloat64
		finest := math.Inf(1)
		for _, src := range sources {
			minX, minY, maxX, maxY := src.BoundsInCRS()
			mergedMinX, mergedMinY = min(mergedMinX, minX), min(mergedMinY, minY)
			mergedMaxX, mergedMaxY = max(mergedMaxX, maxX), max(mergedMaxY, maxY)
			finest = min(finest, src.PixelSize())
		}
		b.WriteString(fmt.Sprintf("  Extent (CRS): [%.2f, %.2f] - [%.2f, %.2f]\n",
			mergedMinX, mergedMinY, mergedMaxX, mergedMaxY))
		pixelSize = fmt.Sprintf("%g", finest)
	}
	b.WriteString(fmt.Sprintf("  Extent (WGS84): [%.6f, %.6f] - [%.6f, %.6f]\n",
		mergedBounds.MinLon, mergedBounds.MinLat, mergedBounds.MaxLon, mergedBounds.MaxLat))
	b.WriteString(fmt.Sprintf("  Pixel size: %s\n", pixelSize))

	b.WriteString(fmt.Sprintf("  Data: %s", sources[0].FormatDescription()))

	// Holes are only looked for within one CRS (see coverageGaps).
	switch {
	case len(epsgs) > 1:
	case len(gaps) == 0:
		b.WriteString("\n  Holes: none")
	default:
		b.WriteString(fmt.Sprintf("\n  Holes: %d gap(s)", len(gaps)))
	}

	return b.String()
}

// parseBandConfig parses CLI flags into a cog.BandConfig.
func parseBandConfig(bandsStr, alphaBandStr, rescaleStr, rescaleRange string, sources []*cog.Reader) (cog.BandConfig, error) {
	firstSrc := sources[0]
	var cfg cog.BandConfig

	// Parse --bands. 1-band input is always rendered gray by the reader.
	if bandsStr == "auto" {
		cfg.Bands = [3]int{1, 2, 3}
		if firstSrc.SamplesPerPixel() == 2 {
			cfg.Bands = [3]int{1, 1, 1}
		}
	} else {
		parts := strings.Split(bandsStr, ",")
		if len(parts) != 3 {
			return cfg, fmt.Errorf("--bands must be \"auto\" or 3 comma-separated band numbers (e.g. \"1,2,3\"), got %q", bandsStr)
		}
		for i, p := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || v < 1 {
				return cfg, fmt.Errorf("invalid band number %q (must be >= 1)", p)
			}
			cfg.Bands[i] = v
		}
	}

	// Parse --alpha-band.
	switch alphaBandStr {
	case "auto":
		cfg.AlphaBand = 0
	case "none":
		cfg.AlphaBand = -1
	default:
		v, err := strconv.Atoi(strings.TrimSpace(alphaBandStr))
		if err != nil || v < -1 {
			return cfg, fmt.Errorf("--alpha-band must be auto, none or a band number, got %q", alphaBandStr)
		}
		cfg.AlphaBand = v
	}

	for _, src := range sources {
		spp := src.SamplesPerPixel()
		if spp == 1 {
			continue
		}
		for _, b := range append(cfg.Bands[:], cfg.AlphaBand) {
			if b > spp {
				return cfg, fmt.Errorf("band %d does not exist: %s has %d band(s); set --bands/--alpha-band", b, src.Path(), spp)
			}
		}
	}

	// Parse --rescale and --rescale-range.
	// 9-16 bit samples (incl. bit-packed depths such as Sentinel-2's 15-bit
	// L2A) are widened to 16 bits by the reader and need rescaling to 8-bit.
	bits := firstSrc.BitsPerSample()
	is16 := bits > 8 && bits <= 16
	switch rescaleStr {
	case "auto":
		if is16 {
			if rescaleRange == "" {
				// Try auto-detection from GDAL metadata before erroring.
				if preset, ok := firstSrc.DetectPreset(); ok && bandsStr == "auto" && preset.Format == "" {
					if cli.IsFlagSet(flag.CommandLine, "alpha-band") {
						preset.BandCfg.AlphaBand = cfg.AlphaBand
					}
					log.Printf("Auto-detected: %s (%s)", preset.Name, preset.BandCfg)
					return preset.BandCfg, nil
				}
			}
			cfg.Rescale = cog.RescaleLinear
			minV, maxV, err := resolveRescaleRange(rescaleRange, sources, cfg)
			if err != nil {
				return cfg, err
			}
			cfg.RescaleMin = minV
			cfg.RescaleMax = maxV
		} else {
			cfg.Rescale = cog.RescaleNone
		}
	case "linear":
		minV, maxV, err := resolveRescaleRange(rescaleRange, sources, cfg)
		if err != nil {
			return cfg, err
		}
		cfg.Rescale = cog.RescaleLinear
		cfg.RescaleMin = minV
		cfg.RescaleMax = maxV
	case "log":
		minV, maxV, err := resolveRescaleRange(rescaleRange, sources, cfg)
		if err != nil {
			return cfg, err
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

// resolveRescaleRange returns the --rescale-range values, or when the flag is
// unset, the combined value range of all sources (GDAL statistics, else a
// pixel scan), logging what was selected.
func resolveRescaleRange(rescaleRange string, sources []*cog.Reader, cfg cog.BandConfig) (float64, float64, error) {
	if rescaleRange != "" {
		minV, maxV, err := parseRange(rescaleRange)
		if err != nil {
			return 0, 0, fmt.Errorf("--rescale-range: %w", err)
		}
		return minV, maxV, nil
	}
	ranges := make([]sourceRange, len(sources))
	for i, src := range sources {
		r := &ranges[i]
		r.path = src.Path()
		r.lo, r.hi, r.origin, r.err = src.ValueRange(cfg)
	}
	return mergeValueRanges(ranges)
}

// sourceRange is the result of one source's ValueRange.
type sourceRange struct {
	path   string
	lo, hi float64
	origin string
	err    error
}

// mergeValueRanges returns the union of the sources' value ranges. With
// several sources, one without usable values (all nodata, such as an
// open-ocean tile of a composite) is skipped; any other error fails.
func mergeValueRanges(ranges []sourceRange) (float64, float64, error) {
	const hint = "\n  Hint: set it explicitly, e.g. --rescale-range 0,5000"
	minV, maxV := math.Inf(1), math.Inf(-1)
	origins := map[string]bool{}
	var skipped []string
	for _, r := range ranges {
		if errors.Is(r.err, cog.ErrNoValueRange) && len(ranges) > 1 {
			skipped = append(skipped, r.path)
			continue
		}
		if r.err != nil {
			return 0, 0, fmt.Errorf("auto rescale range: %s: %w"+hint, r.path, r.err)
		}
		minV, maxV = math.Min(minV, r.lo), math.Max(maxV, r.hi)
		origins[r.origin] = true
	}
	if len(origins) == 0 {
		return 0, 0, fmt.Errorf("auto rescale range: %w in any of the %d sources"+hint, cog.ErrNoValueRange, len(ranges))
	}
	if len(skipped) > 0 {
		log.Printf("Auto rescale range: skipped %d source(s) without usable values (all nodata?), e.g. %s", len(skipped), skipped[0])
	}
	log.Printf("Auto rescale range: [%g, %g] (from %s)", minV, maxV, strings.Join(slices.Sorted(maps.Keys(origins)), " + "))
	return minV, maxV, nil
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
	if minV >= maxV {
		return 0, 0, fmt.Errorf("min (%g) must be less than max (%g)", minV, maxV)
	}
	return minV, maxV, nil
}
