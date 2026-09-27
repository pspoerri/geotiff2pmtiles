// coginfo prints the georeferencing, levels and GDAL metadata of a GeoTIFF
// or COG and test-reads a tile of every level.
//
// Usage:
//
//	coginfo [flags] <file.tif>
package main

import (
	"flag"
	"fmt"
	"image"
	"io"
	"math"
	"os"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	raw := flag.Bool("raw", false, "Also print the raw tags of the full-resolution IFD (compression, samples, sample format, predictor, nodata), its first tile's offset, size and bytes, and the value range of the first tile of float or signed input")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: coginfo [flags] <file.tif>\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Print the georeferencing, levels and GDAL metadata of a GeoTIFF/COG\n")
		fmt.Fprintf(flag.CommandLine.Output(), "and test-read a tile of every level.\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Printf("coginfo %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := flag.Arg(0)

	r, err := cog.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	fmt.Printf("File: %s\n", path)
	fmt.Printf("EPSG: %d\n", r.EPSG())
	fmt.Printf("Full-res size: %d x %d\n", r.Width(), r.Height())
	fmt.Printf("Pixel size (CRS units): %f\n", r.PixelSize())
	fmt.Printf("IFD count: %d (1 full-res + %d overviews)\n", r.IFDCount(), r.NumOverviews())

	geo := r.GeoInfo()
	fmt.Printf("Origin: X=%f, Y=%f\n", geo.OriginX, geo.OriginY)

	minX, minY, maxX, maxY := r.BoundsInCRS()
	fmt.Printf("Bounds (CRS): X=[%f, %f], Y=[%f, %f]\n", minX, maxX, minY, maxY)

	// Print GDAL metadata if present.
	if md := r.GDALMeta(); md != nil {
		fmt.Printf("\nGDAL Metadata:\n")
		for k, v := range md.Items {
			fmt.Printf("  %s: %s\n", k, v)
		}
		for sample, items := range md.BandItems {
			for k, v := range items {
				fmt.Printf("  [band %d] %s: %s\n", sample, k, v)
			}
		}
	}

	// Print auto-detected preset if available.
	if preset, ok := r.DetectPreset(); ok {
		fmt.Printf("\nDetected preset: %s\n", preset.Name)
		if preset.Format != "" {
			fmt.Printf("  Format: %s\n", preset.Format)
		}
		if preset.BandCfg.Rescale != cog.RescaleNone {
			fmt.Printf("  Bands: %d,%d,%d\n", preset.BandCfg.Bands[0], preset.BandCfg.Bands[1], preset.BandCfg.Bands[2])
			fmt.Printf("  Rescale: linear [%.0f, %.0f]\n", preset.BandCfg.RescaleMin, preset.BandCfg.RescaleMax)
		}
	}

	if *raw {
		printRaw(os.Stdout, r)
	}

	// Try reading a tile at each IFD level to check compression support
	for level := 0; level < r.IFDCount(); level++ {
		ts := r.IFDTileSize(level)
		w := r.IFDWidth(level)
		h := r.IFDHeight(level)
		ps := r.IFDPixelSize(level)
		fmt.Printf("\n  IFD %d: %dx%d, tile %dx%d, pixel size=%f\n", level, w, h, ts[0], ts[1], ps)

		tile, err := r.ReadTile(level, 0, 0)
		if err != nil {
			fmt.Printf("  ReadTile(level=%d, 0, 0): ERROR: %v\n", level, err)
		} else {
			bounds := tile.Bounds()
			fmt.Printf("  ReadTile(level=%d, 0, 0): OK, image: %dx%d, type: %T\n", level, bounds.Dx(), bounds.Dy(), tile)

			// Sample a few pixels to verify content
			if level == 0 {
				samplePixels(tile, 5)
			}
		}
	}
}

// printRaw prints what the reader parsed from the full-resolution IFD and
// the first tile as stored.
func printRaw(w io.Writer, r *cog.Reader) {
	info := r.DebugIFD(0)
	fmt.Fprintf(w, "\nRaw IFD 0:\n")
	fmt.Fprintf(w, "  Data: %s\n", r.FormatDescription())
	fmt.Fprintf(w, "  Compression: %d, SamplesPerPixel: %d, BitsPerSample: %v, SampleFormat: %v, Predictor: %d, Photometric: %d, PlanarConfig: %d\n",
		info.Compression, info.SamplesPerPixel, info.BitsPerSample, info.SampleFormat, info.Predictor, info.Photometric, info.PlanarConfig)
	fmt.Fprintf(w, "  IsFloat (float or signed): %v\n", r.IsFloat())
	fmt.Fprintf(w, "  NoData: %q\n", r.NoData())
	fmt.Fprintf(w, "  Tiles: %d offsets, %d byte counts\n", len(info.TileOffsets), len(info.TileByteCounts))
	if len(info.TileOffsets) > 0 && len(info.TileByteCounts) > 0 {
		fmt.Fprintf(w, "  First tile: offset=%d, size=%d\n", info.TileOffsets[0], info.TileByteCounts[0])
		fmt.Fprintf(w, "  First 20 bytes: % x\n", r.RawBytes(info.TileOffsets[0], 20))
	}
	if !r.IsFloat() {
		return
	}
	data, tw, th, err := r.ReadFloatTile(0, 0, 0)
	switch {
	case err != nil:
		fmt.Fprintf(w, "  Float tile (0,0): ERROR: %v\n", err)
	case data == nil:
		fmt.Fprintf(w, "  Float tile (0,0): empty\n")
	default:
		nan := 0
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, v := range data {
			if f := float64(v); math.IsNaN(f) {
				nan++
			} else {
				lo, hi = min(lo, f), max(hi, f)
			}
		}
		fmt.Fprintf(w, "  Float tile (0,0): %dx%d, NaN %d/%d, range [%.2f, %.2f]\n", tw, th, nan, len(data), lo, hi)
	}
}

func samplePixels(img image.Image, count int) {
	b := img.Bounds()
	step := b.Dx() / (count + 1)
	if step < 1 {
		step = 1
	}
	fmt.Printf("  Sample pixels (diagonal):\n")
	for i := 0; i < count; i++ {
		x := b.Min.X + (i+1)*step
		y := b.Min.Y + (i+1)*step
		if x >= b.Max.X || y >= b.Max.Y {
			break
		}
		rr, g, bb, a := img.At(x, y).RGBA()
		fmt.Printf("    (%d,%d): R=%d G=%d B=%d A=%d\n", x, y, rr>>8, g>>8, bb>>8, a>>8)
	}
}
