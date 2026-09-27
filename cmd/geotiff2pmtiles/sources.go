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

// checkSourceCRSs fails for the first source whose CRS has no projection,
// and notes each CRS that falls back to github.com/wroge/crs.
func checkSourceCRSs(sources []*cog.Reader) error {
	checked := map[int]bool{}
	for _, src := range sources {
		epsg := src.EPSG()
		if checked[epsg] {
			continue
		}
		checked[epsg] = true
		switch proj := coord.ForEPSG(epsg); {
		case proj == nil && epsg == 0: // the reader guesses a CRS for any pixel grid
			return fmt.Errorf("%s is not georeferenced: it has no GeoTIFF tags and no .tfw world file", src.Path())
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
