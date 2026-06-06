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
//
// A [Publisher] fans out log output to multiple subscribers, which is useful
// for displaying logs inside a Bubble Tea TUI:
//
//	pub := log.NewPublisher()
//	handler := log.NewHandler(pub, log.LevelInfo, log.FormatJSON)
//	logger := slog.New(handler)
//
//	sub := pub.Subscribe()
//	go func() {
//	    for entry := range sub.C() {
//	        // Deliver entry to the TUI.
//	    }
//	}()
//
// Combine it with [io.MultiWriter] to write to multiple locations:
//
//	pub := log.NewPublisher()
//	w := io.MultiWriter(logFile, pub)
//	handler := log.NewHandler(w, log.LevelInfo, log.FormatJSON)
//	logger := slog.New(handler)
package log
