# Unified Preset Auto-Detection

Consolidate float/terrarium and multi-band satellite detection into a single
`DetectPreset()` method. The `Preset` struct now carries an optional `Format` field
(e.g. "terrarium") in addition to `BandCfg`.

## Changes

- **cog/reader.go**: `Preset` gains `Format string` field. `DetectPreset()` checks
  float data first (returns `float-terrarium` preset with `Format: "terrarium"`),
  then multi-band GDAL metadata detection. Removed separate `detectSentinel2()`
  method — band detection is purely generic via per-band DESCRIPTION items and
  dataset-level "bands" strings.

- **cmd/geotiff2pmtiles/main.go**: Replaced inline float detection with preset-based
  flow. `DetectPreset()` is called once before `parseBandConfig`; if the preset has
  a format override and the current format is the default (jpeg), the format is
  switched. The terrarium validation check remains.

- **cmd/coginfo/main.go**: Show `Format` field when present in detected preset.
  Only show band/rescale info when rescaling is configured.

- **cog/reader_test.go**: Added `TestDetectPresetFloatTerrarium` for float32 data.
