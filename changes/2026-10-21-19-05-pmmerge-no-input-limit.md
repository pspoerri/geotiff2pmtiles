# pmmerge: no 64-input limit

`pmmerge` refused more than 64 inputs because the inputs covering a position were a
`uint64` bit set. `mergePos` now carries a slice of covering input indices, so any
number of archives can be merged at once. `tile.MaxMergeInputs` is gone.

A directory argument now stands for the `.pmtiles` files directly in it, in name order
(`expandInputs` in `cmd/pmmerge`); the output is left out if it lies in that directory.
