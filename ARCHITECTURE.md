# Architecture

```
cmd/
  geotiff2pmtiles/main.go          CLI entry point
  coginfo/main.go                   COG metadata inspector
  debug/main.go                     Low-level COG debug utility
internal/
  cog/
    reader.go                       COG/GeoTIFF tile-level reader (memory-mapped, nodata-aware)
    ifd.go                          TIFF IFD parser
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
    generator.go                    Parallel tile generation pipeline
    resample.go                     Lanczos/bicubic/bilinear/nearest/mode interpolation + reprojection (LUT-accelerated)
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
    writer.go                       PMTiles v3 two-pass writer with tile clustering
    header.go                       Header serialization (127 bytes)
    directory.go                    Hilbert-curve tile IDs + directory compression
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

## Memory Efficiency

- Memory-mapped file access (no full-image decode)
- LRU tile cache prevents redundant reads (~256 tiles, configurable)
- Tiles stored as encoded bytes (PNG/WebP/JPEG) in memory: 5-25x smaller than raw pixels
- Continuous disk spilling via dedicated I/O goroutine with configurable memory backpressure (auto ~90% of RAM)
- Memory limit accounts for both encoded tile data and Go map overhead (uniform entries, disk index entries) to prevent actual usage from exceeding the configured limit
- Map pre-allocation sized to the working set (tiles that fit in memory), not the total tile count, avoiding multi-GB upfront waste on empty hash buckets
- Uniform tiles (single color) stored as 4 bytes, never spilled to disk
- `sync.Pool` for `*image.RGBA` buffers: render, downsample, and decode paths reuse 256 KB buffers (zeroed on get) instead of allocating/GC'ing per tile
- Single-band nodata pixels decoded as transparent (alpha=0) so resampling/downsampling automatically excludes them
- Source fallthrough on nodata: transparent (alpha=0) samples are skipped and the next source is tried, preventing holes in one source from blocking valid data in another and eliminating all-transparent tiles from the pyramid
- Gray tile RGBA expansions (from `AsImage()`) cached in the TileData so `Release()` returns them to the pool
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
