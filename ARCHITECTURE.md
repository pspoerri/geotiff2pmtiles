# Architecture

Code structure, pipelines and memory model. For the reasoning behind them see
[DESIGN.md](DESIGN.md); for building and testing see [BUILDING.md](BUILDING.md) and
[DEVELOPMENT.md](DEVELOPMENT.md).

## Layout

```
cmd/
  geotiff2pmtiles/main.go          CLI: GeoTIFF/COG → PMTiles conversion
  geotiff2pmtiles/sources.go       Per-source CRS and geotransform checks, WGS84 bounds check, coverage holes (single CRS only), finest pixel size
  pmtransform/main.go              CLI: PMTiles → PMTiles transformation
  checkpmtiles/main.go             PMTiles v3 archive validator (local + HTTP; every directory at any depth)
  pmheader/main.go                 Patch PMTiles header/metadata/directories without touching tile data
  coginfo/main.go                  COG metadata inspector; -raw dumps the IFD 0 tags and first tile
internal/
  cli/
    cli.go                          Helpers shared by geotiff2pmtiles and pmtransform: colour flags (--nodata-color, --fill-missing, deprecated --fill-color), profiling flags, per-run temp directory with signal cleanup
  cog/
    reader.go                       COG/GeoTIFF tile-level reader: overview choice, strip-to-tile promotion (JPEG strips decoded per strip, sparse strips as nodata), samples16 (the shared 1-16 bit sample decode), RGBA/float32/raw uint16 decode, palette (ColorMap) and WhiteIsZero, band reorder/rescale, nodata, GDAL internal masks, preset auto-detection, merged WGS84 bounds
    ifd.go                          TIFF IFD parser (bounded against the file size; GDAL_METADATA XML tag 42112), level and mask selection (imageIFDs, levelMasks)
    geotags.go                      GeoTIFF keys: EPSG by GTModelTypeGeoKey, PixelIsPoint normalised to the corner origin, CRS units
    tfw.go                          TFW (TIFF World File) parser + EPSG guess from coordinate ranges
    tilecache.go                    Tile caches for RGBA, float32 and raw uint16 tiles: thin wrappers over one generic sharded LRU (shardedLRU[V]) with a goroutine-local view (lruView[V])
    source.go                       ByteSource (all byte access; mmap-backed via Open, any source via OpenSource), Reader.slice range check, closed-reader sentinel, 64 KiB read-ahead for header parsing
    valuerange.go                   Auto rescale range over the rendered bands (GDAL statistics, else a grid of up to 64 tiles); ErrNoValueRange
    flood.go                        Source-level nodata flood mask (--nodata-flood)
    lzw.go                          LZW decompression (ZSTD via klauspost/compress in reader.go)
    mmap_unix.go                    mmap/munmap via syscall.Mmap (unix)
    mmap_windows.go                 mmap via CreateFileMapping/MapViewOfFile (windows)
    mmap_other.go                   Unsupported-platform stubs
  coord/
    swiss.go                        EPSG:2056 <-> WGS84 transforms
    utm.go                          UTM zones (EPSG:326xx/327xx/258xx) <-> WGS84, Krüger series
    fallback.go                     Any other EPSG code via wroge/crs, with a cached datum-shift grid
    mercator.go                     WGS84 <-> Web Mercator tile math, TilesInBounds (wraps at the antimeridian), CRS pixel size <-> ground metres
    projection.go                   Projection interface, ForEPSG, WGS84Identity (with Lon360/Lon360Min), CRS units, WrapLonRange
    hilbert.go                      Hilbert curve for spatial tile ordering
  tile/
    generator.go                    Parallel tile generation pipeline (GeoTIFF sources)
    transform.go                    PMTiles transform pipeline (passthrough/re-encode/rebuild), SelectTransformMode
    resample.go                     Per-source projections (buildSourceInfos), Lanczos/bicubic/bilinear/nearest/mode interpolation + reprojection (LUT-accelerated, alpha-weighted, optional gamma encode)
    downsample.go                   Pyramid downsampling for lower zoom levels (alpha-weighted)
    diskstore.go                    Disk-backed tile store with memory backpressure and a sticky I/O error
    memlimit.go                     Auto spill-queue limit (90% of RAM or the cgroup limit, minus usage and 2 GB, at least 256 MB)
    tiledata.go                     Compact tile representation (uniform / gray / RGBA)
    rgbapool.go                     sync.Pool for *image.RGBA reuse (keyed by dimensions)
    progress.go                     Progress reporting (live bar on a terminal, one line per level otherwise)
    sysinfo_linux.go                Total RAM via sysinfo, capped by the cgroup v2/v1 memory limit (linux)
    sysinfo_darwin.go               Total RAM via sysctl HW_MEMSIZE (darwin)
    sysinfo_windows.go              Total RAM via GlobalMemoryStatusEx (windows)
    sysinfo_other.go                Unsupported-platform stub
  encode/
    encoder.go                      Unified encoding interface, zero-copy NRGBA views (asNRGBA)
    decode.go                       Decode encoded tiles back to straight-alpha images (pyramid building, pmtransform)
    jpeg.go                         JPEG encoder
    png.go                          PNG encoder
    webp.go                         WebP encoder/decoder (native libwebp via CGo)
    webp_stub.go                    Pure-Go WebP for non-CGo builds (x/image/webp decode with libwebp's YUV→RGB arithmetic, nativewebp lossless encode)
    webp_available.go               CGo availability flag for conditional tests
    terrarium.go                    Terrarium encoder for elevation data
  pmtiles/
    writer.go                       PMTiles v3 writer: temp tile file, small-tile dedup, run merging while writing, clustered Finalize via <output>.partial
    reader.go                       PMTiles v3 reader: sorted run-length index (binary-search lookup, never expanded per tile), leaf directories at any depth, none/gzip internal compression, metadata
    header.go                       Header serialization/deserialization (127 bytes); bounds kept as exact E7 alongside the float32 fields; antimeridian bounds written as -180..180
    directory.go                    Hilbert-curve tile IDs, directory serialization/deserialization, run-length merging, 16 KiB root budget enforcement
integration/
  helpers_test.go                 Synthetic GeoTIFF writer, pipeline runners, PMTiles validation, plausibility checks
  synthetic_test.go               End-to-end tests using generated GeoTIFFs
  flood_test.go                   --nodata-flood end-to-end tests
  transform_mode_test.go          pmtransform mode selection (passthrough, added levels, fill, re-encode)
  transform_terrarium_test.go     pmtransform terrarium rebuild tests
  satellite_*_test.go             Per-dataset tests using real COGs (skipped if data absent)
  testdata/                       download.sh + one directory per dataset (see DEVELOPMENT.md)
```

