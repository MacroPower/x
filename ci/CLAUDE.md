# ci

This repository's own CI module, registered as `ci` in the root `dagger.json`.
Unlike the shared modules under `toolchains/`, it is not designed for remote
consumption: it orchestrates the repo's `dagger -> devbox -> task` flow so CI
reproduces exactly what `task check` runs locally, and it owns the ansivideo
release pipeline.

## Functions

### Checks (run via devbox)

- `lint`, `test`, `test-integration` (all +check) run the matching Taskfile
  target inside the project's devbox environment via the `devbox` toolchain,
  with the Go module/build and golangci-lint caches mounted.
- `test-coverage` runs the coverage target the same way and returns the
  coverage profile file.
- `lint-renovate` (+check) validates the Renovate configuration with
  renovate-config-validator at a pinned version in a Node container — the one
  gate that runs through neither devbox nor a shared toolchain, so Renovate can
  bump its own validator.

Because the gates are Taskfile targets calling local tools, CI reproduces
exactly what developers run locally: `local` skips the container for speed, CI
keeps it for reproducibility.

### Security (composes the security toolchain)

- `security` (+check) scans source dependencies for known vulnerabilities by
  composing the `security` toolchain (Trivy) directly, the same pattern the
  release functions use for `goreleaser`. Trivy is not on the devbox PATH, so
  the scan does not run through devbox. It scans the `ci` toolchain's source,
  whose root `dagger.json` customization already excludes `toolchains` and
  `.worktrees`, so the intentionally-vulnerable test fixtures are not scanned
  and no skip-dir filter is needed.

### ansivideo release (composes the shared toolchains; see `release.go`)

ansivideo is the monorepo's first released binary. These functions compose the
`goreleaser` toolchain directly rather than going through devbox, because those
tools are not on the devbox PATH. The goreleaser toolchain also installs cosign
and syft (via its `with-cosign`/`with-syft`) and signs image digests (its
`sign-keyless`), so the release pipeline depends on it alone.

- `lint-releaser` (+check) runs `goreleaser check` against
  `ansivideo/.goreleaser.yaml`.
- `build` snapshot-cross-compiles ansivideo for linux/darwin × amd64/arm64 and
  returns the GoReleaser `dist/` directory (no publishing).
- `release` builds, signs, and publishes a tagged release: GoReleaser produces
  the binaries, archives, checksums, SBOMs (syft), and signs the checksums
  (cosign) — syft and cosign installed via the goreleaser toolchain; the GitHub
  release is created against the real `ansivideo/vX.Y.Z`
  tag with the gh CLI; the multi-arch image (debian + ffmpeg + binary) is
  published to `ghcr.io/macropower/ansivideo` and signed. Returns the `dist/`
  directory including `digests.txt` for attestation.

GoReleaser's monorepo tag-prefix handling is Pro-only, so `release` runs
GoReleaser from `ansivideo/` with `GORELEASER_CURRENT_TAG` set to the
prefix-stripped version and `release.disable: true`, then publishes the GitHub
release itself. Signing is keyless (Sigstore Fulcio + Rekor): the workflow
forwards the GitHub Actions OIDC token (`ACTIONS_ID_TOKEN_REQUEST_URL`/`_TOKEN`)
into the container so cosign mints a short-lived, workflow-identity-bound cert
on demand — no long-lived secrets. With no OIDC token the release is unsigned.
The `release.yaml` workflow (tag `ansivideo/v*`) and `build.yaml` (snapshot) are
thin callers; `task release:check` / `task release:snapshot` wrap the same
functions locally.

## Layout

- `main.go` defines the `Ci` module (Go module path `dagger/ci`) and the check
  functions; `release.go` holds the ansivideo release functions.
- Dependencies in `dagger.json`: the `devbox` toolchain (checks), the
  `goreleaser` toolchain (release, which carries the folded-in cosign and syft
  tooling), and the `security` toolchain (the vulnerability scan), all
  referenced relatively under `../toolchains/`.
- It has no `tests/` submodule, so `task dagger:test` (which discovers suites
  under `toolchains/*/tests`) does not cover it.

The `engineVersion` in `dagger.json` is pinned in lockstep with every other
module's and with the CLI version in `.github/workflows`; bump them together
via `task dagger:update VERSION=<tag>`.
