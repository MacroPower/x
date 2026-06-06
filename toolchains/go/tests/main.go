// Package main implements fixture-based tests for the go toolchain module.
//
// The tests run the toolchain against a synthetic, dependency-free fixture
// module (see testdata/fixture) rather than any real project layout, so they
// stay portable across consumers.
package main

import (
	"context"
	"fmt"
	"slices"

	"dagger/tests/internal/dagger"
)

// Tests exercises the go toolchain against a synthetic fixture module.
type Tests struct{}

// fixture returns the synthetic minimal Go module used as test input. It is
// self-contained with a nested submodule so multi-module discovery, tidy,
// lint, build, and test are all exercised without coupling to a real project.
func (t *Tests) fixture() *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/fixture")
}

// subject constructs the go module under test with the fixture as source.
func (t *Tests) subject() *dagger.Go {
	fixture := t.fixture()
	return dag.Go(dagger.GoOpts{
		Source: fixture,
		GoMod:  fixture,
	})
}

// workspaceFixture returns the synthetic go.work workspace used as test
// input. Its root has a go.work but no go.mod, so the default "./..."
// package pattern matches nothing there and must be expanded into
// per-module patterns.
func (t *Tests) workspaceFixture() *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/workspace")
}

// workspaceSubject constructs the go module under test with the workspace
// fixture as source.
func (t *Tests) workspaceSubject() *dagger.Go {
	fixture := t.workspaceFixture()
	return dag.Go(dagger.GoOpts{
		Source: fixture,
		GoMod:  fixture,
	})
}

// All runs every test in sequence and reports the first failure. Stages
// share cached container layers, so sequential execution stays cheap.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"modules", t.Modules},
		{"build", t.Build},
		{"test-unit", t.TestUnit},
		{"test-integration", t.TestIntegration},
		{"lint", t.Lint},
		{"check-tidy", t.CheckTidy},
		{"tidy", t.Tidy},
		{"generate", t.Generate},
		{"ensure-git", t.EnsureGit},
		{"workspace-modules", t.WorkspaceModules},
		{"workspace-build", t.WorkspaceBuild},
		{"workspace-test-unit", t.WorkspaceTestUnit},
		{"workspace-lint", t.WorkspaceLint},
		{"workspace-check-tidy", t.WorkspaceCheckTidy},
		{"workspace-generate", t.WorkspaceGenerate},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// Modules verifies multi-module discovery finds the root and nested modules.
func (t *Tests) Modules(ctx context.Context) error {
	got, err := t.subject().Modules(ctx)
	if err != nil {
		return err
	}
	for _, want := range []string{".", "nested"} {
		if !slices.Contains(got, want) {
			return fmt.Errorf("discovery missing %q in %v", want, got)
		}
	}
	return nil
}

// Build verifies the fixture's main package compiles to a binary. Build
// returns a lazy directory; the build executes when Glob resolves it.
func (t *Tests) Build(ctx context.Context) error {
	bins, err := t.subject().Build().Glob(ctx, "bin/*")
	if err != nil {
		return err
	}
	if len(bins) == 0 {
		return fmt.Errorf("no binary produced")
	}
	return nil
}

// TestUnit verifies the unit test path runs the fixture's tests.
func (t *Tests) TestUnit(ctx context.Context) error {
	return t.subject().TestUnit(ctx)
}

// TestIntegration verifies the integration selection (-run "Integration")
// runs the fixture's integration-named test.
func (t *Tests) TestIntegration(ctx context.Context) error {
	return t.subject().TestIntegration(ctx)
}

// Tidy verifies the changeset-producing tidy across all discovered modules is
// empty for the already-tidy, dependency-free fixture (also exercises the
// no-go.sum branch and mergeChangesets across root + nested).
func (t *Tests) Tidy(ctx context.Context) error {
	patch, err := t.subject().Tidy().AsPatch().Contents(ctx)
	if err != nil {
		return err
	}
	if patch != "" {
		return fmt.Errorf("fixture not tidy:\n%s", patch)
	}
	return nil
}

// Generate verifies go generate produces no changes for a fixture without
// generate directives (an empty changeset).
func (t *Tests) Generate(ctx context.Context) error {
	patch, err := t.subject().Generate().AsPatch().Contents(ctx)
	if err != nil {
		return err
	}
	if patch != "" {
		return fmt.Errorf("unexpected generate output:\n%s", patch)
	}
	return nil
}

// EnsureGit verifies EnsureGitInit produces a container with a real git
// repository at the working directory.
func (t *Tests) EnsureGit(ctx context.Context) error {
	s := t.subject()
	_, err := s.EnsureGitInit(s.Env()).
		WithExec([]string{"git", "rev-parse", "--git-dir"}).
		Sync(ctx)
	return err
}

// Lint verifies golangci-lint runs clean across the fixture modules.
func (t *Tests) Lint(ctx context.Context) error {
	return t.subject().Lint(ctx)
}

// CheckTidy verifies the dependency-free fixture is reported tidy.
func (t *Tests) CheckTidy(ctx context.Context) error {
	return t.subject().CheckTidy(ctx)
}

// WorkspaceModules verifies discovery finds the workspace's member modules
// (and not a root module, since the workspace root has no go.mod).
func (t *Tests) WorkspaceModules(ctx context.Context) error {
	got, err := t.workspaceSubject().Modules(ctx)
	if err != nil {
		return err
	}
	for _, want := range []string{"alpha", "beta"} {
		if !slices.Contains(got, want) {
			return fmt.Errorf("discovery missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, ".") {
		return fmt.Errorf("discovery found a root module in %v", got)
	}
	return nil
}

// WorkspaceBuild verifies the default "./..." pattern expands across the
// workspace and compiles the alpha main package to a binary.
func (t *Tests) WorkspaceBuild(ctx context.Context) error {
	bins, err := t.workspaceSubject().Build().Glob(ctx, "bin/*")
	if err != nil {
		return err
	}
	if len(bins) == 0 {
		return fmt.Errorf("no binary produced")
	}
	return nil
}

// WorkspaceTestUnit verifies the unit test path runs every workspace
// module's tests via the expanded package patterns.
func (t *Tests) WorkspaceTestUnit(ctx context.Context) error {
	return t.workspaceSubject().TestUnit(ctx)
}

// WorkspaceLint verifies golangci-lint runs clean across the workspace's
// member modules.
func (t *Tests) WorkspaceLint(ctx context.Context) error {
	return t.workspaceSubject().Lint(ctx)
}

// WorkspaceCheckTidy verifies per-module tidy checks work inside a
// workspace (go mod tidy operates on each member module).
func (t *Tests) WorkspaceCheckTidy(ctx context.Context) error {
	return t.workspaceSubject().CheckTidy(ctx)
}

// WorkspaceGenerate verifies go generate resolves the expanded workspace
// patterns and produces no changes for a fixture without directives.
func (t *Tests) WorkspaceGenerate(ctx context.Context) error {
	patch, err := t.workspaceSubject().Generate().AsPatch().Contents(ctx)
	if err != nil {
		return err
	}
	if patch != "" {
		return fmt.Errorf("unexpected generate output:\n%s", patch)
	}
	return nil
}