## Pipeline

1. **Scan**: collect the input files (directories recursively; glob patterns the shell left
   unexpanded, as on Windows, are expanded here).
2. **Open**: parse each file's IFDs and GeoTIFF keys (or TFW). `OpenSource` keeps IFD 0
   plus the IFDs that can be its overviews and pairs GDAL internal masks with their
   levels; promotes strips (chunky or planar-separate) to virtual tiles, each strip
   keeping its rows (sparse strips read as nodata); and checks every level's layout, so a
   file the decoders cannot read fails here with its name.
3. **CRS**: `--source-epsg` overrides every source's EPSG (`Reader.SetEPSG`); without it,
   one warning covers the sources whose CRS was guessed from world-file coordinates
   (`Reader.EPSGGuessed`). Then every source needs a geotransform (a positive pixel size)
   and a CRS that resolves to a projection (unknown codes and user-defined 32767 fail).
   Coverage holes are searched only when all sources share one CRS.
4. **Plan**: detect the preset and format from the first source, the band config and
   rescale range (merged over all sources), and the merged WGS84 bounds
   (`cog.MergedBoundsWGS84`: 16 intervals along each source edge, and all longitudes when a
   pole lies inside a source); `checkBoundsWGS84` rejects latitudes beyond ±90° plus the
   coarsest source pixel.
   The auto max zoom comes from the finest source's ground pixel size.
5. **Generate (max zoom)**: enumerate tiles in the bounds, sort them along the Hilbert curve,
   distribute batches to a worker pool.
6. **Reproject**: per output pixel, from WGS84 into each candidate source's own CRS
   (see Per-source projection).
7. **Resample**: Lanczos-3, bicubic (Catmull-Rom), bilinear, nearest-neighbor, or mode
   (most common value) from source COG tiles (cached); the first source with a
   non-transparent result wins.
8. **Downsample (lower zooms)**: combine 4 child tiles into parent tiles.
9. **Encode**: JPEG/PNG/WebP/Terrarium.
10. **Write**: tiles go to the writer's temp file; `Finalize` sorts the index, builds the
    directories and streams the clustered archive into `<output>.partial`, then renames it
    over the output.

`--nodata-color` and `--fill-missing` work as in `pmtransform` (below): the first recolours
transparent pixels of rendered tiles, the second writes solid tiles at tile positions
without data and stands in for missing children when downsampling.

## Conventions

These hold across packages; code that touches pixels or coordinates must follow them.

