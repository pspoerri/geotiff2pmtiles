# Automatic rescale range

`--rescale-range` is no longer required for 16-bit input. When omitted (with
`--rescale auto`, `linear` or `log`) and no metadata preset applies, the range is
taken from GDAL `STATISTICS_MINIMUM/MAXIMUM`, else from a bounded pixel scan
(coarsest level, ≤64 tiles, nodata and tile padding skipped), unioned over all
sources. The selected values are logged:

    Auto rescale range: [7180, 14655] (from GDAL statistics)

- `internal/cog/valuerange.go`: `Reader.ValueRange()`; tests in `valuerange_test.go`.
- `cmd/geotiff2pmtiles/main.go`: `resolveRescaleRange` replaces three copies of
  the range-parsing/erroring code; help text updated.
