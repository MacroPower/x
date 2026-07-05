// Release orchestration for the monorepo's binary packages. Unlike the +check
// functions in main.go (which run Taskfile targets inside devbox), these compose
// the shared goreleaser toolchain directly -- including its folded-in cosign
// signing and syft SBOM helpers -- to build, sign, and publish a release. The
// pipeline is package-agnostic: the package to act on is resolved from a
// release.yaml manifest (see packages.go), so adding a releasable package is a
// matter of dropping a manifest, not editing this file.
//
// A package is tagged <package>/vX.Y.Z (a Go submodule prefix). GoReleaser's
// OSS build cannot strip that prefix (monorepo mode is Pro only), so GoReleaser
// runs from the package directory with GORELEASER_CURRENT_TAG set to the
// stripped version and only builds, archives, checksums, SBOMs, and signs;
// [Ci.Release] then creates the GitHub release against the real prefixed tag
// with the gh CLI and publishes the multi-arch image (when the package declares
// one) natively via Dagger.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger/ci/internal/dagger"

	"golang.org/x/sync/errgroup"
)

const (
	// goreleaserVersion pins the GoReleaser release used for package builds.
	goreleaserVersion = "v2.17.0" // renovate: datasource=github-releases depName=goreleaser/goreleaser

	// ghVersion pins the GitHub CLI used to create GitHub releases.
	ghVersion = "v2.96.0" // renovate: datasource=github-releases depName=cli/cli

	// debianImage is the runtime base for package container images and the
	// tool-download containers, pulled from Docker's verified publisher space on
	// ECR Public to avoid Docker Hub pull rate limits.
	debianImage = "public.ecr.aws/docker/library/debian:13-slim" // renovate: datasource=docker depName=public.ecr.aws/docker/library/debian

	// repoRemoteURL is configured as origin on the bootstrapped release repo so
	// GoReleaser resolves the repository for git state.
	repoRemoteURL = "https://github.com/MacroPower/x.git"

	// repoURL is the canonical project URL stamped into image OCI metadata.
	repoURL = "https://github.com/MacroPower/x"

	// repoLicense is the SPDX license expression stamped into image OCI metadata.
	repoLicense = "Apache-2.0"

	// githubRepo is the GitHub repository releases are published to.
	githubRepo = "MacroPower/x"
)

// releaserBase builds the release toolchain: the goreleaser-equipped Go base
// extended with the cosign and syft binaries via the goreleaser toolchain's
// WithCosign/WithSyft (so GoReleaser's sign and sbom steps can invoke them),
// the source mounted at /src, and a bootstrapped git repo. Tools are installed
// before source is mounted so source changes only invalidate the git-bootstrap
// layer onward.
//
// GOWORK is disabled so the build resolves the versions pinned in the package's
// go.mod rather than the go.work overlay, keeping releases reproducible.
func (m *Ci) releaserBase(_ context.Context) *dagger.Container {
	ctr := m.Goreleaser.GoreleaserBase()
	ctr = m.Goreleaser.WithCosign(ctr)
	ctr = m.Goreleaser.WithSyft(ctr)
	ctr = ctr.
		WithEnvVariable("GOWORK", "off").
		// USER and HOSTNAME feed the GoReleaser BuildUser ldflag template.
		WithEnvVariable("USER", "dagger").
		WithEnvVariable("HOSTNAME", "dagger").
		WithMountedDirectory("/src", m.Source).
		WithWorkdir("/src")
	return m.Goreleaser.EnsureGitRepo(ctr, dagger.GoreleaserEnsureGitRepoOpts{
		RemoteURL: repoRemoteURL,
	})
}

// LintReleaser validates every releasable package's GoReleaser configuration
// with `goreleaser check`. The goreleaser toolchain's own Check expects
// .goreleaser.yaml at the source root, so each package's subdirectory config is
// passed explicitly. Discovering the packages also parses their manifests, so a
// malformed release.yaml fails here too.
//
// +check
func (m *Ci) LintReleaser(ctx context.Context) error {
	pkgs, err := m.discoverPackages(ctx)
	if err != nil {
		return err
	}

	base := m.Goreleaser.CheckBase()
	for _, p := range pkgs {
		_, err := base.
			WithExec([]string{"goreleaser", "check", "-f", p.goreleaserConfig()}).
			Sync(ctx)
		if err != nil {
			return fmt.Errorf("check %s: %w", p.name, err)
		}
	}

	return nil
}

