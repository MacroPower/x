// Package profile adds runtime profiling capabilities to CLI applications.
//
// It supports CPU, heap, allocs, goroutine, threadcreate, block, and mutex
// profiles through command-line flags. Use [Config.RegisterFlags] to add CLI
// flags and [Config.MustRegisterCompletions] to wire up shell completions.
//
// Typical usage creates a [Config], registers flags, then creates a [Profiler]
// to wrap command execution:
//
//	cfg := profile.NewConfig()
//	p := cfg.NewProfiler()
//
//	rootCmd := &cobra.Command{
//	    PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
//	        return p.Start()
//	    },
//	}
//
//	cfg.RegisterFlags(rootCmd.PersistentFlags())
//	cfg.MustRegisterCompletions(rootCmd)
//	err := fang.Execute(ctx, rootCmd, ...)
//	err = errors.Join(err, p.Stop())
//
// When the configuration is already populated before execution begins,
// [Profiler.Run] wraps the start/stop lifecycle around a single function.
//
// Users can then enable profiling via flags like --cpu-profile=cpu.prof.
package profile
