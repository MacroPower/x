package log

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	charmlog "charm.land/log/v2"
)

// Handler is an alias of [slog.Handler].
type Handler = slog.Handler

// Format represents a log output format.
type Format string

// Log output format.
const (
	FormatJSON   Format = "json"
	FormatLogfmt Format = "logfmt"
	FormatText   Format = "text"
)

// EqualFold reports whether s is equal to f under Unicode case-folding.
func (f Format) EqualFold(s string) bool {
	return strings.EqualFold(string(f), s)
}

// Level represents a log severity level as a string.
type Level string

// Log severity level.
const (
	LevelError Level = "error"
	LevelWarn  Level = "warn"
	LevelInfo  Level = "info"
	LevelDebug Level = "debug"
)

// EqualFold reports whether s is equal to l under Unicode case-folding.
func (l Level) EqualFold(s string) bool {
	return strings.EqualFold(string(l), s)
}

// Level returns a [slog.Level], implementing the [slog.Leveler] interface.
// Unrecognized levels default to [slog.LevelInfo].
func (l Level) Level() slog.Level {
	switch l {
	case LevelError:
		return slog.LevelError
	case LevelWarn:
		return slog.LevelWarn
	case LevelInfo:
		return slog.LevelInfo
	case LevelDebug:
		return slog.LevelDebug
	}

	return slog.LevelInfo
}

// Sentinel errors returned by parsing functions.
var (
	ErrInvalidArgument  = errors.New("invalid argument")
	ErrUnknownLogLevel  = errors.New("unknown log level")
	ErrUnknownLogFormat = errors.New("unknown log format")
)

// NewHandlerFromStrings creates a [Handler] by strings.
func NewHandlerFromStrings(w io.Writer, logLevel, logFormat string) (Handler, error) {
	logLvl, err := ParseLevel(logLevel)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}

	logFmt, err := ParseFormat(logFormat)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}

	return NewHandler(w, logLvl, logFmt), nil
}

// NewHandler creates a [Handler] with the given level and format.
func NewHandler(w io.Writer, logLevel Level, logFormat Format) Handler {
	switch logFormat {
	case FormatJSON:
		return slog.NewJSONHandler(w, &slog.HandlerOptions{
			AddSource: true,
			Level:     logLevel,
		})

	case FormatLogfmt:
		return slog.NewTextHandler(w, &slog.HandlerOptions{
			AddSource: true,
			Level:     logLevel,
		})

	case FormatText:
		return charmlog.NewWithOptions(w, charmlog.Options{
			Level:           charmlog.Level(logLevel.Level()),
			Formatter:       charmlog.TextFormatter,
			ReportTimestamp: true,
			ReportCaller:    true,
			TimeFormat:      time.StampMilli,
		})
	}

	return nil
}

// GetAllLevelStrings returns all valid log level strings.
func GetAllLevelStrings() []string {
	return []string{
		string(LevelError),
		string(LevelWarn),
		string(LevelInfo),
		string(LevelDebug),
	}
}

// ParseLevel parses a level string into a [Level].
func ParseLevel(level string) (Level, error) {
	switch {
	case LevelError.EqualFold(level):
		return LevelError, nil
	case LevelWarn.EqualFold(level), Level("warning").EqualFold(level):
		return LevelWarn, nil
	case LevelInfo.EqualFold(level):
		return LevelInfo, nil
	case LevelDebug.EqualFold(level):
		return LevelDebug, nil
	}

	return "", ErrUnknownLogLevel
}

// GetAllFormatStrings returns all valid log format strings.
func GetAllFormatStrings() []string {
	return []string{
		string(FormatJSON),
		string(FormatLogfmt),
		string(FormatText),
	}
}

// ParseFormat parses a format string into a [Format].
func ParseFormat(format string) (Format, error) {
	switch {
	case FormatJSON.EqualFold(format):
		return FormatJSON, nil
	case FormatLogfmt.EqualFold(format):
		return FormatLogfmt, nil
	case FormatText.EqualFold(format):
		return FormatText, nil
	}

	return "", ErrUnknownLogFormat
}
