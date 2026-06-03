// Package main implements fixture-based tests for the syft toolchain module:
// Sbom detects packages from a fixture, and WithSyft installs a runnable binary.
package main

import (
	"context"
	"fmt"
	"strings"
)

// Tests exercises the syft toolchain.
type Tests struct{}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"sbom-detects-package", t.SbomDetectsPackage},
		{"with-syft-installs", t.WithSyftInstalls},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// SbomDetectsPackage verifies a directory scan produces a valid SPDX SBOM that
// lists a package declared in the fixture's requirements.txt.
func (t *Tests) SbomDetectsPackage(ctx context.Context) error {
	src := dag.CurrentModule().Source().Directory("testdata/fixture")
	out, err := dag.Syft().Sbom(src).Contents(ctx)
	if err != nil {
		return err
	}
	for _, want := range []string{"spdxVersion", "requests", "flask"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("SBOM missing %q", want)
		}
	}
	return nil
}

// WithSyftInstalls verifies the installed binary is runnable in another image.
func (t *Tests) WithSyftInstalls(ctx context.Context) error {
	_, err := dag.Syft().
		WithSyft(dag.Container().From("debian:13-slim")).
		WithExec([]string{"syft", "version"}).
		Sync(ctx)
	return err
}
