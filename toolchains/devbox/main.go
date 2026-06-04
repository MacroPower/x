// Devbox runs commands inside a project's Devbox (Nix-backed) environment,
// installing and caching the packages declared in its devbox.json so CI uses
// the same toolchain as local development. The full command surface stays in
// the consuming project, which composes Run with its own scripts.
package main

import (
	"context"

	"dagger/devbox/internal/dagger"
)

const (
	defaultImage = "jetpackio/devbox:0.13.7" // renovate: datasource=docker depName=jetpackio/devbox

	defaultCacheNamespace = "go.jacobcolvin.com/x/toolchains/devbox"

	// nixStore is where the Nix store lives in the devbox image. Both the
	// bootstrap installation and every package devbox realises live here, so it
	// is the cache that makes repeated runs fast.
	nixStore = "/nix"
	// containerUser is the non-root user the devbox image runs as. The Nix store
	// is owned by it, so the cache volume and mounted source must be too.
	containerUser = "devbox"
	// workdir is where the project source is mounted.
	workdir = "/src"
)

// Devbox runs commands inside a project's Devbox environment. Create instances
// with [New].
type Devbox struct {
	// Project source directory.
	Source *dagger.Directory
	// devbox container image, with Nix and the devbox CLI preinstalled.
	Image string
	// Namespace prefix for the Nix store cache volume.
	CacheNamespace string // +private
}

// New creates a new [Devbox] module.
func New(
	// Project source directory. Ignore patterns belong in the consuming
	// project's root dagger.json customizations, not here.
	// +defaultPath="/"
	source *dagger.Directory,
	// devbox container image.
	// +optional
	image string,
	// Namespace prefix for the Nix store cache volume. Override to avoid
	// collisions when multiple projects share an engine.
	// +optional
	cacheNamespace string,
) *Devbox {
	if image == "" {
		image = defaultImage
	}
	if cacheNamespace == "" {
		cacheNamespace = defaultCacheNamespace
	}
	return &Devbox{
		Source:         source,
		Image:          image,
		CacheNamespace: cacheNamespace,
	}
}

// Base returns the devbox image with the Nix store mounted as a cache volume.
// The volume is seeded from the image's own store so the bootstrap Nix
// installation keeps working, then accumulates installed packages across runs.
// Source is not mounted.
func (m *Devbox) Base() *dagger.Container {
	ctr := dag.Container().From(m.Image)
	return ctr.WithMountedCache(
		nixStore,
		dag.CacheVolume(m.CacheNamespace+":nix"),
		dagger.ContainerWithMountedCacheOpts{
			Source: ctr.Directory(nixStore),
			Owner:  containerUser,
		},
	)
}

// Install returns a container with the project's devbox.json (and lockfile)
// added and `devbox install` run, so the declared packages are realised into
// the cached Nix store. Only the manifest is added, so the install layer is
// keyed on the lockfile rather than on every source change.
func (m *Devbox) Install() *dagger.Container {
	return m.Base().
		WithWorkdir(workdir).
		WithDirectory(workdir, m.Source, dagger.ContainerWithDirectoryOpts{
			Include: []string{"devbox.json", "devbox.lock", "devbox.d/**"},
			Owner:   containerUser,
		}).
		WithExec([]string{"devbox", "install"})
}

// WithSource returns the install container with the full project source
// overlaid on top of the installed environment. Exposed so consumers can chain
// further steps or wrap it (e.g. with a cache-bust) for benchmarks. Source is
// overlaid rather than replacing the mount, so the .devbox state produced by
// Install survives (provided the consumer ignores .devbox).
func (m *Devbox) WithSource() *dagger.Container {
	return m.Install().WithDirectory(workdir, m.Source, dagger.ContainerWithDirectoryOpts{
		Owner: containerUser,
	})
}

// Run executes a command inside the devbox environment (equivalent to
// `devbox run -- <args>`) and returns its standard output. Use a shell form
// (e.g. ["sh", "-c", "..."]) for pipelines or multiple commands.
func (m *Devbox) Run(
	ctx context.Context,
	// Command and arguments to run inside the environment.
	args []string,
) (string, error) {
	return m.WithSource().
		WithExec(append([]string{"devbox", "run", "--"}, args...)).
		Stdout(ctx)
}
