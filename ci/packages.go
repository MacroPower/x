// Releasable-package discovery for the x monorepo. A package opts into the
// release pipeline by dropping a release.yaml manifest at its module root; the
// ci module discovers every such manifest under the source root and resolves it
// into a [pkg]. Everything else -- the Go submodule tag prefix, the GoReleaser
// config path, the dist directory, and the built binary name -- derives from the
// package's directory name by convention, so nothing here is hardcoded to a
// specific package. The release functions in release.go consume these specs;
// [Ci.Packages] and [Ci.ImagePackages] expose the discovered set as JSON so the
// GitHub Actions workflows can build their matrices without per-package edits.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"

	"dagger/ci/internal/dagger"

	"gopkg.in/yaml.v3"
)

// releaseManifest is the file, at a package's module root, that declares the
// package releasable and carries its release configuration. Its presence is
// what makes a package releasable; the ci module discovers every
// <package>/release.yaml under the source root.
const releaseManifest = "release.yaml"

// packageNamePattern restricts package directory names to a safe charset. The
// name flows unquoted into the GitHub Actions matrices ([Ci.Packages] /
// [Ci.ImagePackages]) and into shell-expanded Dagger calls, so discovery
// rejects anything outside this class. It matches the release workflow's tag
// prefix guard.
const packageNamePattern = `^[a-z0-9._-]+$`

// packageNameRE is the compiled [packageNamePattern].
var packageNameRE = regexp.MustCompile(packageNamePattern)

// pkg is a releasable package resolved from a release.yaml manifest. The name
// is the single source of every derived path: the directory, the Go submodule
// tag prefix (name/vX.Y.Z), the GoReleaser project and build id, and the
// default binary name.
type pkg struct {
	// name is the package directory and Go submodule tag prefix.
	name string
	// binary is the built binary's file name; defaults to name.
	binary string
	// image is the container-image configuration, or nil for a binary-only
	// package (released as archives, checksums, SBOMs, and signatures, with no
	// image built, published, or scanned).
	image *pkgImage
}

// pkgImage is the container-image configuration for a releasable package.
type pkgImage struct {
	// registry is the image repository (e.g. "ghcr.io/macropower/ansivideo").
	registry string
	// description is the org.opencontainers.image.description label value.
	description string
	// runtimeAptPackages are Debian packages installed into the runtime image
	// for the binary's runtime dependencies (e.g. ["ffmpeg"]).
	runtimeAptPackages []string
}

// releaseManifestSchema mirrors the on-disk release.yaml. Only image-producing
// packages need an image block; binary-only packages may leave the manifest
// empty (its presence alone marks the package releasable). The package name is
// not configurable here: it is the directory, which is also the Go submodule
// tag prefix and the GoReleaser project/build id, so it must stay in lockstep.
type releaseManifestSchema struct {
	// Binary overrides the built binary name; defaults to the package name.
	Binary string `yaml:"binary"`
	// Image, when present, opts the package into a published container image.
	Image *struct {
		Registry           string   `yaml:"registry"`
		Description        string   `yaml:"description"`
		RuntimeAptPackages []string `yaml:"runtimeAptPackages"`
	} `yaml:"image"`
}

// dir is the package's module directory inside the release container, where
// GoReleaser runs so its config and build paths resolve.
func (p *pkg) dir() string { return "/src/" + p.name }

// distDir is the GoReleaser output directory inside the release container.
func (p *pkg) distDir() string { return p.dir() + "/dist" }

// goreleaserConfig is the package's GoReleaser config, relative to the source
// root, passed to `goreleaser check` and discovered by [Ci.LintReleaser].
func (p *pkg) goreleaserConfig() string { return p.name + "/.goreleaser.yaml" }

// discoverPackages resolves every releasable package from the
// <package>/release.yaml manifests under the source root, sorted by name for
// deterministic output.
func (m *Ci) discoverPackages(ctx context.Context) ([]*pkg, error) {
	matches, err := m.Source.Glob(ctx, "*/"+releaseManifest)
	if err != nil {
		return nil, fmt.Errorf("discover release manifests: %w", err)
	}

	sort.Strings(matches)

	pkgs := make([]*pkg, 0, len(matches))
	for _, match := range matches {
		p, err := m.loadPackage(ctx, path.Dir(match))
		if err != nil {
			return nil, err
		}

		pkgs = append(pkgs, p)
	}

	return pkgs, nil
}

// loadPackage resolves a single releasable package by name (its directory),
// reading and parsing <name>/release.yaml. It errors when the manifest is
// absent, so callers that take a package argument reject non-releasable inputs.
func (m *Ci) loadPackage(ctx context.Context, name string) (*pkg, error) {
	if !packageNameRE.MatchString(name) {
		return nil, fmt.Errorf("package name %q must match %s", name, packageNamePattern)
	}

	manifestPath := name + "/" + releaseManifest

	content, err := m.Source.File(manifestPath).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("%q is not a releasable package (no %s): %w", name, releaseManifest, err)
	}

	var schema releaseManifestSchema
	if err := yaml.Unmarshal([]byte(content), &schema); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}

	// The directory is the single source of truth for the package name (it is
	// also the tag prefix and the GoReleaser project/build id), so it is not
	// overridable; only the binary file name is.
	p := &pkg{name: name, binary: schema.Binary}
	if p.binary == "" {
		p.binary = name
	}

	if schema.Image != nil {
		if schema.Image.Registry == "" {
			return nil, fmt.Errorf("%s: image.registry is required when an image block is set", manifestPath)
		}
		p.image = &pkgImage{
			registry:           schema.Image.Registry,
			description:        schema.Image.Description,
			runtimeAptPackages: schema.Image.RuntimeAptPackages,
		}
	}

	return p, nil
}

// Packages lists every releasable package, as a JSON array of names, for the
// GitHub Actions build matrix. The package set is the manifests discovered at
// the source root, so a new package is picked up with no workflow edits.
func (m *Ci) Packages(ctx context.Context) (*dagger.File, error) {
	pkgs, err := m.discoverPackages(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(pkgs))
	for i, p := range pkgs {
		names[i] = p.name
	}

	return jsonArrayFile("packages.json", names)
}

// ImagePackages lists the releasable packages that build a container image
// (those whose manifest declares an image block), as a JSON array of names, for
// the GitHub Actions image-scan matrix. The array is empty when no package
// builds an image, which leaves the matrix empty and the scan job skipped.
func (m *Ci) ImagePackages(ctx context.Context) (*dagger.File, error) {
	pkgs, err := m.discoverPackages(ctx)
	if err != nil {
		return nil, err
	}

	names := []string{}
	for _, p := range pkgs {
		if p.image != nil {
			names = append(names, p.name)
		}
	}

	return jsonArrayFile("image-packages.json", names)
}

// jsonArrayFile renders names as a compact JSON array file, consumable by
// `export --path` and GitHub Actions `fromJSON` for a dynamic matrix.
func jsonArrayFile(name string, names []string) (*dagger.File, error) {
	data, err := json.Marshal(names)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", name, err)
	}

	return dag.Directory().WithNewFile(name, string(data)).File(name), nil
}
