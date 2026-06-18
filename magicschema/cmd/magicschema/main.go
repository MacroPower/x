// Package main provides the CLI entry point for magicschema, a tool that
// generates JSON Schema (Draft 7) from YAML files.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/cobra"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm"
)

func main() {
	cfg := magicschema.NewConfig()
	cfg.Registry = helm.DefaultRegistry()

	rootCmd := &cobra.Command{
		Use:   "magicschema [flags] <file.yaml> [file2.yaml ...]",
		Short: "Generate JSON Schema from YAML files",
		Long: `magicschema generates JSON Schema (Draft 7) from YAML files on a best-effort
basis. It detects common schema annotations from the Helm ecosystem and infers
types from YAML structure when annotations are absent.`,
		Args:          cobra.MinimumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(cfg, args)
		},
	}

	cfg.RegisterFlags(rootCmd.Flags())
	cfg.MustRegisterCompletions(rootCmd)

	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(cfg *magicschema.Config, args []string) error {
	gen, err := cfg.NewGenerator()
	if err != nil {
		return err
	}

	schema, err := generate(gen, args)
	if err != nil {
		return err
	}

	var out []byte

	if cfg.Indent > 0 {
		out, err = json.MarshalIndent(schema, "", strings.Repeat(" ", cfg.Indent))
	} else {
		out, err = json.Marshal(schema)
	}

	if err != nil {
		return fmt.Errorf("%w: %w", magicschema.ErrWriteOutput, err)
	}

	out = append(out, '\n')

	if cfg.Output == "" || cfg.Output == "-" {
		_, err = os.Stdout.Write(out)
		if err != nil {
			return fmt.Errorf("%w: %w", magicschema.ErrWriteOutput, err)
		}
	} else {
		err := os.WriteFile(cfg.Output, out, 0o644)
		if err != nil {
			return fmt.Errorf("%w: %w", magicschema.ErrWriteOutput, err)
		}
	}

	return nil
}

// generate produces a schema from the CLI arguments. Plain file paths go
// through [magicschema.Generator.GenerateFiles]; an argument of "-" reads
// stdin (a CLI concern the library does not handle), so any "-" falls back
// to reading each input here.
func generate(gen *magicschema.Generator, args []string) (*jsonschema.Schema, error) {
	if !slices.Contains(args, "-") {
		return gen.GenerateFiles(args...)
	}

	// Stdin can only be consumed once. A second "-" would read the
	// already-drained stream as empty, which becomes a permit-everything
	// TrueSchema and silently widens the merged result (and overrides
	// --strict), so reject the misuse outright.
	stdinArgs := 0

	for _, arg := range args {
		if arg == "-" {
			stdinArgs++
		}
	}

	if stdinArgs > 1 {
		return nil, fmt.Errorf("%w: stdin (\"-\") may be given at most once", magicschema.ErrReadInput)
	}

	inputs := make([][]byte, 0, len(args))

	for _, arg := range args {
		var (
			data []byte
			err  error
		)

		if arg == "-" {
			data, err = io.ReadAll(os.Stdin)
			if err != nil {
				return nil, fmt.Errorf("%w: stdin: %w", magicschema.ErrReadInput, err)
			}
		} else {
			data, err = os.ReadFile(arg)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", magicschema.ErrReadInput, err)
			}
		}

		inputs = append(inputs, data)
	}

	return gen.Generate(inputs...)
}
