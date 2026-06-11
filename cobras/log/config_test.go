package log_test

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/cobras/log"
)

func TestConfig_RegisterFlags_Defaults(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		level      string
		format     string
		args       []string
		wantLevel  string
		wantFormat string
	}{
		"empty fields fall back to info and text": {
			level:      "",
			format:     "",
			wantLevel:  "info",
			wantFormat: "text",
		},
		"pre-set values become the defaults": {
			level:      "warn",
			format:     "json",
			wantLevel:  "warn",
			wantFormat: "json",
		},
		"flags override pre-set values": {
			level:      "warn",
			format:     "json",
			args:       []string{"--log-level=debug", "--log-format=logfmt"},
			wantLevel:  "debug",
			wantFormat: "logfmt",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := log.NewConfig()
			cfg.Level = tc.level
			cfg.Format = tc.format

			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			cfg.RegisterFlags(flags)

			// DefValue drives the default shown in help text.
			wantLevelDef := tc.level
			if wantLevelDef == "" {
				wantLevelDef = "info"
			}

			wantFormatDef := tc.format
			if wantFormatDef == "" {
				wantFormatDef = "text"
			}

			assert.Equal(t, wantLevelDef, flags.Lookup(cfg.Flags.Level).DefValue)
			assert.Equal(t, wantFormatDef, flags.Lookup(cfg.Flags.Format).DefValue)

			require.NoError(t, flags.Parse(tc.args))

			assert.Equal(t, tc.wantLevel, cfg.Level)
			assert.Equal(t, tc.wantFormat, cfg.Format)
		})
	}
}

func TestConfig_ParsedLevel(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		level string
		want  log.Level
		err   error
	}{
		"valid level": {
			level: "warn",
			want:  log.LevelWarn,
		},
		"case insensitive": {
			level: "DEBUG",
			want:  log.LevelDebug,
		},
		"unknown level": {
			level: "nope",
			err:   log.ErrUnknownLogLevel,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := log.NewConfig()
			cfg.Level = tc.level

			got, err := cfg.ParsedLevel()
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConfig_ParsedFormat(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		format string
		want   log.Format
		err    error
	}{
		"valid format": {
			format: "json",
			want:   log.FormatJSON,
		},
		"case insensitive": {
			format: "TEXT",
			want:   log.FormatText,
		},
		"unknown format": {
			format: "nope",
			err:    log.ErrUnknownLogFormat,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := log.NewConfig()
			cfg.Format = tc.format

			got, err := cfg.ParsedFormat()
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
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

			cfg := log.NewConfig()
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

			_, ok := cmd.GetFlagCompletionFunc(cfg.Flags.Level)
			assert.True(t, ok)
		})
	}
}
