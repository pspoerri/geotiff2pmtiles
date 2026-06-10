# Flood-mask overview fix, parallel build, word-skip apply, LRU cache

## Bug fix: flood mask now applies at overview levels

With `--nodata-flood` active, the decode paths skip per-pixel nodata matching
at *every* IFD level (the mask is the transparency authority), but `ReadTile`
only applied the mask at level 0. Reads through `OverviewForZoom` — which
happen whenever `--max-zoom` sits below the source's native resolution —
therefore returned fully opaque tiles: no border transparency at all.

`ReadTile` now applies the mask at all levels. Level 0 uses the existing 1:1
path; overview levels use `applyFloodMaskRGBAScaled`, which samples the
level-0 mask at the center of each overview pixel's footprint
(nearest-neighbor). Covered by `TestApplyFloodMaskRGBAScaled`.

## Parallel mask build (`BuildFloodMask(workers int)`)

Pass 1 (tile decode + tolerance matching, ~all of the build cost) now runs on
a worker pool; `workers <= 0` means GOMAXPROCS. Workers assemble word-local
bit accumulators and merge them into the shared candidate bitmap with
`atomic.OrUint64`, so horizontally adjacent tiles sharing boundary words
never race (`TestMarkTileCandidatesConcurrent` exercises this under `-race`).
The scanline flood (pass 2) stays serial — it is a small fraction of the
total.

The CLI additionally builds masks for up to 4 sources concurrently, dividing
the `--concurrency` budget among them.

Measured on the motivating 24081×18046 JPEG-compressed scan
(Katahdin1952_rgb_jpg.tif, 18-core M-series): 6.5 s → **0.67 s**.

## Word-skipping mask application

`applyFloodMaskRGBA` runs on every uncached level-0 `ReadTile`. It previously
did a bounds-checked per-pixel bit test (65k iterations per 256² tile). It
now consumes the mask a 64-pixel `uint64` word at a time: all-zero words —
the common case on interior tiles — cost a single load, and set bits are
visited via `bits.TrailingZeros64` iteration. Tile extents are clamped once
up front instead of per pixel.

## Usage polish for `--nodata-flood`

- A note is printed when `--nodata-tolerance` is 0 (flood with exact-match
  tolerance only removes exactly-matching edge-connected pixels), suggesting
  20–40 for scanned/JPEG sources.
- A "Building nodata flood masks for N source(s)..." line and a total-time
  summary are printed even without `--verbose`, so the pre-progress-bar pause
  is attributable. Per-source timing stays verbose-only.
- Flag help text now states the `--nodata-tolerance` pairing explicitly.

## Tile caches: FIFO → true LRU

`TileCache` and `FloatTileCache` evicted in insertion order ("LRU-like");
`Get` never refreshed recency, so a hot tile inserted early was evicted as
readily as a cold one. Each shard now keeps a `container/list` recency list:
`Get` (and duplicate `Put`) promote to front, eviction removes the back.
Shard locks changed from `RWMutex` to `Mutex` since `Get` now mutates the
list; the 64-way sharding keeps contention low. Covered by
`TestTileCacheLRUEviction`, `TestFloatTileCacheLRUEviction`,
`TestTileCachePutExistingRefreshes`.

## Verification

- `go test -race ./...` passes; new unit tests for word-skip apply, scaled
  apply, concurrent candidate marking, tolerance window, and LRU semantics;
  new end-to-end `TestBuildFloodMaskEndToEnd` (multi-tile synthetic GeoTIFF,
  border ring transparent, interior speckle preserved).
- Real-source run on Katahdin1952_rgb_jpg.tif completes with mask build in
  666 ms and a valid archive.
- `fieldalignment ./...` remains clean (new/changed structs included).
