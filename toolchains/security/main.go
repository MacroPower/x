// Security scans source dependencies and container images for known
// vulnerabilities using Trivy.
package main

import (
	"context"

	"dagger/security/internal/dagger"
)

const (
	// Official Trivy image from GHCR (listed alongside Docker Hub and ECR
	// in Trivy's install docs), avoiding Docker Hub pull rate limits.
	defaultTrivyImage = "ghcr.io/aquasecurity/trivy:0.71.2" // renovate: datasource=docker depName=ghcr.io/aquasecurity/trivy

	defaultSeverity       = "CRITICAL,HIGH"
	defaultScanners       = "vuln"
	defaultSourcePkgTypes = "library"
	defaultImagePkgTypes  = "os,library"

	// defaultCacheNamespace is the module's canonical path, used as the prefix
	// for the Trivy database cache volume. Override via the cacheNamespace
	// constructor parameter when consuming from another project so cache
	// volumes do not collide across projects sharing an engine.
	defaultCacheNamespace = "go.jacobcolvin.com/x/toolchains/security"

	// sarifOutput is the in-container path where SARIF results are written.
	sarifOutput = "/tmp/trivy-results.sarif"
)

// Security scans source dependencies and container images for known
// vulnerabilities using Trivy. Create instances with [New].
type Security struct {
	// Source directory to scan for dependency vulnerabilities.
	Source *dagger.Directory
	// Trivy container image reference.
	Image string
	// Comma-separated Trivy severity filter applied to all scans.
	Severity string
	// Trivy --scanners value applied to all scans (source and image). Defaults
	// to vuln only, so neither scan gates on Trivy's image-default secret
	// scanner.
	Scanners string
	// Trivy --pkg-types value for source/filesystem scans.
	SourcePkgTypes string
	// Trivy --pkg-types value for image scans.
	ImagePkgTypes string
	// Name of the Trivy cache volume (the vulnerability database cache).
	CacheNamespace string // +private
}

// New creates a new [Security] module.
func New(
	// Project source directory. Ignore patterns belong in the consuming
	// project's root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// Trivy container image.
	// +optional
	image string,
	// Comma-separated Trivy severity filter applied to all scan functions.
	// +optional
	severity string,
	// Trivy --scanners value applied to all scans (source and image).
	// +optional
	scanners string,
	// Trivy --pkg-types value for source/filesystem scans.
	// +optional
	sourcePkgTypes string,
	// Trivy --pkg-types value for image scans.
	// +optional
	imagePkgTypes string,
	// Name of the Trivy cache volume (vulnerability database cache),
	// mounted at /root/.cache with locked sharing. Override to namespace
	// the cache when multiple toolchains share an engine.
	// +optional
	cacheNamespace string,
) *Security {
	if image == "" {
		image = defaultTrivyImage
	}
	if severity == "" {
		severity = defaultSeverity
	}
	if scanners == "" {
		scanners = defaultScanners
	}
	if sourcePkgTypes == "" {
		sourcePkgTypes = defaultSourcePkgTypes
	}
	if imagePkgTypes == "" {
		imagePkgTypes = defaultImagePkgTypes
	}
	if cacheNamespace == "" {
		cacheNamespace = defaultCacheNamespace
	}
	return &Security{
		Source:         source,
		Image:          image,
		Severity:       severity,
		Scanners:       scanners,
		SourcePkgTypes: sourcePkgTypes,
		ImagePkgTypes:  imagePkgTypes,
		CacheNamespace: cacheNamespace,
	}
}

// trivyBase returns a Trivy container with a locked cache volume
// at /root/.cache for reproducible scans.
func (m *Security) trivyBase() *dagger.Container {
	return dag.Container().
		From(m.Image).
		WithMountedCache(
			"/root/.cache",
			dag.CacheVolume(m.CacheNamespace),
			dagger.ContainerWithMountedCacheOpts{
				Sharing: dagger.CacheSharingModeLocked,
			},
		).
		WithWorkdir("/root")
}

// ScanSource scans source dependencies for known vulnerabilities.
// Reports the configured severities. Trivy auto-discovers a .trivyignore
// file in the scanned directory for CVE suppression.
func (m *Security) ScanSource(ctx context.Context) error {
	_, err := m.trivyBase().
		WithMountedDirectory(".", m.Source).
		WithExec([]string{
			"trivy", "fs",
			"--scanners=" + m.Scanners,
			"--pkg-types=" + m.SourcePkgTypes,
			"--exit-code=1",
			"--severity=" + m.Severity,
			".",
		}).
		Sync(ctx)
	return err
}

// ScanImage scans a container image for known vulnerabilities in both
// OS packages and application libraries. Reports the configured severities.
func (m *Security) ScanImage(
	ctx context.Context,
	// Container to scan.
	target *dagger.Container,
) error {
	_, err := m.trivyBase().
		WithMountedFile("target.tar", target.AsTarball()).
		WithExec([]string{
			"trivy", "image",
			"--scanners=" + m.Scanners,
			"--pkg-types=" + m.ImagePkgTypes,
			"--exit-code=1",
			"--severity=" + m.Severity,
			"--input=target.tar",
		}).
		Sync(ctx)
	return err
}

// ScanSourceSarif scans source dependencies for known vulnerabilities and
// returns the results in SARIF format. The SARIF file can be uploaded to
// GitHub's Security tab for Code Scanning visibility on PRs.
//
// Unlike [Security.ScanSource], this function does not use --exit-code=1.
// SARIF output is intended to capture results as structured data for
// consumption by GitHub Code Scanning; failing the pipeline here would
// prevent the SARIF file from being produced and uploaded.
func (m *Security) ScanSourceSarif() *dagger.File {
	return m.trivyBase().
		WithMountedDirectory(".", m.Source).
		WithExec([]string{
			"trivy", "fs",
			"--scanners=" + m.Scanners,
			"--pkg-types=" + m.SourcePkgTypes,
			"--severity=" + m.Severity,
			"--format=sarif",
			"--output=" + sarifOutput,
			".",
		}).
		File(sarifOutput)
}

// ScanImageSarif scans a container image for known vulnerabilities in both
// OS packages and application libraries and returns the results in SARIF
// format. The SARIF file can be uploaded to GitHub's Security tab for Code
// Scanning visibility on PRs.
//
// Unlike [Security.ScanImage], this function does not use --exit-code=1.
// SARIF output is intended to capture results as structured data for
// consumption by GitHub Code Scanning; failing the pipeline here would
// prevent the SARIF file from being produced and uploaded.
func (m *Security) ScanImageSarif(
	// Container to scan.
	target *dagger.Container,
) *dagger.File {
	return m.trivyBase().
		WithMountedFile("target.tar", target.AsTarball()).
		WithExec([]string{
			"trivy", "image",
			"--scanners=" + m.Scanners,
			"--pkg-types=" + m.ImagePkgTypes,
			"--severity=" + m.Severity,
			"--format=sarif",
			"--output=" + sarifOutput,
			"--input=target.tar",
		}).
		File(sarifOutput)
}
