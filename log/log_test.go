package log_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/log"
)

func TestGetLevel(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input       string
		expected    log.Level
		expectError bool
	}{
		"error level": {
			input:       "error",
			expected:    log.LevelError,
			expectError: false,
		},
		"warn level": {
			input:       "warn",
			expected:    log.LevelWarn,
			expectError: false,
		},
		"warning level": {
			input:       "warning",
			expected:    log.LevelWarn,
			expectError: false,
		},
		"info level": {
			input:       "info",
			expected:    log.LevelInfo,
			expectError: false,
		},
		"debug level": {
			input:       "debug",
			expected:    log.LevelDebug,
			expectError: false,
		},
		"case insensitive": {
			input:       "INFO",
			expected:    log.LevelInfo,
			expectError: false,
		},
		"unknown level": {
			input:       "unknown",
			expected:    "",
			expectError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lvl, err := log.ParseLevel(tc.input)
			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, log.ErrUnknownLogLevel)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, lvl)
			}
		})
	}
}

func TestGetFormat(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input       string
		expected    log.Format
		expectError bool
	}{
		"json format": {
			input:       "json",
			expected:    log.FormatJSON,
			expectError: false,
		},
		"logfmt format": {
			input:       "logfmt",
			expected:    log.FormatLogfmt,
			expectError: false,
		},
		"text format": {
			input:       "text",
			expected:    log.FormatText,
			expectError: false,
		},
		"case insensitive": {
			input:       "JSON",
			expected:    log.FormatJSON,
			expectError: false,
		},
		"unknown format": {
			input:       "unknown",
			expected:    "",
			expectError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fmt, err := log.ParseFormat(tc.input)
			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, log.ErrUnknownLogFormat)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, fmt)
			}
		})
	}
}

func TestCreateHandler(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		checkFunc func(*testing.T, []byte)
		format    log.Format
	}{
		"json handler": {
			format: log.FormatJSON,
			checkFunc: func(t *testing.T, output []byte) {
				t.Helper()

				var logEntry map[string]any

				err := json.Unmarshal(output, &logEntry)
				require.NoError(t, err)
				assert.Equal(t, "test message", logEntry["msg"])
				assert.Equal(t, "INFO", logEntry["level"])
				assert.Equal(t, "value", logEntry["key"])
			},
		},
		"logfmt handler": {
			format: log.FormatLogfmt,
			checkFunc: func(t *testing.T, output []byte) {
				t.Helper()

				outputStr := string(output)
				assert.Contains(t, outputStr, "level=INFO")
				assert.Contains(t, outputStr, "msg=\"test message\"")
				assert.Contains(t, outputStr, "key=value")
			},
		},
		"text handler": {
			format: log.FormatText,
			checkFunc: func(t *testing.T, output []byte) {
				t.Helper()

				outputStr := string(output)
				assert.Contains(t, outputStr, "INFO")
				assert.Contains(t, outputStr, "test message")
				assert.Contains(t, outputStr, "key=value")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			handler := log.NewHandler(&buf, log.LevelInfo, tc.format)
			require.NotNil(t, handler)

			logger := slog.New(handler)
			logger.Info("test message", slog.String("key", "value"))

			tc.checkFunc(t, buf.Bytes())
		})
	}
}

func TestCreateHandlerWithStrings(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		checkOutput func(*testing.T, *bytes.Buffer)
		levelStr    string
		formatStr   string
		message     string
		expectError bool
	}{
		"valid json handler": {
			levelStr:    "info",
			formatStr:   "json",
			expectError: false,
			message:     "test message",
			checkOutput: func(t *testing.T, buf *bytes.Buffer) {
				t.Helper()

				var logEntry map[string]any

				err := json.Unmarshal(buf.Bytes(), &logEntry)
				require.NoError(t, err)
				assert.Equal(t, "test message", logEntry["msg"])
			},
		},
		"invalid level": {
			levelStr:    "invalid",
			formatStr:   "json",
			expectError: true,
			message:     "",
			checkOutput: func(t *testing.T, buf *bytes.Buffer) {
				t.Helper()
				assert.Empty(t, buf.Bytes())
			},
		},
		"invalid format": {
			levelStr:    "info",
			formatStr:   "invalid",
			expectError: true,
			message:     "",
			checkOutput: func(t *testing.T, buf *bytes.Buffer) {
				t.Helper()
				assert.Empty(t, buf.Bytes())
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			handler, err := log.NewHandlerFromStrings(&buf, tc.levelStr, tc.formatStr)

			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, log.ErrInvalidArgument)
			} else {
				require.NoError(t, err)
				require.NotNil(t, handler)

				logger := slog.New(handler)
				logger.Info(tc.message)
			}

			tc.checkOutput(t, &buf)
		})
	}
}

func TestRegisterCompletions(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		flag string
		want []string
	}{
		"log-level completions": {
			flag: "log-level",
			want: log.GetAllLevelStrings(),
		},
		"log-format completions": {
			flag: "log-format",
			want: log.GetAllFormatStrings(),
		},
	}

	cfg := log.NewConfig()

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
			assert.Equal(t, tc.want, values)
		})
	}
}

func TestLogLevelFiltering(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		logFunc       func(*slog.Logger)
		format        log.Format
		level         log.Level
		shouldContain bool
	}{
		"info level passes info log": {
			level:  log.LevelInfo,
			format: log.FormatJSON,
			logFunc: func(logger *slog.Logger) {
				logger.Info("test message")
			},
			shouldContain: true,
		},
		"info level blocks debug log": {
			level:  log.LevelInfo,
			format: log.FormatJSON,
			logFunc: func(logger *slog.Logger) {
				logger.Debug("test message")
			},
			shouldContain: false,
		},
		"error level passes error log": {
			level:  log.LevelError,
			format: log.FormatJSON,
			logFunc: func(logger *slog.Logger) {
				logger.Error("test message")
			},
			shouldContain: true,
		},
		"error level blocks info log": {
			level:  log.LevelError,
			format: log.FormatJSON,
			logFunc: func(logger *slog.Logger) {
				logger.Info("test message")
			},
			shouldContain: false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			handler := log.NewHandler(&buf, tc.level, tc.format)
			logger := slog.New(handler)

			tc.logFunc(logger)

			if tc.shouldContain {
				assert.NotEmpty(t, buf.String())
				assert.Contains(t, buf.String(), "test message")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}
