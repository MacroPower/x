// Package main implements fixture-based tests for the prettier toolchain
// module: a clean fixture must pass Lint, a badly-formatted fixture must fail
// Lint, and Format must produce a non-empty changeset for the bad fixture.
package main

import (
	"context"
	"fmt"

	"dagger/tests/internal/dagger"
)

// Tests exercises the prettier toolchain.
type Tests struct{}

func (t *Tests) fixture(name string) *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/" + name)
}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"lint-clean", t.LintClean},
		{"lint-detects-unformatted", t.LintDetectsUnformatted},
		{"format-produces-changes", t.FormatProducesChanges},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// LintClean verifies a well-formatted fixture passes the check.
func (t *Tests) LintClean(ctx context.Context) error {
	return dag.Prettier(dagger.PrettierOpts{Source: t.fixture("clean")}).Lint(ctx)
}

// LintDetectsUnformatted verifies a badly-formatted fixture fails the check.
func (t *Tests) LintDetectsUnformatted(ctx context.Context) error {
	if err := dag.Prettier(dagger.PrettierOpts{Source: t.fixture("dirty")}).Lint(ctx); err == nil {
		return fmt.Errorf("expected prettier lint to flag the unformatted fixture")
	}
	return nil
}

// FormatProducesChanges verifies Format emits a non-empty changeset for the
// badly-formatted fixture.
func (t *Tests) FormatProducesChanges(ctx context.Context) error {
	patch, err := dag.Prettier(dagger.PrettierOpts{Source: t.fixture("dirty")}).
		Format().
		AsPatch().
		Contents(ctx)
	if err != nil {
		return err
	}
	if patch == "" {
		return fmt.Errorf("expected Format to produce a non-empty changeset for the unformatted fixture")
	}
	return nil
}
