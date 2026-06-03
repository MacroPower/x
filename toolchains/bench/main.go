// Bench times the wall-clock duration of named pipeline stages, where each
// stage is a container whose evaluation is the work to measure. Stages are
// accumulated with WithStage and run either sequentially (isolated timings) or
// concurrently (real-world full-pipeline wall-clock).
//
// A stage is a container rather than a callback so the harness can be shared
// across projects: containers cross Dagger module boundaries, closures do not.
// Cache-busting belongs in the stage container (applied before the work, e.g.
// via the go toolchain's CacheBust) -- this harness only evaluates and times.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger/bench/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// Bench accumulates named container stages to time. Create instances with [New]
// and add stages with [Bench.WithStage].
type Bench struct {
	// Accumulated stages to time, in insertion order.
	Stages []*stage // +private
}

// stage pairs a stage name with the container whose evaluation is the work to
// time. Both fields stay +private so the type never crosses the module boundary.
type stage struct {
	// Stage name (e.g. "lint", "test").
	Name string // +private
	// Container whose evaluation is the work to time.
	Ctr *dagger.Container // +private
}

// New creates an empty [Bench].
func New() *Bench {
	return &Bench{}
}

// Result holds the timing for a single stage.
type Result struct {
	// Stage name.
	Name string
	// Wall-clock duration in seconds.
	DurationSecs float64
	// Whether the stage completed successfully.
	Ok bool
	// Error message if the stage failed.
	Error string
}

// WithStage adds a named stage to time. The container should be fully built up
// to the point that evaluating it performs the work to measure (including any
// cache-busting); [Bench.Run] calls Sync on it and records the duration.
func (b *Bench) WithStage(
	// Stage name (e.g. "lint", "test").
	name string,
	// Container whose evaluation is the work to time.
	ctr *dagger.Container,
) *Bench {
	return &Bench{Stages: append(b.Stages, &stage{Name: name, Ctr: ctr})}
}

// Run evaluates each stage and returns its timing. When parallel is false,
// stages run one at a time for isolated timings; when true, concurrently to
// measure full-pipeline wall-clock time. A failing stage is recorded (Ok=false)
// rather than aborting the run.
//
// +cache="session"
func (b *Bench) Run(
	ctx context.Context,
	// Run stages concurrently instead of sequentially.
	// +default=false
	parallel bool,
) ([]*Result, error) {
	if parallel {
		return b.runParallel(ctx), nil
	}
	return b.runSequential(ctx), nil
}

// Summary runs the stages and returns an aligned text table, a convenience
// wrapper around [Bench.Run] for CLI use without jq post-processing.
//
// +cache="session"
func (b *Bench) Summary(
	ctx context.Context,
	// Run stages concurrently instead of sequentially.
	// +default=false
	parallel bool,
) (string, error) {
	results, err := b.Run(ctx, parallel)
	if err != nil {
		return "", err
	}
	return formatTable(results, parallel), nil
}

func (b *Bench) runSequential(ctx context.Context) []*Result {
	results := make([]*Result, 0, len(b.Stages))
	for _, s := range b.Stages {
		results = append(results, timeStage(ctx, s.Name, s.Ctr))
	}
	return results
}

func (b *Bench) runParallel(ctx context.Context) []*Result {
	results := make([]*Result, len(b.Stages))
	g, gCtx := errgroup.WithContext(ctx)
	for i, s := range b.Stages {
		g.Go(func() error {
			results[i] = timeStage(gCtx, s.Name, s.Ctr)
			return nil // always collect results; don't abort on stage failure
		})
	}
	_ = g.Wait()
	return results
}

// timeStage syncs a single stage container and records its wall-clock duration.
func timeStage(ctx context.Context, name string, ctr *dagger.Container) *Result {
	start := time.Now()
	_, err := ctr.Sync(ctx)
	elapsed := time.Since(start).Seconds()

	r := &Result{Name: name, DurationSecs: elapsed, Ok: err == nil}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// formatTable renders results as an aligned text table. In parallel mode it
// reports both the wall-clock (slowest stage) and the sum of stage durations.
func formatTable(results []*Result, parallel bool) string {
	var b strings.Builder

	mode := "sequential"
	if parallel {
		mode = "parallel"
	}
	fmt.Fprintf(&b, "Benchmark (%s)\n", mode)
	fmt.Fprintf(&b, "%-20s %10s %8s\n", "STAGE", "DURATION", "STATUS")
	fmt.Fprintf(&b, "%-20s %10s %8s\n", "-----", "--------", "------")

	var total float64
	var maxDur float64
	allOk := true
	for _, r := range results {
		status := "ok"
		if !r.Ok {
			status = "FAIL"
			allOk = false
		}
		fmt.Fprintf(&b, "%-20s %9.1fs %8s\n", r.Name, r.DurationSecs, status)
		total += r.DurationSecs
		if r.DurationSecs > maxDur {
			maxDur = r.DurationSecs
		}
	}

	fmt.Fprintf(&b, "%-20s %10s %8s\n", "-----", "--------", "------")

	totalStatus := "ok"
	if !allOk {
		totalStatus = "FAIL"
	}
	if parallel {
		fmt.Fprintf(&b, "%-20s %9.1fs %8s\n", "WALL-CLOCK", maxDur, totalStatus)
		fmt.Fprintf(&b, "%-20s %9.1fs\n", "SUM", total)
	} else {
		fmt.Fprintf(&b, "%-20s %9.1fs %8s\n", "TOTAL", total, totalStatus)
	}

	return b.String()
}
