# RGBNIR / uint16 GeoTIFF Support

## Summary

Added support for 16-bit (uint16) GeoTIFF files with band reordering, alpha band
selection, and configurable rescaling (linear/logarithmic) from uint16 to uint8.

## Changes

### Bug fix: 16-bit horizontal differencing predictor
- `undoHorizontalDifferencing` now accepts `bytesPerSample` and `binary.ByteOrder`
  parameters, correctly handling uint16 delta accumulation for predictor=2 with
  16-bit data. Previously read 1 byte per sample, garbling 16-bit deflate+predictor
  compressed files (e.g. ESA WorldCover S2 RGBNIR).

### New: BandConfig and rescaling
- `BandConfig` struct controls band selection (`Bands [3]int`), alpha band
  (`AlphaBand int`), and rescaling mode/range.
- `buildRescaler` returns a `func(uint16) uint8` for None/Linear/Log modes.
- `decodeRawTile` rewritten to handle both 8-bit and 16-bit samples, with band
  reordering, configurable alpha, and rescaling. Zero-value BandConfig preserves
  exact legacy behavior for 8-bit data.

### New CLI flags (geotiff2pmtiles)
- `--bands`: 1-indexed band numbers for R,G,B output (default: `1,2,3`)
- `--alpha-band`: Alpha band selection (`auto`, `-1`=none, or 1-indexed band)
- `--rescale`: Rescale mode (`auto`, `linear`, `log`, `none`)
- `--rescale-range`: Input value range `min,max` (required when --rescale is explicit)

### New accessors
- `Reader.SetBandConfig(cfg BandConfig)` — set config after open, before reading
- `Reader.BitsPerSample() int` — bits per sample of first IFD
- `Reader.SamplesPerPixel() int` — samples per pixel of first IFD

## Files modified
- `internal/cog/reader.go` — core changes
- `internal/cog/ifd.go` — `bytesPerSample()` method
- `internal/cog/reader_test.go` — new test file with 15 tests
- `cmd/geotiff2pmtiles/main.go` — CLI flags, band config parsing
- `DESIGN.md`, `ARCHITECTURE.md`, `README.md` — documentation
