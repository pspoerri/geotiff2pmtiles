# README cleanup

Shortened `README.md` from 412 to ~310 lines and fixed remarks that went stale when the
pure-Go WebP fallback landed:

- `CGO_ENABLED=0` / `cross-windows*` builds are no longer "no WebP" — they have lossless
  WebP. Same fix in the `Makefile` comments.
- libwebp is a prerequisite for *lossy* WebP only, not for WebP support.
- `make build-all` builds all four tools, not "both binaries".
- Merged Platform Support / Prerequisites / Installation / Cross-compilation into one
  Installation section with a release-binary table (Windows: static libwebp; Linux/macOS:
  pure-Go lossless).
- Added the undocumented `pmheader` tool to Utilities; collapsed Utilities to one block.
- Condensed Features (dropped implementation details covered by ARCHITECTURE.md), removed
  the duplicated test-data paragraph from the intro, two near-duplicate examples, the
  Performance blurb and the per-dataset test target list.
- Moved Installation to the bottom of the README and split the build-from-source
  instructions into a new `BUILDING.md`, linked from the README.
- Moved the Development section (integration tests, profiling) into a new
  `DEVELOPMENT.md`, linked from the README.

Other documents:

- `DESIGN.md`: sections grouped by topic (input, projections, nodata, resampling, memory,
  encoding/platforms, PMTiles output, pmtransform, testing) instead of chronological order.
  Removed the "not yet implemented" fill-color bullet that contradicted the one after it,
  the claim that zstd is the only external dependency, the reference to CGo-free Windows
  builds, and `make cross-all CGO=0` (the `cross-*` targets ignore `CGO`). Predictor 2/3
  details moved from under "ZSTD compression" to "TIFF predictor support"; "Performance
  profile" folded into the libwebp and bicubic sections.
- `ARCHITECTURE.md`: layout lists `pmheader`, `flood.go`, `memlimit.go`, `tiledata.go`,
  `decode.go` and the newer integration tests; nodata bullets moved out of "Memory
  Efficiency" into their own section and shortened; duplicate `--fill-color` paragraph removed.
- `integration/testdata/README.md`: NDVI/SWIR/gamma0 depth and band counts corrected
  (were listed as 16-bit single-band); duplicated target lists replaced by a link.
- `AGENTS.md` deleted; its task checklist was already in `CLAUDE.md` and its doc-scope
  list (now including the two new documents) moved there.
- README Layout row: tiled band-interleaved files are JPEG-only, stripped ones anything but.

Makefile (519 → 358 lines):

- Removed the twelve `example-*-{jpeg,png,webp}` wrapper targets; use `FORMAT=` instead
  (`make example-swissimage FORMAT=png`). `example-all` now loops over the formats with
  `$(MAKE)` — before, the three wrappers shared one prerequisite, which make runs only once
  per invocation, so `example-all` only ever produced the first format.
- Shared example flags moved into `EXAMPLE_FLAGS`; the transform examples reuse the output
  of `example-swissimage` instead of repeating the conversion.
- Removed unused `MIN_ZOOM` and `MEM_LIMIT` variables; `all` is now `build-all`.
- `make help`: target list no longer breaks on descriptions containing `:`; dropped the
  hand-maintained example list that duplicated it. `.PHONY` regenerated.
- `help` is now the default goal (plain `make` no longer builds; use `make all`). Its output
  starts with a short "Common" list, then targets grouped as Build / Test and code quality /
  Integration tests / Examples / Profiling / Cross-compilation, driven by `##@ Section`
  lines in the Makefile. `run`, `clean` and `clean-all` moved into Build.

No code changes; CLI help is unaffected.
