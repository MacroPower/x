// [Profiler] adds runtime profiling capabilities to CLI applications.
//
// It supports CPU, heap, allocs, goroutine, threadcreate, block, and mutex
// profiles through command-line flags.
//
// Typical usage wraps command execution with profiler lifecycle methods:
//
//	profiler := profiler.New()
//
//	rootCmd := &cobra.Command{
//	    PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
//	        return profiler.Start()
//	    },
//	}
//
//	profiler.RegisterFlags(rootCmd.PersistentFlags())
//	err := fang.Execute(ctx, rootCmd, ...)
//	stopErr := profiler.Stop()
//
// Users can then enable profiling via flags like --cpu-profile=cpu.prof.
package profiler
