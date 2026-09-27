# Min zoom: whole extent in one tile, in both tools

## Problem

`pmtransform` kept the source's min zoom, so an archive whose lowest level still split the
data across several tiles never got a one-tile overview. `MinZoomForSingleTile` also
treated the east/south edges as inclusive: data ending exactly on a tile boundary
(e.g. lon 0–90) was counted as spanning two tiles and got a min zoom one level too low.

## Changes

- `internal/coord/mercator.go`: `MinZoomForSingleTile` treats the east and south edges as
  exclusive (1e-9°, far below one z28 tile).
- `cmd/pmtransform/main.go`: `--min-zoom -1` (default) is now the source min zoom, extended
  down to the zoom where all data fits in one tile. The added levels are downsampled from
  the source's min zoom in a second rebuild pass; existing levels keep their mode
  (passthrough stays passthrough).
- `internal/tile/zoom.go`: deleted (`AutoZoomRange` had no callers).
- Tests: Europe → z0 and a tile-aligned extent → z2 in `TestMinZoomForSingleTile`.
