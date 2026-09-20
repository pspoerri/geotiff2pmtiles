package cog

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxRangeScanTiles bounds the pixel scan in ValueRange.
const maxRangeScanTiles = 64

// ValueRange returns the min/max sample value of a 16-bit (signed or unsigned) raster for
// automatic rescaling, and where it came from. GDAL band statistics
// (STATISTICS_MINIMUM/MAXIMUM) are used when present; otherwise pixels are scanned.
func (r *Reader) ValueRange() (lo, hi float64, source string, err error) {
	if lo, hi, ok := r.statisticsRange(); ok {
		return lo, hi, "GDAL statistics", nil
	}
	lo, hi, err = r.scanRange()
	return lo, hi, "sampled pixels", err
}

func (r *Reader) statisticsRange() (lo, hi float64, ok bool) {
	md := r.ifds[0].GDALMetadata
	if md == nil {
		return 0, 0, false
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, items := range md.BandItems {
		bMin, errMin := strconv.ParseFloat(strings.TrimSpace(items["STATISTICS_MINIMUM"]), 64)
		bMax, errMax := strconv.ParseFloat(strings.TrimSpace(items["STATISTICS_MAXIMUM"]), 64)
		if errMin != nil || errMax != nil {
			continue
		}
		lo, hi = math.Min(lo, bMin), math.Max(hi, bMax)
	}
	return lo, hi, lo < hi
}

// scanRange scans the coarsest level, strided down to maxRangeScanTiles tiles.
// ponytail: plain min/max over a tile subsample; switch to percentiles if
// outliers wash out the contrast.
func (r *Reader) scanRange() (float64, float64, error) {
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

	across, down := ifd.TilesAcross(), ifd.TilesDown()
	tw, th := int(ifd.TileWidth), int(ifd.TileHeight)
	stride := (across*down + maxRangeScanTiles - 1) / maxRangeScanTiles
	lo, hi, found := uint16(math.MaxUint16), uint16(0), false
	for i := 0; i < across*down; i += stride {
		col, row := i%across, i/across
		samples, _, _, sppTile, err := r.ReadUint16Tile(level, col, row)
		if err != nil {
			return 0, 0, err
		}
		validW := min(tw, int(ifd.Width)-col*tw)
		validH := min(th, int(ifd.Height)-row*th)
		tLo, tHi, ok := minMaxSamples(samples, sppTile, tw, validW, validH, bias, uint16(nodata), hasNodata)
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
	if !found || lo == hi {
		return 0, 0, fmt.Errorf("no usable value range found in sampled pixels")
	}
	return float64(lo) - float64(bias), float64(hi) - float64(bias), nil
}

// minMaxSamples returns the min/max of the chunky samples in the
// validW×validH region of a tile tw pixels wide, skipping nodata samples.
// Samples are XOR-ed with bias (see signBias16); nodata and the result are in
// that biased space.
func minMaxSamples(samples []uint16, spp, tw, validW, validH, bias int, nodata uint16, hasNodata bool) (lo, hi uint16, ok bool) {
	lo = math.MaxUint16
	for y := 0; y < validH; y++ {
		rowOff := y * tw * spp
		if rowOff+validW*spp > len(samples) {
			break
		}
		for s := 0; s < validW*spp; s++ {
			v := samples[rowOff+s] ^ uint16(bias)
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
	return lo, hi, ok
}
