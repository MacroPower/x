package profile

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flags holds CLI flag names for profiling configuration, allowing callers to
// customize flag names while keeping sensible defaults via [NewConfig].
type Flags struct {
	// Profile output path flag names.
	CPUProfile          string
	HeapProfile         string
	AllocsProfile       string
	GoroutineProfile    string
	ThreadcreateProfile string
	BlockProfile        string
	MutexProfile        string

	// Rate configuration flag names.
	MemProfileRate       string
	BlockProfileRate     string
	MutexProfileFraction string
}

// NewConfig creates a new [Config] embedding these flag names.
func (f Flags) NewConfig() *Config {
	return &Config{
		Flags: f,
	}
}

// Config holds profiling configuration for CLI applications, including output
// paths and sampling rates. A zero-value Config has all profiles disabled.
//
// Create instances with [NewConfig] and register CLI flags with
// [Config.RegisterFlags]. Use [Config.NewProfiler] to create a [Profiler]
// that executes the profiling.
type Config struct {
	Flags Flags

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

// NewConfig creates a new [Config] with default flag names and all profiles
// disabled. Use [Config.RegisterFlags] to add CLI flags, or set profile paths
// directly.
func NewConfig() *Config {
	f := Flags{
		CPUProfile:           "cpu-profile",
		HeapProfile:          "heap-profile",
		AllocsProfile:        "allocs-profile",
		GoroutineProfile:     "goroutine-profile",
		ThreadcreateProfile:  "threadcreate-profile",
		BlockProfile:         "block-profile",
		MutexProfile:         "mutex-profile",
		MemProfileRate:       "mem-profile-rate",
		BlockProfileRate:     "block-profile-rate",
		MutexProfileFraction: "mutex-profile-fraction",
	}

	return f.NewConfig()
}

// RegisterFlags adds profiling flags to the given [*pflag.FlagSet].
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	// Profile output paths.
	flags.StringVar(&c.CPUProfile, c.Flags.CPUProfile, "", "write CPU profile to file")
	flags.StringVar(&c.HeapProfile, c.Flags.HeapProfile, "", "write heap profile to file")
	flags.StringVar(&c.AllocsProfile, c.Flags.AllocsProfile, "", "write allocs profile to file")
	flags.StringVar(&c.GoroutineProfile, c.Flags.GoroutineProfile, "", "write goroutine profile to file")
	flags.StringVar(&c.ThreadcreateProfile, c.Flags.ThreadcreateProfile, "", "write threadcreate profile to file")
	flags.StringVar(&c.BlockProfile, c.Flags.BlockProfile, "", "write block profile to file")
	flags.StringVar(&c.MutexProfile, c.Flags.MutexProfile, "", "write mutex profile to file")

	// Rate configuration.
	flags.IntVar(&c.MemProfileRate, c.Flags.MemProfileRate, 524288, "memory profile rate (bytes per sample)")
	flags.IntVar(&c.BlockProfileRate, c.Flags.BlockProfileRate, 1, "block profile rate (nanoseconds)")
	flags.IntVar(&c.MutexProfileFraction, c.Flags.MutexProfileFraction, 1, "mutex profile fraction (1/N sampling)")
}

// RegisterCompletions registers shell completions for profile flags on cmd.
// Integer flags disable file completion; path flags use default file completion.
func (c *Config) RegisterCompletions(cmd *cobra.Command) error {
	noFileComp := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	err := cmd.RegisterFlagCompletionFunc(c.Flags.MemProfileRate, noFileComp)
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.MemProfileRate, err)
	}

	err = cmd.RegisterFlagCompletionFunc(c.Flags.BlockProfileRate, noFileComp)
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.BlockProfileRate, err)
	}

	err = cmd.RegisterFlagCompletionFunc(c.Flags.MutexProfileFraction, noFileComp)
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.MutexProfileFraction, err)
	}

	return nil
}

// NewProfiler creates a new [Profiler] using this [Config].
func (c *Config) NewProfiler() *Profiler {
	return &Profiler{
		Config: *c,
	}
}
