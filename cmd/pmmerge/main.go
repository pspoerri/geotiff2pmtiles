// pmmerge merges several PMTiles archives into one. The inputs may have
// different max zooms: the output goes up to the highest, and an input's
// tiles are scaled up above its own max zoom. Where inputs overlap, the one
// with the higher max zoom wins (ties: the earlier input), and its
// transparent pixels show the next one through.
//
// Usage:
//
//	pmmerge [flags] -o <output.pmtiles> <input.pmtiles>...
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
		output      string
		format      string
		quality     int
		minZoom     int
		maxZoom     int
		concurrency int
		attribution string
		layerType   string
		upsampling  string
		resampling  string
		showVersion bool
	)
	flag.StringVar(&output, "o", "", "Output .pmtiles file (required)")
	flag.StringVar(&format, "format", "", "Tile encoding of re-encoded tiles: jpeg, png, webp (default: the inputs' format; required when they differ)")
	flag.IntVar(&quality, "quality", 85, "JPEG/WebP quality 1-100 (ignored for png)")
	flag.IntVar(&minZoom, "min-zoom", -1, "Minimum zoom level; -1 = auto: the highest zoom at which the merged extent fits in one tile. Levels below the highest input min zoom are downsampled from the merged level there")
	flag.IntVar(&maxZoom, "max-zoom", -1, "Maximum zoom level; -1 = the highest input max zoom")
	flag.IntVar(&concurrency, "concurrency", runtime.NumCPU(), "Number of parallel workers (>= 1)")
	flag.StringVar(&attribution, "attribution", "", "Attribution string (default: the inputs' attributions, joined)")
	flag.StringVar(&layerType, "type", "", "Layer type: baselayer, overlay (default: the first input's)")
	flag.StringVar(&resampling, "resampling", "bicubic", "Downsampling method for levels added below the inputs: lanczos, bicubic, bilinear, nearest, mode")
	flag.StringVar(&upsampling, "upsampling", "nearest", "How an input's tiles are scaled up above its max zoom: nearest (exact values, blocky; for elevation and classes) or bilinear (smooth; terrarium is interpolated as elevation)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: pmmerge [flags] -o <output.pmtiles> <input.pmtiles>...\n\n")
		fmt.Fprintf(os.Stderr, "Merge PMTiles archives, which may have different max zooms. Where they\n")
		fmt.Fprintf(os.Stderr, "overlap, the finer input wins (ties: the earlier one) and its transparent\n")
		fmt.Fprintf(os.Stderr, "pixels show the next one through. Above its own max zoom an input's tiles\n")
		fmt.Fprintf(os.Stderr, "are scaled up (--upsampling).\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Printf("pmmerge %s (commit %s, built %s)\n", version, commit, buildDate)
		fmt.Printf("formats: %s\n", encode.Formats())
		os.Exit(0)
	}
	inputPaths := flag.Args()
	if output == "" || len(inputPaths) < 2 {
		flag.Usage()
		os.Exit(1)
	}
	if !strings.EqualFold(filepath.Ext(output), ".pmtiles") {
		log.Fatal("Output file must have .pmtiles extension")
	}
	if quality < 1 || quality > 100 {
		log.Fatalf("--quality must be 1-100, got %d", quality)
	}
	if concurrency < 1 {
		log.Fatalf("--concurrency must be >= 1, got %d", concurrency)
	}
	upsamplingMode, err := tile.ParseResampling(upsampling)
	if err != nil || (upsamplingMode != tile.ResamplingNearest && upsamplingMode != tile.ResamplingBilinear) {
		log.Fatalf("--upsampling must be nearest or bilinear, got %q", upsampling)
	}
	resamplingMode, err := tile.ParseResampling(resampling)
	if err != nil {
		log.Fatalf("--resampling: %v", err)
	}
	if len(inputPaths) > tile.MaxMergeInputs {
		log.Fatalf("%d inputs; pmmerge takes at most %d (merge in steps)", len(inputPaths), tile.MaxMergeInputs)
	}

	start := time.Now()
	var (
		inputs       []tile.MergeInput
		formats      []string
		attributions []string
		encodings    []string
		tileSize     int
		bounds       = cog.Bounds{MinLon: 180, MinLat: 90, MaxLon: -180, MaxLat: -90}
		inputMinZoom = 0
		inputMaxZoom = 0
		description  strings.Builder
		paths        []string // of the inputs with tiles
		names        []string
		descriptions []string
		sources      []map[string]any // each input's own metadata, for provenance
	)
	fmt.Fprintf(&description, "Processing: pmmerge %s\n  Upsampling: %s\n", version, upsampling)
	for _, p := range inputPaths {
		if sameFile(p, output) {
			log.Fatalf("Output %s is also an input", output)
		}
		r, err := pmtiles.OpenReader(p)
		if err != nil {
			log.Fatalf("Opening %s: %v", p, err)
		}
		defer r.Close()
		if r.NumTiles() == 0 {
			log.Printf("Skipping %s: it has no tiles", p)
			continue
		}
		h := r.Header()
		f := pmtiles.TileTypeString(h.TileType)
		size := tile.SourceTileSize(r, f)
		if tileSize == 0 {
			tileSize = size
		} else if size != tileSize {
			log.Fatalf("%s has %d px tiles, the inputs before it %d px; resizing tiles is not supported", p, size, tileSize)
		}
		meta, err := r.ReadMetadata()
		if err != nil {
			log.Fatalf("Reading metadata of %s: %v", p, err)
		}
		if a, _ := meta["attribution"].(string); a != "" && !slices.Contains(attributions, a) {
			attributions = append(attributions, a)
		}
		if layerType == "" {
			layerType, _ = meta["type"].(string)
		}
		n, _ := meta["name"].(string)
		d, _ := meta["description"].(string)
		names, descriptions = append(names, n), append(descriptions, d)
		src := map[string]any{"file": filepath.Base(p), "minzoom": int(h.MinZoom), "maxzoom": int(h.MaxZoom)}
		for k, v := range meta {
			if !slices.Contains(pmtiles.DerivedMetadataKeys, k) {
				src[k] = v
			}
		}
		sources = append(sources, src)
		paths = append(paths, p)
		enc, _ := meta["encoding"].(string)
		encodings = append(encodings, enc)
		formats = append(formats, f)
		inputs = append(inputs, tile.MergeInput{Reader: r, Format: f, TileType: h.TileType})

		b := h.Bounds()
		bounds.MinLon, bounds.MinLat = min(bounds.MinLon, b.MinLon), min(bounds.MinLat, b.MinLat)
		bounds.MaxLon, bounds.MaxLat = max(bounds.MaxLon, b.MaxLon), max(bounds.MaxLat, b.MaxLat)
		inputMinZoom = max(inputMinZoom, int(h.MinZoom))
		inputMaxZoom = max(inputMaxZoom, int(h.MaxZoom))
		fmt.Fprintf(&description, "  Input: %s (%s, zoom %d-%d)\n", filepath.Base(p), f, h.MinZoom, h.MaxZoom)
	}

	if len(inputs) == 0 {
		log.Fatal("None of the inputs has tiles")
	}
	distinct := func(s []string) int { return len(slices.Compact(slices.Sorted(slices.Values(s)))) }
	if distinct(encodings) > 1 {
		log.Fatalf("Inputs have different pixel encodings %q; cannot merge them", encodings)
	}
	terrarium := encodings[0] == "terrarium"
	if format == "" {
		if distinct(formats) > 1 {
			log.Fatalf("Inputs have different formats %q; pass --format", formats)
		}
		format = formats[0]
	}
	format = strings.ToLower(format)
	if format == "jpg" {
		format = "jpeg"
	}
	enc, err := encode.NewEncoder(format, quality)
	if err != nil {
		log.Fatalf("Encoder: %v", err)
	}
	if terrarium && format != "png" {
		log.Printf("Warning: inputs are terrarium-encoded elevation data; lossy %s encoding will corrupt elevations", format)
	}
	if maxZoom < 0 {
		maxZoom = inputMaxZoom
	}
	if minZoom < 0 {
		// As geotiff2pmtiles: down to the zoom where all data fits in one
		// tile, never dropping a level every input has.
		minZoom = min(inputMinZoom, coord.MinZoomForSingleTile(bounds.MinLon, bounds.MinLat, bounds.MaxLon, bounds.MaxLat), maxZoom)
	}
	if minZoom > maxZoom {
		log.Fatalf("invalid zoom range %d-%d: --min-zoom must be <= --max-zoom", minZoom, maxZoom)
	}
	if maxZoom > inputMaxZoom {
		log.Fatalf("--max-zoom %d exceeds the highest input max zoom %d; pmmerge cannot add detail (let the viewer overzoom)", maxZoom, inputMaxZoom)
	}
	for i, in := range inputs {
		if z := int(in.Reader.Header().MaxZoom); z < maxZoom {
			log.Printf("WARNING: %s has max zoom %d; its tiles are scaled up (%s) to fill zoom %d-%d",
				paths[i], z, upsampling, z+1, maxZoom)
		}
	}
	if attribution == "" {
		attribution = strings.Join(attributions, "; ")
	}
	encoding := ""
	if terrarium {
		encoding = "terrarium"
	}
	// Name and description carry over when every input agrees on them.
	name := "pmmerge"
	if distinct(names) == 1 && names[0] != "" {
		name = names[0]
	}

	fmt.Printf("pmmerge %s (commit %s, built %s)\n", version, commit, buildDate)
	fmt.Printf("  %-14s %d archives\n", "Inputs:", len(inputs))
	fmt.Printf("  %-14s %s\n", "Format:", format)
	fmt.Printf("  %-14s %dpx\n", "Tile size:", tileSize)
	fmt.Printf("  %-14s %d – %d (inputs: %d – %d)\n", "Zoom:", minZoom, maxZoom, inputMinZoom, inputMaxZoom)
	if base := min(inputMinZoom, maxZoom); minZoom < base {
		fmt.Printf("  %-14s %d – %d, downsampled (%s) from the merged zoom %d\n", "Added levels:", minZoom, base-1, resampling, base)
		fmt.Fprintf(&description, "  Added zoom %d-%d: downsampled (%s)\n", minZoom, base-1, resampling)
	}
	if distinct(descriptions) == 1 && descriptions[0] != "" {
		fmt.Fprintf(&description, "\n%s", descriptions[0])
	}
	fmt.Printf("  %-14s %s\n", "Upsampling:", upsampling)
	fmt.Printf("  %-14s %s\n", "Output:", output)

	// Nothing goes to the temp dir (the StreamWriter needs none); it is made
	// for the cleanup of <output>.partial on errors and Ctrl-C.
	_, cleanup, err := cli.MakeTmpDir("", output, ".pmmerge-tmp-")
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	// Tiles arrive in tile-ID order, so they go straight into the archive:
	// the peak disk use is the output alone.
	writer, err := pmtiles.NewStreamWriter(output, pmtiles.WriterOptions{
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      bounds,
		TileFormat:  enc.PMTileType(),
		TileSize:    tileSize,
		Name:        name,
		Description: description.String(),
		Attribution: attribution,
		Type:        layerType,
		Encoding:    encoding,
		Extra:       map[string]any{"sources": sources},
	})
	if err != nil {
		cleanup()
		log.Fatalf("Creating PMTiles writer: %v", err)
	}
	stats, err := tile.Merge(tile.MergeConfig{
		Encoder:     enc,
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		TileSize:    tileSize,
		Concurrency: concurrency,
		Upsampling:  upsamplingMode,
		Resampling:  resamplingMode,
		IsTerrarium: terrarium,
	}, inputs, writer)
	if err != nil {
		writer.Abort()
		cleanup()
		log.Fatalf("Merge: %v", err)
	}
	if err := writer.Finalize(); err != nil {
		cleanup()
		log.Fatalf("Finalizing PMTiles: %v", err)
	}
	fi, _ := os.Stat(output)
	fmt.Printf("Done: %d tiles, %s, %v → %s\n", stats.TileCount, cli.HumanSize(fi.Size()), time.Since(start).Round(time.Millisecond), output)
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
