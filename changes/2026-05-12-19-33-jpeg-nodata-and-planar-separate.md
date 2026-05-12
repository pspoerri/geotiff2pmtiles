# JPEG nodata, tolerance, output-format auto-switch, and planar-separate JPEG

## Problem

Reported while converting `Katahdin1952_rgb_jpg.tif` — a JPEG-compressed, 3-band,
`PlanarConfiguration=2` BigTIFF carrying a grayscale scan padded with black at
the scanned-area edges:

```
./geotiff2pmtiles --verbose --nodata 0 Katahdin1952_rgb_jpg.tif kt.pmtiles
...
Band config: bands 1,2,3, nodata 0   ← logged, but no pixels were ever masked
```

The black border was never detected as nodata. Investigation surfaced four
distinct issues:

1. `decodeJPEGTile` in `internal/cog/reader.go` returned the JPEG-decoded image
   directly and never consulted `BandConfig.HasNodata`/`Nodata`. Nodata worked
   on raw / Deflate / LZW tiles but was silently dropped on JPEG.
2. Even where nodata was honoured, the comparison was strict equality. Borders
   in lossy-JPEG sources are smeared by quantisation (typical values 1..8 for a
   pure-black border at quality 50), so an exact-match `value == 0` matched
   nothing useful.
3. Output format defaulted to `jpeg`, which cannot carry alpha. Even with the
   other two issues fixed, transparent pixels would be re-encoded as black.
4. The reader parsed `PlanarConfiguration` but never used it. With a planar-
   separate file, only the first plane's tile index was decoded. The Katahdin
   file rendered "correctly" only because all three bands held identical
   grayscale data — a genuinely-colour planar-separate JPEG COG would have been
   rendered as red-only.

## Changes

### `internal/cog/reader.go`

- `BandConfig` gains `NodataTolerance float64`. A sample matches nodata when
  `|sample - Nodata| ≤ NodataTolerance`. Default 0 keeps the exact-match
  behaviour.
- `decodeRawTile`'s general-path nodata loop now uses the tolerance.
- `decodeJPEGTile` was split: `decodeJPEGBytes` produces the raw decoded image
  (preserving the YCbCr/Gray fast path when no nodata is configured), and
  `decodeJPEGTile` materialises RGBA + applies a nodata mask only when
  `BandConfig.HasNodata` is set.
- `decodePlanarSeparateJPEG` decodes each per-plane JPEG tile (offsets are
  plane-major) and merges into RGBA. Plane 0→R, 1→G, 2→B, 3→A; channels beyond
  the available plane count fall back to plane 0 so grayscale planar-separate
  files render correctly. Nodata is applied after the merge.
- `ReadTile` dispatches `PlanarConfig=2 && SamplesPerPixel>1` to the new
  function. Non-JPEG planar-separate (uncompressed, Deflate, LZW) returns an
  explicit error rather than silently returning plane 0.

### `cmd/geotiff2pmtiles/main.go`

- New `--nodata-tolerance` flag.
- When `--nodata` is set and `--format` was left at its default, the CLI
  switches output `jpeg` → `webp` so transparency survives encoding. When the
  user explicitly chose `--format=jpeg`, the CLI warns instead of switching.
- `isFlagSet(name)` helper distinguishes default values from explicit user
  input.

### Tests (`internal/cog/jpeg_test.go`)

- `TestDecodeJPEGTileNodataExact` — a synthesised single-band JPEG with a
  half-black/half-gray layout becomes half-transparent/half-opaque.
- `TestDecodeJPEGTileNoNodataPassthrough` — verifies the YCbCr/Gray fast path
  is preserved when no nodata is configured (return type is *not* `*image.RGBA`).
- `TestPlanarSeparateJPEG` — three solid-colour grayscale JPEG planes merge to
  the expected RGBA pixel within JPEG's quality tolerance.
- `TestPlanarSeparateRawErrors` — uncompressed planar-separate returns a clear
  error instead of returning wrong pixels.
- `TestNodataTolerance` — `(3,2,4)` is masked when tolerance is 5.

### Docs

- `README.md`: flag table entries for `--nodata` (auto-switch behaviour) and
  `--nodata-tolerance`; new example for the lossy-border use case.
- `ARCHITECTURE.md`: updated nodata bullet and added a planar-separate bullet.
- `DESIGN.md`: extended the nodata section with the tolerance rationale, JPEG
  decode path, planar-separate decoding, and the output-format auto-switch.

## Verification

```
$ ./dist/geotiff2pmtiles --verbose --nodata 0 --nodata-tolerance 8 \
    Katahdin1952_rgb_jpg.tif /tmp/kt.pmtiles
Nodata is active; switching output format jpeg → webp ...
Band config: bands 1,2,3, nodata 0 (tol 8)
Zoom 15: completed (2067 tiles so far, 1493 gray, 366 uniform, 0 empty)
```

`366 uniform` tiles at z15 are the all-transparent border tiles being compacted
away — they were `0 uniform` before the fix.

```
$ go test ./...
ok      .../integration
ok      .../internal/cog
ok      .../internal/coord
ok      .../internal/encode
ok      .../internal/pmtiles
ok      .../internal/tile
```

## Out of scope

- Raw/Deflate/LZW planar-separate COGs return an explicit error rather than a
  full decode. The TIFF spec allows it, but it's rare in practice and would
  need a per-plane raw decode path. Filed as a follow-up.
- The legacy single-band nodata path (`spp ≤ 2 && default BandConfig`) reads
  nodata from the IFD GDAL_NODATA tag only and ignores `BandConfig.HasNodata`.
  This was the existing behaviour and is unchanged here; the general path now
  handles the case via tolerance, and the legacy fast path remains.
