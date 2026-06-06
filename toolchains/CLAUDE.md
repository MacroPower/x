# Shared Dagger Toolchains

Reusable Dagger toolchain modules designed to be consumed by any project
(including this repo, which dogfoods them via the root `dagger.json`) and
referenced remotely as `github.com/MacroPower/x/toolchains/<module>@<ref>`.

## Modules

- **`go`** — Go CI toolchain: `build`/`binary`, `test`/`test-unit`/
  `test-integration`/`test-coverage`, `lint` (golangci-lint, installed onto
  the Go base from the GitHub release binary to avoid a Docker Hub pull and
  reuse the Go caches), `lint-deadcode`
  (advisory `golang.org/x/tools` deadcode analysis, not a `+check`),
  `format-go` (not `+generate`; consumers compose it into their own `*-ci`
  Format alongside prettier), `generate`, `tidy`/`check-tidy`, multi-module
  discovery (`modules`), `ensure-git-init`/`ensure-git-repo`, and benchmark
  stages (timed via the shared `bench` module). Understands `go.work`
  workspaces: the `goMod` sync includes `go.work`/`go.work.sum` and nested
  `go.mod`/`go.sum` files (excluding worktree/toolchain/testdata trees so
  stray module files do not bust the download cache), and the default
  `./...` package pattern expands into per-module `./<dir>/...` patterns
  when the source root has a `go.work` but no `go.mod` (where `./...` would
  match nothing).
- **`security`** — Trivy scanner: `scan-source`/`scan-image` (gate scans that
  fail on findings) and `scan-source-sarif`/`scan-image-sarif` (non-gating,
  emit SARIF for GitHub Code Scanning).
- **`zizmor`** — GitHub Actions workflow linter: `lint` (+check, runs zizmor
  over `.github/workflows`; `--config` is passed only when `config-path` is set,
  otherwise zizmor auto-discovers a config or uses its built-in defaults, so it
  drops into projects without a config file) and `lint-base` (the configured
  container, exposed so consumers can wrap it for benchmarks without a `go`
  dependency). `image`/`config-path`/`workflows-dir` are optional overrides.
  Mirrors `security`'s self-contained, literal-defaulting shape.
- **`prettier`** — Prettier formatter/linter for YAML/JSON/Markdown: `lint`
  (+check, `prettier --check`), `format` (returns a `Changeset` the consumer
  merges with its other formatters, e.g. gofmt), and `lint-base` (the configured
  container, for benchmarks). `image`/`version`/`config-path`/`patterns`/
  `cache-namespace` are optional overrides.
- **`commitlint`** — Commit-message validation against a project's
  conventional commit policy: `lint` runs commitlint with the project's config
  mounted, optionally against a message file (e.g. `.git/COMMIT_EDITMSG` from
  a commit-msg hook). It is a Callable, not a `+check` — `source` and
  `args`/`msg-file` are per-invocation inputs, so consumers wire it into git
  hooks (e.g. lefthook) rather than `dagger check`. The default container is
  built in-module: the node image (ECR Public) with `@commitlint/cli` and
  `@commitlint/config-conventional` npm-installed at a pinned version
  (commitlint publishes its image only to Docker Hub). `image` overrides it
  with a prebuilt image that has commitlint on its entrypoint. This repo
  dogfoods it: the `.lefthook.yaml` commit-msg hook runs
  `dagger call commitlint lint` against `.commitlintrc.yaml`.
- **`goreleaser`** — Reusable GoReleaser primitives (Tier A): `goreleaser-base`
  (a Go base + the goreleaser binary), `binary`/`with-goreleaser` (install the
  goreleaser binary onto another container, mirroring cosign/syft), `check-base`,
  `check` (+check, validates `.goreleaser.yaml`), `ensure-git-repo`
  (worktree-aware git bootstrap),
  `verify-binary-platform` (asserts a built binary's arch matches its target,
  catching cross-compilation mismatches), and the pure tag/digest helpers
  `version-tags`/`is-prerelease`/`deduplicate-digests`/`format-digest-checksums`/
  `registry-host` (logic lives in the `release` subpackage with plain `go test`
  unit tests). It is **independent** of the `go` toolchain — pass the consumer's
  Go base via the `base` arg so it reuses those caches. The full release
  pipeline (publish/sign/runtime images) stays in each project's `*-ci` module,
  which composes these primitives. `release-base` + `snapshot` are the planned
  Tier B follow-up.
