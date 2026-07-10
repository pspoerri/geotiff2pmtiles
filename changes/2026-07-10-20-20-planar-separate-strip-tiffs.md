# Support planar-separate strip TIFFs

## Problem

Strip-organized TIFFs with `PlanarConfiguration=2` (GDAL `INTERLEAVE=BAND` — GDAL's
default output layout when copying from a band-interleaved source) rendered as
garbage: alternating horizontal black bands with wrong colors in the content bands.

The strip path in `readTileDecoded` dispatches before the planar-separate guard, so
these files were never rejected. `promoteStripsToTiles` treated the plane-major
strip sequence (all of plane 0's strips, then plane 1's, …) as chunky rows: each
virtual tile received one plane's bytes — a third of the expected data — so the
decode filled only the top third of each 256-row virtual tile (with single-plane
bytes misread as interleaved RGB) and left the rest transparent.

## Fix

`internal/cog/reader.go`:

- `stripLayout` gains `planes` and `stripsPerPlane`.
- `promoteStripsToTiles` derives virtual tiles from one plane's worth of strips and
  sums byte counts across planes.
- `readStripTileRaw` reads each plane's strips for the tile row (extracted helper
  `readStripsRaw`), undoes the predictor per plane at `samplesPerPixel=1`, and
  interleaves the planes into chunky order so downstream decoding is unchanged.
- Planar strips with JPEG compression return a clear error instead of garbage
  (encoded planes cannot be byte-interleaved).

## Testing

New `internal/cog/strip_planar_test.go` writes synthetic strip TIFFs (chunky and
planar, RowsPerStrip 1 and 2, 300 rows to span multiple virtual tiles) and verifies
every pixel. Verified end-to-end against a GDAL-produced `INTERLEAVE=BAND` file
(previously banded, now renders correctly).

Also ran `make fmt`, which realigned the `tileSource` struct in
`internal/tile/resample.go` left misformatted by the non-square-pixel fix.