- **Straight alpha.** Every pipeline `*image.RGBA` holds straight (non-premultiplied)
  alpha in `Pix`, although Go defines that type as premultiplied. Encoders get a zero-copy
  `*image.NRGBA` view of it (`asNRGBA`); never hand a pipeline `*image.RGBA` to a standard
  library consumer that interprets it (`image/png`, `image/draw` as a source, nativewebp).
  `encode.DecodeImage` returns images with alpha as straight `*image.RGBA`; never
  `draw.Draw` a decoded image with alpha into an `*image.RGBA`, which premultiplies it.
  `TileData.At` and `ColorModel` are NRGBA. JPEG writes straight RGB and drops alpha.
- **Alpha-weighted averaging.** Bilinear, bicubic and Lanczos, in the samplers and in the
  pyramid, weight RGB by alpha: RGB = Σ(c·a·w) / Σ(a·w), alpha = Σ(a·w) / Σ(w). A result
  whose alpha rounds to 0 is written as 0,0,0,0. Nearest and mode copy pixels.
- **Generation loss.** Lower zooms are downsampled from tiles decoded from the output
  encoding, so lossy formats lose quality at each level, and JPEG, which drops alpha at
  max zoom, blends nodata into the overviews as black.
- **Pixel centres.** `cog.GeoInfo.OriginX/Y` is always the upper-left corner of pixel
  (0,0); PixelIsPoint files and TFWs are normalised at parse time. The two sampling
  dispatchers convert corner-based to centre-based coordinates once (`- 0.5`, after the
  bounds check); every sampler treats integer coordinates as pixel centres. Output pixels
  are sampled at `px + 0.5`.
- **Bounds.** `cog.Bounds` has MinLon in [-180, 180); MaxLon > 180 means the data crosses
  the antimeridian, and a full turn is [-180, 180]. `Projection.ToWGS84` returns
  longitudes continuous around the projection's central meridian; callers wrap merged
  intervals with `coord.WrapLonRange`. `TilesInBounds` continues from column 0 past 180.
  The PMTiles header and metadata record crossing bounds as -180..180 with the centre on
  the data (`pmtiles.archiveBounds`, `archiveCenter`), because viewers do not accept
  wrapped bounds. `WriterOptions.Center` overrides the derived centre, so a copy keeps
  its source's `Header.Center()`.
- **Out-of-domain points.** A projection returns ±Inf, never NaN, for a point it cannot
  transform: the per-pixel bounds checks reject Inf, but NaN comparisons are false.

## Per-source projection

Each source is reprojected from its own CRS. `buildSourceInfos` runs once in `Generate`
and creates one `coord.Projection` per distinct (EPSG, Lon360, Lon360Min), shared
read-only by all workers (`CRSFallback` keeps its datum-shift cache per instance).
EPSG:4326 sources whose longitudes run from at most a pixel west of 0 past 180 get
`WGS84Identity{Lon360: true, Lon360Min: min(minX, 0)}`, decided per source. Per output
tile, `prepareTileSources` computes the tile's box and pixel size once per distinct
projection (overlap test, overview choice) and keeps the overlapping sources in input
order, which is also their priority. Lon/lat per output column and row are
precomputed; a pixel is projected again only when the next candidate source's projection
differs from the previous one, so single-CRS input costs one `FromWGS84` per pixel.

## Reading sources

- **Levels**: IFD 0 plus later IFDs that are tiled, no larger than IFD 0, of the same
  samples per pixel and depth, in a supported compression and with at most 2^28 samples per
  tile. Masks, pages, thumbnails and striped reduced images are dropped, so `IFDCount` and
  `NumOverviews` count levels only. IFD 0 has the same cap, except that a strip file's
  virtual tiles may exceed it when their strips hold the bytes to fill them
  (`stripLayout.backed`).
- **Internal masks**: a GDAL mask IFD (NewSubfileType bit 2 and Photometric 4, tiled, 1-8
  bit) is paired with the level of the same size and tiling. Masks are used only when
  level 0 has one; overviews without one are then dropped. `ReadTile` applies the mask
  before the flood mask, so the tile cache, `ReadRegion` and the flood build see it. The
  float and raw uint16 paths do not apply masks.
- **Byte access**: every tile, strip and mask range goes through `Reader.slice`, which checks
  it against `ByteSource.Size()` without overflow, so a `ByteSource` only ever sees
  `0 <= off <= end <= Size()`. `Slice` results are valid until `Close`; `OpenSource` owns
  its source and closes it on error. A closed reader returns errors, not panics.
- **Setters**: `SetBandConfig`, `SetEPSG` and `SetID` are plain field writes; call them
  before the reader is shared with workers. Reader IDs (the tile cache key) come from a
  process-wide counter; `SetID` is an optional override.