// Build runs GoReleaser in snapshot mode, cross-compiling the given package for
// all targets and producing archives and checksums. Docker, signing, and SBOM
// steps are skipped (snapshot builds do not publish). Returns the dist/
// directory.
func (m *Ci) Build(
	ctx context.Context,
	// Package to build (a directory with a release.yaml manifest, e.g. "ansivideo").
	pkg string,
) (*dagger.Directory, error) {
	p, err := m.loadPackage(ctx, pkg)
	if err != nil {
		return nil, err
	}

	return m.releaserBase(ctx).
		WithWorkdir(p.dir()).
		WithExec([]string{
			"goreleaser", "release", "--snapshot", "--clean",
			"--skip=docker,sign,sbom",
			"--parallelism=0",
		}).
		Directory(p.distDir()), nil
}

// SecurityImageSarif builds the given package's runtime image and scans it for
// known vulnerabilities, returning the results as a SARIF file for upload to
// GitHub Code Scanning. It composes the release image builder ([Ci.Build] plus
// runtimeImages) so it scans exactly what a release publishes, then scans the
// native linux/amd64 variant (Dagger evaluates only that variant lazily). Unlike
// the gating scans it does not fail on findings, and it surfaces OS-layer CVEs
// (the runtime base and apt packages) that the source scan, seeing only Go
// modules, cannot. The package must declare an image.
func (m *Ci) SecurityImageSarif(
	ctx context.Context,
	// Package whose image to scan (must declare an image block, e.g. "ansivideo").
	pkg string,
) (*dagger.File, error) {
	p, err := m.loadPackage(ctx, pkg)
	if err != nil {
		return nil, err
	}
	if p.image == nil {
		return nil, fmt.Errorf("package %q builds no container image to scan", pkg)
	}

	dist, err := m.Build(ctx, pkg)
	if err != nil {
		return nil, err
	}

	// The version only feeds cosmetic OCI labels on an image that is never
	// published, so a fixed placeholder is fine.
	variants, err := p.runtimeImages(ctx, dist, "0.0.0-scan")
	if err != nil {
		return nil, err
	}

	return m.Scanner.ScanImageSarif(variants[0]), nil
}

// Release builds, signs, and publishes a tagged release. The package is
// resolved from the tag's prefix (<package>/vX.Y.Z), so the same function
// releases any releasable package:
//
//   - GoReleaser cross-compiles the binaries, builds archives and checksums,
//     generates SBOMs (syft), and signs the checksums (cosign keyless, when an
//     OIDC token is provided). The version is taken from the prefix-stripped tag
//     via GORELEASER_CURRENT_TAG; GoReleaser's own release step is disabled.
//   - The GitHub release is created against the real <package>/vX.Y.Z tag with
//     the gh CLI and the archives, checksums, SBOMs, and signature are uploaded.
//   - When the package declares an image, the multi-arch container image is
//     built and published natively via Dagger and signed with cosign keyless
//     signing. Binary-only packages skip this step.
//
// Both the checksums and the image are signed with Sigstore keyless signing
// (Fulcio + Rekor) when OIDC request credentials are provided; signing is
// skipped otherwise.
//
// Returns the dist directory, including digests.txt (the published image
// digests in checksum format) for attestation when an image was published.
//
// +cache="never"
func (m *Ci) Release(
	ctx context.Context,
	// GitHub token for creating the release and pushing the image.
	githubToken *dagger.Secret,
	// Registry username for container image authentication.
	registryUsername string,
	// Registry password or token for container image authentication.
	registryPassword *dagger.Secret,
	// Full git tag to release (e.g. "ansivideo/v1.2.3").
	tag string,
	// OIDC token request URL for keyless Sigstore signing. In GitHub Actions
	// this is the ACTIONS_ID_TOKEN_REQUEST_URL environment variable.
	// +optional
	oidcRequestURL string,
	// Bearer token for the OIDC token request. In GitHub Actions this is the
	// ACTIONS_ID_TOKEN_REQUEST_TOKEN environment variable. Signing is skipped
	// when omitted.
	// +optional
	oidcRequestToken *dagger.Secret,
) (*dagger.Directory, error) {
	name, version, err := splitReleaseTag(tag)
	if err != nil {
		return nil, err
	}

	p, err := m.loadPackage(ctx, name)
	if err != nil {
		return nil, err
	}

	prerelease, err := m.Goreleaser.IsPrerelease(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("classify tag: %w", err)
	}

	// validate is skipped because the bootstrapped repo carries no tags to
	// validate the build against; sign is skipped when no OIDC token is set.
	// cosign (invoked by GoReleaser's signs section) detects
	// ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN and mints a fresh OIDC token on demand
	// via its built-in GitHub Actions provider.
	skip := "validate,sign"
	if oidcRequestToken != nil {
		skip = "validate"
	}

	ctr := m.releaserBase(ctx).
		WithSecretVariable("GITHUB_TOKEN", githubToken).
		// The source mount excludes .git, so supply the version explicitly
		// rather than relying on git tag discovery.
		WithEnvVariable("GORELEASER_CURRENT_TAG", version).
		WithEnvVariable("ACTIONS_ID_TOKEN_REQUEST_URL", oidcRequestURL).
		With(optSecretVariable("ACTIONS_ID_TOKEN_REQUEST_TOKEN", oidcRequestToken)).
		WithWorkdir(p.dir())

	built := ctr.WithExec([]string{"goreleaser", "release", "--clean", "--skip=" + skip})
	dist := built.Directory(p.distDir())

	dist, err = m.publishRelease(ctx, built, dist, p, tag, version, prerelease)
	if err != nil {
		return nil, err
	}

	return m.publishImage(ctx, p, dist, version, registryUsername, registryPassword, oidcRequestURL, oidcRequestToken)
}

