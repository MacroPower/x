// Prettier checks and formats YAML, JSON, Markdown (and other prettier-supported
// files) for a project. Lint is a +check; Format returns a Changeset the
// consumer can merge with its other formatters (e.g. gofmt).
package main

import (
	"context"

	"dagger/prettier/internal/dagger"
)

const (
	defaultImage   = "node:lts-slim"
	defaultVersion = "3.5.3" // renovate: datasource=npm depName=prettier

	defaultConfigPath     = "./.prettierrc.yaml"
	defaultCacheNamespace = "go.jacobcolvin.com/x/toolchains/prettier"
)

// Prettier checks and formats files with prettier. Create instances with [New].
type Prettier struct {
	// Project source directory.
	Source *dagger.Directory
	// Node container image used to install and run prettier.
	Image string
	// prettier version installed via npm.
	Version string
	// Default prettier config path, relative to the source root.
	ConfigPath string
	// Default file patterns to check/format.
	Patterns []string
	// Namespace prefix for the npm cache volume.
	CacheNamespace string // +private
}

// New creates a new [Prettier] module.
func New(
	// Project source directory. Ignore patterns belong in the consuming
	// project's root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// Node container image.
	// +optional
	image string,
	// prettier version (npm).
	// +optional
	version string,
	// Default prettier config path, relative to the source root.
	// +optional
	configPath string,
	// Default file patterns to check/format. Defaults to YAML/JSON/Markdown.
	// +optional
	patterns []string,
	// Namespace prefix for the npm cache volume. Override to avoid collisions
	// when multiple toolchains share an engine.
	// +optional
	cacheNamespace string,
) *Prettier {
	if image == "" {
		image = defaultImage
	}
	if version == "" {
		version = defaultVersion
	}
	if configPath == "" {
		configPath = defaultConfigPath
	}
	if len(patterns) == 0 {
		patterns = defaultPatterns()
	}
	if cacheNamespace == "" {
		cacheNamespace = defaultCacheNamespace
	}
	return &Prettier{
		Source:         source,
		Image:          image,
		Version:        version,
		ConfigPath:     configPath,
		Patterns:       patterns,
		CacheNamespace: cacheNamespace,
	}
}

// Base returns a Node container with prettier installed and an npm cache
// volume mounted. Source is not mounted.
func (m *Prettier) Base() *dagger.Container {
	return dag.Container().
		From(m.Image).
		WithMountedCache("/root/.npm", dag.CacheVolume(m.CacheNamespace+":npm")).
		WithExec([]string{"npm", "install", "-g", "prettier@" + m.Version})
}

// LintBase returns the prettier container with the project source mounted at
// /src. Exposed so consumers can wrap it (e.g. with a cache-bust) for
// benchmarks.
func (m *Prettier) LintBase() *dagger.Container {
	return m.Base().
		WithMountedDirectory("/src", m.Source).
		WithWorkdir("/src")
}

// Lint checks formatting of the configured patterns without modifying files.
//
// +check
func (m *Prettier) Lint(
	ctx context.Context,
	// Prettier config path, relative to the source root. Defaults to the
	// configured path.
	// +optional
	configPath string,
	// File patterns to check. Defaults to the configured patterns.
	// +optional
	patterns []string,
) error {
	configPath, patterns = m.resolve(configPath, patterns)
	args := append([]string{"prettier", "--config", configPath, "--check"}, patterns...)
	_, err := m.LintBase().WithExec(args).Sync(ctx)
	return err
}

// Format rewrites the configured patterns in place and returns a Changeset of
// the modifications, for the consumer to apply or merge with other formatters.
func (m *Prettier) Format(
	// Prettier config path, relative to the source root. Defaults to the
	// configured path.
	// +optional
	configPath string,
	// File patterns to format. Defaults to the configured patterns.
	// +optional
	patterns []string,
) *dagger.Changeset {
	configPath, patterns = m.resolve(configPath, patterns)
	args := append([]string{"prettier", "--config", configPath, "-w"}, patterns...)
	formatted := m.LintBase().WithExec(args).Directory("/src")
	return formatted.Changes(m.Source)
}

// resolve fills in the configured defaults for empty arguments.
func (m *Prettier) resolve(configPath string, patterns []string) (string, []string) {
	if configPath == "" {
		configPath = m.ConfigPath
	}
	if len(patterns) == 0 {
		patterns = m.Patterns
	}
	return configPath, patterns
}

// defaultPatterns returns the default file patterns (YAML, JSON, Markdown).
func defaultPatterns() []string {
	return []string{
		"*.yaml", "*.md", "*.json",
		"**/*.yaml", "**/*.md", "**/*.json",
	}
}
