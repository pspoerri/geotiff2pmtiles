# Source-level nodata flood fill

## Problem

After the JPEG-nodata fix, `--nodata 0 --nodata-tolerance 8` on the Katahdin
scan still left a speckle fringe along the alpha boundary (visible against a
green background — the user shared a screenshot showing it clearly). Two
forces are in tension:

- A small tolerance (≤ 8) misses the JPEG-smear pixels just inside the true
  border that have values like 12, 28, 45 — these render as opaque dark
  speckles, jagging the alpha edge.
- A wide tolerance (40+) catches the smear but also absorbs *interior* dark
  pixels (text, shadows, forest canopy, contour lines), eroding the actual map
  content.

Per-pixel matching can't tell the two apart because they're locally
indistinguishable. The only signal that separates them is *connectivity to the
image boundary*: the smear is part of the contiguous near-zero region that
starts at the COG's outer edge; interior dark pixels are not.

## Solution

Source-level 4-connected flood fill seeded from the COG's outer boundary,
operating on a "candidate" set (pixels within the widened tolerance).
Transparency = candidate ∧ reachable-from-edge.

### `internal/cog/flood.go` (new)

- `bitmap` — 1-bit-per-pixel packed bitmap over `[]uint64`.
- `Reader.BuildFloodMask()`:
  1. Iterates every source tile with `HasNodata` temporarily cleared, decodes
     each, and sets a bit in the *candidate* bitmap for every pixel whose RGB
     channels are all within `NodataTolerance` of `Nodata`.
  2. Runs `scanlineFlood` with seeds drawn from every boundary-row and
     boundary-column pixel that's in the candidate set, producing the final
     `flood` bitmap which is retained on the Reader.
- `scanlineFlood` — 4-connected scanline flood fill (per row, extend
  left/right, then for each filled column check the row above and below for
  the *start* of new contiguous runs). Peak queue size scales with the
  perimeter of the flooded region, not its area; for the Katahdin file
  (24081×18046, ~10% near-zero border) the queue stays well under 1 MB while
  filling tens of millions of pixels.
- `Reader.applyFloodMaskRGBA` — given a decoded tile and its source-pixel
  origin, zeroes alpha (keeping RGB) wherever the mask bit is set.

### `internal/cog/reader.go`

- New fields on `Reader`: `floodMask *bitmap`, `floodMaskW`/`floodMaskH int`.
- `ReadTile` split into a public wrapper and the existing dispatch
  (`readTileDecoded`). When `floodMask != nil` and `level == 0`, the wrapper
  forces RGBA materialisation and calls `applyFloodMaskRGBA`.
- The three decode paths short-circuit per-pixel nodata when a flood mask is
  present:
  - `decodeJPEGTile` returns the raw decoded image untouched.
  - `decodePlanarSeparateJPEG` skips the post-merge `applyNodataMaskRGBA`.
  - `decodeRawTile` skips both the legacy spp≤2 path and the general-path
    tolerance check.
  This is essential: if a decode path zeroed RGB for every candidate, the
  mask couldn't recover interior false positives.

### `cmd/geotiff2pmtiles/main.go`

- New `--nodata-flood` boolean flag (default false). Requires `--nodata` to
  be set (or auto-detected). Calls `BuildFloodMask` on each source after
  `SetBandConfig` and before the tile generators start, with a per-source
  timing log line under `--verbose`.

### Tests (`internal/cog/flood_test.go`)

- `TestScanlineFloodReachable` — a U-shape touching the top edge floods
  entirely; an isolated interior speckle does not. Verifies the core
  edge-reachability semantics.
- `TestScanlineFloodFullBorder` — a closed ring of candidates floods, the
  enclosed non-candidate interior stays empty.
- `TestBitmapSetGet` — uint64 word-boundary off-by-ones across n = 1, 63, 64,
  65, 127, 128, 129, 1000.

### Docs

- `README.md`: flag table entry; new example pairing `--nodata-tolerance 40`
  with `--nodata-flood`.
- `ARCHITECTURE.md`: bullet under the memory/I/O section.
- `DESIGN.md`: new "Source-level nodata flood fill" section covering the
  candidate-vs-reached semantics, why scanline flood, and trade-offs vs
  per-tile / overview-based variants.

## Verification

```
$ ./dist/geotiff2pmtiles --verbose --nodata 0 --nodata-tolerance 40 \
    --nodata-flood Katahdin1952_rgb_jpg.tif /tmp/kt_flood.pmtiles
Band config: bands 1,2,3, nodata 0 (tol 40)
Flood mask built for source 1/1 (Katahdin1952_rgb_jpg.tif) in 6.541s
...
Zoom 15: completed (2067 tiles so far, 1512 gray, 366 uniform, 0 empty)
```

The 6.5 s build is the source-tile decode pass (~6500 256×256 JPEG tiles,
sequential). Subsequent flood-mask lookups during tile generation are O(1)
bit-checks per pixel.

Compared with the simpler `--nodata 0 --nodata-tolerance 8` run, the
boundary is now clean of speckles and interior dark content is preserved.

```
$ go test ./...
ok      .../integration
ok      .../internal/cog
ok      .../internal/coord
ok      .../internal/encode
ok      .../internal/pmtiles
ok      .../internal/tile
```

## Out of scope / future work

- The flood mask is built and used only at IFD level 0. For COGs that *do*
  have overviews, reads at higher levels currently fall back to per-pixel
  tolerance matching. A natural extension is to build the mask on the
  smallest available overview, then upsample on demand for higher-resolution
  reads — but no test data with both overviews and a tolerance-needing border
  exists in this repo yet.
- The source-tile decode pass is sequential. For very large COGs (multi-GB)
  parallelising it across `runtime.NumCPU()` workers would cut wall-clock
  time but complicates the temporary `HasNodata` save/restore. Filed for
  later.
