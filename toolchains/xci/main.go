// CI functions specific to the x repository. Generic capability lives in the
// sibling shared toolchains (go, security, devbox, prettier, zizmor), which
// the root dagger.json registers directly; this module adds only what direct
// registration cannot express: surfacing unit tests as a check with the
// project's Go configuration, and validating the Renovate configuration
// inside the project's Devbox environment.
package main

import (
	"context"

	"dagger/xci/internal/dagger"
)

// renovateConfig is the Renovate configuration file validated by
// [Xci.LintRenovate], relative to the source root.
const renovateConfig = ".github/renovate.json5"

// Xci provides CI functions for the x repository. Create instances with [New].
type Xci struct {
	// Project source directory.
	Source *dagger.Directory
	// Go toolchain module instance for delegation.
	Go *dagger.Go // +private
	// Devbox toolchain module instance for running project tooling.
	Devbox *dagger.Devbox // +private
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
		Devbox:   dag.Devbox(dagger.DevboxOpts{Source: source}),
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
// renovate-config-validator, running inside the project's Devbox
// environment so CI validates with the same Renovate version developers
// install locally.
//
// +check
func (m *Xci) LintRenovate(ctx context.Context) error {
	_, err := m.Devbox.Run(ctx, []string{"renovate-config-validator", renovateConfig})
	return err
}
