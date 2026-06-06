package main

import (
	"context"
	"path/filepath"
	"time"

	"dagger/go/internal/dagger"
)

// BenchmarkSummary measures the wall-clock time of key pipeline stages and
// returns a human-readable table.
// When parallel is true, stages run concurrently to measure the real-world
// wall-clock time of the full pipeline; the total row then shows overall
// elapsed time rather than the sum of individual stages.
//
// +cache="session"
func (m *Go) BenchmarkSummary(
	ctx context.Context,
	// Run stages concurrently to measure full-pipeline wall-clock time.
	// +default=false
	parallel bool,
) (string, error) {
	suite, err := m.benchSuite(ctx)
	if err != nil {
		return "", err
	}
	return suite.Summary(ctx, dagger.BenchSummaryOpts{Parallel: parallel})
}

// benchSuite builds the Go toolchain's benchmark stages, delegating timing and
// reporting to the shared [Bench] module. Each stage is a cache-busted
// container whose evaluation is the work to time. The lint stage runs each
// discovered module sequentially in one container, mirroring [Go.Lint]'s
// per-module invocations as a single timed unit.
func (m *Go) benchSuite(ctx context.Context) (*dagger.Bench, error) {
	pkgs, err := m.resolvePkgs(ctx, []string{defaultPkgs})
	if err != nil {
		return nil, err
	}
	mods, err := m.Modules(ctx, nil, nil)
	if err != nil {
		return nil, err
	}

	lint := m.CacheBust(m.LintBase(""))
	for _, mod := range mods {
		cmd := []string{"golangci-lint", "run"}
		if isNestedModule(mod) {
			cmd = append(cmd, "--path-prefix", mod)
		}
		lint = lint.WithWorkdir(filepath.Join("/src", mod)).WithExec(cmd)
	}

	return dag.Bench().
		WithStage("env", m.CacheBust(m.Env(""))).
		WithStage("lint", lint).
		WithStage("test", m.CacheBust(m.Env("")).
			WithExec(append([]string{"go", "test"}, pkgs...))), nil
}

// CacheBust returns a container with a unique cache-busting environment
// variable that forces Dagger to re-evaluate the pipeline instead of returning
// cached results. Apply it to a stage's base before its work so the work
// re-runs each benchmark.
func (m *Go) CacheBust(
	// Container to bust the cache for.
	ctr *dagger.Container,
) *dagger.Container {
	return ctr.WithEnvVariable("_BENCH_TS", time.Now().String())
}
