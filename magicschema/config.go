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
//
// The shorthand fields exist because pflag panics when two flags register
// the same shorthand: a host CLI that already uses -o or -a clears (or
// remaps) the shorthand here, which renaming the long flag alone cannot
// avoid. An empty shorthand registers none.
type Flags struct {
	Output              string
	OutputShorthand     string
	Draft               string
	Indent              string
	Title               string
	Description         string
	ID                  string
	Annotators          string
	AnnotatorsShorthand string
	Strict              string
	InferDefaults       string
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

// NewConfig returns a new [Config] with default flag names and the
// Registry-independent runtime defaults whose zero value would otherwise be
// invalid (Draft 7, Indent 2, Output "-"). Annotators is intentionally left
// empty: annotator names resolve only against a [Config.Registry], which is
// the caller's to supply, so seeding them here would make
// NewConfig+NewGenerator fail with an unknown-annotator error. A caller that
// wants a --annotators default sets Annotators alongside Registry before
// [Config.RegisterFlags] (the magicschema CLI joins
// [go.jacobcolvin.com/x/magicschema/helm.DefaultNames]).
func NewConfig() *Config {
	f := Flags{
		Output:              "output",
		OutputShorthand:     "o",
		Draft:               "draft",
		Indent:              "indent",
		Title:               "title",
		Description:         "description",
		ID:                  "id",
		Annotators:          "annotators",
		AnnotatorsShorthand: "a",
		Strict:              "strict",
		InferDefaults:       "infer-defaults",
	}

	return &Config{
		Flags:  f,
		Draft:  7,
		Indent: 2,
		Output: "-",
	}
}

// RegisterFlags adds schema generation flags to the given [*pflag.FlagSet].
// Each flag's default is the current field value rather than a hardcoded
// literal, so a caller that sets a field before registering flags keeps that
// value as the default (pflag's *Var registration would otherwise overwrite it
// immediately), and the flag defaults stay in step with [NewConfig]. That
// includes --annotators: an Annotators left empty stays empty, matching a
// [Config.Registry] the caller has not populated (see [NewConfig]).
func (c *Config) RegisterFlags(flags *pflag.FlagSet) {
	flags.StringVarP(&c.Output, c.Flags.Output, c.Flags.OutputShorthand, c.Output,
		"output file path (- for stdout)")
	flags.IntVar(&c.Draft, c.Flags.Draft, c.Draft,
		"JSON Schema draft version (only 7 is supported)")
	flags.IntVar(&c.Indent, c.Flags.Indent, c.Indent,
		"JSON indentation spaces (0 for compact output)")
	flags.StringVar(&c.Title, c.Flags.Title, c.Title,
		"schema title field")
	flags.StringVar(&c.Description, c.Flags.Description, c.Description,
		"schema description field")
	flags.StringVar(&c.ID, c.Flags.ID, c.ID,
		"schema $id field")
	flags.StringVarP(&c.Annotators, c.Flags.Annotators, c.Flags.AnnotatorsShorthand, c.Annotators,
		"comma-separated list of enabled annotation parsers (in priority order)")
	flags.BoolVar(&c.Strict, c.Flags.Strict, c.Strict,
		"set additionalProperties: false on objects")
	flags.BoolVar(&c.InferDefaults, c.Flags.InferDefaults, c.InferDefaults,
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

	err = cmd.RegisterFlagCompletionFunc(c.Flags.Annotators, c.annotatorsCompletion)
	if err != nil {
		return fmt.Errorf("registering %s completion: %w", c.Flags.Annotators, err)
	}

	noFileComp := cobra.FixedCompletions(nil, cobra.ShellCompDirectiveNoFileComp)

	for _, flag := range []string{c.Flags.Indent, c.Flags.Title, c.Flags.Description, c.Flags.ID} {
		regErr := cmd.RegisterFlagCompletionFunc(flag, noFileComp)
		if regErr != nil {
			return fmt.Errorf("registering %s completion: %w", flag, regErr)
		}
	}

	return nil
}

// annotatorsCompletion completes the comma-separated --annotators flag. It
// suggests the next annotator name after each comma rather than replacing
// the whole value (the behavior of a fixed completion on a list flag), and
// omits names already present. The no-space directive lets the user keep
// appending names after each comma.
func (c *Config) annotatorsCompletion(
	_ *cobra.Command, _ []string, toComplete string,
) ([]string, cobra.ShellCompDirective) {
	base, partial := "", toComplete

	if idx := strings.LastIndex(toComplete, ","); idx >= 0 {
		base, partial = toComplete[:idx+1], toComplete[idx+1:]
	}

	// Whitespace between the comma and the partial name (a quoted
	// "helm-schema, bit") moves into the kept base rather than being
	// dropped: the shell filters candidates by prefix against the typed
	// word, so a candidate that loses the typed space never survives the
	// filter and the user sees no suggestions at all.
	trimmed := strings.TrimLeft(partial, " \t")
	base += partial[:len(partial)-len(trimmed)]
	partial = strings.TrimSpace(trimmed)

	// The names already typed, cleaned with the shared splitAnnotatorNames rule
	// so a name preceded by a space (from a quoted "helm-schema, bitnami,")
	// still matches the canonical Registry name and is filtered out below. The
	// empty entry from the trailing comma drops, so used holds only real names.
	used := splitAnnotatorNames(base)

	var out []string

	for _, name := range c.Registry.Names() {
		if strings.HasPrefix(name, partial) && !slices.Contains(used, name) {
			out = append(out, base+name)
		}
	}

	return out, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
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

	// A negative indent is meaningless. Reject it explicitly instead of letting
	// it fall through to compact output (the writer only indents when Indent is
	// positive), so a typo surfaces as an error rather than silently dropping
	// the requested indentation.
	if c.Indent < 0 {
		return nil, fmt.Errorf("%w: negative JSON indentation %d", ErrInvalidOption, c.Indent)
	}

	annotators, err := c.parseAnnotatorNames()
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

// splitAnnotatorNames splits a comma-separated annotator list, trims
// surrounding whitespace from each element, and drops empty entries. The flag
// parser and the completion handler share this one definition of the cleaning
// rule so a completion can never offer a spelling the parser rejects.
func splitAnnotatorNames(s string) []string {
	parts := strings.Split(s, ",")
	cleaned := make([]string, 0, len(parts))

	for _, name := range parts {
		if name = strings.TrimSpace(name); name != "" {
			cleaned = append(cleaned, name)
		}
	}

	return cleaned
}

// parseAnnotatorNames parses the comma-separated [Config.Annotators] list and
// returns the corresponding Annotator instances. Whitespace around names is
// trimmed and empty entries are dropped (CLI parsing concerns); resolution
// itself goes through [Registry.Lookup].
func (c *Config) parseAnnotatorNames() ([]Annotator, error) {
	// An empty or whitespace-only list needs no special case: splitAnnotatorNames
	// drops it to no names and Lookup returns an empty slice, which the caller
	// treats as "no annotators".
	annotators, err := c.Registry.Lookup(splitAnnotatorNames(c.Annotators)...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOption, err)
	}

	return annotators, nil
}
