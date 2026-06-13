# ci

This repository's own CI module, registered as `ci` in the root `dagger.json`.
Unlike the shared modules under `toolchains/`, it is not designed for remote
consumption: it orchestrates the repo's `dagger -> devbox -> task` flow so CI
reproduces exactly what `task check` runs locally.

## Functions

- `lint`, `test`, `test-integration`, `security` (all +check) run the matching
  Taskfile target inside the project's devbox environment via the `devbox`
  toolchain, with the Go module/build and golangci-lint caches mounted.
- `test-coverage` runs the coverage target the same way and returns the
  coverage profile file.
- `lint-renovate` (+check) validates the Renovate configuration with
  renovate-config-validator at a pinned version in a Node container — the one
  gate that does not run through devbox, so Renovate can bump its own validator.

Because the gates are Taskfile targets calling local tools, CI reproduces
exactly what developers run locally: `local` skips the container for speed, CI
keeps it for reproducibility.

## Layout

- `main.go` defines the `Ci` module (Go module path `dagger/ci`).
- Its only dependency is the `devbox` toolchain, referenced relatively as
  `../toolchains/devbox` in `dagger.json`.
- It has no `tests/` submodule, so `task dagger:test` (which discovers suites
  under `toolchains/*/tests`) does not cover it.

The `engineVersion` in `dagger.json` is pinned in lockstep with every other
module's and with the CLI version in `.github/workflows`; bump them together
via `task dagger:update VERSION=<tag>`.