// splitReleaseTag splits a prefixed release tag "<package>/vX.Y.Z" into the
// package name and the prefix-stripped SemVer version. It rejects unprefixed or
// malformed tags; the workflow validates the SemVer shape before calling, so
// this guards against a missing or empty prefix.
func splitReleaseTag(tag string) (string, string, error) {
	name, version, ok := strings.Cut(tag, "/")
	if !ok || name == "" || version == "" {
		return "", "", fmt.Errorf("tag %q is not of the form <package>/vX.Y.Z", tag)
	}

	return name, version, nil
}

// publishRelease creates (or reuses) the GitHub release for the real prefixed
// tag with the gh CLI and uploads the GoReleaser artifacts to it. built is the
// post-GoReleaser container (it already carries GITHUB_TOKEN and the dist
// directory); dist lists the artifacts to upload.
func (m *Ci) publishRelease(
	ctx context.Context,
	built *dagger.Container,
	dist *dagger.Directory,
	p *pkg,
	tag, version string,
	prerelease bool,
) (*dagger.Directory, error) {
	assets, err := releaseAssets(ctx, dist)
	if err != nil {
		return nil, err
	}

	prereleaseFlag := ""
	if prerelease {
		prereleaseFlag = "--prerelease"
	}

	// Idempotent and race-safe: create the release if it is absent, then upload
	// the artifacts with --clobber so re-runs overwrite rather than fail. If a
	// concurrent run (e.g. a tag push racing a manual dispatch) created it first,
	// the create fails and the trailing view succeeds, so the chain still
	// proceeds. The tag already exists on the remote (the push triggered this),
	// so --verify-tag guards against a mistyped tag.
	script := strings.Join([]string{
		"set -e",
		`gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1 ||`,
		`  gh release create "$TAG" --repo "$REPO" --title "$NAME $VERSION"` +
			` --generate-notes --verify-tag $PRERELEASE ||`,
		`  gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1`,
		`gh release upload "$TAG" --repo "$REPO" --clobber $ASSETS`,
	}, "\n")

	_, err = built.
		WithFile("/usr/local/bin/gh", ghBinary()).
		WithEnvVariable("TAG", tag).
		WithEnvVariable("REPO", githubRepo).
		WithEnvVariable("NAME", p.name).
		WithEnvVariable("VERSION", version).
		WithEnvVariable("PRERELEASE", prereleaseFlag).
		WithEnvVariable("ASSETS", strings.Join(assets, " ")).
		WithExec([]string{"sh", "-c", script}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("publish github release: %w", err)
	}

	return dist, nil
}

