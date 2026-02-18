# Architecture

```
cmd/
  geotiff2pmtiles/main.go          CLI entry point
  coginfo/main.go                   COG metadata inspector
  debug/main.go                     Low-level COG debug utility
internal/
  cog/
    reader.go                       COG/GeoTIFF tile-level reader (memory-mapped)
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
    resample.go                     Lanczos/bicubic/bilinear/nearest interpolation + reprojection
    downsample.go                   Pyramid downsampling for lower zoom levels
    diskstore.go                    Disk-backed tile store with memory backpressure
    zoom.go                         Zoom level auto-calculation
    progress.go                     Progress reporting
  encode/
    encoder.go                      Unified encoding interface
    jpeg.go                         JPEG encoder
    png.go                          PNG encoder
    webp.go                         WebP encoder (pure Go via gen2brain/webp)
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
6. **Resample**: Lanczos-3, bicubic (Catmull-Rom), bilinear, or nearest-neighbor interpolation from source COG tiles (cached)
7. **Downsample (lower zooms)**: Combine 4 child tiles into parent tiles via pyramid downsampling
8. **Encode**: JPEG/PNG/WebP/Terrarium encoding
9. **Write**: Two-pass PMTiles assembly (temp file for tile data, then final archive with clustering)

## Memory Efficiency

- Memory-mapped file access (no full-image decode)
- LRU tile cache prevents redundant reads (~256 tiles, configurable)
- Tiles stored as encoded bytes (PNG/WebP/JPEG) in memory: 5-25x smaller than raw pixels
- Continuous disk spilling via dedicated I/O goroutine with configurable memory backpressure (auto ~90% of RAM)
- Uniform tiles (single color) stored as 4 bytes, never spilled to disk
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
