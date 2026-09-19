# UTM support and wroge/crs fallback for other projections

Closes #50 (drone imagery is usually delivered in UTM).

- New native `coord.UTM`: EPSG:32601–32660, 32701–32760 (WGS84) and 25828–25838
  (ETRS89, treated as WGS84). Cross-checked against wroge/crs to < 1 mm.
- New `coord.CRSFallback`: any other EPSG code known to `github.com/wroge/crs`.
  The datum shift is cached on a 0.05° grid and interpolated, because the library's
  per-call datum path search (~50 µs) is too slow per pixel. See DESIGN.md.
- geotiff2pmtiles logs a note when the fallback projection is used.
- `cog.MergedBoundsWGS84` uses `coord.ForEPSG` instead of its own EPSG switch
  (unknown codes were silently treated as lon/lat); duplicate LV95/Web Mercator
  helpers in `cog/reader.go` removed.
- New pure-Go dependency `github.com/wroge/crs` v0.0.4. The embedded EPSG registry
  grows the CGO=0 binary from ~6.5 MB to ~14 MB.

Not included: EPSG inference for UTM without GeoKeys (the zone is ambiguous), and
user-defined CRSs (GeoKey 32767).
