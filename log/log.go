package log

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// Format represents the log output format.
type Format string

const (
	// FormatJSON outputs logs as JSON objects.
	FormatJSON Format = "json"
	// FormatLogfmt outputs logs in logfmt format.
	FormatLogfmt Format = "logfmt"
)

var (
	// ErrInvalidArgument indicates an invalid argument was provided.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrUnknownLogLevel indicates an unrecognized log level string.
	ErrUnknownLogLevel = errors.New("unknown log level")
	// ErrUnknownLogFormat indicates an unrecognized log format string.
	ErrUnknownLogFormat = errors.New("unknown log format")
)

// CreateHandlerWithStrings creates a [slog.Handler] by strings.
func CreateHandlerWithStrings(w io.Writer, logLevel, logFormat string) (slog.Handler, error) {
	logLvl, err := GetLevel(logLevel)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}

	logFmt, err := GetFormat(logFormat)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}

	return CreateHandler(w, logLvl, logFmt), nil
}

// CreateHandler creates a [slog.Handler] with the specified level and format.
func CreateHandler(w io.Writer, logLvl slog.Level, logFmt Format) slog.Handler {
	switch logFmt {
	case FormatJSON:
		return slog.NewJSONHandler(w, &slog.HandlerOptions{
			AddSource: true,
			Level:     logLvl,
		})

	case FormatLogfmt:
		return slog.NewTextHandler(w, &slog.HandlerOptions{
			AddSource: true,
			Level:     logLvl,
		})
	}

	return nil
}

// GetLevel parses a log level string and returns the corresponding
// [slog.Level].
func GetLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "error":
		return slog.LevelError, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	}

	return 0, ErrUnknownLogLevel
}

// GetFormat parses a log format string and returns the corresponding [Format].
func GetFormat(format string) (Format, error) {
	logFmt := Format(strings.ToLower(format))
	if slices.Contains([]Format{FormatJSON, FormatLogfmt}, logFmt) {
		return logFmt, nil
	}

	return "", ErrUnknownLogFormat
}
