package profiler

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"

	"github.com/spf13/pflag"
)

// Profiler manages runtime profiling for CLI applications.
//
// Create instances with [NewProfiler].
type Profiler struct {
	// Internal state.
	cpuFile *os.File

	// Output paths (empty = disabled).
	CPUProfile          string
	HeapProfile         string
	AllocsProfile       string
	GoroutineProfile    string
	ThreadcreateProfile string
	BlockProfile        string
	MutexProfile        string

	// Rate configuration.
	MemProfileRate       int
	BlockProfileRate     int
	MutexProfileFraction int
}

// New creates a new [Profiler] with all profiles disabled.
// Use [Profiler.RegisterFlags] to add CLI flags, or set profile paths directly.
func New() Profiler {
	return Profiler{}
}

// RegisterFlags adds profiling flags to the given [*pflag.FlagSet].
func (c *Profiler) RegisterFlags(flags *pflag.FlagSet) {
	// Profile output paths.
	flags.StringVar(&c.CPUProfile, "cpu-profile", "", "write CPU profile to file")
	flags.StringVar(&c.HeapProfile, "heap-profile", "", "write heap profile to file")
	flags.StringVar(&c.AllocsProfile, "allocs-profile", "", "write allocs profile to file")
	flags.StringVar(&c.GoroutineProfile, "goroutine-profile", "", "write goroutine profile to file")
	flags.StringVar(&c.ThreadcreateProfile, "threadcreate-profile", "", "write threadcreate profile to file")
	flags.StringVar(&c.BlockProfile, "block-profile", "", "write block profile to file")
	flags.StringVar(&c.MutexProfile, "mutex-profile", "", "write mutex profile to file")

	// Rate configuration.
	flags.IntVar(&c.MemProfileRate, "mem-profile-rate", 524288, "memory profile rate (bytes per sample)")
	flags.IntVar(&c.BlockProfileRate, "block-profile-rate", 1, "block profile rate (nanoseconds)")
	flags.IntVar(&c.MutexProfileFraction, "mutex-profile-fraction", 1, "mutex profile fraction (1/N sampling)")
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
