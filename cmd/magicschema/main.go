// Package main provides the CLI entry point for magicschema, a tool that
// generates JSON Schema (Draft 7) from YAML files.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/bitnami"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
	"go.jacobcolvin.com/x/magicschema/helm/norwoodj"
)

func main() {
	cfg := magicschema.NewConfig()
	cfg.Registry = map[string]func() magicschema.Annotator{
		"helm-schema":        func() magicschema.Annotator { return dadav.New() },
		"helm-values-schema": func() magicschema.Annotator { return losisin.New() },
		"bitnami":            func() magicschema.Annotator { return bitnami.New() },
		"helm-docs":          func() magicschema.Annotator { return norwoodj.New() },
	}

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

	completionErr := cfg.RegisterCompletions(rootCmd)
	if completionErr != nil {
		fmt.Fprintf(os.Stderr, "register completions: %v\n", completionErr)
	}

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

	var inputs [][]byte

	for _, arg := range args {
		var data []byte

		if arg == "-" {
			data, err = io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("%w: stdin: %w", magicschema.ErrReadInput, err)
			}
		} else {
			data, err = os.ReadFile(arg)
			if err != nil {
				return fmt.Errorf("%w: %w", magicschema.ErrReadInput, err)
			}
		}

		inputs = append(inputs, data)
	}

	schema, err := gen.Generate(inputs...)
	if err != nil {
		return err
	}

	indent := "  "
	if cfg.Indent > 0 {
		indent = ""
		for range cfg.Indent {
			indent += " "
		}
	}

	out, err := json.MarshalIndent(schema, "", indent)
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
