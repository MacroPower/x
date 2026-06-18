package magicschema

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ErrUnknownAnnotator indicates an annotator name with no registered parser.
// Configuration paths such as [Config.NewGenerator] additionally wrap
// [ErrInvalidOption], so their errors match both sentinels with [errors.Is].
var ErrUnknownAnnotator = errors.New("unknown annotator")

// Flags holds CLI flag names for schema generation configuration, allowing
// callers to customize flag names while keeping sensible defaults.
type Flags struct {
	Output        string
	Draft         string
	Indent        string
	Title         string
	Description   string
	ID            string
	Annotators    string
	Strict        string
	InferDefaults string
}

// Registry maps annotator names (as used in the --annotators flag) to
// prototype [Annotator] instances. Prototypes are never mutated; the
// generator calls [Annotator.ForContent] to obtain a prepared clone for
// each input file.
type Registry map[string]Annotator

// Add registers one or more annotators in the registry, using each
// annotator's [Annotator.Name] as the map key.
func (r Registry) Add(annotators ...Annotator) {
	for _, a := range annotators {
		r[a.Name()] = a
	}
}

// Lookup resolves annotator names to their registered prototypes, preserving
// the given order. Names must match registered names exactly; a miss returns
// an error wrapping [ErrUnknownAnnotator].
func (r Registry) Lookup(names ...string) ([]Annotator, error) {
	annotators := make([]Annotator, 0, len(names))

	for _, name := range names {
		annotator, ok := r[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownAnnotator, name)
		}

		annotators = append(annotators, annotator)
	}

	return annotators, nil
}

// Names returns the registered annotator names, sorted.
func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r))
}

// Config holds CLI flag values for schema generation configuration.
//
// Create instances with [NewConfig] and register CLI flags with
// [Config.RegisterFlags]. Use [Config.NewGenerator] to create a [Generator].
type Config struct {
	Flags         Flags
	Registry      Registry
	Output        string
	Title         string
	Description   string
	ID            string
	Annotators    string
	Draft         int
	Indent        int
	Strict        bool
	InferDefaults bool
}

// NewConfig returns a new [Config] with default flag names.
func NewConfig() *Config {
	f := Flags{
		Output:        "output",
		Draft:         "draft",
		Indent:        "indent",
		Title:         "title",
		Description:   "description",
		ID:            "id",
		Annotators:    "annotators",
		Strict:        "strict",
		InferDefaults: "infer-defaults",
	}

	return &Config{Flags: f, Draft: 7}
}

// RegisterFlags adds schema generation flags to the given [*pflag.FlagSet].
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	flags.StringVarP(&c.Output, c.Flags.Output, "o", "-",
		"output file path (- for stdout)")
	flags.IntVar(&c.Draft, c.Flags.Draft, 7,
		"JSON Schema draft version (only 7 is supported)")
	flags.IntVar(&c.Indent, c.Flags.Indent, 2,
		"JSON indentation spaces (0 for compact output)")
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
	flags.BoolVar(&c.InferDefaults, c.Flags.InferDefaults, false,
		"record observed YAML values as schema defaults")
}

// RegisterCompletions registers shell completions for schema generation flags
// on cmd.
func (c *Config) RegisterCompletions(cmd *cobra.Command) error {
	err := cmd.RegisterFlagCompletionFunc(c.Flags.Draft,
		cobra.FixedCompletions([]string{"7"}, cobra.ShellCompDirectiveNoFileComp))
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.Draft, err)
	}

	err = cmd.RegisterFlagCompletionFunc(c.Flags.Annotators,
		cobra.FixedCompletions(c.Registry.Names(), cobra.ShellCompDirectiveNoFileComp))
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

// MustRegisterCompletions registers shell completions for schema generation
// flags on cmd, panicking when registration returns an error. Registration
// can only go wrong through programmer error -- a flag missing from cmd
// because [Config.RegisterFlags] was never called, or a completion already
// registered for the same flag -- so the panic is unreachable for a
// correctly wired command.
func (c *Config) MustRegisterCompletions(cmd *cobra.Command) {
	err := c.RegisterCompletions(cmd)
	if err != nil {
		panic(err)
	}
}

// NewGenerator creates a [Generator] using this [Config].
func (c *Config) NewGenerator() (*Generator, error) {
	// Only Draft 7 output is implemented; reject any other requested draft
	// instead of silently emitting draft-07. NewConfig defaults Draft to 7 and
	// RegisterFlags registers 7 as the flag default, so any other value -- 0
	// included -- is an explicit, unsupported request rather than an unset
	// field.
	if c.Draft != 7 {
		return nil, fmt.Errorf("%w: unsupported JSON Schema draft %d (only 7 is supported)",
			ErrInvalidOption, c.Draft)
	}

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

	if c.InferDefaults {
		opts = append(opts, WithInferDefaults(true))
	}

	return NewGenerator(opts...), nil
}

// parseAnnotatorNames parses a comma-separated list of annotator names and
// returns the corresponding Annotator instances. Whitespace around names is
// trimmed and empty entries are dropped (CLI parsing concerns); resolution
// itself goes through [Registry.Lookup].
func (c *Config) parseAnnotatorNames(names string) ([]Annotator, error) {
	if names == "" {
		return nil, nil
	}

	parts := strings.Split(names, ",")
	cleaned := make([]string, 0, len(parts))

	for _, name := range parts {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		cleaned = append(cleaned, name)
	}

	annotators, err := c.Registry.Lookup(cleaned...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOption, err)
	}

	return annotators, nil
}
