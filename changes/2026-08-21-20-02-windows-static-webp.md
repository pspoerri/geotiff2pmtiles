# Windows binaries ship with statically linked WebP

## Problem

The released Windows binaries were built at `CGO_ENABLED=0` and had no WebP encoder:
`--format webp` returned an error, even though a source build with libwebp installed had
it. The cause was the CI layout — a single Ubuntu job cross-compiled all six targets, and
there is no packaged mingw libwebp on Ubuntu to cross-link against.

(The released Linux and macOS binaries are still cross-compiled at `CGO_ENABLED=0` and
still lack WebP; giving them the same treatment means building each natively too.)

## Change

Windows is now built natively in CI, with WebP as the default build variant on both
architectures:

- **New `windows` CI job**, matrixed over `windows-latest` (UCRT64, gcc, amd64) and
  `windows-11-arm` (CLANGARM64, clang, arm64). `msys2/setup-msys2` installs MSYS2's
  prebuilt `libwebp` package — no cross toolchain, no libwebp source build. The job runs
  vet, unit tests and the synthetic integration tests before building, so the Windows
  binaries are now tested on the architecture that produced them.
- **Static linking** via `-extldflags=-static`: libwebp, libsharpyuv and the compiler
  runtime end up inside the `.exe`, which therefore needs no MSYS2 DLLs to run.
- **Smoke test** runs the built binary with the MSYS2 `bin` directory off `PATH` (a
  dynamically linked build would fail to start) and asserts `--version` lists `webp`.
- The Ubuntu build job now covers Linux and macOS only; the release job collects
  artifacts from both jobs.

Supporting changes:

- `encode/webp.go`: `#cgo !windows pkg-config: libwebp` plus `#cgo windows LDFLAGS:
  -lwebp -lsharpyuv`. cgo runs `pkg-config` without `--static`, so on Windows it would
  drop `-lsharpyuv` and the static link would fail; MSYS2 has the headers and libraries
  on the default search path, so pkg-config is not needed there at all.
- `encode.Formats()` reports the formats compiled into the binary. Both CLIs print it
  under `--version`, and `NewEncoder`'s "unsupported format" error uses it instead of a
  hardcoded list that lied in `CGO_ENABLED=0` builds.
- `Makefile`: on Windows (`OS=Windows_NT`) outputs get an `.exe` suffix and the static
  link flag automatically, so `make build` is the full-featured Windows build.
- `make cross-windows{,-arm64}` still build at `CGO_ENABLED=0` (no toolchain, no WebP)
  for anyone cross-compiling from Unix.

## Verification

- `go test ./...` and `gofmt`/`go vet` clean on macOS arm64.
- `--version` prints `formats: jpeg, png, webp, terrarium` with CGo and
  `formats: jpeg, png, terrarium` at `CGO_ENABLED=0`; `TestFormatsMatchesBuild` locks
  that in.
- The Windows build paths themselves are verified by the new CI job — they cannot be
  exercised on a macOS host.
