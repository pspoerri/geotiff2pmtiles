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
// (~50 µs), which is far too slow per pixel. The projection math therefore
// runs per pixel, while the datum shift WGS84 -> source datum is computed
// once per node of a shiftCellDeg grid, cached and interpolated bilinearly.
type CRSFallback struct {
	epsg      int
	datum     crs.Datum
	project   crs.Func   // source datum lon/lat -> source CRS, fast
	unproject crs.Func   // source CRS -> source datum lon/lat, fast
	refShift  [2]float64 // shift used where no datum transformation applies
	shifts    sync.Map   // [2]int32 grid node -> [2]float64 lon/lat shift (degrees)
}

const shiftCellDeg = 0.05 // ~5 km

// anywhere replaces the EPSG area of use, which wroge/crs enforces on every
// call. Rasters routinely reach past it (the standard EEA grid in EPSG:3035
// spans lon -57..73), and the projection math holds there. Longitudes run
// past ±180 for points across the antimeridian from the central meridian.
var anywhere = crs.BoundingBox{MinLon: -540, MinLat: -90, MaxLon: 540, MaxLat: 90}

// crsFallbackForEPSG returns nil if wroge/crs does not know the EPSG code.
func crsFallbackForEPSG(epsg int) Projection {
	src, err := crs.Load(epsg)
	if err != nil {
		return nil
	}
	c := &CRSFallback{epsg: epsg, datum: src.Datum}

	// Where no datum transformation covers a point (ETRS89 on the Canaries,
	// say), use the shift at the centre of the area of use rather than none:
	// it is ~0 for WGS84-like datums and within tens of meters for the rest.
	// If even that fails, fall back to no shift, like PROJ's ballpark.
	area := src.BoundingBox
	lonSpan := math.Mod(area.MaxLon-area.MinLon+360, 360) // area may cross 180
	lon, lat := math.Remainder(area.MinLon+lonSpan/2, 360), (area.MinLat+area.MaxLat)/2
	if sLon, sLat, _, err := crs.WGS84.TransformTo(src.Datum, lon, lat, 0); err == nil {
		c.refShift = [2]float64{math.Remainder(sLon-lon, 360), sLat - lat}
	}

	src.BoundingBox = anywhere
	geog := crs.CoordinateReferenceSystem{Conversion: crs.Geographic{}, Datum: src.Datum, BoundingBox: anywhere}
	if c.project, err = geog.TransformTo(src); err != nil {
		return nil
	}
	if c.unproject, err = src.TransformTo(geog); err != nil {
		return nil
	}
	return c
}

func (c *CRSFallback) EPSG() int { return c.epsg }

// ToWGS84 returns +Inf for points outside the projection's domain, which
// fall outside every source's bounds and are treated as nodata. The
// longitude is not wrapped into [-180, 180].
func (c *CRSFallback) ToWGS84(x, y float64) (lon, lat float64) {
	lon, lat, _, err := c.unproject(x, y, 0)
	if err != nil {
		return math.Inf(1), math.Inf(1)
	}
	// The shift is a function of the WGS84 position, but it changes by far
	// less than a millimeter over its own size, so evaluating it at the
	// source-datum position is exact enough.
	dLon, dLat := c.shiftAt(lon, lat)
	return lon - dLon, lat - dLat
}

// FromWGS84 returns +Inf on error, see ToWGS84.
func (c *CRSFallback) FromWGS84(lon, lat float64) (x, y float64) {
	dLon, dLat := c.shiftAt(lon, lat)
	x, y, _, err := c.project(lon+dLon, lat+dLat, 0)
	if err != nil {
		return math.Inf(1), math.Inf(1)
	}
	return x, y
}

// shiftAt interpolates the WGS84 -> source datum shift bilinearly between
// the four surrounding grid nodes.
func (c *CRSFallback) shiftAt(lon, lat float64) (dLon, dLat float64) {
	gx, gy := lon/shiftCellDeg, lat/shiftCellDeg
	fx, fy := math.Floor(gx), math.Floor(gy)
	ix, iy := int32(fx), int32(fy)
	tx, ty := gx-fx, gy-fy

	s00, s10 := c.datumShift(ix, iy), c.datumShift(ix+1, iy)
	s01, s11 := c.datumShift(ix, iy+1), c.datumShift(ix+1, iy+1)
	var shift [2]float64
	for i := range shift {
		bottom := s00[i] + (s10[i]-s00[i])*tx
		top := s01[i] + (s11[i]-s01[i])*tx
		shift[i] = bottom + (top-bottom)*ty
	}
	return shift[0], shift[1]
}

// datumShift returns the cached WGS84 -> source datum lon/lat shift at a
// grid node, or the reference shift if no transformation covers the node.
func (c *CRSFallback) datumShift(ix, iy int32) [2]float64 {
	node := [2]int32{ix, iy}
	if v, ok := c.shifts.Load(node); ok {
		return v.([2]float64)
	}
	// Transformation areas are given in -180..180.
	lon, lat := math.Remainder(float64(ix)*shiftCellDeg, 360), float64(iy)*shiftCellDeg
	shift := c.refShift
	if sLon, sLat, _, err := crs.WGS84.TransformTo(c.datum, lon, lat, 0); err == nil {
		shift = [2]float64{math.Remainder(sLon-lon, 360), sLat - lat}
	}
	c.shifts.Store(node, shift)
	return shift
}
