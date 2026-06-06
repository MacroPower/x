package main

import (
	"context"
	"fmt"

	"dagger/go/internal/dagger"
)

// integrationTestPattern is the regex matched against test names to separate
// integration tests from unit tests ([Go.TestUnit] skips it, [Go.TestIntegration]
// selects it).
const integrationTestPattern = "Integration"

// ---------------------------------------------------------------------------
// Testing
// ---------------------------------------------------------------------------

// Test runs the Go test suite. Uses only cacheable flags so that Go's
// internal test result cache (GOCACHE) can skip unchanged packages
// across runs via the persistent go-build cache volume.
//
// +cache="session"
func (m *Go) Test(
	ctx context.Context,
	// Only run tests matching this regex.
	// +optional
	run string,
	// Skip tests matching this regex.
	// +optional
	skip string,
	// Abort test run on first failure.
	// +optional
	failfast bool,
	// How many tests to run in parallel. Defaults to the number of CPUs.
	// +optional
	// +default=0
	parallel int,
	// How long before timing out the test run.
	// +optional
	// +default="30m"
	timeout string,
	// Number of times to run each test. Zero uses Go's default (enables
	// test result caching).
	// +optional
	// +default=0
	count int,
	// Packages to test.
	// +optional
	// +default=["./..."]
	pkgs []string,
) error {
	pkgs, err := m.resolvePkgs(ctx, pkgs)
	if err != nil {
		return err
	}

	cmd := []string{"go", "test"}
	if parallel != 0 {
		cmd = append(cmd, fmt.Sprintf("-parallel=%d", parallel))
	}
	cmd = append(cmd, fmt.Sprintf("-timeout=%s", timeout))
	if count > 0 {
		cmd = append(cmd, fmt.Sprintf("-count=%d", count))
	}
	if run != "" {
		cmd = append(cmd, "-run", run)
	}
	if failfast {
		cmd = append(cmd, "-failfast")
	}
	if skip != "" {
		cmd = append(cmd, "-skip", skip)
	}
	_, err = m.Env("").
		WithExec(goCommand(cmd, pkgs, m.Ldflags, m.Values, m.Race)).
		Sync(ctx)
	return err
}

// TestUnit runs only unit tests by skipping tests that match common
// integration test naming patterns. Uses -skip to exclude tests whose
// names match the pattern. If skip is provided, it overrides the
// default pattern. Delegates to [Go.Test].
//
// Not annotated +check: project-specific base containers may need
// extra packages (see aptPackages in [Go.New]). Consuming projects
// should wrap this in their own +check function that constructs [Go]
// with the right base.
//
// +cache="session"
func (m *Go) TestUnit(
	ctx context.Context,
	// Skip tests matching this regex. Overrides the default integration
	// test pattern when provided.
	// +optional
	skip string,
	// Only run tests matching this regex.
	// +optional
	run string,
	// Abort test run on first failure.
	// +optional
	failfast bool,
	// How many tests to run in parallel. Defaults to the number of CPUs.
	// +optional
	// +default=0
	parallel int,
	// How long before timing out the test run.
	// +optional
	// +default="30m"
	timeout string,
	// Number of times to run each test. Zero uses Go's default (enables
	// test result caching).
	// +optional
	// +default=0
	count int,
	// Packages to test.
	// +optional
	// +default=["./..."]
	pkgs []string,
) error {
	if skip == "" {
		skip = integrationTestPattern
	}
	return m.Test(ctx, run, skip, failfast, parallel, timeout, count, pkgs)
}

