// Zizmor lints GitHub Actions workflows for security issues using zizmor.
package main

import (
	"context"

	"dagger/zizmor/internal/dagger"
)

const (
	defaultImage = "ghcr.io/zizmorcore/zizmor:1.26.1" // renovate: datasource=docker depName=ghcr.io/zizmorcore/zizmor

	defaultWorkflowsDir = ".github/workflows"
)

// Zizmor lints GitHub Actions workflows for security issues using zizmor.
// Create instances with [New].
type Zizmor struct {
	// Project source directory.
	Source *dagger.Directory
	// zizmor container image reference.
	Image string
	// Path to the zizmor config file, relative to the source root. When empty,
	// zizmor auto-discovers a config (e.g. .github/zizmor.yml) or falls back to
	// its built-in audit defaults, so the module drops into projects that have
	// no config file.
	ConfigPath string
	// Directory of workflows to lint, relative to the source root.
	WorkflowsDir string
}

// New creates a new [Zizmor] module.
func New(
	// Project source directory. Ignore patterns belong in the consuming
	// project's root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// zizmor container image.
	// +optional
	image string,
	// Path to the zizmor config file, relative to the source root. When empty,
	// zizmor auto-discovers a config or uses its built-in defaults.
	// +optional
	configPath string,
	// Directory of workflows to lint, relative to the source root.
	// +optional
	workflowsDir string,
) *Zizmor {
	if image == "" {
		image = defaultImage
	}
	if workflowsDir == "" {
		workflowsDir = defaultWorkflowsDir
	}
	return &Zizmor{
		Source:       source,
		Image:        image,
		ConfigPath:   configPath,
		WorkflowsDir: workflowsDir,
	}
}

// LintBase returns the zizmor container with the project source mounted at
// /src. It is exposed so consumers can wrap it (e.g. with a cache-bust) for
// benchmarks without pulling in another toolchain dependency.
func (m *Zizmor) LintBase() *dagger.Container {
	return dag.Container().
		From(m.Image).
		WithMountedDirectory("/src", m.Source).
		WithWorkdir("/src")
}

// Lint runs zizmor against the workflows directory. The config file is passed
// only when [Zizmor.ConfigPath] is set; otherwise zizmor auto-discovers a
// config or uses its built-in defaults.
//
// +check
func (m *Zizmor) Lint(ctx context.Context) error {
	args := []string{"zizmor", m.WorkflowsDir}
	if m.ConfigPath != "" {
		args = append(args, "--config", m.ConfigPath)
	}
	_, err := m.LintBase().WithExec(args).Sync(ctx)
	return err
}
