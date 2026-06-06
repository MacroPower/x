// Package main implements tests for the bench toolchain module. They prove the
// core contract: containers handed to WithStage actually cross the module
// boundary, get evaluated and timed, results map 1:1 in order, and a failing
// stage is recorded rather than aborting the run.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger/tests/internal/dagger"
)

// Tests exercises the bench toolchain.
type Tests struct{}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"results-and-order", t.ResultsAndOrder},
		{"failure-captured", t.FailureCaptured},
		{"summary-table", t.SummaryTable},
		{"timing", t.Timing},
		{"parallel-wall-clock", t.ParallelWallClock},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

func base() *dagger.Container {
	return dag.Container().From("public.ecr.aws/docker/library/alpine:3.20")
}

// bust forces re-execution so timings reflect real work, not cache hits.
func bust(ctr *dagger.Container) *dagger.Container {
	return ctr.WithEnvVariable("BENCH_TEST_BUST", fmt.Sprintf("%d", time.Now().UnixNano()))
}

// ResultsAndOrder verifies stages map to results 1:1, in order, with Ok set.
func (t *Tests) ResultsAndOrder(ctx context.Context) error {
	results, err := dag.Bench().
		WithStage("first", bust(base()).WithExec([]string{"true"})).
		WithStage("second", bust(base()).WithExec([]string{"true"})).
		Run(ctx)
	if err != nil {
		return err
	}
	if len(results) != 2 {
		return fmt.Errorf("want 2 results, got %d", len(results))
	}
	wantNames := []string{"first", "second"}
	for i := range results {
		name, err := results[i].Name(ctx)
		if err != nil {
			return err
		}
		if name != wantNames[i] {
			return fmt.Errorf("result %d name = %q, want %q", i, name, wantNames[i])
		}
		ok, err := results[i].Ok(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("stage %q: want Ok=true", name)
		}
	}
	return nil
}

// FailureCaptured verifies a failing stage is recorded (Ok=false, Error set)
// rather than aborting the run, and that a later stage still runs.
func (t *Tests) FailureCaptured(ctx context.Context) error {
	results, err := dag.Bench().
		WithStage("bad", bust(base()).WithExec([]string{"sh", "-c", "exit 3"})).
		WithStage("good", bust(base()).WithExec([]string{"true"})).
		Run(ctx)
	if err != nil {
		return err
	}
	if len(results) != 2 {
		return fmt.Errorf("want 2 results, got %d", len(results))
	}
	badOk, err := results[0].Ok(ctx)
	if err != nil {
		return err
	}
	if badOk {
		return fmt.Errorf("stage \"bad\": want Ok=false")
	}
	badErr, err := results[0].Error(ctx)
	if err != nil {
		return err
	}
	if badErr == "" {
		return fmt.Errorf("stage \"bad\": want non-empty Error")
	}
	goodOk, err := results[1].Ok(ctx)
	if err != nil {
		return err
	}
	if !goodOk {
		return fmt.Errorf("stage \"good\": want Ok=true (run must not abort on prior failure)")
	}
	return nil
}

// SummaryTable verifies the formatted table includes stage names and a total.
func (t *Tests) SummaryTable(ctx context.Context) error {
	out, err := dag.Bench().
		WithStage("alpha", bust(base()).WithExec([]string{"true"})).
		Summary(ctx)
	if err != nil {
		return err
	}
	for _, want := range []string{"alpha", "TOTAL", "STAGE", "DURATION"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	return nil
}

// Timing verifies a sleeping stage is timed as real wall-clock time, confirming
// the container actually executes across the module boundary.
func (t *Tests) Timing(ctx context.Context) error {
	results, err := dag.Bench().
		WithStage("sleep", bust(base()).WithExec([]string{"sleep", "2"})).
		Run(ctx)
	if err != nil {
		return err
	}
	dur, err := results[0].DurationSecs(ctx)
	if err != nil {
		return err
	}
	if dur < 1.5 {
		return fmt.Errorf("sleep stage timed at %.2fs, want >= 1.5s (did it actually execute?)", dur)
	}
	return nil
}

// ParallelWallClock exercises the parallel run path and the WALL-CLOCK/SUM
// branch of the summary table, neither of which the sequential tests cover. Two
// sleeping stages both execute and are timed, and the parallel summary reports
// the wall-clock and sum rows.
func (t *Tests) ParallelWallClock(ctx context.Context) error {
	b := dag.Bench().
		WithStage("sleep-a", bust(base()).WithExec([]string{"sleep", "2"})).
		WithStage("sleep-b", bust(base()).WithExec([]string{"sleep", "2"}))

	results, err := b.Run(ctx, dagger.BenchRunOpts{Parallel: true})
	if err != nil {
		return err
	}
	if len(results) != 2 {
		return fmt.Errorf("want 2 results, got %d", len(results))
	}
	for i := range results {
		ok, err := results[i].Ok(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("parallel stage %d: want Ok=true", i)
		}
		dur, err := results[i].DurationSecs(ctx)
		if err != nil {
			return err
		}
		if dur < 1.5 {
			return fmt.Errorf("parallel stage %d timed at %.2fs, want >= 1.5s", i, dur)
		}
	}

	out, err := b.Summary(ctx, dagger.BenchSummaryOpts{Parallel: true})
	if err != nil {
		return err
	}
	for _, want := range []string{"WALL-CLOCK", "SUM", "parallel"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("parallel summary missing %q:\n%s", want, out)
		}
	}
	return nil
}
