// Goreleaser provides reusable GoReleaser CI primitives: a goreleaser-equipped
// build base, config validation, a git-repo bootstrap, and the pure
// tag/digest helpers shared by release pipelines. The full release
// orchestration (publishing, signing, runtime images) stays in each project's
// own CI module, which composes these primitives.
package main

import (
	"context"

	"dagger/goreleaser/internal/dagger"
	"dagger/goreleaser/release"
)

const (
	defaultGoVersion  = "1.26"    // renovate: datasource=golang-version depName=go
	goreleaserVersion = "v2.13.3" // renovate: datasource=github-releases depName=goreleaser/goreleaser
)

// Goreleaser provides reusable GoReleaser CI primitives. Create instances
// with [New].
type Goreleaser struct {
	// Project source directory.
	Source *dagger.Directory
	// Base container to build on (typically the consumer's Go build base).
	Base *dagger.Container
	// GoReleaser version (image tag for ghcr.io/goreleaser/goreleaser).
	Version string
	// Git remote URL configured on the bootstrapped repo, used by GoReleaser
	// for changelog/version derivation and homebrew/nix repo resolution.
	RemoteURL string
}

// New creates a [Goreleaser] module with the given project source directory.
func New(
	// Project source directory. Ignore patterns belong in the consuming
	// project's root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// Base container to build on, typically the consumer's Go build base
	// (e.g. the go toolchain's Base()), so GoReleaser reuses its caches and
	// Go version. When nil, a plain golang:<goVersion> base is used.
	// +optional
	base *dagger.Container,
	// Go version for the fallback base image. Only used when base is nil.
	// +optional
	goVersion string,
	// GoReleaser version. Defaults to the version pinned in this module.
	// +optional
	version string,
	// Git remote URL to configure as origin on the bootstrapped repo.
	// +optional
	remoteURL string,
) *Goreleaser {
	if goVersion == "" {
		goVersion = defaultGoVersion
	}
	if version == "" {
		version = goreleaserVersion
	}
	if base == nil {
		base = dag.Container().From("golang:" + goVersion).WithWorkdir("/src")
	}
	return &Goreleaser{
		Source:    source,
		Base:      base,
		Version:   version,
		RemoteURL: remoteURL,
	}
}

// ---------------------------------------------------------------------------
// Base containers
// ---------------------------------------------------------------------------

// GoreleaserBase returns the base container with the goreleaser binary
// installed. This is the common base for config checks and release builds;
// callers mount source and bootstrap a git repo before running goreleaser.
// The binary is copied out of the official image rather than running that
// image directly, so it layers onto the caller's Go build environment.
func (m *Goreleaser) GoreleaserBase() *dagger.Container {
	return m.Base.WithFile("/usr/local/bin/goreleaser",
		dag.Container().From("ghcr.io/goreleaser/goreleaser:"+m.Version).
			File("/usr/bin/goreleaser"))
}

// CheckBase returns a container with goreleaser, the project source mounted
// at /src, and a bootstrapped git repo -- sufficient for `goreleaser check`.
func (m *Goreleaser) CheckBase() *dagger.Container {
	ctr := m.GoreleaserBase().
		WithMountedDirectory("/src", m.Source).
		WithWorkdir("/src")
	return m.EnsureGitRepo(ctr, m.RemoteURL)
}

// Check validates the GoReleaser configuration (.goreleaser.yaml) syntax.
//
// +check
func (m *Goreleaser) Check(ctx context.Context) error {
	_, err := m.CheckBase().
		WithExec([]string{"goreleaser", "check"}).
		Sync(ctx)
	return err
}

// ---------------------------------------------------------------------------
// Git bootstrap
// ---------------------------------------------------------------------------

// EnsureGitRepo ensures the container has a valid git repository at its
// working directory with all files staged and committed. When running from a
// git worktree, the .git file references a host path absent in the container;
// in that case a full repository is initialized so tools like GoReleaser that
// depend on committed files, dirty-tree detection, and version derivation
// continue to work. A fixed committer date keeps the result cache-stable.
func (m *Goreleaser) EnsureGitRepo(
	// Container to initialize.
	ctr *dagger.Container,
	// Remote URL to add as origin. When empty, no remote is configured.
	// +optional
	remoteURL string,
) *dagger.Container {
	remoteCmd := ""
	if remoteURL != "" {
		remoteCmd = "git remote add origin " + remoteURL + " && "
	}
	return ctr.WithExec([]string{
		"sh", "-c",
		"if ! git rev-parse --git-dir >/dev/null 2>&1; then " +
			"rm -f .git && " +
			"git init -q && " +
			remoteCmd +
			"git add -A && " +
			"GIT_COMMITTER_DATE='2000-01-01T00:00:00+00:00' " +
			"git -c user.email=ci@dagger -c user.name=ci commit -q --allow-empty -m init " +
			"--date='2000-01-01T00:00:00+00:00'; " +
			"fi",
	})
}

// ---------------------------------------------------------------------------
// Version + digest helpers (pure; logic lives in the release subpackage so it
// is unit-testable without a live engine)
// ---------------------------------------------------------------------------

// IsPrerelease reports whether the version tag contains a pre-release
// identifier (e.g. "v1.0.0-rc.1").
func (m *Goreleaser) IsPrerelease(
	// Version tag (e.g. "v1.2.3").
	tag string,
) bool {
	return release.IsPrerelease(tag)
}

// VersionTags returns the image tags derived from a version tag string.
// For example, "v1.2.3" yields ["latest", "v1.2.3", "v1", "v1.2"].
// Pre-release versions (e.g. "v1.0.0-rc.1") yield only the exact tag.
func (m *Goreleaser) VersionTags(
	// Version tag (e.g. "v1.2.3").
	tag string,
) []string {
	return release.VersionTags(tag)
}

// DeduplicateDigests returns unique image references from a list, keeping
// only the first occurrence of each sha256 digest.
func (m *Goreleaser) DeduplicateDigests(
	// Image references (e.g. "registry/image:tag@sha256:hex").
	refs []string,
) []string {
	return release.DeduplicateDigests(refs)
}

// FormatDigestChecksums converts publish output references to the checksums
// format expected by actions/attest-build-provenance. Each reference has the
// form "registry/image:tag@sha256:hex"; this emits "hex  registry/image:tag"
// lines, deduplicating by digest.
func (m *Goreleaser) FormatDigestChecksums(
	// Image references (e.g. "registry/image:tag@sha256:hex").
	refs []string,
) string {
	return release.FormatDigestChecksums(refs)
}

// RegistryHost extracts the host (with optional port) from a registry address.
// For example, "ghcr.io/macropower/eidetic" returns "ghcr.io".
func (m *Goreleaser) RegistryHost(
	// Registry address (e.g. "ghcr.io/macropower/eidetic").
	registry string,
) string {
	return release.RegistryHost(registry)
}
