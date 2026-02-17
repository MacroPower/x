package magicschema

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flags holds CLI flag names for schema generation configuration, allowing
// callers to customize flag names while keeping sensible defaults.
type Flags struct {
	Output      string
	Draft       string
	Indent      string
	Title       string
	Description string
	ID          string
	Annotators  string
	Strict      string
}

// Config holds CLI flag values for schema generation configuration.
//
// Create instances with [NewConfig] and register CLI flags with
// [Config.RegisterFlags]. Use [Config.NewGenerator] to create a [Generator].
type Config struct {
	Flags       Flags
	Registry    map[string]func() Annotator
	Output      string
	Title       string
	Description string
	ID          string
	Annotators  string
	Draft       int
	Indent      int
	Strict      bool
}

// NewConfig returns a new [Config] with default flag names.
func NewConfig() *Config {
	f := Flags{
		Output:      "output",
		Draft:       "draft",
		Indent:      "indent",
		Title:       "title",
		Description: "description",
		ID:          "id",
		Annotators:  "annotators",
		Strict:      "strict",
	}

	return &Config{Flags: f}
}

// RegisterFlags adds schema generation flags to the given [*pflag.FlagSet].
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	flags.StringVarP(&c.Output, c.Flags.Output, "o", "-",
		"output file path (- for stdout)")
	flags.IntVar(&c.Draft, c.Flags.Draft, 7,
		"JSON Schema draft version")
	flags.IntVar(&c.Indent, c.Flags.Indent, 2,
		"JSON indentation spaces")
	flags.StringVar(&c.Title, c.Flags.Title, "",
		"schema title field")
	flags.StringVar(&c.Description, c.Flags.Description, "",
		"schema description field")
	flags.StringVar(&c.ID, c.Flags.ID, "",
		"schema $id field")
	flags.StringVarP(&c.Annotators, c.Flags.Annotators, "a",
		"helm-schema,helm-values-schema,bitnami,helm-docs",
		"comma-separated list of enabled annotation parsers (in priority order)")
	flags.BoolVar(&c.Strict, c.Flags.Strict, false,
		"set additionalProperties: false on objects")
}

// RegisterCompletions registers shell completions for schema generation flags
// on cmd.
func (c *Config) RegisterCompletions(cmd *cobra.Command) error {
	err := cmd.RegisterFlagCompletionFunc(c.Flags.Draft,
		cobra.FixedCompletions([]string{"7"}, cobra.ShellCompDirectiveNoFileComp))
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.Draft, err)
	}

	var names []string

	for name := range c.Registry {
		names = append(names, name)
	}

	slices.Sort(names)

	err = cmd.RegisterFlagCompletionFunc(c.Flags.Annotators,
		cobra.FixedCompletions(names, cobra.ShellCompDirectiveNoFileComp))
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.Annotators, err)
	}

	noFileComp := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	for _, flag := range []string{c.Flags.Indent, c.Flags.Title, c.Flags.Description, c.Flags.ID} {
		regErr := cmd.RegisterFlagCompletionFunc(flag, noFileComp)
		if regErr != nil {
			return fmt.Errorf("registering %s completion: %w", flag, regErr)
		}
	}

	return nil
}

// NewGenerator creates a [Generator] using this [Config].
func (c *Config) NewGenerator() (*Generator, error) {
	annotators, err := c.parseAnnotatorNames(c.Annotators)
	if err != nil {
		return nil, err
	}

	var opts []Option

	if len(annotators) > 0 {
		opts = append(opts, WithAnnotators(annotators...))
	}

	if c.Title != "" {
		opts = append(opts, WithTitle(c.Title))
	}

	if c.Description != "" {
		opts = append(opts, WithDescription(c.Description))
	}

	if c.ID != "" {
		opts = append(opts, WithID(c.ID))
	}

	if c.Strict {
		opts = append(opts, WithStrict(true))
	}

	return NewGenerator(opts...), nil
}

// parseAnnotatorNames parses a comma-separated list of annotator names and
// returns the corresponding Annotator instances.
func (c *Config) parseAnnotatorNames(names string) ([]Annotator, error) {
	if names == "" {
		return nil, nil
	}

	parts := strings.Split(names, ",")
	annotators := make([]Annotator, 0, len(parts))

	for _, name := range parts {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		constructor, ok := c.Registry[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown annotator %q", ErrInvalidOption, name)
		}

		annotators = append(annotators, constructor())
	}

	return annotators, nil
}
