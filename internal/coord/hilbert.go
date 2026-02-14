package coord

import "sort"

// xyToHilbert converts (x, y) to a Hilbert curve index for an n x n grid.
// n must be a power of two.
func xyToHilbert(x, y, n uint64) uint64 {
	var d uint64
	s := n / 2
	for s > 0 {
		var rx, ry uint64
		if (x & s) > 0 {
			rx = 1
		}
		if (y & s) > 0 {
			ry = 1
		}
		d += s * s * ((3 * rx) ^ ry)
		// Rotate quadrant.
		if ry == 0 {
			if rx == 1 {
				x = s*2 - 1 - x
				y = s*2 - 1 - y
			}
			x, y = y, x
		}
		s /= 2
	}
	return d
}

// SortTilesByHilbert sorts tile coordinates [z, x, y] by their Hilbert curve
// index within the zoom level. This preserves 2D spatial locality: tiles that
// are close on the Hilbert curve are close in the tile grid, which improves
// cache hit rates when workers process tiles sequentially from a shared queue.
//
// All tiles must be at the same zoom level.
func SortTilesByHilbert(tiles [][3]int) {
	if len(tiles) <= 1 {
		return
	}
	z := tiles[0][0]
	n := uint64(1) << uint(z)

	// Precompute Hilbert indices so each value is computed once (O(n))
	// rather than on every comparison (O(n log n) times).
	indices := make([]uint64, len(tiles))
	for i, t := range tiles {
		indices[i] = xyToHilbert(uint64(t[1]), uint64(t[2]), n)
	}

	sort.Sort(hilbertSorter{tiles: tiles, indices: indices})
}

type hilbertSorter struct {
	tiles   [][3]int
	indices []uint64
}

func (s hilbertSorter) Len() int           { return len(s.tiles) }
func (s hilbertSorter) Less(i, j int) bool { return s.indices[i] < s.indices[j] }
func (s hilbertSorter) Swap(i, j int) {
	s.tiles[i], s.tiles[j] = s.tiles[j], s.tiles[i]
	s.indices[i], s.indices[j] = s.indices[j], s.indices[i]
}
