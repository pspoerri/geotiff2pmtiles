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
		case proj == nil && epsg == 0:
			return fmt.Errorf("%s has no CRS; set it with --source-epsg", src.Path())
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

// coverageGaps looks for holes among the sources of each CRS separately:
// boxes in different CRSs cannot be compared.
func coverageGaps(sources []*cog.Reader) []crsGap {
	byEPSG := map[int][]*cog.Reader{}
	for _, src := range sources {
		byEPSG[src.EPSG()] = append(byEPSG[src.EPSG()], src)
	}
	var gaps []crsGap
	for _, epsg := range sourceEPSGs(sources) {
		for _, g := range cog.CheckCoverageGaps(byEPSG[epsg]) {
			gaps = append(gaps, crsGap{g, epsg})
		}
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