// publishImage builds the package's multi-arch container image from the dist
// binaries, publishes it to the registry under the derived tags, signs the
// digests with cosign keyless signing, and records the digests in
// dist/digests.txt for attestation. Binary-only packages (no image block) are a
// no-op: dist is returned unchanged with no digests.txt.
func (m *Ci) publishImage(
	ctx context.Context,
	p *pkg,
	dist *dagger.Directory,
	version, registryUsername string,
	registryPassword *dagger.Secret,
	oidcRequestURL string,
	oidcRequestToken *dagger.Secret,
) (*dagger.Directory, error) {
	if p.image == nil {
		return dist, nil
	}

	tags, err := m.Goreleaser.VersionTags(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("derive version tags: %w", err)
	}

	variants, err := p.runtimeImages(ctx, dist, version)
	if err != nil {
		return nil, err
	}

	digests, err := m.publishImages(ctx, p, variants, tags, registryUsername, registryPassword)
	if err != nil {
		return nil, fmt.Errorf("publish images: %w", err)
	}

	if err := m.signImages(ctx, p, digests, registryUsername, registryPassword, oidcRequestURL, oidcRequestToken); err != nil {
		return nil, err
	}

	if len(digests) > 0 {
		checksums, err := m.Goreleaser.FormatDigestChecksums(ctx, digests)
		if err != nil {
			return nil, fmt.Errorf("format digest checksums: %w", err)
		}
		dist = dist.WithNewFile("digests.txt", checksums)
	}

	return dist, nil
}

// releaseAssets selects the GoReleaser artifacts to attach to the GitHub
// release: the archives, checksums, SBOMs, and checksum signature. The build
// metadata (artifacts.json, metadata.json, config.yaml) and the unpacked binary
// directories are left out.
func releaseAssets(ctx context.Context, dist *dagger.Directory) ([]string, error) {
	entries, err := dist.Entries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dist: %w", err)
	}

	var assets []string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e, ".tar.gz"),
			e == "checksums.txt",
			strings.HasSuffix(e, ".sbom.json"),
			strings.HasSuffix(e, ".sigstore.json"):
			assets = append(assets, "dist/"+e)
		}
	}

	return assets, nil
}

// ghBinary returns the GitHub CLI binary for the release container's
// architecture, downloaded in a dedicated container for independent caching.
func ghBinary() *dagger.File {
	v := strings.TrimPrefix(ghVersion, "v")
	return dag.Container().
		From(debianImage).
		WithExec([]string{
			"sh", "-c",
			"apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && " +
				"arch=$(dpkg --print-architecture) && " +
				"curl -fsSL https://github.com/cli/cli/releases/download/" + ghVersion +
				"/gh_" + v + "_linux_${arch}.tar.gz | tar -xz -C /tmp && " +
				"install -m0755 /tmp/gh_" + v + "_linux_${arch}/bin/gh /gh",
		}).
		File("/gh")
}

// runtimeImages builds the package's multi-arch container image variants from a
// GoReleaser dist directory. Each variant is debian-slim with the package's
// declared runtime apt packages and the matching cross-compiled binary.
func (p *pkg) runtimeImages(ctx context.Context, dist *dagger.Directory, version string) ([]*dagger.Container, error) {
	platforms := []struct {
		platform dagger.Platform
		goarch   string
	}{
		{"linux/amd64", "amd64"},
		{"linux/arm64", "arm64"},
	}
	variants := make([]*dagger.Container, len(platforms))
	created := time.Now().UTC().Format(time.RFC3339)

	for i, pl := range platforms {
		bin, err := p.distBinary(ctx, dist, pl.goarch)
		if err != nil {
			return nil, err
		}

		variants[i] = p.runtimeBase(pl.platform).
			WithLabel("org.opencontainers.image.version", version).
			WithLabel("org.opencontainers.image.created", created).
			WithAnnotation("org.opencontainers.image.version", version).
			WithAnnotation("org.opencontainers.image.created", created).
			WithFile("/usr/local/bin/"+p.binary, bin).
			WithEntrypoint([]string{p.binary})
	}

	return variants, nil
}

// distBinary locates the linux/<goarch> binary in a GoReleaser dist directory.
// GoReleaser writes each target to <build-id>_<os>_<arch>_<variant>/<binary>;
// globbing for it rather than reconstructing the directory avoids assuming the
// build id matches the package name or that the GOAMD64/GOARM64 variant suffix
// takes its default value.
func (p *pkg) distBinary(ctx context.Context, dist *dagger.Directory, goarch string) (*dagger.File, error) {
	pattern := "*_linux_" + goarch + "_*/" + p.binary
	matches, err := dist.Glob(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("glob dist for %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no linux/%s binary %q in dist (pattern %s)", goarch, p.binary, pattern)
	}

	return dist.File(matches[0]), nil
}

