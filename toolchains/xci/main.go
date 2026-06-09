// CI functions specific to the x repository. Generic capability lives in the
// sibling shared toolchains (go, security, devbox, prettier, zizmor), which
// the root dagger.json registers directly; this module adds only what direct
// registration cannot express: surfacing unit tests as a check with the
// project's Go configuration, and validating the Renovate configuration
// with a pinned renovate-config-validator.
package main

import (
	"context"

	"dagger/xci/internal/dagger"
)

const (
	// renovateConfig is the Renovate configuration file validated by
	// [Xci.LintRenovate], relative to the source root.
	renovateConfig = ".github/renovate.json5"

	// Docker Official Image, pulled from Docker's verified publisher
	// space on ECR Public to avoid Docker Hub pull rate limits.
	renovateImage   = "public.ecr.aws/docker/library/node:22-slim" // renovate: datasource=docker depName=public.ecr.aws/docker/library/node
	renovateVersion = "43.216.1"                                   // renovate: datasource=npm depName=renovate

	// cacheNamespace prefixes this module's cache volumes.
	cacheNamespace = "go.jacobcolvin.com/x/toolchains/xci"
)

// Xci provides CI functions for the x repository. Create instances with [New].
type Xci struct {
	// Project source directory.
	Source *dagger.Directory
	// Go toolchain module instance for delegation.
	Go *dagger.Go // +private
	// Prettier toolchain module instance for non-Go formatting.
	Prettier *dagger.Prettier // +private
}

// New creates an [Xci] module with the given project source directory.
func New(
	// Project source directory. Ignore patterns (e.g. .git, dist) belong
	// in the root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// Go module files (go.mod, go.sum, and go.work files). Synced
	// separately from source so that the go mod download layer is cached
	// independently of source code changes. Mirrors the go toolchain's
	// goMod ignore defaults.
	// +defaultPath="/"
	// +ignore=["**", "!go.work", "!go.work.sum", "!**/go.mod", "!**/go.sum", ".git", ".claude", ".worktrees", ".workmux", ".devbox", ".task", ".test", ".tmp", "dist", "toolchains", "**/testdata"]
	goMod *dagger.Directory,
) *Xci {
	return &Xci{
		Source: source,
		Go: dag.Go(dagger.GoOpts{
			Source: source,
			GoMod:  goMod,
			Race:   true,
		}),
		Prettier: dag.Prettier(dagger.PrettierOpts{Source: source}),
	}
}

// TestUnit runs the workspace's unit tests via the go toolchain with the
// race detector enabled. The shared go toolchain deliberately leaves
// test-unit unchecked (consumers may need project-specific bases); this
// wrapper is the project's checked entry point.
//
// +check
func (m *Xci) TestUnit(ctx context.Context) error {
	return m.Go.TestUnit(ctx)
}

// TestIntegration runs the workspace's integration tests (tests whose names
// match the Integration pattern, which [Xci.TestUnit] skips) via the go
// toolchain with the race detector enabled.
//
// +check
func (m *Xci) TestIntegration(ctx context.Context) error {
	return m.Go.TestIntegration(ctx)
}

// LintRenovate validates the Renovate configuration with
// renovate-config-validator, installed at a pinned version in a Node
// container so the check is self-contained and Renovate can bump its own
// validator version.
//
// +check
func (m *Xci) LintRenovate(ctx context.Context) error {
	_, err := dag.Container().
		From(renovateImage).
		WithMountedCache("/root/.npm", dag.CacheVolume(cacheNamespace+":npm")).
		WithExec([]string{"npm", "install", "-g", "renovate@" + renovateVersion}).
		WithMountedFile("/src/"+renovateConfig, m.Source.File(renovateConfig)).
		WithWorkdir("/src").
		WithExec([]string{"renovate-config-validator", renovateConfig}).
		Sync(ctx)
	return err
}
