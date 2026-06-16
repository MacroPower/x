# ci

This repository's own CI module, registered as `ci` in the root `dagger.json`.
Unlike the shared modules under `toolchains/`, it is not designed for remote
consumption: it orchestrates the repo's `dagger -> devbox -> task` flow so CI
reproduces exactly what `task check:all` runs locally, and it owns the monorepo's
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

### Lint actions & Security (compose sibling toolchains)

Both gates compose a sibling toolchain directly rather than running through
devbox, because their tools are not on the devbox PATH — the same pattern the
release functions use for `goreleaser`.

- `lint-actions` (+check) lints the GitHub Actions workflows for security issues
  by composing the `zizmor` toolchain. It pins `.github/zizmor.yaml` as the
  config path rather than relying on zizmor's auto-discovery.
- `security` (+check) scans source dependencies for known vulnerabilities by
  composing the `security` toolchain (Trivy). It scans the `ci` toolchain's
  source, whose root `dagger.json` customization already excludes `toolchains`
  and `.worktrees`, so the intentionally-vulnerable test fixtures are not
  scanned and no skip-dir filter is needed.
- `security-source-sarif` and `security-image-sarif` are the non-gating
  counterparts: they return SARIF files rather than failing on findings.
  `security-source-sarif` scans the same source as `security`;
  `security-image-sarif --pkg=<name>` scans a package's container image, built
  the same way a release publishes it, so the scan matches the real artifact.
  The `security.yaml` workflow exports the source SARIF and one image SARIF per
  image-producing package (discovered via `ci image-packages`) on push to `main`
  and uploads them to GitHub Code Scanning under the `trivy-source` and
  `trivy-image-<package>` categories; the gating `security` check above is
  unaffected. The image scan also surfaces OS-layer CVEs (the runtime base and
  apt packages) that the source scan cannot see.

### Release pipeline (composes the shared toolchains; see `release.go` + `packages.go`)

The release functions are package-agnostic. A package opts in by dropping a
`release.yaml` manifest at its module root; `packages.go` discovers every
`<package>/release.yaml` under the source root and resolves each into a spec.
Everything else — the Go submodule tag prefix (`<package>/vX.Y.Z`), the
GoReleaser config (`<package>/.goreleaser.yaml`), the project/build id, and the
built binary — derives from the package's directory name by convention. The
manifest's optional `image` block (registry, description, runtime apt packages)
opts the package into a published, signed container image; without it the
package is binary-only. Adding a releasable package needs no edits here.

These functions compose the `goreleaser` toolchain directly rather than going
through devbox, because those tools are not on the devbox PATH. The goreleaser
toolchain also installs cosign and syft (via its `with-cosign`/`with-syft`) and
signs image digests (its `sign-keyless`), so the release pipeline depends on it
alone.

- `packages` / `image-packages` return the discovered package names (all, and
  those that build an image) as JSON arrays. The `build.yaml` and `security.yaml`
  workflows consume them to build their matrices, so a new package flows through
  CI with no workflow edits.
- `lint-releaser` (+check) runs `goreleaser check` against every package's
  `<package>/.goreleaser.yaml`; discovery also parses the manifests, so a
  malformed `release.yaml` fails here too.
- `build --pkg=<name>` snapshot-cross-compiles a package for linux/darwin ×
  amd64/arm64 and returns the GoReleaser `dist/` directory (no publishing).
- `release --tag=<package>/vX.Y.Z` resolves the package from the tag prefix,
  then builds, signs, and publishes the release: GoReleaser produces the
  binaries, archives, checksums, SBOMs (syft), and signs the checksums (cosign)
  — syft and cosign installed via the goreleaser toolchain; the GitHub release
  is created against the real `<package>/vX.Y.Z` tag with the gh CLI; and, when
  the package declares an image, the multi-arch image (debian + runtime apt
  packages + binary) is published to the manifest's registry and signed.
  Returns the `dist/` directory, including `digests.txt` for attestation when an
  image was published.

GoReleaser's monorepo tag-prefix handling is Pro-only, so `release` runs
GoReleaser from the package directory with `GORELEASER_CURRENT_TAG` set to the
prefix-stripped version and `release.disable: true`, then publishes the GitHub
release itself. Signing is keyless (Sigstore Fulcio + Rekor): the workflow
forwards the GitHub Actions OIDC token (`ACTIONS_ID_TOKEN_REQUEST_URL`/`_TOKEN`)
into the container so cosign mints a short-lived, workflow-identity-bound cert
on demand — no long-lived secrets. With no OIDC token the release is unsigned.
The `release.yaml` workflow (tag `*/v*`) and `build.yaml` (snapshot) are thin
callers; `task release:check` validates every package and
`task release:snapshot PACKAGE=<name>` wraps the snapshot build locally.

## Layout

- `main.go` defines the `Ci` module (Go module path `dagger/ci`) and the check
  functions; `release.go` holds the release orchestration; `packages.go` holds
  the `release.yaml` manifest model, discovery, and the `packages`/
  `image-packages` accessors.
- Dependencies in `dagger.json`: the `devbox` toolchain (checks), the
  `goreleaser` toolchain (release, which carries the folded-in cosign and syft
  tooling), the `security` toolchain (the vulnerability scan), and the `zizmor`
  toolchain (the Actions workflow lint), all referenced relatively under
  `../toolchains/`.
- It has no `tests/` submodule, so `task dagger:test` (which discovers suites
  under `toolchains/*/tests`) does not cover it.

The `engineVersion` in `dagger.json` is pinned in lockstep with every other
module's and with the CLI version in `.github/workflows`; bump them together
via `task dagger:update VERSION=<tag>`.
