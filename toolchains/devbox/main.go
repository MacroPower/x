// Devbox runs commands inside a project's Devbox (Nix-backed) environment,
// installing and caching the packages declared in its devbox.json so CI uses
// the same toolchain as local development. The full command surface stays in
// the consuming project, which composes Run with its own scripts.
package main

import (
	"context"
	"fmt"

	"dagger/devbox/internal/dagger"
)

const (
	// debianImage is the Docker Official debian image, pulled from Docker's
	// verified publisher space on ECR Public to avoid Docker Hub pull rate
	// limits. jetify publishes the devbox image only to Docker Hub, so the
	// default container is built here instead, mirroring jetify's own
	// Dockerfile with pinned downloads.
	debianImage = "public.ecr.aws/docker/library/debian:13-slim" // renovate: datasource=docker depName=public.ecr.aws/docker/library/debian

	// devboxVersion is the jetify devbox release installed into the built
	// image.
	devboxVersion = "0.17.5" // renovate: datasource=github-releases depName=jetify-com/devbox

	// nixVersion pins the single-user Nix installer so the bootstrap store
	// is reproducible across image builds.
	nixVersion = "2.34.7" // renovate: datasource=github-tags depName=NixOS/nix

	defaultCacheNamespace = "go.jacobcolvin.com/x/toolchains/devbox"

	// nixStore is where the Nix store lives in the devbox image. Both the
	// bootstrap installation and every package devbox realises live here, so it
	// is the cache that makes repeated runs fast.
	nixStore = "/nix"
	// containerUser is the non-root user the devbox image runs as. The Nix store
	// is owned by it, so the cache volume and mounted source must be too.
	containerUser = "devbox"
	// nixProfileBin is where the single-user Nix install places its profile
	// binaries (nix itself and anything realised into the profile). It is
	// baked onto PATH so execs see nix without sourcing a shell profile.
	nixProfileBin = "/home/" + containerUser + "/.nix-profile/bin"
	// workdir is where the project source is mounted.
	workdir = "/src"
)

// Devbox runs commands inside a project's Devbox environment. Create instances
// with [New].
type Devbox struct {
	// Project source directory.
	Source *dagger.Directory
	// Image, when set, is a prebuilt devbox container image with Nix and the
	// devbox CLI preinstalled and /nix owned by the devbox user. When empty,
	// an equivalent image is built in-module from the debian base.
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
	// Prebuilt devbox container image. When empty, the image is built
	// in-module from the debian base with pinned Nix and devbox.
	// +optional
	image string,
	// Namespace prefix for the Nix store cache volume. Override to avoid
	// collisions when multiple projects share an engine.
	// +optional
	cacheNamespace string,
) *Devbox {
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
//
// The cache key includes the image identity — the devbox and Nix versions for
// the in-module build, or the image reference for an override — because the
// seed (Source) and Owner only take effect when the volume is first created.
// Keying on the identity rotates the volume when the image is bumped, so a
// new image's bootstrap store is never shadowed by a stale snapshot. Writes
// are serialized (Locked) since the Nix store is backed by a SQLite database
// that concurrent writers can corrupt.
func (m *Devbox) Base() *dagger.Container {
	ctr, key := m.image()
	return ctr.WithMountedCache(
		nixStore,
		dag.CacheVolume(m.CacheNamespace+":nix:"+key),
		dagger.ContainerWithMountedCacheOpts{
			Source:  ctr.Directory(nixStore),
			Owner:   containerUser,
			Sharing: dagger.CacheSharingModeLocked,
		},
	)
}

// image returns the devbox container image and its cache-key identity: the
// consumer override (keyed on the image reference) when set, otherwise a
// debian base with a single-user Nix installation owned by the devbox user
// and the devbox CLI on PATH (keyed on the devbox and Nix versions). The
// build mirrors jetify's upstream devbox image Dockerfile, with the
// moving-target installer scripts replaced by pinned downloads (the devbox
// release binary instead of the get.jetify.com launcher, a versioned Nix
// installer instead of the nixos.org redirect) so the bootstrap /nix store
// that [Devbox.Base] seeds the cache volume from is reproducible.
func (m *Devbox) image() (*dagger.Container, string) {
	if m.Image != "" {
		return dag.Container().From(m.Image), m.Image
	}

	ctr := dag.Container().
		From(debianImage).
		WithExec([]string{"sh", "-c",
			"apt-get update" +
				" && apt-get install -y --no-install-recommends bash binutils git xz-utils wget sudo ca-certificates" +
				" && rm -rf /var/lib/apt/lists/*",
		}).
		// The devbox user owns /nix (created via sudo by the single-user Nix
		// installer below) and matches the Owner of the cache volume seed.
		WithExec([]string{"sh", "-c", fmt.Sprintf(
			"useradd --create-home --shell /bin/bash %[1]s"+
				" && usermod -aG sudo %[1]s"+
				" && echo '%[1]s ALL=(ALL:ALL) NOPASSWD: ALL' > /etc/sudoers.d/%[1]s",
			containerUser,
		)}).
		// Nix's seccomp syscall filtering does not work on arm64; jetify's
		// upstream image disables it there too.
		WithExec([]string{"sh", "-c",
			`if [ "$(dpkg --print-architecture)" = "arm64" ]; then` +
				` mkdir -p /etc/nix && echo 'filter-syscalls = false' >> /etc/nix/nix.conf; fi`,
		}).
		// The release tarball contains the bare devbox binary at its root.
		WithExec([]string{"sh", "-c", fmt.Sprintf(
			`wget -qO- "https://github.com/jetify-com/devbox/releases/download/%[1]s/devbox_%[1]s_linux_$(dpkg --print-architecture).tar.gz"`+
				` | tar -xz -C /usr/local/bin devbox`,
			devboxVersion,
		)}).
		WithUser(containerUser).
		WithExec([]string{"sh", "-c", fmt.Sprintf(
			`wget -qO /tmp/nix-install "https://releases.nixos.org/nix/nix-%s/install"`+
				` && sh /tmp/nix-install --no-daemon`+
				` && rm /tmp/nix-install`,
			nixVersion,
		)}).
		// Put the Nix profile on PATH directly so later WithExec calls see
		// nix without sourcing a shell profile.
		WithEnvVariable("PATH",
			nixProfileBin+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	return ctr, "devbox-" + devboxVersion + "-nix-" + nixVersion
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
