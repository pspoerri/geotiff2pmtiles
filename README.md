# geotiff2pmtiles

A memory-efficient, pure-Go tool that converts GeoTIFF/COG files into PMTiles v3 archives.

## Features

- **Memory-efficient**: Reads COG tiles on-demand via seek-based I/O; never loads entire rasters into memory (~15-20 MB peak for typical workloads vs multi-GB for alternatives)
- **Pure Go**: No CGo or GDAL dependency; all encodings including WebP work without system libraries
- **Multiple encodings**: JPEG, PNG, and WebP
- **Auto zoom detection**: Calculates maximum zoom level from source resolution
- **Parallel processing**: Concurrent tile generation with configurable worker pool
- **PMTiles v3**: Writes spec-compliant archives with Hilbert-curve tile ordering
- **COG-aware**: Exploits Cloud Optimized GeoTIFF overview levels for lower zoom tiles

## Supported Input

- GeoTIFF / Cloud Optimized GeoTIFF (COG) files
- JPEG-compressed tiled TIFFs (the most common COG format)
- Currently supports EPSG:2056 (Swiss LV95), EPSG:4326 (WGS84), and EPSG:3857 (Web Mercator) source CRS
- Extensible projection interface for adding additional CRS support

## Installation

```bash
go build -o geotiff2pmtiles ./cmd/geotiff2pmtiles/
```

Or using the Makefile:

```bash
make build
```

## Usage

```
geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>
```

### Flags

| Flag            | Default       | Description                                    |
| --------------- | ------------- | ---------------------------------------------- |
| `--format`      | `jpeg`        | Tile encoding: `jpeg`, `png`, `webp`           |
| `--quality`     | `85`          | JPEG/WebP quality (1-100)                      |
| `--min-zoom`    | auto          | Minimum zoom level (default: max_zoom - 6)     |
| `--max-zoom`    | auto          | Maximum zoom level (auto-detected from resolution) |
| `--tile-size`   | `256`         | Output tile size in pixels                     |
| `--concurrency` | `NumCPU`      | Number of parallel workers                     |
| `--verbose`     | `false`       | Verbose progress output                        |

### Examples

Convert a directory of GeoTIFFs with auto zoom detection:

```bash
./geotiff2pmtiles --verbose data/ output.pmtiles
```

Convert specific files with custom zoom range and PNG format:

```bash
./geotiff2pmtiles --format png --min-zoom 10 --max-zoom 18 \
  file1.tif file2.tif output.pmtiles
```

High-quality JPEG at maximum zoom only:

```bash
./geotiff2pmtiles --format jpeg --quality 95 --min-zoom 20 --max-zoom 20 \
  data/ output.pmtiles
```

## Architecture

```
cmd/geotiff2pmtiles/main.go        CLI entry point
internal/
  cog/
    reader.go                       COG/GeoTIFF tile-level reader (seek-based)
    ifd.go                          TIFF IFD parser
    geotags.go                      GeoTIFF metadata extraction
    tilecache.go                    LRU tile cache for decoded source tiles
  coord/
    swiss.go                        EPSG:2056 <-> WGS84 transforms
    mercator.go                     WGS84 <-> Web Mercator tile math
    projection.go                   Extensible projection interface
  tile/
    generator.go                    Parallel tile generation pipeline
    resample.go                     Bilinear interpolation + reprojection
    zoom.go                         Zoom level auto-calculation
  encode/
    encoder.go                      Unified encoding interface
    jpeg.go / png.go / webp.go      Format-specific encoders
  pmtiles/
    writer.go                       PMTiles v3 two-pass writer
    header.go                       Header serialization (127 bytes)
    directory.go                    Hilbert-curve tile IDs + directory compression
```

### Pipeline

1. **Scan**: Collect and open GeoTIFF/COG input files
2. **Metadata**: Parse GeoTIFF tags for CRS, bounds, and resolution
3. **Plan**: Compute merged WGS84 bounds and zoom range
4. **Generate**: For each zoom level, enumerate tiles and distribute to worker pool
5. **Reproject**: Per-pixel inverse projection from output tile to source CRS
6. **Resample**: Bilinear interpolation from source COG tiles (cached)
7. **Encode**: JPEG/PNG/WebP encoding
8. **Write**: Two-pass PMTiles assembly (temp file for tile data, then final archive)

### Memory Efficiency

- COG tiles are read on-demand via `ReadAt` (no full-image decode)
- LRU tile cache prevents redundant reads (~256 tiles, configurable)
- Worker buffers are bounded by concurrency setting
- PMTiles writer uses temp file for tile data (only directory entries in memory)
- Typical peak memory: 15-20 MB for 36 input files at z14-z20

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

## License

MIT