## Transform Pipeline (pmtransform)

`pmtransform` reads an existing PMTiles archive and produces a new one with modifications.
The original file is never touched. `tile.SelectTransformMode` picks the cheapest mode;
the CLI and the integration helpers both use it:

1. **Passthrough**: no format change and no `--nodata-color` — raw tile bytes are copied directly (fastest)
2. **Re-encode**: format change (e.g. WebP → PNG) or `--nodata-color` — each tile is decoded and re-encoded
3. **Rebuild pyramid**: `--rebuild` — max-zoom tiles are decoded, then the entire lower-zoom
   pyramid is rebuilt via downsampling with the chosen resampling method.
   The source tile size is discovered by decoding one tile; `--tile-size` must match it.
   Terrarium archives (detected via the `encoding` metadata key, or forced with `--terrarium`)
   are downsampled in elevation space — per-channel RGBA averaging would corrupt elevations
   at the 256 m channel boundaries.

`--fill-missing` is not an input of the mode choice: filling missing positions needs no
decoding, so passthrough writes pre-encoded fill tiles (encoded in the source format) next
to the copied ones. They fill `fillBounds`: the header bounds, except that full-width
(-180..180) bounds whose data crosses the antimeridian are narrowed to the shortest run of
max-zoom columns round the globe that holds the data.

Levels below the source's min zoom (requested with `--min-zoom`, or by default down to the
zoom where all data fits in one tile) are added inside `tile.Transform` without forcing a
full rebuild: the chosen mode runs over the source's own levels, then a rebuild pass
starts from the source's min zoom and downsamples from there. A `zoomCap` writer drops
that pass's re-render of the source's min zoom, which the first pass already wrote. Adding
levels needs an encoder.

In a rebuild without `--fill-missing`, each lower zoom visits only the parents of the tiles
one level down (`parentTiles`), not every position in the header bounds, which are
whole-world for an archive that crosses the antimeridian. With `--fill-missing`, rebuild
tracks which positions contain real (non-fill) data and propagates upward through zoom
levels. Only real positions go through the expensive downsample/encode pipeline; all-fill
positions get pre-encoded bytes written directly, skipping DiskTileStore overhead entirely.

`--nodata-color` uses a color transformation model: transparent pixels are substituted
with the target color rather than resampled, in decoded source tiles (re-encode and
rebuild) and in rendered max-zoom tiles (geotiff2pmtiles).

Source metadata keys that the writer does not derive (anything but name, description,
format, type, minzoom, maxzoom, bounds, center, attribution, encoding) are passed through
`WriterOptions.Extra`, and the output header keeps the source's exact E7 bounds and its
centre (`WriterOptions.Center`, with the zoom clamped to the output range).

## Memory Efficiency

- Memory-mapped file access (no full-image decode)
- Source tile caches (max(256, 128 × concurrency) tiles): `TileCache`, `FloatTileCache`
  and `Uint16TileCache` wrap one generic sharded LRU (`shardedLRU[V]`), with per-shard
  hit/miss `Stats()`. `renderTile` / `renderTileTerrarium` read through a goroutine-local
  view (`Local()`, 256 B, `lruView[V]`) that memoizes the last 2×2 source tiles, keeping
  per-pixel lookups off the shard locks
- Tiles stored as encoded bytes (PNG/WebP/JPEG) in memory: 5-25x smaller than raw pixels
- Continuous disk spilling via dedicated I/O goroutine: every non-uniform tile is streamed
  to the spill file as it is produced; `--mem-limit` only caps the encoded bytes queued for
  that goroutine before workers pause (auto: 90% of RAM, or of the cgroup limit on Linux,
  minus current usage minus 2 GB, at least 256 MB, so spilling is never turned off)
- Uniform tiles (single color) stored as 4 bytes, never spilled to disk
- `sync.Pool` for `*image.RGBA` buffers: render, downsample, and decode paths reuse 256 KB buffers instead of allocating/GC'ing per tile
- PMTiles writer: tile data goes to a temp file; memory holds the index (24 B per entry
  until `Finalize`; runs of one deduplicated tile are merged while writing), a dedup map
  for tiles of at most tileSize²/16 bytes, and at `Finalize` 16 B per unique tile for the
  copy list. `Finalize` assigns clustered offsets in memory and streams the unique tiles
  from the temp file into `<output>.partial`, then renames it over the output after
  `Sync` and `Close`
- PMTiles reader: the index stays as sorted run-length entries (memory proportional to
  directory entries, not addressed tiles); `TilesAtZoom` returns one zoom's coordinates
