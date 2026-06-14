// Release orchestration for the ansivideo binary. Unlike the +check functions
// in main.go (which run Taskfile targets inside devbox), these compose the
// shared goreleaser toolchain directly -- including its folded-in cosign
// signing and syft SBOM helpers -- to build, sign, and publish ansivideo: the
// monorepo's first released artifact. ansivideo is pure
// Go (it shells out to ffmpeg at runtime), so every target cross-compiles
// statically and the only runtime dependency, ffmpeg, is bundled into the
// container image rather than linked.
//
// ansivideo is tagged ansivideo/vX.Y.Z (a Go submodule prefix). GoReleaser's
// OSS build cannot strip that prefix (monorepo mode is Pro only), so GoReleaser
// runs from the ansivideo/ directory with GORELEASER_CURRENT_TAG set to the
// stripped version and only builds, archives, checksums, SBOMs, and signs;
// [Ci.Release] then creates the GitHub release against the real prefixed tag
// with the gh CLI and publishes the multi-arch image natively via Dagger.
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
	// goreleaserVersion pins the GoReleaser release used for ansivideo builds.
	goreleaserVersion = "v2.16.0" // renovate: datasource=github-releases depName=goreleaser/goreleaser

	// ghVersion pins the GitHub CLI used to create the ansivideo GitHub release.
	ghVersion = "v2.94.0" // renovate: datasource=github-releases depName=cli/cli

	// debianImage is the runtime base for the ansivideo container image and the
	// tool-download containers, pulled from Docker's verified publisher space on
	// ECR Public to avoid Docker Hub pull rate limits.
	debianImage = "public.ecr.aws/docker/library/debian:13-slim" // renovate: datasource=docker depName=public.ecr.aws/docker/library/debian

	// ansivideoRegistry is the container image registry for ansivideo.
	ansivideoRegistry = "ghcr.io/macropower/ansivideo"

	// ansivideoRemoteURL is configured as origin on the bootstrapped release
	// repo so GoReleaser resolves the repository for git state.
	ansivideoRemoteURL = "https://github.com/MacroPower/x.git"

	// githubRepo is the GitHub repository the ansivideo release is published to.
	githubRepo = "MacroPower/x"

	// goreleaserConfig is the ansivideo GoReleaser config, relative to the repo
	// root, used by [Ci.LintReleaser].
	goreleaserConfig = "ansivideo/.goreleaser.yaml"

	// ansivideoDir is the ansivideo module directory inside the release
	// container; GoReleaser runs here so its config and build paths resolve.
	ansivideoDir = "/src/ansivideo"

	// ansivideoDistDir is the GoReleaser output directory inside the release
	// container.
	ansivideoDistDir = ansivideoDir + "/dist"

	// ansivideoTagPrefix is the Go submodule tag prefix stripped to recover the
	// SemVer version (e.g. "ansivideo/v1.2.3" -> "v1.2.3").
	ansivideoTagPrefix = "ansivideo/"
)

// releaserBase builds the release toolchain: the goreleaser-equipped Go base
// extended with the cosign and syft binaries via the goreleaser toolchain's
// WithCosign/WithSyft (so GoReleaser's sign and sbom steps can invoke them),
// the source mounted at /src, and a bootstrapped git repo. Tools are installed
// before source is mounted so source changes only invalidate the git-bootstrap
// layer onward.
//
// GOWORK is disabled so the build resolves the versions pinned in
// ansivideo/go.mod rather than the go.work overlay, keeping releases
// reproducible.
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
		RemoteURL: ansivideoRemoteURL,
	})
}

// LintReleaser validates the ansivideo GoReleaser configuration with
// `goreleaser check`. The goreleaser toolchain's own Check expects
// .goreleaser.yaml at the source root, so the subdirectory config is passed
// explicitly.
//
// +check
func (m *Ci) LintReleaser(ctx context.Context) error {
	_, err := m.Goreleaser.CheckBase().
		WithExec([]string{"goreleaser", "check", "-f", goreleaserConfig}).
		Sync(ctx)
	return err
}

// Build runs GoReleaser in snapshot mode, cross-compiling ansivideo for all
// targets and producing archives and checksums. Docker, signing, and SBOM
// steps are skipped (snapshot builds do not publish). Returns the dist/
// directory.
func (m *Ci) Build(ctx context.Context) (*dagger.Directory, error) {
	return m.releaserBase(ctx).
		WithWorkdir(ansivideoDir).
		WithExec([]string{
			"goreleaser", "release", "--snapshot", "--clean",
			"--skip=docker,sign,sbom",
			"--parallelism=0",
		}).
		Directory(ansivideoDistDir), nil
}

// Release builds, signs, and publishes a tagged ansivideo release:
//
//   - GoReleaser cross-compiles the binaries, builds archives and checksums,
//     generates SBOMs (syft), and signs the checksums (cosign keyless, when an
//     OIDC token is provided). The version is taken from the prefix-stripped tag
//     via GORELEASER_CURRENT_TAG; GoReleaser's own release step is disabled.
//   - The GitHub release is created against the real ansivideo/vX.Y.Z tag with
//     the gh CLI and the archives, checksums, SBOMs, and signature are uploaded.
//   - The multi-arch container image (debian + ffmpeg + binary) is built and
//     published natively via Dagger and signed with cosign keyless signing.
//
// Both the checksums and the image are signed with Sigstore keyless signing
// (Fulcio + Rekor) when OIDC request credentials are provided; signing is
// skipped otherwise.
//
// Returns the dist directory, including digests.txt (the published image
// digests in checksum format) for attestation.
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
	version := strings.TrimPrefix(tag, ansivideoTagPrefix)

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
		WithWorkdir(ansivideoDir)

	built := ctr.WithExec([]string{"goreleaser", "release", "--clean", "--skip=" + skip})
	dist := built.Directory(ansivideoDistDir)

	dist, err = m.publishRelease(ctx, built, dist, tag, version, prerelease)
	if err != nil {
		return nil, err
	}

	return m.publishImage(ctx, dist, version, registryUsername, registryPassword, oidcRequestURL, oidcRequestToken)
}

