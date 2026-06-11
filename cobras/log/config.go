package log

import (
	"cmp"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.jacobcolvin.com/x/cobras"
)

// Flags holds CLI flag names for log configuration, allowing callers to
// customize flag names while keeping sensible defaults.
type Flags struct {
	Level  string
	Format string
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

// NewConfig returns a new [Config] with default flag names and zero-value
// Level and Format. Use [Config.RegisterFlags] to add CLI flags, or set
// values directly. Values set before [Config.RegisterFlags] become the
// registered flag defaults.
func NewConfig() *Config {
	f := Flags{
		Level:  "log-level",
		Format: "log-format",
	}

	return &Config{Flags: f}
}

// RegisterFlags adds logging flags to the given [*pflag.FlagSet].
//
// Values already set on c become the flag defaults, both the runtime value
// and the help-text DefValue. Empty fields fall back to "info" and "text".
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	flags.StringVar(&c.Level, c.Flags.Level, cmp.Or(c.Level, "info"),
		fmt.Sprintf("log level, one of: %s", GetAllLevelStrings()))
	flags.StringVar(&c.Format, c.Flags.Format, cmp.Or(c.Format, "text"),
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

// MustRegisterCompletions registers shell completions for log flags on cmd,
// panicking on error. Registration only fails on programmer error: the flags
// in c.Flags are not registered on cmd, or a completion is already registered
// for them.
func (c *Config) MustRegisterCompletions(cmd *cobra.Command) {
	cobras.Must(c.RegisterCompletions(cmd))
}

// NewHandler creates a new [Handler] that writes to w, using the level and
// format strings stored in c. It delegates to [NewHandlerFromStrings].
func (c *Config) NewHandler(w io.Writer) (Handler, error) {
	return NewHandlerFromStrings(w, c.Level, c.Format)
}

// ParsedLevel returns the typed [Level] for c.Level.
func (c *Config) ParsedLevel() (Level, error) {
	return ParseLevel(c.Level)
}

// ParsedFormat returns the typed [Format] for c.Format.
func (c *Config) ParsedFormat() (Format, error) {
	return ParseFormat(c.Format)
}
