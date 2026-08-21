# Windows support + toolchain/action updates

## Problem

The toolset built on Linux and macOS only. `internal/cog/mmap_unix.go` is
tagged `//go:build unix` and the `!unix` fallback returned "memory mapping is
not supported on this platform", so `cog.Open` failed on every file. RAM
detection had the same shape (`sysinfo_{linux,darwin,other}.go`). Beyond the
two missing implementations, several places assumed POSIX file and console
semantics:

- `pmheader` renamed the rewritten archive over its input while the input
  handle was still open. Windows opens without `FILE_SHARE_DELETE`
  (GOROOT `syscall/syscall_windows.go`), so `MoveFileEx` is refused and every
  in-place `--set` / `--unset` / `--rebuild-dirs` run failed.
- Same-file guards (`pmheader`, `pmtransform`) and `.pmtiles` extension checks
  compared path strings, so `map.pmtiles` vs `MAP.PMTILES` — one file on a
  case-insensitive filesystem — slipped through.
- `pmtransform` built the tile encoder before deciding the transform mode, so
  passthrough of a WebP archive needed a WebP encoder that a CGO-less build
  does not have. `geotiff2pmtiles` treated the automatic jpeg → webp switch
  for nodata as fatal for the same reason.
- The progress bar ended each redraw with `\033[K`; Windows consoles print the
  escape literally unless VT processing is enabled.
- `collectTIFFs` passed arguments straight to `os.Stat`. Neither cmd.exe nor
  PowerShell expands wildcards, so `geotiff2pmtiles data\*.tif out.pmtiles`
  aborted.

Separately, `readTileRaw` undid TIFF predictors in place on the *read-only
mapping* for uncompressed tiles (the tiled RGB path at `reader.go:737` already
copied; the float path did not). That is a SIGBUS on Unix and an access
violation on Windows — reproduced with a 16×16 uncompressed float32 tile with
`Predictor=3`.

## Fix

- `internal/cog/mmap_windows.go` — `CreateFileMapping` + `MapViewOfFile`
  (`PAGE_READONLY` / `FILE_MAP_READ`) from stdlib `syscall`, no new
  dependency. The mapping handle is closed immediately; the view keeps the
  section alive, so the `os.File` can be closed as on Unix. The slice header
  is built by hand because `unsafe.Slice` on a `uintptr` trips `go vet`'s
  `unsafeptr` check.
- `internal/tile/sysinfo_windows.go` — `GlobalMemoryStatusEx` via
  `syscall.NewLazyDLL("kernel32.dll")`.
- `mmap_other.go` / `sysinfo_other.go` build tags narrowed to `&& !windows`.
- `readTileRaw` copies before undoing the predictor on uncompressed tiles.
- `pmheader` closes the input before the in-place rename; both `pmheader` and
  `pmtransform` now compare file identity with `os.SameFile`; extension checks
  use `strings.EqualFold(filepath.Ext(p), ".pmtiles")`.
- `pmtransform` constructs the encoder only for re-encode/rebuild modes
  (passthrough copies raw bytes and never needs one).
- `geotiff2pmtiles` falls back to PNG with a warning when the nodata-driven
  WebP switch finds no WebP encoder, and expands `*`/`?`/`[` patterns with
  `filepath.Glob` when a path does not stat.
- Progress bar pads with spaces instead of `\033[K`.
- `DiskTileStore.Close` warns instead of silently ignoring a failed spill-file
  removal (Windows refuses to delete files that any handle still holds).
- Build: `make cross-windows` / `cross-windows-arm64` (CGO off), `CGO ?= 1` so
  `make build CGO=0` works, `*.exe` in `.gitignore`, CI gains a
  `windows-latest` test job and windows/amd64 + windows/arm64 build legs with
  an `.exe` suffix.
- Versions: go directive 1.25.5 → 1.27.0; actions checkout v4→v7, setup-go
  v5→v7, upload-artifact v4→v7, download-artifact v4→v8, cache v4→v6,
  softprops/action-gh-release v2→v3. There are no external Go modules.

## Testing

`internal/cog/float_predictor_test.go` builds an uncompressed float32 TIFF
with `Predictor=3` and reads it back; it faults on the pre-fix reader and
passes after. `make check` passes, as does `CGO_ENABLED=0 go test ./...`
(the configuration the Windows CI job uses). `GOOS=windows go vet ./...`
passes for amd64 and arm64, and both Windows binaries cross-compile.

Windows runtime behaviour (the mapping itself, console output, the rename
path) is covered by the new `Test (windows)` CI job — it is not exercisable
from a macOS or Linux checkout.
