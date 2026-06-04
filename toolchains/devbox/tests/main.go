// Package main implements fixture-based tests for the devbox toolchain module:
// arbitrary commands must execute inside the environment, and a package
// declared in the synthetic project's devbox.json must be realised onto PATH.
package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/tests/internal/dagger"
)

// Tests exercises the devbox toolchain.
type Tests struct{}

func (t *Tests) fixture() *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/fixture")
}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"run-executes-command", t.RunExecutesCommand},
		{"run-uses-installed-package", t.RunUsesInstalledPackage},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// RunExecutesCommand verifies an arbitrary command runs inside the environment
// and its stdout is returned to the caller.
func (t *Tests) RunExecutesCommand(ctx context.Context) error {
	out, err := dag.Devbox(dagger.DevboxOpts{Source: t.fixture()}).
		Run(ctx, []string{"sh", "-c", "echo devbox-ok"})
	if err != nil {
		return err
	}
	if !strings.Contains(out, "devbox-ok") {
		return fmt.Errorf("expected output to contain %q, got: %q", "devbox-ok", out)
	}
	return nil
}

// RunUsesInstalledPackage verifies a package declared in the fixture's
// devbox.json is installed into the environment and runnable on PATH.
func (t *Tests) RunUsesInstalledPackage(ctx context.Context) error {
	out, err := dag.Devbox(dagger.DevboxOpts{Source: t.fixture()}).
		Run(ctx, []string{"jq", "--version"})
	if err != nil {
		return err
	}
	if !strings.Contains(out, "jq") {
		return fmt.Errorf("expected jq version output, got: %q", out)
	}
	return nil
}
