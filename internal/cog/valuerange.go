package cog

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// maxRangeScanTiles bounds the pixel scan in ValueRange.
const maxRangeScanTiles = 64

// ErrNoValueRange is returned by ValueRange when every sampled pixel is
// nodata (or all have one value), e.g. for a source that is all ocean.
var ErrNoValueRange = errors.New("no usable value range found in sampled pixels")

// ValueRange returns the min/max sample value of a 9..16-bit (signed or
// unsigned, bit-packed included) raster for automatic rescaling, and where
// it came from. Only the bands cfg renders count
// (see renderedBands): bands left out by cfg.Bands and the alpha band would
// otherwise stretch the range. GDAL band statistics
// (STATISTICS_MINIMUM/MAXIMUM) are used when every rendered band has them;
// otherwise pixels are scanned.
func (r *Reader) ValueRange(cfg BandConfig) (lo, hi float64, source string, err error) {
	if lo, hi, ok := r.statisticsRange(renderedBands(cfg, int(r.ifds[0].SamplesPerPixel))); ok {
		return lo, hi, "GDAL statistics", nil
	}
	lo, hi, err = r.scanRange(cfg)
	return lo, hi, "sampled pixels", err
}

// renderedBands returns the 0-indexed bands that decodeRawTile maps to R, G
// and B under cfg, for a raster with spp samples per pixel: without
// duplicates, without the alpha band, and without bands the raster lacks.
func renderedBands(cfg BandConfig, spp int) []int {
	bands := cfg.Bands
	for i := range bands {
		if bands[i] == 0 {
			bands[i] = i + 1
		}
	}
	if spp <= 1 {
		spp = 1
		bands = [3]int{1, 1, 1} // single-band data renders gray
	}
	var out []int
	for _, b := range bands {
		if b != cfg.AlphaBand && b <= spp && !slices.Contains(out, b-1) {
			out = append(out, b-1)
		}
	}
	return out
}

func (r *Reader) statisticsRange(bands []int) (lo, hi float64, ok bool) {
	md := r.ifds[0].GDALMetadata
	if md == nil || len(bands) == 0 {
		return 0, 0, false
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, b := range bands {
		items := md.BandItems[b]
		bMin, errMin := strconv.ParseFloat(strings.TrimSpace(items["STATISTICS_MINIMUM"]), 64)
		bMax, errMax := strconv.ParseFloat(strings.TrimSpace(items["STATISTICS_MAXIMUM"]), 64)
		if errMin != nil || errMax != nil {
			return 0, 0, false // a band without statistics would be clipped
		}
		lo, hi = math.Min(lo, bMin), math.Max(hi, bMax)
	}
	return lo, hi, lo < hi
}

// scanRange scans up to maxRangeScanTiles tiles of the coarsest level.
// ponytail: plain min/max over a tile subsample; switch to percentiles if
// outliers wash out the contrast.
func (r *Reader) scanRange(cfg BandConfig) (float64, float64, error) {
	level := len(r.ifds) - 1
	ifd := &r.ifds[level]
	// Depth rather than bytesPerSample, so bit-packed widths such as 15 are
	// scanned too; the tile read below unpacks them.
	if bits := ifd.bitsPerSample(); bits <= 8 || bits > 16 || ifd.Compression == 7 {
		return 0, 0, fmt.Errorf("pixel scan supports only non-JPEG 9..16-bit data")
	}
	nodata, err := strconv.ParseFloat(strings.TrimSpace(r.ifds[0].NoData), 64)
	bias := r.ifds[0].sampleBias()
	nodata += float64(bias)
	hasNodata := err == nil && nodata >= 0 && nodata <= math.MaxUint16
	bands := renderedBands(cfg, int(ifd.SamplesPerPixel))

	tw, th := int(ifd.TileWidth), int(ifd.TileHeight)
	cols, rows := scanGrid(ifd.TilesAcross(), ifd.TilesDown())
	lo, hi, found := uint16(math.MaxUint16), uint16(0), false
	for _, row := range rows {
		for _, col := range cols {
			samples, _, _, sppTile, err := r.ReadUint16Tile(level, col, row)
			if err != nil {
				return 0, 0, err
			}
			validW := min(tw, int(ifd.Width)-col*tw)
			validH := min(th, int(ifd.Height)-row*th)
			tLo, tHi, ok := minMaxSamples(samples, sppTile, bands, tw, validW, validH, bias, uint16(nodata), hasNodata)
			if ok {
				found = true
				if tLo < lo {
					lo = tLo
				}
				if tHi > hi {
					hi = tHi
				}
			}
		}
	}
	if !found || lo == hi {
		return 0, 0, ErrNoValueRange
	}
	return float64(lo) - float64(bias), float64(hi) - float64(bias), nil
}

// scanGrid picks up to maxRangeScanTiles tiles of an across×down grid, spread
// over both axes: the centres of nx×ny equal bins, so every tile when there
// are few enough. (A stride through the row-major tile index aliases: with 64
// tiles across it only ever read column 0.)
func scanGrid(across, down int) (cols, rows []int) {
	if across <= 0 || down <= 0 {
		return nil, nil
	}
	ny := min(down, 8) // √maxRangeScanTiles, unless the grid is short
	nx := min(across, maxRangeScanTiles/ny)
	ny = min(down, maxRangeScanTiles/nx) // a narrow grid gets more rows
	for i := range nx {
		cols = append(cols, (2*i+1)*across/(2*nx))
	}
	for j := range ny {
		rows = append(rows, (2*j+1)*down/(2*ny))
	}
	return cols, rows
}

// minMaxSamples returns the min/max of the given bands (sample offsets) of
// the chunky samples in the validW×validH region of a tile tw pixels wide,
// skipping nodata samples. Samples are XOR-ed with bias (see signBias16);
// nodata and the result are in that biased space.
func minMaxSamples(samples []uint16, spp int, bands []int, tw, validW, validH, bias int, nodata uint16, hasNodata bool) (lo, hi uint16, ok bool) {
	lo = math.MaxUint16
	for y := 0; y < validH; y++ {
		rowOff := y * tw * spp
		if rowOff+validW*spp > len(samples) {
			break
		}
		for px := rowOff; px < rowOff+validW*spp; px += spp {
			for _, b := range bands {
				v := samples[px+b] ^ uint16(bias)
				if hasNodata && v == nodata {
					continue
				}
				ok = true
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
	}
	return lo, hi, ok
}
