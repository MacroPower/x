# Shared Dagger Toolchains

Reusable Dagger toolchain modules, consolidated from the per-repo copies that
previously lived in eidetic, terrarium, and kclipper. They are designed to be
consumed by any project (including this repo, which dogfoods them via the root
`dagger.json`) and, eventually, referenced remotely as
`github.com/MacroPower/x/toolchains/<module>@<ref>`.

## Modules

- **`go`** — Go CI toolchain: `build`/`binary`, `test`/`test-unit`/
  `test-integration`/`test-coverage`, `lint` (golangci-lint), `lint-deadcode`
  (advisory `golang.org/x/tools` deadcode analysis, not a `+check`),
  `format-go`, `generate`, `tidy`/`check-tidy`, multi-module discovery
  (`modules`), `ensure-git-init`/`ensure-git-repo`, and a benchmark harness.
  Consolidated from the eidetic base plus kclipper's git helpers.
- **`security`** — Trivy scanner: `scan-source`/`scan-image` (gate scans that
  fail on findings) and `scan-source-sarif`/`scan-image-sarif` (non-gating,
  emit SARIF for GitHub Code Scanning).
- **`zizmor`** — GitHub Actions workflow linter: `lint` (+check, runs zizmor
  over `.github/workflows` using `.github/zizmor.yaml`) and `lint-base` (the
  configured container, exposed so consumers can wrap it for benchmarks without
  a `go` dependency). `image`/`config-path`/`workflows-dir` are optional
  overrides. Mirrors `security`'s self-contained, literal-defaulting shape.
- **`goreleaser`** — Reusable GoReleaser primitives (Tier A): `goreleaser-base`
  (a Go base + the goreleaser binary), `check-base`, `check` (+check, validates
  `.goreleaser.yaml`), `ensure-git-repo` (worktree-aware git bootstrap),
  `verify-binary-platform` (asserts a built binary's arch matches its target,
  catching cross-compilation mismatches), and the pure tag/digest helpers
  `version-tags`/`is-prerelease`/`deduplicate-digests`/`format-digest-checksums`/
  `registry-host` (logic lives in the `release` subpackage with plain `go test`
  unit tests). It is **independent** of the `go` toolchain — pass the consumer's
  Go base via the `base` arg so it reuses those caches. The full release
  pipeline (publish/sign/runtime images) stays in each project's `*-ci` module,
  which composes these primitives. `release-base` + `snapshot` are the planned
  Tier B follow-up.

Each module is self-contained: its own `go.mod`/`go.sum` and no relative
`include`, so it can be sourced remotely. Tests live in per-module `tests/`
submodules that run against a synthetic fixture (`tests/testdata/fixture`)
rather than any real project layout. Run them with `dagger call -m tests all`
from a module directory.

## Conventions

- **engineVersion** is pinned to `v0.20.8` across every `dagger.json` here.
  A consumer's root `dagger.json` and CI must match exactly (the engine
  enforces it), so engine bumps are coordinated across all adopters.
- **Ignore patterns belong in the consumer's root `dagger.json`**
  customizations, not in module `+ignore` annotations. The modules mount the
  whole source (`+defaultPath="/"`); each consumer declares what to exclude.
- **`cacheNamespace`** defaults to each module's canonical path. Consumers
  should override it to their own repo path so cache volumes do not collide
  between projects sharing an engine.
- **`go.test-unit` is not a `+check`.** Consuming projects that want it under
  `dagger check` wrap it in their own CI toolchain (so they can supply a base
  with project-specific `aptPackages`). Only `lint` and `check-tidy` are
  checks here.
- No `go.work`: the two `tests/` submodules share the Dagger-conventional
  module path `dagger/tests`, so they cannot share one workspace, and a
  `go.work` at this level would pull the non-member test dirs into workspace
  scope. Develop each module with the `dagger` CLI instead.
