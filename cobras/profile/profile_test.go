package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/cobras/profile"
)

func TestNew(t *testing.T) {
	t.Parallel()

	p := profile.NewConfig()

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

func TestProfile_RegisterFlags(t *testing.T) {
	t.Parallel()

	p := profile.NewConfig()
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

func TestProfile_RegisterFlags_Parsing(t *testing.T) {
	t.Parallel()

	p := profile.NewConfig()
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

func TestRegisterCompletions(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		flag string
	}{
		"mem-profile-rate completions": {
			flag: "mem-profile-rate",
		},
		"block-profile-rate completions": {
			flag: "block-profile-rate",
		},
		"mutex-profile-fraction completions": {
			flag: "mutex-profile-fraction",
		},
	}

	cfg := profile.NewConfig()

	cmd := &cobra.Command{Use: "test"}
	cfg.RegisterFlags(cmd.Flags())

	err := cfg.RegisterCompletions(cmd)
	require.NoError(t, err)

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			completionFn, ok := cmd.GetFlagCompletionFunc(tc.flag)
			require.True(t, ok)

			values, directive := completionFn(cmd, nil, "")
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
			assert.Nil(t, values)
		})
	}
}

func TestProfile_RegisterFlags_Defaults(t *testing.T) {
	t.Parallel()

	p := profile.NewConfig()
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

func TestProfile_RegisterFlags_PresetPathDefaults(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		cpuProfile string
		args       []string
		want       string
		wantDef    string
	}{
		"empty path stays disabled": {
			cpuProfile: "",
			want:       "",
			wantDef:    "",
		},
		"pre-set path becomes the default": {
			cpuProfile: "preset.prof",
			want:       "preset.prof",
			wantDef:    "preset.prof",
		},
		"flag overrides pre-set path": {
			cpuProfile: "preset.prof",
			args:       []string{"--cpu-profile=flag.prof"},
			want:       "flag.prof",
			wantDef:    "preset.prof",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := profile.NewConfig()
			p.CPUProfile = tc.cpuProfile

			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			p.RegisterFlags(flags)

			// DefValue drives the default shown in help text.
			assert.Equal(t, tc.wantDef, flags.Lookup(p.Flags.CPUProfile).DefValue)

			require.NoError(t, flags.Parse(tc.args))
			assert.Equal(t, tc.want, p.CPUProfile)
		})
	}
}

func TestConfig_MustRegisterCompletions(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		registerFlags bool
		panics        bool
	}{
		"flags registered": {
			registerFlags: true,
			panics:        false,
		},
		"flags missing panics": {
			registerFlags: false,
			panics:        true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := profile.NewConfig()
			cmd := &cobra.Command{Use: "test"}

			if tc.registerFlags {
				cfg.RegisterFlags(cmd.Flags())
			}

			registerFn := func() { cfg.MustRegisterCompletions(cmd) }

			if tc.panics {
				require.Panics(t, registerFn)

				return
			}

			require.NotPanics(t, registerFn)

			_, ok := cmd.GetFlagCompletionFunc(cfg.Flags.MemProfileRate)
			assert.True(t, ok)
		})
	}
}

// TestProfiler_Run is not parallel: profiling mutates process-wide runtime
// state (sampling rates and the single global CPU profiler).
func TestProfiler_Run(t *testing.T) { //nolint:paralleltest // See above.
	errRun := errors.New("run")

	tcs := map[string]struct {
		cpuProfileDir string
		fnErr         error
		wantFnCalled  bool
		wantProfiles  bool
		err           error
	}{
		"profiles written on success": {
			wantFnCalled: true,
			wantProfiles: true,
		},
		"fn error joined with profiles still written": {
			fnErr:        errRun,
			wantFnCalled: true,
			wantProfiles: true,
			err:          errRun,
		},
		"start error returned without invoking fn": {
			cpuProfileDir: "missing-dir",
			wantFnCalled:  false,
			wantProfiles:  false,
		},
	}

	for name, tc := range tcs { //nolint:paralleltest // See above.
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			cfg := profile.NewConfig()
			cfg.CPUProfile = filepath.Join(dir, tc.cpuProfileDir, "cpu.prof")
			cfg.HeapProfile = filepath.Join(dir, "heap.prof")
			cfg.MemProfileRate = 524288

			p := cfg.NewProfiler()

			fnCalled := false
			err := p.Run(func() error {
				fnCalled = true

				return tc.fnErr
			})

			assert.Equal(t, tc.wantFnCalled, fnCalled)

			switch {
			case tc.err != nil:
				require.ErrorIs(t, err, tc.err)
			case tc.wantFnCalled:
				require.NoError(t, err)
			default:
				require.Error(t, err)
			}

			if tc.wantProfiles {
				assert.FileExists(t, cfg.CPUProfile)
				assert.FileExists(t, cfg.HeapProfile)
			} else {
				assert.NoFileExists(t, cfg.CPUProfile)
			}
		})
	}
}

// TestProfiler_Stop_Idempotent is not parallel: profiling mutates
// process-wide runtime state (sampling rates and the single global CPU
// profiler).
func TestProfiler_Stop_Idempotent(t *testing.T) { //nolint:paralleltest // See above.
	dir := t.TempDir()

	cfg := profile.NewConfig()
	cfg.CPUProfile = filepath.Join(dir, "cpu.prof")
	cfg.HeapProfile = filepath.Join(dir, "heap.prof")
	cfg.MemProfileRate = 524288

	p := cfg.NewProfiler()

	require.NoError(t, p.Start())
	require.NoError(t, p.Stop())

	assert.FileExists(t, cfg.CPUProfile)
	assert.FileExists(t, cfg.HeapProfile)

	// A second Stop returns nil and does not rewrite snapshots.
	require.NoError(t, os.Remove(cfg.HeapProfile))
	require.NoError(t, p.Stop())
	assert.NoFileExists(t, cfg.HeapProfile)
}

// TestProfiler_Restart is not parallel: profiling mutates process-wide
// runtime state (sampling rates and the single global CPU profiler).
func TestProfiler_Restart(t *testing.T) { //nolint:paralleltest // See above.
	dir := t.TempDir()

	cfg := profile.NewConfig()
	cfg.CPUProfile = filepath.Join(dir, "cpu.prof")
	cfg.HeapProfile = filepath.Join(dir, "heap.prof")
	cfg.MemProfileRate = 524288

	p := cfg.NewProfiler()

	require.NoError(t, p.Start())
	require.NoError(t, p.Stop())

	// A new Start arms Stop again: the second session must release the
	// global CPU profiler and write its profiles, not silently no-op.
	require.NoError(t, os.Remove(cfg.CPUProfile))
	require.NoError(t, os.Remove(cfg.HeapProfile))

	require.NoError(t, p.Start())
	require.NoError(t, p.Stop())

	assert.FileExists(t, cfg.CPUProfile)
	assert.FileExists(t, cfg.HeapProfile)

	stat, err := os.Stat(cfg.CPUProfile)
	require.NoError(t, err)
	assert.Positive(t, stat.Size(), "second session must write a non-empty CPU profile")
}
