package main

import (
	"fmt"
	"math"
	"os"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

func main() {
	path := os.Args[1]
	r, err := cog.Open(path)
	if err != nil {
		fmt.Printf("Error opening: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	fmt.Printf("IsFloat: %v\n", r.IsFloat())
	fmt.Printf("EPSG: %d\n", r.EPSG())
	fmt.Printf("NoData: %q\n", r.NoData())
	fmt.Printf("Width: %d, Height: %d\n", r.Width(), r.Height())
	fmt.Printf("PixelSize: %f\n", r.PixelSize())
	minX, minY, maxX, maxY := r.BoundsInCRS()
	fmt.Printf("BoundsInCRS: [%f, %f, %f, %f]\n", minX, minY, maxX, maxY)
	fmt.Printf("IFDCount: %d\n", r.IFDCount())

	for i := 0; i < r.IFDCount(); i++ {
		ts := r.IFDTileSize(i)
		fmt.Printf("IFD %d: %dx%d, tileSize=%dx%d, pixelSize=%f\n",
			i, r.IFDWidth(i), r.IFDHeight(i), ts[0], ts[1], r.IFDPixelSize(i))
	}

	// Debug raw tile access
	fmt.Println("\n--- Raw Tile Debug ---")
	info := r.DebugIFD(0)
	fmt.Printf("IFD 0: compression=%d, spp=%d, bps=%v, sampleFormat=%v, predictor=%d\n",
		info.Compression, info.SamplesPerPixel, info.BitsPerSample, info.SampleFormat, info.Predictor)
	fmt.Printf("Num tiles: %d (offsets), %d (bytecounts)\n", len(info.TileOffsets), len(info.TileByteCounts))
	if len(info.TileOffsets) > 0 {
		fmt.Printf("First tile: offset=%d, size=%d\n", info.TileOffsets[0], info.TileByteCounts[0])
		// Show first 20 bytes of raw tile data
		rawBytes := r.RawBytes(info.TileOffsets[0], 20)
		fmt.Printf("First 20 bytes: %x\n", rawBytes)
	}

	// Try reading a float tile
	fmt.Println("\n--- Float Tile Read ---")
	data, w, h, err := r.ReadFloatTile(0, 0, 0)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else if data == nil {
		fmt.Printf("Tile is empty (nil)\n")
	} else {
		fmt.Printf("Success: %dx%d, %d values\n", w, h, len(data))
		nanCount := 0
		minVal := math.Inf(1)
		maxVal := math.Inf(-1)
		for _, v := range data {
			fv := float64(v)
			if math.IsNaN(fv) {
				nanCount++
				continue
			}
			if fv < minVal {
				minVal = fv
			}
			if fv > maxVal {
				maxVal = fv
			}
		}
		fmt.Printf("NaN: %d/%d, range: [%.2f, %.2f]\n", nanCount, len(data), minVal, maxVal)
	}
}
