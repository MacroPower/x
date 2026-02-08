package log

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flags holds CLI flag names for log configuration, allowing callers to
// customize flag names while keeping sensible defaults via [NewConfig].
type Flags struct {
	Level  string
	Format string
}

// NewConfig creates a new [Config] embedding these flag names.
func (f Flags) NewConfig() *Config {
	return &Config{
		Flags: f,
	}
}

// Config holds CLI flag values for log configuration.
//
// Create instances with [NewConfig] and register CLI flags with
// [Config.RegisterFlags]. Use [Config.NewHandler] to create a [Handler]
// for logging.
type Config struct {
	Level  string
	Format string
	Flags  Flags
}

// NewConfig returns a new [Config] with zero-value fields.
// Use [Config.RegisterFlags] to add CLI flags, or set values directly.
func NewConfig() *Config {
	f := Flags{
		Level:  "log-level",
		Format: "log-format",
	}

	return f.NewConfig()
}

// RegisterFlags adds logging flags to the given [*pflag.FlagSet].
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	flags.StringVar(&c.Level, c.Flags.Level, "info",
		fmt.Sprintf("log level, one of: %s", GetAllLevelStrings()))
	flags.StringVar(&c.Format, c.Flags.Format, "text",
		fmt.Sprintf("log format, one of: %s", GetAllFormatStrings()))
}

// RegisterCompletions registers shell completions for log flags on cmd.
func (c *Config) RegisterCompletions(cmd *cobra.Command) error {
	err := cmd.RegisterFlagCompletionFunc(c.Flags.Level,
		cobra.FixedCompletions(GetAllLevelStrings(), cobra.ShellCompDirectiveNoFileComp))
	if err != nil {
		return fmt.Errorf("registering log-level completion: %w", err)
	}

	err = cmd.RegisterFlagCompletionFunc(c.Flags.Format,
		cobra.FixedCompletions(GetAllFormatStrings(), cobra.ShellCompDirectiveNoFileComp))
	if err != nil {
		return fmt.Errorf("registering log-format completion: %w", err)
	}

	return nil
}

// NewHandler creates a new [Handler] that writes to w, using the level and
// format strings stored in c. It delegates to [NewHandlerFromStrings].
func (c *Config) NewHandler(w io.Writer) (Handler, error) {
	return NewHandlerFromStrings(w, c.Level, c.Format)
}