- **`cosign`** — Sigstore image signing: `sign-keyless` (Fulcio + Rekor via an
  OIDC token, for GitHub Actions) and `sign-with-key` (a cosign private key).
  Both sign image digests concurrently and mount a Docker config for cosign's
  own registry requests when credentials are supplied; callers deduplicate
  digests first. Pins the cosign version once. It is **not** unit-tested in
  isolation — real signing needs a reachable registry plus OIDC/key
  credentials, so it is exercised through the consumer release pipelines. It
  also exposes `binary`/`with-cosign` to install the cosign binary into a
  release container, where goreleaser drives its own blob signing.
- **`syft`** — Anchore syft SBOM generator: `binary`/`with-syft` install the
  syft binary into a release container (where goreleaser's sbom step drives it),
  and `sbom` scans a directory to an SBOM file. Pins the syft version once.
- **`bench`** — Pipeline benchmark harness. A stage is a `*dagger.Container`
  rather than a closure, which is what lets the harness be shared at all
  (containers cross module boundaries, closures do not): `with-stage`
  accumulates named stages, `run` times each one's evaluation (sequential for
  isolated timings, parallel for full-pipeline wall-clock), and `summary`
  renders the table. Consumed by `go` and every `*-ci` module, which supply the
  project-specific stages and apply cache-busting before handing the container
  over.
- **`xci`** — This repository's own CI module (registered as `ci` in the
  root `dagger.json`), mirroring the `*-ci` modules consumers write. It is
  not designed for remote consumption: `test-unit`/`test-integration`
  (+check) surface the workspace test suite with the race detector,
  `lint-renovate` (+check) validates the Renovate configuration with
  renovate-config-validator installed at a pinned version in a Node
  container, and `format` (+generate) merges `go.format-go` with
  `prettier.format`. It has no `tests/` submodule; `dagger check` in this
  repo's CI exercises every function directly.
- **`devbox`** — Runs commands inside a project's Devbox (Nix-backed)
  environment so CI uses the same toolchain as local development: `base` (the
  devbox image with the Nix store mounted as a seeded cache volume), `install`
  (adds the project's `devbox.json` + lockfile and runs `devbox install`, keyed
  on the lockfile so the package-realisation layer caches), `with-source` (the
  installed environment with full source overlaid, for chaining or benchmark
  wrapping), and `run` (executes an arbitrary command via `devbox run --`,
  returning stdout). The default image is built in-module on the debian base
  (ECR Public) with a pinned single-user Nix install owned by the `devbox`
  user and the pinned devbox release binary, mirroring jetify's upstream
  Dockerfile (jetify publishes the image only to Docker Hub); `image`
  overrides it with a prebuilt equivalent. The `/nix` cache volume is seeded
  from the image's own store so the bootstrap Nix install keeps working and
  realised packages persist across runs, keyed on the devbox+nix versions (or
  the override image ref) so version bumps rotate it.
  `image`/`cache-namespace` are optional overrides. Like the other
  tool wrappers it has no `+check` — the commands are project-defined.

Each module is self-contained: its own `go.mod`/`go.sum` and no relative
`include`, so it can be sourced remotely. Tests live in per-module `tests/`
submodules that run against synthetic fixtures rather than any real project
layout — most from a `tests/testdata/fixture` directory, though some modules
(bench, commitlint, cosign) construct fixtures inline. Run them with
`dagger call -m tests all` from a module directory.

## Conventions

- **engineVersion** is pinned to `v0.21.4` across every `dagger.json` here.
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
- No `go.work`: the `tests/` submodules share the Dagger-conventional
  module path `dagger/tests`, so they cannot share one workspace, and a
  `go.work` at this level would pull the non-member test dirs into workspace
  scope. Develop each module with the `dagger` CLI instead.
