# G16 · Package layout note

## Decision (2026-07-23)

**No `internal/` package split** for the CPA c-shared plugin.

Reasons:

1. `-buildmode=c-shared` requires `package main` with exported C ABI entry
   points in the same main package.
2. Splitting into `internal/*` forces awkward re-exports or multi-package
   cgo glue for little gain.
3. Current modularization already uses **same-package multi-file**:
   - `main.go` — ABI, OAuth, executor
   - `management.go` — panel, check-in, import
   - `scheduler.go` — scheduler.pick
   - `panel.html` — embedded UI
   - `*_test.go` — unit tests

## Done instead of G16

- New domain files (`scheduler.go`) rather than growing only `main.go`
- Document boundaries in README

Full DDD `internal/` (as in cli-smart-router) is frozen unless CPA adds a
non-c-shared plugin packaging model.
