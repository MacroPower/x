package profiler_test

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/profiler"
)

func TestNew(t *testing.T) {
	t.Parallel()

	p := profiler.New()

	// All profile paths should be empty (disabled).
	assert.Empty(t, p.CPUProfile)
	assert.Empty(t, p.HeapProfile)
	assert.Empty(t, p.AllocsProfile)
	assert.Empty(t, p.GoroutineProfile)
	assert.Empty(t, p.ThreadcreateProfile)
	assert.Empty(t, p.BlockProfile)
	assert.Empty(t, p.MutexProfile)

	// Rate fields should be zero.
	assert.Zero(t, p.MemProfileRate)
	assert.Zero(t, p.BlockProfileRate)
	assert.Zero(t, p.MutexProfileFraction)
}

func TestProfiler_RegisterFlags(t *testing.T) {
	t.Parallel()

	p := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	p.RegisterFlags(flags)

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

	p := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	p.RegisterFlags(flags)

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
	assert.Equal(t, "cpu.prof", p.CPUProfile)
	assert.Equal(t, "heap.prof", p.HeapProfile)
	assert.Equal(t, "allocs.prof", p.AllocsProfile)
	assert.Equal(t, "goroutine.prof", p.GoroutineProfile)
	assert.Equal(t, "threadcreate.prof", p.ThreadcreateProfile)
	assert.Equal(t, "block.prof", p.BlockProfile)
	assert.Equal(t, "mutex.prof", p.MutexProfile)

	// Verify rate values are bound.
	assert.Equal(t, 1024, p.MemProfileRate)
	assert.Equal(t, 100, p.BlockProfileRate)
	assert.Equal(t, 10, p.MutexProfileFraction)
}

func TestProfiler_RegisterFlags_Defaults(t *testing.T) {
	t.Parallel()

	p := profiler.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	p.RegisterFlags(flags)

	// Parse with no flags to get defaults.
	err := flags.Parse([]string{})
	require.NoError(t, err)

	// Verify default rate values from profile.go.
	assert.Equal(t, 524288, p.MemProfileRate)
	assert.Equal(t, 1, p.BlockProfileRate)
	assert.Equal(t, 1, p.MutexProfileFraction)
}
