// Commitlint validates commit messages against a project's conventional
// commit policy using commitlint.

package main

import (
	"context"

	"dagger/commitlint/internal/dagger"
)

const (
	// nodeImage is the Docker Official node image, pulled from Docker's
	// verified publisher space on ECR Public to avoid Docker Hub pull rate
	// limits. commitlint publishes its image only to Docker Hub, so the
	// default container is built here from node + npm instead.
	nodeImage = "public.ecr.aws/docker/library/node:22-slim" // renovate: datasource=docker depName=public.ecr.aws/docker/library/node

	// commitlintVersion pins @commitlint/cli and @commitlint/config-conventional
	// (released in lockstep) installed into the node container. This is the
	// npm package version, which trails the commitlint monorepo release tags.
	commitlintVersion = "21.0.2" // renovate: datasource=npm depName=@commitlint/cli

	// globalNodeModules is the node image's npm global root. commitlint
	// resolves `extends` presets via node module resolution from the config
	// directory, which does not include globally installed packages unless
	// NODE_PATH points at them.
	globalNodeModules = "/usr/local/lib/node_modules"

	// defaultCacheNamespace is the default prefix for cache volume names.
	defaultCacheNamespace = "go.jacobcolvin.com/x/toolchains/commitlint"
)

// Commitlint validates commit messages against a project's conventional
// commit policy. Create instances with [New].
type Commitlint struct {
	// Image, when set, is a prebuilt container image with commitlint on its
	// entrypoint. When empty, a node container with commitlint installed is
	// built in-module.
	Image string
	// Namespace prefix for the npm cache volume.
	CacheNamespace string // +private
}

// New creates a new [Commitlint] module.
func New(
	// Prebuilt container image with commitlint on its entrypoint. When
	// empty, a node container with commitlint installed is built in-module.
	// +optional
	image string,
	// Namespace prefix for the npm cache volume. Override to avoid
	// collisions when multiple projects share an engine.
	// +optional
	cacheNamespace string,
) *Commitlint {
	if cacheNamespace == "" {
		cacheNamespace = defaultCacheNamespace
	}
	return &Commitlint{Image: image, CacheNamespace: cacheNamespace}
}

// Lint validates commit messages against the project's commitlint
// configuration.
func (m *Commitlint) Lint(
	ctx context.Context,
	// Project source directory containing the commitlint config.
	source *dagger.Directory,
	// Commit message file to validate (e.g. .git/COMMIT_EDITMSG).
	// +optional
	msgFile *dagger.File,
	// Arguments to pass to commitlint.
	// +optional
	args []string,
) error {
	ctr := m.base().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src")

	if msgFile != nil {
		ctr = ctr.WithMountedFile("/tmp/commit-msg", msgFile)
		args = append(args, "--edit", "/tmp/commit-msg")
	}

	// A consumer-supplied image has commitlint on its entrypoint; the
	// default container invokes the binary directly.
	useEntrypoint := m.Image != ""
	if !useEntrypoint {
		args = append([]string{"commitlint"}, args...)
	}

	_, err := ctr.
		WithExec(args, dagger.ContainerWithExecOpts{UseEntrypoint: useEntrypoint}).
		Sync(ctx)
	return err
}

// base returns the container commitlint runs in: the consumer override when
// set, otherwise the node image with commitlint npm-installed. NODE_PATH
// points at the npm global root so `extends` presets like
// @commitlint/config-conventional resolve from the global installation.
func (m *Commitlint) base() *dagger.Container {
	if m.Image != "" {
		return dag.Container().From(m.Image)
	}
	return dag.Container().
		From(nodeImage).
		// commitlint shells out to git (e.g. `git config core.commentChar`
		// in --edit mode), which the slim node image does not include.
		WithExec([]string{"sh", "-c",
			"apt-get update" +
				" && apt-get install -y --no-install-recommends git" +
				" && rm -rf /var/lib/apt/lists/*",
		}).
		WithMountedCache("/root/.npm", dag.CacheVolume(m.CacheNamespace+":npm")).
		WithExec([]string{
			"npm", "install", "-g",
			"@commitlint/cli@" + commitlintVersion,
			"@commitlint/config-conventional@" + commitlintVersion,
		}).
		WithEnvVariable("NODE_PATH", globalNodeModules)
}