- Temporary files (`pmtiles-tiles-*.tmp` from the writer, `pmtiles-tilestore-*.tmp` spill
  files) go into one per-run directory (`.geotiff2pmtiles-tmp-*` / `.pmtransform-tmp-*`)
  under `--tmp-dir` or the output directory; `<output>.partial` goes next to the output so
  the rename stays on one filesystem. `cli.MakeTmpDir` removes both on exit, on errors
  and on SIGINT/SIGTERM, and warns about directories left by killed runs. Peak disk use is
  about 2× the final archive
- Pyramid downsampling avoids redundant source reads for lower zoom levels

## Errors and cancellation

- Each zoom level of `Generate` and `Transform` runs under `context.WithCancelCause`: the
  first worker error cancels the level, the other workers stop at their next tile, and the
  batch producer stops.
- `DiskTileStore` records its first spill write, read-back or decode error and stops
  writing; `Put`, `Drain` and `Err` return it. `Get` returns nil for a tile it could not
  read, which looks like a missing tile, so callers check `Err` after reading a level.
- `Finalize` removes its temp file and `.partial` on failure; an existing archive at the
  output path is replaced only by a complete one.

## Nodata and Transparency

- Nodata pixels (all bands within `--nodata-tolerance` of the nodata value) are decoded as
  transparent (alpha=0) on every decode path. The value is auto-detected from GDAL_NODATA
  and overridable with `--nodata`. With nodata or a GDAL internal mask active and
  `--format auto`, output is `webp` instead of `jpeg` so transparency survives encoding. Terrarium takes `--nodata` as a
  float (`tile.Config.FloatNodata`) that overrides every source's tag.
- GDAL internal masks make masked pixels transparent (see Reading sources).
- `--nodata-flood` builds a per-source bitmap (1 bit per pixel) by flood-filling from the
  image edge through near-nodata pixels (`cog.Reader.BuildFloodMask`, parallel decode, up
  to 4 sources at once). Raw-decoded sources are matched on their stored samples of the
  rendered bands (`renderedBands`), JPEG on the decoded RGB. Only edge-connected pixels
  become transparent; `ReadTile` applies the mask after decoding, sampling it at footprint
  centers for overview levels.
- Resampling and downsampling exclude alpha=0 pixels. When one source yields a transparent
  sample the next source is tried, so holes in one file don't hide data in another.
- Planar-separate JPEG COGs are decoded plane by plane and merged into RGBA at read time.

Details and trade-offs: DESIGN.md, "Nodata and transparency".

## Platform Support

Linux, macOS and Windows on amd64 and arm64. Platform differences are confined
to three groups of build-tagged files — `cog/mmap_*.go` (memory mapping),
`tile/sysinfo_*.go` (total RAM) and `encode/webp{,_stub,_available}.go` (CGo
availability); everything else is portable Go.

Source builds use `CGO_ENABLED=1` and link libwebp, resolved through
`pkg-config` on Linux and macOS and named directly (`-lwebp -lsharpyuv`) on
Windows. A `CGO_ENABLED=0` build swaps in pure Go: `golang.org/x/image/webp`
decodes lossy and lossless WebP (lossy colours converted with libwebp's
arithmetic), `HugoSmits86/nativewebp` encodes lossless VP8L (no pure-Go lossy
VP8 encoder exists, so `--quality` is ignored), and `--version` says which
variant a binary has.

CI builds the Windows binaries natively under MSYS2 (UCRT64 on amd64,
CLANGARM64 on arm64) with CGo on and `-extldflags=-static`, producing a
self-contained `.exe` that includes WebP. The Linux and macOS binaries are
still cross-compiled from the Ubuntu job at `CGO_ENABLED=0`, so the released
builds for those platforms carry the pure-Go lossless variant.

Windows keeps a file locked while a handle or mapping is open, so code that
renames or deletes over a file it also reads closes first (`pmheader`,
`DiskTileStore.Close`), and same-file checks use `os.SameFile` rather than
string comparison.

## Adding New Projections

Implement the `coord.Projection` interface:

```go
type Projection interface {
    ToWGS84(x, y float64) (lon, lat float64)
    FromWGS84(lon, lat float64) (x, y float64)
    EPSG() int
}
```

Then register it in `coord.ForEPSG()` (and in `unitSize` if its unit is not the metre).
`ToWGS84` returns longitudes continuous around the central meridian, not wrapped per
point, and `FromWGS84` returns ±Inf, not NaN, outside its domain (see Conventions). Codes
without a native implementation already work through `CRSFallback` (wroge/crs); add a
native one when the fallback is too slow for a CRS you use a lot. `utm_test.go` shows how
to cross-check against wroge/crs.
