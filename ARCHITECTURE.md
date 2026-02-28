# Architecture

```
cmd/
  geotiff2pmtiles/main.go          CLI: GeoTIFF/COG → PMTiles conversion
  pmtransform/main.go              CLI: PMTiles → PMTiles transformation
  coginfo/main.go                   COG metadata inspector
  debug/main.go                     Low-level COG debug utility
internal/
  cog/
    reader.go                       COG/GeoTIFF tile-level reader (memory-mapped, nodata-aware, 8/16/32-bit, predictor 2+3, band reorder/rescale, preset auto-detection)
    ifd.go                          TIFF IFD parser (incl. GDAL_METADATA XML tag 42112)
    geotags.go                      GeoTIFF metadata extraction
    tfw.go                          TFW (TIFF World File) parser + EPSG inference
    tilecache.go                    LRU tile cache for decoded source tiles
    lzw.go                          LZW decompression
  coord/
    swiss.go                        EPSG:2056 <-> WGS84 transforms
    mercator.go                     WGS84 <-> Web Mercator tile math
    projection.go                   Extensible projection interface
    hilbert.go                      Hilbert curve for spatial tile ordering
  tile/
    generator.go                    Parallel tile generation pipeline (GeoTIFF sources)
    transform.go                    PMTiles transform pipeline (passthrough/re-encode/rebuild)
    resample.go                     Lanczos/bicubic/bilinear/nearest/mode interpolation + reprojection (LUT-accelerated, optional gamma encode)
    downsample.go                   Pyramid downsampling for lower zoom levels
    diskstore.go                    Disk-backed tile store with memory backpressure
    rgbapool.go                     sync.Pool for *image.RGBA reuse (keyed by dimensions)
    zoom.go                         Zoom level auto-calculation
    progress.go                     Progress reporting
  encode/
    encoder.go                      Unified encoding interface
    jpeg.go                         JPEG encoder
    png.go                          PNG encoder
    webp.go                         WebP encoder/decoder (native libwebp via CGo)
    webp_stub.go                    WebP stubs for non-CGo builds (returns errors)
    webp_available.go               CGo availability flag for conditional tests
    terrarium.go                    Terrarium encoder for elevation data
  pmtiles/
    writer.go                       PMTiles v3 two-pass writer with tile clustering and metadata
    reader.go                       PMTiles v3 reader (header, directory, tile data, metadata)
    header.go                       Header serialization/deserialization (127 bytes)
    directory.go                    Hilbert-curve tile IDs, directory serialization/deserialization
integration/
  helpers_test.go                 Synthetic GeoTIFF writer, pipeline runners, PMTiles validation, plausibility checks
  synthetic_test.go               12 end-to-end tests using generated GeoTIFFs
  satellite_*_test.go             Per-dataset tests using real COGs (skipped if data absent)
  testdata/
    download.sh                   Script to fetch real satellite/raster test data
    swissimage/                   swisstopo SWISSIMAGE DOP10 (8-bit RGB, EPSG:2056 LV95)
```

## Pipeline

1. **Scan**: Collect and open GeoTIFF/COG input files (tiled or strip-based, with optional TFW sidecar)
2. **Metadata**: Parse GeoTIFF tags (or TFW) for CRS, bounds, and resolution; promote strips to virtual tiles
3. **Plan**: Compute merged WGS84 bounds and zoom range; auto-detect float data
4. **Generate (max zoom)**: Enumerate tiles, sort by Hilbert curve, distribute to worker pool
5. **Reproject**: Per-pixel inverse projection from output tile to source CRS
6. **Resample**: Lanczos-3, bicubic (Catmull-Rom), bilinear, nearest-neighbor, or mode (most common value) interpolation from source COG tiles (cached)
7. **Downsample (lower zooms)**: Combine 4 child tiles into parent tiles via pyramid downsampling
8. **Encode**: JPEG/PNG/WebP/Terrarium encoding
9. **Write**: Two-pass PMTiles assembly (temp file for tile data, then final archive with clustering)

Empty tile filling (`--fill-color`) uses the same color transformation model as
`pmtransform`: transparent/nodata pixels in rendered tiles are substituted with
the target color, nil-child quadrants during downsampling become fill tiles, and
solid-color tiles are generated for tile positions with no source data.

## Transform Pipeline (pmtransform)

`pmtransform` reads an existing PMTiles archive and produces a new one with modifications.
The original file is never touched. Three processing modes are selected automatically:

1. **Passthrough**: No format or zoom change — raw tile bytes are copied directly (fastest)
2. **Re-encode**: Format changes (e.g. WebP → PNG) — each tile is decoded and re-encoded
3. **Rebuild pyramid**: Zoom range extension or `--rebuild` flag — max-zoom tiles are decoded,
   then the entire lower-zoom pyramid is rebuilt via downsampling with the chosen resampling method.
   When `--tile-size` is omitted, the source tile size is discovered by decoding one tile.

Empty tile filling (`--fill-color`) uses a color transformation model: transparent/
nodata pixels are substituted with the target color rather than resampled. During
rebuild, transparent pixels in decoded tiles and nil-child quadrants in downsampled
tiles become the fill color. Additionally, solid-color tiles are generated for tile
positions within bounds that have no data.

For sparse datasets, rebuild tracks which positions contain real (non-fill) data and
propagates upward through zoom levels. Only real positions go through the expensive
downsample/encode pipeline; all-fill positions get pre-encoded bytes written directly,
skipping DiskTileStore overhead entirely.

## Memory Efficiency

- Memory-mapped file access (no full-image decode)
- LRU tile cache prevents redundant reads (~256 tiles, configurable)
- Tiles stored as encoded bytes (PNG/WebP/JPEG) in memory: 5-25x smaller than raw pixels
- Continuous disk spilling via dedicated I/O goroutine with configurable memory backpressure (auto ~90% of RAM)
- Uniform tiles (single color) stored as 4 bytes, never spilled to disk
- `sync.Pool` for `*image.RGBA` buffers: render, downsample, and decode paths reuse 256 KB buffers instead of allocating/GC'ing per tile
- Nodata pixels (all bands equal to GDAL_NODATA tag value) decoded as transparent (alpha=0) for single-band and multi-band/16-bit data; stored in `BandConfig.HasNodata`/`Nodata`, auto-detected from GeoTIFF, overridable with `--nodata`
- Source fallthrough on nodata: transparent (alpha=0) samples are skipped and the next source is tried, preventing holes in one source from blocking valid data in another
- PMTiles writer uses temp file for tile data (only directory entries in memory)
- Pyramid downsampling avoids redundant source reads for lower zoom levels

## Adding New Projections

Implement the `coord.Projection` interface:

```go
type Projection interface {
    ToWGS84(x, y float64) (lon, lat float64)
    FromWGS84(lon, lat float64) (x, y float64)
    EPSG() int
}
```

Then register it in `coord.ForEPSG()`.
