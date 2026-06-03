// Package main implements fixture-based tests for the zizmor toolchain module.
// A clean workflow set must pass the lint; a workflow with a template-injection
// finding (a stable offline zizmor audit) must fail it.
package main

import (
	"context"
	"fmt"

	"dagger/tests/internal/dagger"
)

// Tests exercises the zizmor toolchain.
type Tests struct{}

// fixture returns the named fixture project under testdata.
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
		{"lint-detects-issue", t.LintDetectsIssue},
		{"lint-no-config", t.LintNoConfig},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// LintClean verifies a finding-free workflow set passes the lint.
func (t *Tests) LintClean(ctx context.Context) error {
	return dag.Zizmor(dagger.ZizmorOpts{Source: t.fixture("clean")}).Lint(ctx)
}

// LintDetectsIssue verifies a workflow with a template-injection finding fails
// the lint, proving zizmor runs and its exit code is honored.
func (t *Tests) LintDetectsIssue(ctx context.Context) error {
	if err := dag.Zizmor(dagger.ZizmorOpts{Source: t.fixture("dirty")}).Lint(ctx); err == nil {
		return fmt.Errorf("expected zizmor lint to flag the template-injection workflow")
	}
	return nil
}

// LintNoConfig verifies that a project with no zizmor config file lints against
// zizmor's built-in defaults and passes, rather than erroring on a missing
// config. This is the "droppable into any consumer" path.
func (t *Tests) LintNoConfig(ctx context.Context) error {
	return dag.Zizmor(dagger.ZizmorOpts{Source: t.fixture("no-config")}).Lint(ctx)
}
