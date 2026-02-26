# Documentation cleanup: reduce redundancy across docs

Trimmed overlapping and verbose content across ARCHITECTURE.md, README.md, and DESIGN.md
to give each file a clear, non-overlapping purpose:

- **ARCHITECTURE.md**: Removed implementation-level memory bullets (map pre-allocation
  sizing, map overhead tracking, gray tile RGBA leak) that duplicate DESIGN.md entries.
  Trimmed sync.Pool bullet to architectural level.

- **README.md**: Replaced two detailed optimization tables (~30 lines of per-technique
  measurements) with a brief summary paragraph. Kept profiling commands.

- **DESIGN.md**: Condensed four verbose entries:
  - Performance profile: removed date-specific numbers, kept key insights
  - Disk tile store accounting: trimmed worked example, kept the three fixes
  - sync.Pool: removed recycling points list, kept the pattern and zeroing rationale
  - Nodata fallthrough: consolidated two numbered sub-points into one paragraph
