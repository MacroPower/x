// Package log provides structured logging handler construction for use with
// [log/slog].
//
// It supports multiple output formats ([FormatJSON], [FormatLogfmt], and
// [FormatText]) and severity levels ([LevelError], [LevelWarn], [LevelInfo],
// and [LevelDebug]). Use [NewHandler] to create a handler directly, or use
// [Config] with CLI flag integration via [github.com/spf13/pflag] and shell
// completion support via [github.com/spf13/cobra].
//
// Typical usage creates a [Config], registers flags, then builds a handler
// at startup:
//
//	cfg := log.NewConfig()
//	cfg.RegisterFlags(rootCmd.PersistentFlags())
//	cfg.RegisterCompletions(rootCmd)
//
//	handler, err := cfg.NewHandler(os.Stderr)
//	slog.SetDefault(slog.New(handler))
package log
