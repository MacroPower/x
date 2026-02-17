// Package magicschema generates JSON Schema (Draft 7) from YAML files on a
// best-effort basis. It detects common schema annotations from the Helm
// ecosystem (dadav/helm-schema, losisin/helm-values-schema-json, bitnami
// readme-generator, helm-docs) and infers types from YAML structure when
// annotations are absent.
//
// The tool is designed to fail open -- it never assumes a YAML file is a
// complete representation of the schema, producing schemas that guide users
// rather than strictly validate.
//
// # Basic Usage
//
//	gen := magicschema.NewGenerator()
//	schema, err := gen.Generate(yamlBytes)
//	out, _ := json.MarshalIndent(schema, "", "  ")
//
// # With Options
//
//	gen := magicschema.NewGenerator(
//	    magicschema.WithTitle("My Values"),
//	    magicschema.WithAnnotators(
//	        dadav.New(),
//	        norwoodj.New(),
//	    ),
//	)
//	schema, err := gen.Generate(file1, file2)
//
// # CLI Integration
//
//	cfg := magicschema.NewConfig()
//	cfg.Registry = map[string]func() magicschema.Annotator{
//	    "helm-schema": func() magicschema.Annotator { return dadav.New() },
//	    // ...
//	}
//	cfg.RegisterFlags(rootCmd.PersistentFlags())
//	cfg.RegisterCompletions(rootCmd)
//
//	gen, err := cfg.NewGenerator()
//	schema, err := gen.Generate(yamlBytes)
package magicschema
