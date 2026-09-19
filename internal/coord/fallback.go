package coord

import (
	"math"
	"sync"

	"github.com/wroge/crs"
)

// CRSFallback implements the Projection interface for EPSG codes without a
// native implementation, using github.com/wroge/crs. It is slower than the
// native projections, so callers should tell the user when it is in use.
//
// wroge/crs searches for the best datum transformation path on every call
// (~50 µs), which is far too slow per pixel. FromWGS84 therefore splits the
// work: the datum shift WGS84 -> source datum is computed once per node of a
// shiftCellDeg grid, cached and interpolated bilinearly; only the projection
// math runs per pixel.
type CRSFallback struct {
	epsg      int
	toWGS84   crs.Func // exact, slow; only used for bounds
	fromWGS84 crs.Func // exact, slow; evaluated once per shift grid node
	project   crs.Func // source datum lon/lat -> source CRS, fast
	unproject crs.Func // source CRS -> source datum lon/lat, fast
	shifts    sync.Map // [2]int32 grid node -> [2]float64 lon/lat shift (degrees)
}

const shiftCellDeg = 0.05 // ~5 km

// crsFallbackForEPSG returns nil if wroge/crs does not know the EPSG code.
func crsFallbackForEPSG(epsg int) Projection {
	src, err := crs.Load(epsg)
	if err != nil {
		return nil
	}
	to, err := crs.Transform(epsg, 4326)
	if err != nil {
		return nil
	}
	from, err := crs.Transform(4326, epsg)
	if err != nil {
		return nil
	}
	geog := crs.CoordinateReferenceSystem{Conversion: crs.Geographic{}, Datum: src.Datum, BoundingBox: src.BoundingBox}
	project, err := geog.TransformTo(src)
	if err != nil {
		return nil
	}
	unproject, err := src.TransformTo(geog)
	if err != nil {
		return nil
	}
	return &CRSFallback{epsg: epsg, toWGS84: to, fromWGS84: from, project: project, unproject: unproject}
}

func (c *CRSFallback) EPSG() int { return c.epsg }

// ToWGS84 returns +Inf for points outside the projection's domain, which
// fall outside every source's bounds and are treated as nodata.
func (c *CRSFallback) ToWGS84(x, y float64) (lon, lat float64) {
	lon, lat, _, err := c.toWGS84(x, y, 0)
	if err != nil {
		return math.Inf(1), math.Inf(1)
	}
	return lon, lat
}

// FromWGS84 returns +Inf on error, see ToWGS84.
func (c *CRSFallback) FromWGS84(lon, lat float64) (x, y float64) {
	// Bilinear interpolation of the datum shift between the four
	// surrounding grid nodes.
	gx, gy := lon/shiftCellDeg, lat/shiftCellDeg
	fx, fy := math.Floor(gx), math.Floor(gy)
	ix, iy := int32(fx), int32(fy)
	tx, ty := gx-fx, gy-fy

	s00, s10 := c.datumShift(ix, iy), c.datumShift(ix+1, iy)
	s01, s11 := c.datumShift(ix, iy+1), c.datumShift(ix+1, iy+1)
	for i := 0; i < 2; i++ {
		bottom := s00[i] + (s10[i]-s00[i])*tx
		top := s01[i] + (s11[i]-s01[i])*tx
		shift := bottom + (top-bottom)*ty
		if i == 0 {
			lon += shift
		} else {
			lat += shift
		}
	}

	x, y, _, err := c.project(lon, lat, 0)
	if err != nil {
		return math.Inf(1), math.Inf(1)
	}
	return x, y
}

// datumShift returns the cached WGS84 -> source datum lon/lat shift at a grid
// node, or +Inf if the node cannot be transformed.
func (c *CRSFallback) datumShift(ix, iy int32) [2]float64 {
	node := [2]int32{ix, iy}
	if v, ok := c.shifts.Load(node); ok {
		return v.([2]float64)
	}
	lon, lat := float64(ix)*shiftCellDeg, float64(iy)*shiftCellDeg
	shift := [2]float64{math.Inf(1), math.Inf(1)}

	if x, y, _, err := c.fromWGS84(lon, lat, 0); err == nil {
		// Back to lon/lat on the source datum: invert the projection only.
		if sLon, sLat, _, err := c.unproject(x, y, 0); err == nil {
			shift = [2]float64{sLon - lon, sLat - lat}
		}
	}
	c.shifts.Store(node, shift)
	return shift
}