// TestIntegration runs only integration tests by selecting tests whose
// names match common integration test naming patterns. Uses -run to
// include only tests matching the pattern. If run is provided, it
// overrides the default pattern. Delegates to [Go.Test].
//
// +cache="session"
func (m *Go) TestIntegration(
	ctx context.Context,
	// Only run tests matching this regex. Overrides the default
	// integration test pattern when provided.
	// +optional
	run string,
	// Skip tests matching this regex.
	// +optional
	skip string,
	// Abort test run on first failure.
	// +optional
	failfast bool,
	// How many tests to run in parallel. Defaults to the number of CPUs.
	// +optional
	// +default=0
	parallel int,
	// How long before timing out the test run.
	// +optional
	// +default="30m"
	timeout string,
	// Number of times to run each test. Zero uses Go's default (enables
	// test result caching).
	// +optional
	// +default=0
	count int,
	// Packages to test.
	// +optional
	// +default=["./..."]
	pkgs []string,
) error {
	if run == "" {
		run = integrationTestPattern
	}
	return m.Test(ctx, run, skip, failfast, parallel, timeout, count, pkgs)
}

// TestCoverage runs Go tests with coverage profiling and returns the
// profile file. Runs independently of [Go.Test] because -coverprofile
// disables Go's internal test result caching. Dagger's layer caching
// still shares the base container layers (image, module download) with
// [Go.Test].
func (m *Go) TestCoverage(
	ctx context.Context,
	// Packages to test.
	// +optional
	// +default=["./..."]
	pkgs []string,
) (*dagger.File, error) {
	pkgs, err := m.resolvePkgs(ctx, pkgs)
	if err != nil {
		return nil, err
	}

	cmd := []string{"go", "test", "-race", "-coverprofile=/tmp/coverage.txt"}
	return m.Env("").
		WithEnvVariable("CGO_ENABLED", "1").
		WithExec(append(cmd, pkgs...)).
		File("/tmp/coverage.txt"), nil
}

// ---------------------------------------------------------------------------
// Linting
// ---------------------------------------------------------------------------

// Lint runs golangci-lint on all discovered Go modules. Modules are linted
// in parallel with bounded concurrency.
//
// +check
func (m *Go) Lint(
	ctx context.Context,
	// Include only modules whose directory matches one of these globs.
	// +optional
	include []string,
	// Exclude modules whose directory matches any of these globs.
	// +optional
	exclude []string,
) error {
	mods, err := m.Modules(ctx, include, exclude)
	if err != nil {
		return err
	}

	p := newParallel().withLimit(defaultParallelism)
	for _, mod := range mods {
		p = p.withJob("lint:"+mod, func(ctx context.Context) error {
			return m.LintModule(ctx, mod)
		})
	}
	return p.run(ctx)
}

// LintModule runs golangci-lint on a single module directory.
func (m *Go) LintModule(ctx context.Context,
	// Module directory relative to the source root.
	mod string,
) error {
	cmd := []string{"golangci-lint", "run"}
	if isNestedModule(mod) {
		cmd = append(cmd, "--path-prefix", mod)
	}
	_, err := m.LintBase(mod).
		WithExec(cmd).
		Sync(ctx)
	return err
}

// LintDeadcode reports unreachable functions using the golang.org/x/tools
// deadcode analyzer. The analyzer is installed at call time into the Go build
// environment and run against pkgs. It scans only the root module, like the
// `deadcode` tool itself.
//
// This is an advisory lint: it is intentionally not annotated +check, so it
// does not run under dagger check. Invoke it explicitly (dagger call go
// lint-deadcode) or wrap it in a consuming module.
func (m *Go) LintDeadcode(
	ctx context.Context,
	// Packages to analyze.
	// +optional
	// +default=["./..."]
	pkgs []string,
	// deadcode analyzer version (golang.org/x/tools). Defaults to the
	// version pinned in this module.
	// +optional
	version string,
) error {
	if version == "" {
		version = deadcodeVersion
	}
	pkgs, err := m.resolvePkgs(ctx, pkgs)
	if err != nil {
		return err
	}
	_, err = m.Env("").
		WithExec([]string{"go", "install", "golang.org/x/tools/cmd/deadcode@" + version}).
		WithExec(append([]string{"deadcode"}, pkgs...)).
		Sync(ctx)
	return err
}
