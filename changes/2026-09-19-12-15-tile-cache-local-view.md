# Goroutine-local view in front of the source tile cache

Profiling the SWISSIMAGE example (zoom 18, WebP, 18 workers) showed no
regression against `250e059` / `210ee62`, but a long-standing hotspot:
`runtime.usleep` at ~35% of CPU samples and `cog.TileCache.Get` at ~18%
cumulative. The cache is already sharded; the cost was one mutex + LRU update
per output pixel, with neighbouring workers hammering the same shards.

- `cog.TileCache.Local()` returns a single-goroutine view with a 4-slot memo
  (slot = `col&1 | row&1<<1`); only memo misses reach the shared cache.
- `renderTile` creates one view per rendered tile.
- `TestTileCacheLocal` covers read-through, slot collisions, Put forwarding
  and the nil cache.

Result (two runs each): 21.7–22.6 s → 15.2–16.5 s wall, ~300 s → ~190 s user
CPU, peak RSS unchanged (~1.6 GB), tile data section SHA-256 identical.

`FloatTileCache.Local()` + `renderTileTerrarium` get the same treatment
(`TestFloatTileCacheLocal`). The Copernicus test set finishes in ~2 s and shows
no contention, so this half is verified for byte-identical tile data (bicubic
and mode) only — no speed claim.