// publishRelease creates (or reuses) the GitHub release for the real prefixed
// tag with the gh CLI and uploads the GoReleaser artifacts to it. built is the
// post-GoReleaser container (it already carries GITHUB_TOKEN and the dist
// directory); dist lists the artifacts to upload.
func (m *Ci) publishRelease(
	ctx context.Context,
	built *dagger.Container,
	dist *dagger.Directory,
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
		`  gh release create "$TAG" --repo "$REPO" --title "ansivideo $VERSION"` +
			` --generate-notes --verify-tag $PRERELEASE ||`,
		`  gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1`,
		`gh release upload "$TAG" --repo "$REPO" --clobber $ASSETS`,
	}, "\n")

	_, err = built.
		WithFile("/usr/local/bin/gh", ghBinary()).
		WithEnvVariable("TAG", tag).
		WithEnvVariable("REPO", githubRepo).
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

// publishImage builds the multi-arch ansivideo container image from the dist
// binaries, publishes it to the registry under the derived tags, signs the
// digests with cosign keyless signing, and records the digests in
// dist/digests.txt for attestation.
func (m *Ci) publishImage(
	ctx context.Context,
	dist *dagger.Directory,
	version, registryUsername string,
	registryPassword *dagger.Secret,
	oidcRequestURL string,
	oidcRequestToken *dagger.Secret,
) (*dagger.Directory, error) {
	tags, err := m.Goreleaser.VersionTags(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("derive version tags: %w", err)
	}

	variants := runtimeImages(dist, version)
	digests, err := m.publishImages(ctx, variants, tags, registryUsername, registryPassword)
	if err != nil {
		return nil, fmt.Errorf("publish images: %w", err)
	}

	if err := m.signImages(ctx, digests, registryUsername, registryPassword, oidcRequestURL, oidcRequestToken); err != nil {
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

// runtimeImages builds the multi-arch ansivideo container image variants from a
// GoReleaser dist directory. Each variant is debian-slim with ffmpeg (the only
// runtime dependency) and the matching cross-compiled binary.
func runtimeImages(dist *dagger.Directory, version string) []*dagger.Container {
	platforms := []dagger.Platform{"linux/amd64", "linux/arm64"}
	variants := make([]*dagger.Container, len(platforms))
	created := time.Now().UTC().Format(time.RFC3339)

	for i, platform := range platforms {
		// GoReleaser writes each target to <build-id>_<os>_<arch>_<variant>/.
		distDir := "ansivideo_linux_amd64_v1"
		if platform == "linux/arm64" {
			distDir = "ansivideo_linux_arm64_v8.0"
		}

		variants[i] = runtimeBase(platform).
			WithLabel("org.opencontainers.image.version", version).
			WithLabel("org.opencontainers.image.created", created).
			WithAnnotation("org.opencontainers.image.version", version).
			WithAnnotation("org.opencontainers.image.created", created).
			WithFile("/usr/local/bin/ansivideo", dist.File(distDir+"/ansivideo")).
			WithEntrypoint([]string{"ansivideo"})
	}

	return variants
}

// runtimeBase returns a debian-slim container for the given platform with
// ffmpeg installed and ansivideo's OCI metadata applied.
func runtimeBase(platform dagger.Platform) *dagger.Container {
	return dag.Container(dagger.ContainerOpts{Platform: platform}).
		From(debianImage).
		WithLabel("org.opencontainers.image.title", "ansivideo").
		WithLabel("org.opencontainers.image.description", "Play video in the terminal with ANSI half-block characters").
		WithLabel("org.opencontainers.image.source", "https://github.com/MacroPower/x").
		WithLabel("org.opencontainers.image.url", "https://github.com/MacroPower/x").
		WithLabel("org.opencontainers.image.licenses", "Apache-2.0").
		WithAnnotation("org.opencontainers.image.title", "ansivideo").
		WithAnnotation("org.opencontainers.image.source", "https://github.com/MacroPower/x").
		// ffmpeg is ansivideo's sole runtime dependency.
		WithExec([]string{
			"sh", "-c",
			"apt-get update && apt-get install -y --no-install-recommends ffmpeg && " +
				"rm -rf /var/lib/apt/lists/* /tmp/*",
		})
}

// publishImages publishes the pre-built image variants under each tag. Returns
// the published digest references (one per tag,
// e.g. "registry/image:tag@sha256:hex").
func (m *Ci) publishImages(
	ctx context.Context,
	variants []*dagger.Container,
	tags []string,
	registryUsername string,
	registryPassword *dagger.Secret,
) ([]string, error) {
	publisher := dag.Container()
	if registryPassword != nil {
		host, err := m.Goreleaser.RegistryHost(ctx, ansivideoRegistry)
		if err != nil {
			return nil, fmt.Errorf("resolve registry host: %w", err)
		}
		publisher = publisher.WithRegistryAuth(host, registryUsername, registryPassword)
	}

	// Publish a multi-arch manifest per tag concurrently.
	digests := make([]string, len(tags))
	g, gCtx := errgroup.WithContext(ctx)
	for i, t := range tags {
		ref := fmt.Sprintf("%s:%s", ansivideoRegistry, t)
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
		host, err = m.Goreleaser.RegistryHost(ctx, ansivideoRegistry)
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
