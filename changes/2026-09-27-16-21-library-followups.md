# Library follow-ups from the review fix clusters

Items the fix clusters handed off outside their own files. Each behaviour change has a
regression test that failed before the fix.

## Bounds and the antimeridian

- `cog.MergedBoundsWGS84` samples 16 intervals on every source edge instead of only the
  corners. The EEA grid in EPSG:3035 reaches 72.6N mid-edge (58.95N at the corners); a
  Sentinel-2 UTM tile straddling its central meridian at 60N lost ~400 m of its top, a row
  of tiles from z16 on (now under 2 m). A pole inside a source (EPSG:3413/3031) widens the
  bounds to every longitude; the pole must be a point in the projection, so an EPSG:4326
  raster reaching 90N keeps its longitudes.
- `MergedBoundsWGS84` returns `(Bounds, error)` and errors instead of panicking when no
  point transforms. Callers: `cmd/geotiff2pmtiles` (fatal "Bounds: ..."), integration and
  tile tests.
- EPSG:4326 sources whose longitudes run from >= 0 past 180 (0..360, 100..260 grids) get
  `WGS84Identity{Lon360: true}`, decided per source, so their part past 180 renders into
  the western tiles instead of staying empty. `tileCRSBounds` shifts western tiles whole
  for such sources (a source entirely past 180 was skipped at z0/z1).
- Header and metadata bounds of data crossing the antimeridian (MaxLon > 180) are written
  as -180..180, with the centre kept on the data (`math.Remainder`). TileJSON 3.0 says
  bounds "MUST NOT wrap around the ante-meridian", and MapLibre's `TileBounds` clamps east
  to 180 and matches no tile when west > east. E7 conversions clamp to +-180 (260 degrees
  overflowed int32).
- pmtransform rebuild without `--fill-color` builds lower zooms from the parents of the
  tiles one level down instead of every position in the header bounds, which are now
  full-width for crossing archives.

## TIFF reading

- GDAL internal masks (NewSubfileType bit 2 + Photometric 4, one per level, paired by size
  and tiling) are applied as alpha by `ReadTile`, so masked pixels of JPEG COGs are
  transparent and later sources show through. A sparse mask tile is all invalid, as GDAL
  reads it. When level 0 has a mask, overviews without one are dropped. Checked against
  `gdal_translate -b mask` on GDAL-written COGs: no mismatching pixel at any level.
- `toRGBA` uses draw's YCbCr path (same bytes, 4x faster); masked JPEG tiles go through it.
- Unsupported layouts (predictor on bit-packed samples) fail at open with the file name
  instead of at the first read. Tiles of more than 2^28 samples (TileWidth 2^31, huge
  single-strip images) fail the open for level 0 and drop an overview.

## Encoding and pmtiles

- Pure-Go (`CGO_ENABLED=0`) WebP decoding converts lossy Y'CbCr with libwebp's BT.601
  limited-range arithmetic: 10,20,30 decoded as 25,33,42 and 250 as 231 before.
- The bilinear, bicubic and Lanczos samplers weight RGB by alpha like the pyramid
  downsample (continuous `--alpha-band` sources).
- Metadata `format` names MVT, AVIF and MLT (was "unknown"); `encode.TileTypeAVIF`, an
  unused duplicate of `pmtiles.TileTypeAVIF`, is gone.
- `optimizeRunLengths` keeps input run lengths (`pmheader --rebuild-dirs` cut every run to
  its first tile) and merges in place, saving a second copy of the index at Finalize
  (2.15 GB for a full global z13 archive).
- `Writer` merges runs of one deduplicated blob while writing when enough dedup hits come
  in: 262,144 interleaved fill tiles in 524 runs leave 1,381 entries instead of 262,144.
  Archives without repeats are unaffected (24 B per tile until Finalize).
