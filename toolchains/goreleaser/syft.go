// Anchore syft SBOM generation, folded into the goreleaser toolchain because it
// is part of the same release pipeline: WithSyft installs the syft binary into a
// release container, where GoReleaser's sbom step drives it to produce an SBOM
// per archive. The syft version is pinned in one place.
package main

import (
	"dagger/goreleaser/internal/dagger"
)

const (
	// syftVersion is the pinned syft release used for the default image.
	syftVersion = "v1.46.0" // renovate: datasource=github-releases depName=anchore/syft

	// syftImage is the official syft image the binary is extracted from.
	syftImage = "ghcr.io/anchore/syft:" + syftVersion

	// syftBinPath is the syft executable path inside the official image.
	syftBinPath = "/syft"
)

// syftBinary returns the syft executable, extracted from the official image so
// it can be layered onto a release container (e.g. a goreleaser release base).
func (m *Goreleaser) syftBinary() *dagger.File {
	return dag.Container().From(syftImage).File(syftBinPath)
}

// WithSyft installs the syft binary at /usr/local/bin/syft in the given
// container, for tools (like GoReleaser's sbom step) that invoke it.
func (m *Goreleaser) WithSyft(
	// Container to install syft into.
	ctr *dagger.Container,
) *dagger.Container {
	return ctr.WithFile("/usr/local/bin/syft", m.syftBinary())
}
