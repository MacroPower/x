// Package profile adds runtime profiling capabilities to CLI applications.
//
// It supports CPU, heap, allocs, goroutine, threadcreate, block, and mutex
// profiles through command-line flags. Use [Config.RegisterFlags] to add CLI
// flags and [Config.RegisterCompletions] to wire up shell completions.
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
//	cfg.RegisterCompletions(rootCmd)
//	err := fang.Execute(ctx, rootCmd, ...)
//	stopErr := p.Stop()
//
// Users can then enable profiling via flags like --cpu-profile=cpu.prof.
package profile
