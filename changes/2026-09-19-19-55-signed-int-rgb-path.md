# Signed int16 through the RGB/rescale path

`--rescale linear --format webp` on GEBCO reported `Auto rescale range: [0, 65535]`
and lost the depths: only the terrarium path understood signed samples.

- `decodeRawTile` and the `ValueRange` pixel scan bias signed 16-bit samples
  (`raw ^ 0x8000`), shifting the rescale range and nodata with them. GEBCO now
  resolves to `[-10668, 8627]` with nodata -32767.
- The elevation preset no longer short-circuits rescale config for non-terrarium
  formats; terrarium skips rescale range detection entirely.
- `--nodata` accepts negative integers (down to -32768).
- Single-band sources render as gray in the rescale path (was red-only).
- Tests: `TestSignedInt16RGBRescale`.
