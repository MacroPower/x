package profile

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

// Profiler controls the lifecycle of runtime profiling sessions.
//
// Call [Profiler.Start] to begin profiling and [Profiler.Stop] to write all
// enabled profiles.
//
// Create instances with [Config.NewProfiler].
type Profiler struct {
	cpuFile *os.File
	Config
}

// Start configures runtime profiling rates and starts CPU profiling if enabled.
// Call [Profiler.Stop] when profiling is complete to write snapshot profiles.
func (c *Profiler) Start() error {
	// Configure profiling rates.
	runtime.MemProfileRate = c.MemProfileRate
	runtime.SetBlockProfileRate(c.BlockProfileRate)
	runtime.SetMutexProfileFraction(c.MutexProfileFraction)

	// Start CPU profiling if enabled.
	if c.CPUProfile != "" {
		f, err := os.Create(c.CPUProfile) //nolint:gosec // Profile path from CLI flag is expected.
		if err != nil {
			return fmt.Errorf("creating CPU profile: %w", err)
		}

		c.cpuFile = f

		err = pprof.StartCPUProfile(f)
		if err != nil {
			must(c.cpuFile.Close())

			c.cpuFile = nil

			return fmt.Errorf("starting CPU profile: %w", err)
		}
	}

	return nil
}

// Stop stops CPU profiling and writes all enabled snapshot profiles.
func (c *Profiler) Stop() error {
	// Stop CPU profiling.
	if c.cpuFile != nil {
		pprof.StopCPUProfile()

		err := c.cpuFile.Close()
		if err != nil {
			return fmt.Errorf("closing CPU profile: %w", err)
		}
	}

	return c.writeSnapshots()
}

// writeSnapshots writes all enabled snapshot profiles (heap, allocs, goroutine,
// etc.).
func (c *Profiler) writeSnapshots() error {
	profiles := []struct {
		name string
		path string
	}{
		{"heap", c.HeapProfile},
		{"allocs", c.AllocsProfile},
		{"goroutine", c.GoroutineProfile},
		{"threadcreate", c.ThreadcreateProfile},
		{"block", c.BlockProfile},
		{"mutex", c.MutexProfile},
	}

	for _, p := range profiles {
		if p.path == "" {
			continue
		}

		err := c.writeProfile(p.name, p.path)
		if err != nil {
			return fmt.Errorf("write %s profile: %w", p.name, err)
		}
	}

	return nil
}

// writeProfile writes a named pprof profile to the given file path.
func (c *Profiler) writeProfile(name, path string) error {
	f, err := os.Create(path) //nolint:gosec // Profile path from CLI flag is expected.
	if err != nil {
		return fmt.Errorf("create %s profile: %w", name, err)
	}

	prof := pprof.Lookup(name)
	if prof == nil {
		must(f.Close())

		return fmt.Errorf("unknown profile: %s", name)
	}

	err = prof.WriteTo(f, 0)
	if err != nil {
		must(f.Close())

		return fmt.Errorf("write %s profile: %w", name, err)
	}

	err = f.Close()
	if err != nil {
		return fmt.Errorf("write %s profile: %w", name, err)
	}

	return nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