// runtimeBase returns a debian-slim container for the given platform with the
// package's runtime apt packages installed and its OCI metadata applied. The
// title is the package name and the repository-level OCI metadata (source, url,
// license) is shared across packages; the description and runtime dependencies
// come from the package's manifest.
func (p *pkg) runtimeBase(platform dagger.Platform) *dagger.Container {
	ctr := dag.Container(dagger.ContainerOpts{Platform: platform}).
		From(debianImage).
		WithLabel("org.opencontainers.image.title", p.name).
		WithLabel("org.opencontainers.image.description", p.image.description).
		WithLabel("org.opencontainers.image.source", repoURL).
		WithLabel("org.opencontainers.image.url", repoURL).
		WithLabel("org.opencontainers.image.licenses", repoLicense).
		WithAnnotation("org.opencontainers.image.title", p.name).
		WithAnnotation("org.opencontainers.image.source", repoURL)

	if len(p.image.runtimeAptPackages) > 0 {
		ctr = ctr.WithExec([]string{
			"sh", "-c",
			"apt-get update && apt-get install -y --no-install-recommends " +
				strings.Join(p.image.runtimeAptPackages, " ") + " && " +
				"rm -rf /var/lib/apt/lists/* /tmp/*",
		})
	}

	return ctr
}

// publishImages publishes the pre-built image variants under each tag. Returns
// the published digest references (one per tag,
// e.g. "registry/image:tag@sha256:hex").
func (m *Ci) publishImages(
	ctx context.Context,
	p *pkg,
	variants []*dagger.Container,
	tags []string,
	registryUsername string,
	registryPassword *dagger.Secret,
) ([]string, error) {
	publisher := dag.Container()
	if registryPassword != nil {
		host, err := m.Goreleaser.RegistryHost(ctx, p.image.registry)
		if err != nil {
			return nil, fmt.Errorf("resolve registry host: %w", err)
		}
		publisher = publisher.WithRegistryAuth(host, registryUsername, registryPassword)
	}

	// Publish a multi-arch manifest per tag concurrently.
	digests := make([]string, len(tags))
	g, gCtx := errgroup.WithContext(ctx)
	for i, t := range tags {
		ref := fmt.Sprintf("%s:%s", p.image.registry, t)
		g.Go(func() error {
			digest, err := publisher.Publish(gCtx, ref, dagger.ContainerPublishOpts{
				PlatformVariants: variants,
			})
			if err != nil {
				return fmt.Errorf("publish %s: %w", ref, err)
			}
			digests[i] = digest
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return digests, nil
}

// signImages signs the published image digests with cosign keyless signing
// (Fulcio + Rekor) via the goreleaser toolchain. Cosign's built-in GitHub
// Actions provider uses the OIDC request URL and token to mint fresh tokens on
// demand, avoiding expiry. Digests are deduplicated first since tags often
// share a manifest. Does nothing when no OIDC token is provided.
func (m *Ci) signImages(
	ctx context.Context,
	p *pkg,
	digests []string,
	registryUsername string,
	registryPassword *dagger.Secret,
	oidcRequestURL string,
	oidcRequestToken *dagger.Secret,
) error {
	if oidcRequestToken == nil {
		return nil
	}

	toSign, err := m.Goreleaser.DeduplicateDigests(ctx, digests)
	if err != nil {
		return fmt.Errorf("deduplicate digests: %w", err)
	}

	host := ""
	if registryPassword != nil {
		host, err = m.Goreleaser.RegistryHost(ctx, p.image.registry)
		if err != nil {
			return fmt.Errorf("resolve registry host: %w", err)
		}
	}

	return m.Goreleaser.SignKeyless(ctx, toSign, oidcRequestURL, oidcRequestToken,
		dagger.GoreleaserSignKeylessOpts{
			RegistryHost:     host,
			RegistryUsername: registryUsername,
			RegistryPassword: registryPassword,
		})
}

// optSecretVariable returns a [dagger.WithContainerFunc] that adds a secret
// environment variable when the secret is non-nil, and is a no-op otherwise.
func optSecretVariable(name string, secret *dagger.Secret) dagger.WithContainerFunc {
	return func(ctr *dagger.Container) *dagger.Container {
		if secret == nil {
			return ctr
		}
		return ctr.WithSecretVariable(name, secret)
	}
}
