package profiler_test

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/macropower/x/profiler"
)

func TestNew(t *testing.T) {
	t.Parallel()

	profiler := profiler.New()

	// All profile paths should be empty (disabled).
	assert.Empty(t, profiler.CPUProfile)
	assert.Empty(t, profiler.HeapProfile)
	assert.Empty(t, profiler.AllocsProfile)
	assert.Empty(t, profiler.GoroutineProfile)
	assert.Empty(t, profiler.ThreadcreateProfile)
	assert.Empty(t, profiler.BlockProfile)
	assert.Empty(t, profiler.MutexProfile)

	// Rate fields should be zero.
	assert.Zero(t, profiler.MemProfileRate)
	assert.Zero(t, profiler.BlockProfileRate)
	assert.Zero(t, profiler.MutexProfileFraction)
}

func TestProfiler_RegisterFlags(t *testing.T) {
	t.Parallel()

	profiler := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	profiler.RegisterFlags(flags)

	// Verify all flags are registered.
	wantFlags := []string{
		"cpu-profile",
		"heap-profile",
		"allocs-profile",
		"goroutine-profile",
		"threadcreate-profile",
		"block-profile",
		"mutex-profile",
		"mem-profile-rate",
		"block-profile-rate",
		"mutex-profile-fraction",
	}

	for _, name := range wantFlags {
		flag := flags.Lookup(name)
		require.NotNil(t, flag, "flag %s should be registered", name)
	}
}

func TestProfiler_RegisterFlags_Parsing(t *testing.T) {
	t.Parallel()

	profiler := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	profiler.RegisterFlags(flags)

	err := flags.Parse([]string{
		"--cpu-profile=cpu.prof",
		"--heap-profile=heap.prof",
		"--allocs-profile=allocs.prof",
		"--goroutine-profile=goroutine.prof",
		"--threadcreate-profile=threadcreate.prof",
		"--block-profile=block.prof",
		"--mutex-profile=mutex.prof",
		"--mem-profile-rate=1024",
		"--block-profile-rate=100",
		"--mutex-profile-fraction=10",
	})
	require.NoError(t, err)

	// Verify profile paths are bound.
	assert.Equal(t, "cpu.prof", profiler.CPUProfile)
	assert.Equal(t, "heap.prof", profiler.HeapProfile)
	assert.Equal(t, "allocs.prof", profiler.AllocsProfile)
	assert.Equal(t, "goroutine.prof", profiler.GoroutineProfile)
	assert.Equal(t, "threadcreate.prof", profiler.ThreadcreateProfile)
	assert.Equal(t, "block.prof", profiler.BlockProfile)
	assert.Equal(t, "mutex.prof", profiler.MutexProfile)

	// Verify rate values are bound.
	assert.Equal(t, 1024, profiler.MemProfileRate)
	assert.Equal(t, 100, profiler.BlockProfileRate)
	assert.Equal(t, 10, profiler.MutexProfileFraction)
}

func TestProfiler_RegisterFlags_Defaults(t *testing.T) {
	t.Parallel()

	profiler := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	profiler.RegisterFlags(flags)

	// Parse with no flags to get defaults.
	err := flags.Parse([]string{})
	require.NoError(t, err)

	// Verify default rate values from profile.go.
	assert.Equal(t, 524288, profiler.MemProfileRate)
	assert.Equal(t, 1, profiler.BlockProfileRate)
	assert.Equal(t, 1, profiler.MutexProfileFraction)
}
