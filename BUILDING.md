# Building from source

Requires Go (version in `go.mod`). Prebuilt binaries: see [README](README.md#installation).

Install libwebp (only needed for lossy WebP):

```bash
brew install webp                  # macOS
sudo apt-get install libwebp-dev   # Debian/Ubuntu
sudo dnf install libwebp-devel     # Fedora/RHEL

# Windows: from the MSYS2 UCRT64 shell (CLANGARM64 on arm64)
pacman -S mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-libwebp         # amd64
pacman -S mingw-w64-clang-aarch64-clang mingw-w64-clang-aarch64-libwebp   # arm64
```

Then:

```bash
make build            # geotiff2pmtiles
make build-transform  # pmtransform
make build-all        # all tools, into dist/
make build CGO=0      # without libwebp (lossless-only WebP)
```

Or plain `go build ./cmd/geotiff2pmtiles/`. On Windows, run either from the
[MSYS2](https://www.msys2.org) shell so libwebp is found; the Makefile adds `.exe` and links
libwebp statically (pass `-ldflags "-extldflags=-static"` to a plain `go build`).

`make cross-all` cross-compiles geotiff2pmtiles. The Linux and macOS targets use CGo, so
they need a C cross-compiler and libwebp for the target; `cross-windows*` builds with
`CGO_ENABLED=0` (lossless-only WebP) — build natively under MSYS2 for lossy WebP.
