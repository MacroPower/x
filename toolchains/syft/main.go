// Syft provides the Anchore syft SBOM generator: a Binary/WithSyft to install
// it into a release container (where goreleaser drives it), and a standalone
// Sbom that scans a directory. It pins the syft version in one place.
package main

import (
	"dagger/syft/internal/dagger"
)

const (
	syftVersion = "v1.41.1" // renovate: datasource=github-releases depName=anchore/syft

	defaultImage = "ghcr.io/anchore/syft:" + syftVersion

	// binPath is where the syft binary lives in the official image.
	binPath = "/syft"
)

// Syft provides the syft SBOM generator. Create instances with [New].
type Syft struct {
	// syft container image reference.
	Image string
}

// New creates a new [Syft] module.
func New(
	// syft container image.
	// +optional
	image string,
) *Syft {
	if image == "" {
		image = defaultImage
	}
	return &Syft{Image: image}
}

// Binary returns the syft executable, extracted from the official image so it
// can be layered onto another container (e.g. a goreleaser release base).
func (m *Syft) Binary() *dagger.File {
	return dag.Container().From(m.Image).File(binPath)
}

// WithSyft installs the syft binary at /usr/local/bin/syft in the given
// container, for tools (like goreleaser's sbom step) that invoke it.
func (m *Syft) WithSyft(
	// Container to install syft into.
	ctr *dagger.Container,
) *dagger.Container {
	return ctr.WithFile("/usr/local/bin/syft", m.Binary())
}

// Sbom scans a source directory and returns its SBOM in the given format
// (a syft output format such as "spdx-json", "cyclonedx-json", "syft-json").
func (m *Syft) Sbom(
	// Directory to scan.
	source *dagger.Directory,
	// syft output format.
	// +optional
	// +default="spdx-json"
	format string,
) *dagger.File {
	const out = "/tmp/sbom.json"
	return dag.Container().
		From(m.Image).
		WithMountedDirectory("/src", source).
		WithExec([]string{binPath, "scan", "dir:/src", "-o", format + "=" + out}).
		File(out)
}
