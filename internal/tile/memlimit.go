package tile

import (
	"log"
	"runtime"
)

// DefaultMemoryPressurePercent is the fraction of total RAM at which the tile
// store starts spilling to disk. 0.90 = 90%.
const DefaultMemoryPressurePercent = 0.90

// ComputeMemoryLimit returns the maximum bytes the tile store should use
// before spilling to disk. It takes a fraction (e.g. 0.90 for 90%) of total
// system RAM and subtracts the current Go heap overhead to give headroom for
// non-tile allocations (COG cache, buffers, etc.).
//
// Returns 0 if RAM detection fails or the computed limit is unreasonably small.
func ComputeMemoryLimit(fraction float64, verbose bool) int64 {
	totalRAM, err := totalSystemRAM()
	if err != nil {
		if verbose {
			log.Printf("Cannot detect system RAM: %v; disk spilling disabled", err)
		}
		return 0
	}

	if verbose {
		log.Printf("System RAM: %.1f GB", float64(totalRAM)/(1024*1024*1024))
	}

	// Reserve some headroom for Go runtime, COG caches, encode buffers, etc.
	// We estimate this as the current Sys usage + a fixed 2 GB buffer.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	overhead := m.Sys + 2*1024*1024*1024 // current usage + 2 GB headroom

	limit := int64(float64(totalRAM)*fraction) - int64(overhead)
	if limit < 512*1024*1024 { // minimum 512 MB
		if verbose {
			log.Printf("Computed memory limit too small (%.0f MB); disk spilling disabled",
				float64(limit)/(1024*1024))
		}
		return 0
	}

	if verbose {
		log.Printf("Tile store memory limit: %.1f GB (%.0f%% of RAM minus %.1f GB overhead)",
			float64(limit)/(1024*1024*1024), fraction*100, float64(overhead)/(1024*1024*1024))
	}

	return limit
}
