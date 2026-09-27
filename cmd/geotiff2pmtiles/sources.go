package main

import (
	"fmt"
	"log"
	"math"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// sourceEPSGs returns the distinct EPSG codes of the sources, in the order
// they first appear.
func sourceEPSGs(sources []*cog.Reader) []int {
	var epsgs []int
	seen := map[int]bool{}
	for _, src := range sources {
		if epsg := src.EPSG(); !seen[epsg] {
			seen[epsg] = true
			epsgs = append(epsgs, epsg)
		}
	}
	return epsgs
}

// checkSourceCRSs fails for the first source without a geotransform or
// whose CRS has no projection, and notes each CRS that falls back to
// github.com/wroge/crs.
func checkSourceCRSs(sources []*cog.Reader) error {
	checked := map[int]bool{}
	for _, src := range sources {
		// --source-epsg supplies only the CRS.
		if !(src.PixelSize() > 0) {
			return fmt.Errorf("%s is not georeferenced: it has no geotransform (ModelPixelScale and ModelTiepoint tags, or a .tfw world file; rotated ModelTransformation grids are not supported)", src.Path())
		}
		epsg := src.EPSG()
		if checked[epsg] {
			continue
		}
		checked[epsg] = true
		switch proj := coord.ForEPSG(epsg); {
		case proj == nil && epsg == 32767: // GeoTIFF "user-defined"
			return fmt.Errorf("%s: user-defined CRS is not supported; reproject to an EPSG CRS first (e.g. gdalwarp -t_srs EPSG:4326), or set its EPSG code with --source-epsg", src.Path())
		case proj == nil:
			return fmt.Errorf("%s: unsupported EPSG code %d", src.Path(), epsg)
		default:
			if _, ok := proj.(*coord.CRSFallback); ok {
				log.Printf("Note: EPSG:%d has no native implementation, falling back to github.com/wroge/crs projection.", epsg)
			}
		}
	}
	return nil
}

// checkBoundsWGS84 fails for merged bounds no WGS84 extent has, such as
// those of a projected grid read as EPSG:4326 through --source-epsg.
// MergedBoundsWGS84 wraps the longitudes, so only the latitudes show it. A
// raster registered on pixel centres reaches half a pixel past a pole, so
// the coarsest source pixel is allowed as slack.
func checkBoundsWGS84(b cog.Bounds, sources []*cog.Reader) error {
	slack := 0.0
	for _, src := range sources {
		slack = max(slack, coord.PixelSizeInGroundMeters(src.PixelSize(), src.EPSG(), 0)*360/coord.EarthCircumference)
	}
	if b.MinLat < -90-slack || b.MaxLat > 90+slack {
		return fmt.Errorf("the sources reach latitudes %.6g to %.6g, beyond ±90°; is their CRS right (--source-epsg)?", b.MinLat, b.MaxLat)
	}
	return nil
}

// crsGap is a coverage hole in the CRS of the sources around it.
type crsGap struct {
	cog.CoverageGap
	epsg int
}

// coverageGaps looks for holes among the sources when they share one CRS.
// With mixed CRSs it looks for none: boxes in different CRSs cannot be
// compared, and a hole among the sources of one CRS may be filled by a
// source in another.
func coverageGaps(sources []*cog.Reader) []crsGap {
	epsgs := sourceEPSGs(sources)
	if len(epsgs) > 1 {
		log.Printf("Note: the inputs are in %d CRSs; the coverage hole check compares boxes within one CRS and is skipped.", len(epsgs))
		return nil
	}
	var gaps []crsGap
	for _, g := range cog.CheckCoverageGaps(sources) {
		gaps = append(gaps, crsGap{g, epsgs[0]})
	}
	return gaps
}

// finestPixelSize returns the smallest source pixel in ground metres at
// latitude lat.
func finestPixelSize(sources []*cog.Reader, lat float64) float64 {
	finest := math.Inf(1)
	for _, src := range sources {
		finest = min(finest, coord.PixelSizeInGroundMeters(src.PixelSize(), src.EPSG(), lat))
	}
	return finest
}
