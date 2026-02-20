# Startup Settings Display

## Change

Print a summary of all effective configuration settings when the program starts,
right before tile generation begins. This runs unconditionally (not just with `--verbose`).

## Output format

```
geotiff2pmtiles dev (commit abc1234, built 2026-02-18)
  Format:        jpeg (quality: 85)
  Tile size:     256px
  Zoom:          4 – 10 (auto-max: 10)
  Resampling:    lanczos
  Concurrency:   8
  Mem limit:     auto (~90% of RAM)
  Input:         3 file(s)
  Output:        output.pmtiles
```

Quality is shown only for jpeg and webp. Memory limit adapts to `--no-spill` and
`--mem-limit` flags.

## Files changed

- `cmd/geotiff2pmtiles/main.go` — added settings summary block after zoom/memory
  resolution, before `tile.Config` construction.
