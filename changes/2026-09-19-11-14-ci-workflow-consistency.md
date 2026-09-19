# CI workflow consistency

- `build` job: the three copy-pasted per-tool build steps are now one `for cmd in ...` loop, matching the `windows` job. Same binaries, same flags, same output names.
- `windows` job: synthetic integration tests run with `-v`, like the Linux job.
