# Migrating consumers onto the shared toolchains

These modules were consolidated from the per-repo copies in **eidetic**,
**terrarium**, and **kclipper** (the `go` and `security` toolchains). This
guide moves those repos off their local copies and onto the shared modules.
It is the deferred follow-up to building/dogfooding the modules in this repo;
none of it is required for x itself, which already consumes them locally.

## Publishing model

Each module is self-contained (own `go.mod`/`go.sum`, no `../../go.mod`
`include`), so it can be referenced remotely:

```jsonc
// consumer root dagger.json
{ "name": "go", "source": "github.com/MacroPower/x/toolchains/go@<ref>", "customizations": [ ... ] }
```

No repo references a remote Dagger module today, so this is net-new. Pin to a
dedicated tag namespace on x (e.g. `toolchains/v0.1.0`) or a commit SHA — do
**not** reuse x's library semver tags, or toolchain pins churn on every
library release.

## The hard constraint: engineVersion lockstep

A remote module pins one `engineVersion` (`v0.20.8` here) and consumers must
match exactly — terrarium's and kclipper's CI composite actions `jq`-read the
root `.engineVersion` and fail the build on a mismatch. So **before** pointing
any `source` at the remote ref, bump the repo to `v0.20.8` in its own PR,
across every `dagger.json` (root + each toolchain + each `tests/`) and the CI
pin, and confirm CI is green:

- **eidetic** — already `v0.20.8`, no bump.
- **terrarium** — `v0.20.1` → `v0.20.8`.
- **kclipper** — `v0.20.0` → `v0.20.8`.

(`dotfiles` only needs this if it later adopts a shared module; its `nix`/`dev`
toolchains stay local.)

## Order

Migrate **security first** (no `include`, no dependencies, no install
aliasing — the cleanest), then **go** (the `*-ci` toolchains depend on it via a
local `../go` path that must be repointed to the remote ref too). One
(repo, module) per PR.

## Per-(repo, module) steps

1. Repoint the toolchain `source` from the local path to
   `github.com/MacroPower/x/toolchains/<m>@<ref>`. For `go`, also repoint the
   `*-ci` toolchain's `dependencies[].source` (`../go` → remote ref). Run
   `dagger develop`.
2. **Preserve every existing root customization verbatim** (they live in the
   consumer's root `dagger.json`, not the module). Plus, because the shared
   modules dropped their module-level `+ignore`, re-declare the equivalent as a
   root source-ignore customization:
   - **eidetic `go` + `security`**: add `ignore` = `[".git", ".worktrees",
     ".workmux", "dist", ".tmp"]` (was the module `+ignore`). **Also set
     `cacheNamespace` to eidetic's own path** — the old default string was
     `go.jacobcolvin.com/terrarium/toolchains/go` (a stale copy pointing at
     *terrarium*); the shared default is now `go.jacobcolvin.com/x/toolchains/go`,
     so set it explicitly per repo to keep cache isolation.
   - **kclipper `go`**: keep the existing `cgo=true` default and the `source`
     Go-allowlist customization; pass `version="1.25"` until kclipper moves to
     1.26. **kclipper `security`**: keep the existing `scanSource`-scoped Go
     allowlist exactly (so `ScanSourceSarif` stays unfiltered).
   - **terrarium**: no customizations today; migrates clean. Optionally set
     `cacheNamespace` to its own path to reuse warm caches.
3. Set `cacheNamespace` per repo (to its own canonical path) so existing cache
   volumes are reused.
4. **TestUnit `+check` seam**: the shared `go` module deliberately does *not*
   mark `TestUnit` as a check (so consumers can supply a base with the right
   `aptPackages`). terrarium and kclipper currently expose `go:test-unit` under
   `dagger check`; add a thin `+check` wrapper in their `*-ci` toolchain (the
   eidetic-ci pattern) or switch those call sites to `dagger call go test-unit`.
5. Run `dagger check` + the repo's full CI. On parity, delete the now-duplicate
   local `toolchains/<m>` dir (and repoint or remove its `tests/`). Merge.

Install **names stay identical** (`go`, `security`, and the `*-ci` aliases), so
Taskfile/CI call sites (`go:lint`, `eidetic-ci:lint-prettier`, …) keep working.

## Out of scope

`dev` (two different products — not a real duplicate), `nix` (single repo),
`photo-fixtures`, and the `*-ci` modules stay repo-local. `commitlint` is a
clean candidate for a future shared module but was not built here. A shared
`release-ci` module is possible later (config-struct-driven) once a second
concrete consumer is ready.

## Known follow-ups for these modules

- The two `tests/` submodules pin `github.com/dagger/otel-go v1.43.0` while the
  parent modules pin `v1.41.0` (fresh `dagger init` vs. copied go.mod). Harmless
  — tests are never published — but align them on one version when convenient.
- `go` tests do not yet cover `Binary` (needs a named-command fixture) or
  `TestCoverage` (CGO/`-race`); both underlying paths are exercised via `Build`
  and `Test`.
