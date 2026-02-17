// Package magicschema generates JSON Schema (Draft 7) from YAML files on a
// best-effort basis. It detects common schema annotations and infers types
// from YAML structure when annotations are absent.
//
// The generated schemas are designed to fail open -- we never assumes a YAML
// file is a complete representation of the schema. Our goal is to produce
// schemas that guide users rather than strictly validate.
//
// The primary use case is generating values.schema.json for Helm values.yaml
// files, specifically when chart authors either do not publish schemas, or
// have published an incorrect or incomplete schema. It is NOT intended for
// use by schema authors; authors should use a more strict and comprehensive
// solution. We recommend considering [dadav/helm-schema] for values.yaml use
// cases, as it is a very well-designed and complete solution.
//
// # Design Principles
//
// Four principles guide every design decision in this package:
//
//  1. Fail open: generated schemas should help users, not block them.
//     Default additionalProperties to true. Never mark properties as
//     required unless explicitly annotated. Use permissive type unions
//     when uncertain.
//
//  2. Best-effort: extract as much schema information as possible from
//     annotations and structure. Silently skip unparseable annotations
//     rather than returning errors.
//
//  3. Union semantics: when processing multiple YAML files, produce a
//     schema representing the union of all inputs. Conflicting types
//     widen to the most general type.
//
//  4. Extensible annotation system: support helm-schema (block format),
//     helm-values-schema (inline format), bitnami, and helm-docs
//     annotation styles out of the box, with the [Annotator] interface
//     allowing additional annotation parsers to be registered.
//
// # Schema Generation Pipeline
//
// [Generator.Generate] processes YAML inputs through a five-phase pipeline:
//
//  1. Parse YAML: each input is parsed using goccy/go-yaml with comment
//     preservation. Multi-document files are merged with union semantics,
//     the same as multiple input files. Empty files produce the "true" schema
//     (validates everything). YAML anchors and aliases are resolved by
//     walking the AST.
//
//  2. Extract annotations: the YAML node tree is walked depth-first.
//     For each key-value pair, all enabled annotators run against the
//     node and results are merged by priority order (first annotator in
//     the list has highest priority). For each schema field, the
//     highest-priority annotator that sets a non-zero value wins.
//     [AnnotationResult.Skip], [AnnotationResult.SkipProperties], and
//     [AnnotationResult.MergeProperties] are OR'd across all results.
//     [AnnotationResult.HasRequired] uses the highest-priority annotator
//     that explicitly sets it (non-nil). Description uses the first
//     non-empty value. Extra maps are merged per key.
//
//  3. Infer schema (structural fallback): when no annotator produces
//     output for a node, the schema is derived entirely from YAML
//     structure and comments. Boolean, integer, float, and string
//     literals map to their JSON Schema types. Null and empty values
//     emit no type constraint (maximally permissive). Objects recurse
//     into children. Arrays infer items from element types. Plain YAML
//     comments that do not look like annotation markers become the
//     description; [IsAnnotationComment] identifies markers to skip.
//
//  4. Merge multiple inputs: when multiple YAML files are provided,
//     schemas are generated independently and then merged with union
//     semantics. Properties are unioned. Conflicting types are widened
//     (integer + number becomes number; incompatible types drop the
//     type constraint entirely). Required is intersected (a property
//     is required only if required in all inputs). additionalProperties
//     is merged fail-open (true wins over false). Property order in
//     the output is deterministic: properties appear in YAML source
//     order via the PropertyOrder field on each schema node.
//
//  5. Emit JSON Schema: the root schema is configured with the Draft 7
//     $schema URI, optional title/description/$id from [Option] values,
//     and root-level properties from annotators implementing the
//     [RootAnnotator] interface. CLI-level values override annotator
//     values. additionalProperties on the root object defaults to
//     [TrueSchema] (permits everything), or [FalseSchema] (denies
//     everything) when strict mode is enabled via [WithStrict].
//
// # Errors
//
// The package defines four sentinel errors for use with [errors.Is]:
//
//   - [ErrInvalidYAML]: the input is not valid YAML syntax (fatal).
//   - [ErrInvalidOption]: a configuration value is invalid, such as an
//     unrecognized annotator name in [Config.NewGenerator].
//   - [ErrReadInput]: an I/O error occurred reading input (fatal).
//   - [ErrWriteOutput]: an I/O error occurred writing output (fatal).
//
// Annotation parse failures are not fatal. They are logged as warnings
// and the annotation is skipped.
//
// # Union Semantics
//
// When [Generator.Generate] receives multiple YAML inputs, or a single input
// containing multiple YAML documents (separated by ---), it produces a single
// schema representing the union of all documents. This supports the common
// pattern of having values.yaml with values-prd.yaml or similar overrides.
//
// Type widening follows a strict hierarchy:
//
//	Type A     Type B              Result
//	integer    number              number
//	integer    string              (no type constraint)
//	boolean    string              (no type constraint)
//	number     string              (no type constraint)
//	array      string              (no type constraint)
//	object     anything non-object (no type constraint)
//	any type   null                same type (null means "was empty in one file")
//	any type   same type           same type
//
// The key insight: incompatible types result in removing the type constraint
// entirely, which is the most permissive (fail-open) behavior. Null or empty
// values in one file do not constrain the merged type from another file.
//
// Object properties are unioned across files. Array items schemas are merged
// recursively. The required array is intersected so that a property is only
// required in the merged schema if it is required in every input.
//
// # Annotation System
//
// The [Annotator] interface allows pluggable annotation parsers. Each
// annotator receives a YAML AST node and a dot-separated key path, and
// returns an [AnnotationResult] containing a [jsonschema.Schema] fragment
// and metadata.
//
// [AnnotationResult] carries several signals beyond the schema fragment
// itself:
//
//   - HasRequired is a *bool. When non-nil, it explicitly controls whether
//     the property should appear in its parent's required array. The
//     pointer type distinguishes "not set" (nil) from "explicitly false."
//     During merge, the highest-priority annotator that sets a non-nil
//     value wins; if no annotator sets it, the property is not required.
//
//   - Skip causes the entire subtree rooted at this node to be omitted
//     from the generated schema.
//
//   - SkipProperties strips the Properties map from the output schema
//     for this node, leaving only the type and other constraints.
//
//   - MergeProperties merges all child property schemas into a single
//     additionalProperties schema, then removes Properties. This is
//     useful when a mapping's keys are dynamic.
//
// Skip, SkipProperties, and MergeProperties are OR'd across all annotator
// results for a given node: if any annotator sets them, they take effect.
//
// Annotators that need file-level context (e.g., the bitnami annotator
// which scans for ## @param lines not attached to AST nodes) implement
// [Annotator.ForContent], which is called once per input file before any
// [Annotator.Annotate] calls. Annotators that can provide root-level
// schema properties (e.g., from @schema.root blocks) implement the optional
// [RootAnnotator] interface.
//
// Four built-in annotator sub-packages are provided:
//
//   - [go.jacobcolvin.com/x/magicschema/helm/dadav]: helm-schema block
//     annotator (# @schema blocks, full Draft 7 support)
//   - [go.jacobcolvin.com/x/magicschema/helm/losisin]: helm-values-schema
//     inline annotator (# @schema key:value;... single-line format)
//   - [go.jacobcolvin.com/x/magicschema/helm/bitnami]: bitnami
//     readme-generator annotator (## @param annotations)
//   - [go.jacobcolvin.com/x/magicschema/helm/norwoodj]: helm-docs annotator
//     (# -- description annotations)
//
// # Helpers for Annotator Authors
//
// The package provides helper functions for sub-packages implementing
// [Annotator]:
//
//   - [DefaultValue] converts a Go value to a [json.RawMessage] for use as
//     a JSON Schema default.
//   - [ConstValue] converts a Go value to a *any for use as a JSON Schema
//     const.
//   - [TrueSchema] returns a schema that validates everything (the JSON
//     Schema "true" value, represented as &jsonschema.Schema{}).
//   - [FalseSchema] returns a schema that validates nothing (the JSON
//     Schema "false" value, represented as &jsonschema.Schema{Not: &jsonschema.Schema{}}).
//   - [ToSubSchema] converts an arbitrary Go value to a [*jsonschema.Schema]
//     by round-tripping through JSON.
//   - [ToSubSchemaArray] converts a []any to []*jsonschema.Schema.
//   - [ToSubSchemaMap] converts a map[string]any to map[string]*jsonschema.Schema.
//   - [ParseYAMLValue] parses a YAML value string into a []byte of JSON.
//   - [IsAnnotationComment] reports whether a comment string looks like an
//     annotation marker from any supported annotator format (@schema,
//     @param, @skip, @default, --, etc.), allowing annotators and the
//     fallback comment extractor to avoid treating annotations as plain
//     descriptions.
//
// # CLI Integration
//
// [Config] bridges CLI flags to the library, following the RegisterFlags /
// RegisterCompletions / NewGenerator pattern. The [Flags] type within
// [Config] allows callers to customize flag names while keeping sensible
// defaults. The [Config.Registry] field maps annotator names (as used in
// the --annotators flag) to prototype [Annotator] instances.
// [Config.NewGenerator] looks up each comma-separated name in Registry to
// build the annotator list.
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
// # Config-Based Usage
//
//	cfg := magicschema.NewConfig()
//	cfg.Registry = helm.DefaultRegistry()
//	cfg.RegisterFlags(rootCmd.PersistentFlags())
//	_ = cfg.RegisterCompletions(rootCmd)
//
//	gen, err := cfg.NewGenerator()
//	schema, err := gen.Generate(yamlBytes)
//
// [jsonschema.Schema]: https://pkg.go.dev/github.com/google/jsonschema-go/jsonschema#Schema
// [dadav/helm-schema]: https://github.com/dadav/helm-schema
package magicschema
