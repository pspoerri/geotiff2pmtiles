# Fixes from the branch review

A review of the whole branch reproduced each finding below on real or crafted files. Every
behaviour change has a test that failed before the fix.

## TIFF reading

- **Large single-strip TIFFs.** The 2^28-sample tile cap from 1837b82 also applied to a
  strip file's virtual tiles, which are the full width by RowsPerStrip. A 12000×8000 RGB
  image stored as one strip, DEFLATE or uncompressed, failed to open with "too large to
  decode". A virtual tile past the cap now opens when its strips can fill it: their byte
  counts, each capped at the file size, times the most a byte can decode to (1
  uncompressed, 1032 deflate, 2731 LZW, 32768 otherwise). Garbage Width or RowsPerStrip
  tags still fail at open, in 0.00 s. `tileSamples` saturates on overflow: a crafted
  2^31 × 2^31 × 4 strip file wrapped to 0 samples and opened. The error now suggests
  `gdal_translate -co TILED=YES`. Both 12000×8000 files convert (17 tiles, 1.2-1.6 GB
  peak memory), and the DEFLATE one gives the same archive as a tiled copy.
- **Band-interleaved layouts at open.** Band-interleaved tiles in any compression but JPEG,
  and band-interleaved JPEG strips, which GDAL writes for `INTERLEAVE=BAND`, opened and
  then failed every read. The pipeline turned that into one suppressed warning, an empty
  2 KB archive and exit code 0. `checkLayout` now rejects them at open with the file name
  and a `gdal_translate -co INTERLEAVE=PIXEL` hint, which is what ARCHITECTURE and DESIGN
  already claimed. The supported layouts (LZW strips, JPEG tiles) give the same archives.
- The comment on `decodeRawTile`'s fallback without a rescale said 0..65535; the code uses
  0..2^bits-1 (0..32767 at 15 bits), as the README now says.

## CRS and bounds

- **0..360 grids with an edge half a pixel west of 0.** Lon360 detection required the
  western edge at 0 or east of it. Grids of pixel centres from 0 start half a pixel west
  (GRIB-derived GFS or ERA5 at 0.25° start at -0.125, as does a PixelIsPoint grid after
  the corner correction), so their western hemisphere rendered transparent. Detection now
  accepts an edge up to a pixel west of 0, and `WGS84Identity.Lon360Min` wraps below that
  edge, so the strip between it and Greenwich is sampled where it is. `tileCRSBounds`
  shifts at the same longitude, and projections are keyed on (EPSG, Lon360, Lon360Min).
  Exact 0..360 grids render the same as before.
- **Guessed CRS warning.** `cog.Open` logged a warning per world-file input, before
  geotiff2pmtiles applied `--source-epsg`, so a composite of thousands of TFW tiles printed
  thousands of lines telling the user to set the flag they had set. The reader records
  the guess (`Reader.EPSGGuessed`, cleared by `SetEPSG`); geotiff2pmtiles logs one line
  with the count and an example, only without `--source-epsg`, and coginfo prints
  "(guessed from the coordinate ranges)" after the EPSG code.
- **Georeferencing and poles.** A TIFF without ModelPixelScale and ModelTiepoint tags or a
  `.tfw` passed the "not georeferenced" check once `--source-epsg` had set its EPSG code.
  Its pixel size of 0 drove the auto max zoom to 0, so one such file collapsed a whole
  composite to a single z0 tile, with exit code 0. Every source now needs a positive
  pixel size, whatever its EPSG code; the error also covers rotated ModelTransformation
  grids. A projected grid given `--source-epsg 4326` reached latitudes in the millions;
  merged latitudes beyond ±90° plus the coarsest source pixel now stop the run.
- **Mixed-CRS hole check.** Holes were searched among each CRS's sources separately, so a
  gap among EPSG:2056 tiles that an EPSG:4326 tile filled was logged and written into the
  description as "Holes: 1 gap(s)". Boxes in different CRSs cannot be compared, so with
  mixed CRSs the check is skipped with a note and the description has no Holes line.

## Output tools

- **jpeg warning colours.** The `--fill-missing` and `--nodata-color` warnings for a
  translucent colour said those areas would be black. The jpeg encoder writes the
  colour's straight RGB, opaque, so 255,0,0,128 gives red. Both tools' warnings now print
  the `rgb()` actually written.
- **pmtransform across the antimeridian.** Archives record such data as -180..180, and
  pmtransform rebuilt the header from those bounds: the centre moved from lon 180 to 0,
  and `--fill-missing` filled every column of the latitude band, so a 2°-wide Fiji archive
  grew from 31 tiles to 1983. The writer takes a `WriterOptions.Center`, which pmtransform
  sets from the source's `Header.Center()` with its zoom clamped. The fill extent of
  full-width bounds comes from the max zoom columns the data occupies, when their
  shortest run round the globe crosses the antimeridian. Trade-off: a placeholder centre
  in another tool's archive is now carried through.
- **pmheader.** With `--sync-metadata` on by default, every header edit on an archive of
  this tool changed the metadata and took the full rewrite: a temp file renamed over the
  path, which replaced a symlink with a regular file, left the archive 0600 and streamed
  all tile data; zstd or brotli metadata failed the edit. Now the metadata is overwritten
  in place when its new encoding, padded with JSON spaces and a gzip header comment, fits
  the old section (30 of 72 measured header edits on fresh archives; the others grow the
  gzip by 1-9 bytes). The rewrite resolves symlinks, writes next to the target and keeps
  the file's mode. Metadata pmheader cannot decode leaves a header edit to the header,
  with a warning.
- **checkpmtiles zoom range.** It compared only MinZoom with MaxZoom although its docs said
  it checks the zoom range: a header patched to MinZoom 7 over z6 tiles passed. It now
  fails when the smallest or largest addressed tile ID lies outside the header's zoom
  range, which clients never request. The same commit brought the README and DESIGN in
  line with the mask-aware webp switch and the per-depth range without a rescale.

## Documentation

README, ARCHITECTURE and DESIGN describe the fixes above: the layouts rejected at open and
the backed-strip rule, 0..360 grids of pixel centres and `Lon360Min`, the single guessed-CRS
warning, the geotransform and pole checks, the mixed-CRS hole check, the jpeg colours,
pmtransform's centre and fill extent, pmheader's in-place metadata, and checkpmtiles'
tile zoom check, with the trade-offs in DESIGN. Every tool's `-h` flags and defaults were
compared with the README tables by script.

Not done: huge strips are still decoded whole (bounded-height virtual tiles would cap the
memory), and the writer leaves no slack in the metadata, so most first header edits on a
fresh archive still rewrite it.
