package tile

import (
	"log"
	"runtime"
	"strconv"
	"strings"
)

// DefaultMemoryPressurePercent is the fraction of total RAM that the auto
// memory limit starts from. 0.90 = 90%.
const DefaultMemoryPressurePercent = 0.90

// minMemoryLimit is the smallest limit ComputeMemoryLimit returns, and the
// one it uses when RAM cannot be detected. Auto mode must never turn
// spilling off: small machines, where the formula comes out low, are the
// ones that need it most.
const minMemoryLimit = 256 << 20

// ComputeMemoryLimit returns the auto value for DiskTileStoreConfig's
// MemoryLimitBytes: the encoded bytes that may wait for the spill file
// before Put blocks (spilling itself is continuous). It takes a fraction
// (e.g. 0.90 for 90%) of total system RAM (on Linux, the cgroup memory limit
// when that is lower) and subtracts the current Go runtime usage plus 2 GB to
// give headroom for non-tile allocations (COG cache, buffers, etc.).
//
// The result is never below 256 MB, also when RAM detection fails, so the
// auto setting always leaves spilling on.
func ComputeMemoryLimit(fraction float64, verbose bool) int64 {
	totalRAM, err := totalSystemRAM()

	// Reserve some headroom for Go runtime, COG caches, encode buffers, etc.
	// We estimate this as the current Sys usage + a fixed 2 GB buffer.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	overhead := m.Sys + 2*1024*1024*1024 // current usage + 2 GB headroom

	limit := memoryLimitFor(totalRAM, err, overhead, fraction)
	if verbose {
		if err != nil {
			log.Printf("Cannot detect system RAM: %v; tile store memory limit: %d MB", err, limit>>20)
		} else {
			log.Printf("System RAM: %.1f GB; tile store memory limit: %.1f GB (%.0f%% of RAM minus %.1f GB overhead, at least %d MB)",
				float64(totalRAM)/(1024*1024*1024), float64(limit)/(1024*1024*1024),
				fraction*100, float64(overhead)/(1024*1024*1024), minMemoryLimit>>20)
		}
	}
	return limit
}

// memoryLimitFor is fraction × totalRAM minus overhead, but never less than
// minMemoryLimit; a RAM detection error gives minMemoryLimit.
func memoryLimitFor(totalRAM uint64, err error, overhead uint64, fraction float64) int64 {
	if err != nil {
		return minMemoryLimit
	}
	return max(int64(float64(totalRAM)*fraction)-int64(overhead), minMemoryLimit)
}

// parseCgroupLimit parses a cgroup memory limit file: a byte count, or "max"
// (cgroup v2) for no limit. cgroup v1 reports no limit as a number near
// 2^63, which the caller's minimum with physical RAM ignores.
func parseCgroupLimit(data []byte) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil && n > 0
}
