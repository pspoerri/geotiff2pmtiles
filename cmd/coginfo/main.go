package main

import (
	"fmt"
	"image"
	"os"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: coginfo <file.tif>\n")
		os.Exit(1)
	}

	r, err := cog.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	fmt.Printf("File: %s\n", os.Args[1])
	fmt.Printf("EPSG: %d\n", r.EPSG())
	fmt.Printf("Full-res size: %d x %d\n", r.Width(), r.Height())
	fmt.Printf("Pixel size (CRS units): %f\n", r.PixelSize())
	fmt.Printf("IFD count: %d (1 full-res + %d overviews)\n", r.IFDCount(), r.NumOverviews())

	geo := r.GeoInfo()
	fmt.Printf("Origin: X=%f, Y=%f\n", geo.OriginX, geo.OriginY)

	minX, minY, maxX, maxY := r.BoundsInCRS()
	fmt.Printf("Bounds (CRS): X=[%f, %f], Y=[%f, %f]\n", minX, maxX, minY, maxY)

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
