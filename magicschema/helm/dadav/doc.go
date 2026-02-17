// Package dadav implements the helm-schema block annotator, parsing
// # @schema / # @schema block annotations as defined by the
// [dadav/helm-schema] project.
//
// Example values.yaml annotations:
//
//	# @schema
//	# type: integer
//	# minimum: 1
//	# description: Number of replicas
//	# @schema
//	replicas: 3
//
//	# @schema
//	# type: string
//	# required: true
//	# minLength: 1
//	# @schema
//	name: my-release
//
// # Upstream Behavior
//
// The upstream dadav/helm-schema tool generates JSON Schema (Draft 7) from
// Helm values.yaml files. It walks the YAML AST (using gopkg.in/yaml.v3),
// reads # @schema comment blocks above each key, and produces a corresponding
// JSON Schema. Key behaviors of the upstream tool:
//
//   - Annotation format: Schema metadata is written between paired # @schema
//     delimiters (single-hash only). The delimiters toggle block parsing
//     on/off, so multiple consecutive blocks on the same key are
//     concatenated. Content lines inside the block are stripped of the
//     leading "# " prefix (via [strings.TrimPrefix] for "#" twice, then " ")
//     and parsed as YAML. The upstream uses [GetSchemaFromComment] to extract
//     blocks from the key node's HeadComment field. Delimiter detection
//     uses [strings.HasPrefix](line, "# @schema"), which means lines like
//     "# @schema.root" or "# @schema type:string" also match -- but
//     @schema.root lines are stripped by the two-pass approach (see below),
//     and inline @schema content (losisin format) would be erroneously
//     treated as a toggle delimiter in the upstream.
//
//   - Two-pass comment parsing: The upstream processes comments in two
//     sequential passes. First, [GetRootSchemaFromComment] scans for
//     @schema.root blocks using HasPrefix(line, "# @schema.root"), extracts
//     their content, and returns the remaining comment lines with root block
//     lines removed. Second, [GetSchemaFromComment] processes the remainder
//     using HasPrefix(line, "# @schema") for property-level blocks. Because
//     SchemaPrefix ("# @schema") is a prefix of SchemaRootPrefix
//     ("# @schema.root"), the first pass must strip root lines before the
//     second pass runs, otherwise @schema.root delimiters would be
//     incorrectly matched as @schema delimiters. The first pass also sets
//     HasData on the root schema when any root content is found (via Set()).
//
//   - Root schema: A # @schema.root / # @schema.root block placed in the
//     head comment of the first mapping key applies properties to the root
//     schema object. Root blocks propagate: title, description, $ref,
//     examples, deprecated, readOnly, writeOnly, additionalProperties, and
//     x-* extensions. All other fields in root blocks are silently accepted
//     by the YAML unmarshaler into the Schema struct but only these specific
//     fields are propagated to the document-level schema.
//
//   - Supported fields: The upstream supports the full Draft 7 vocabulary:
//     type, title, description, default, enum, const, pattern, format,
//     minimum/maximum, exclusiveMinimum/exclusiveMaximum, multipleOf,
//     minLength/maxLength, minItems/maxItems, uniqueItems, minProperties/
//     maxProperties, items, properties, additionalProperties,
//     patternProperties, propertyNames, contains, additionalItems,
//     required (bool or string array), deprecated, readOnly, writeOnly,
//     examples, anyOf/oneOf/allOf/not, if/then/else, $ref, $id, $comment,
//     contentEncoding, contentMediaType, dependencies (string-array and
//     schema forms), definitions, $defs (Draft 2019-09+), and x-* custom
//     annotations.
//
//   - HasData flag: The upstream Schema struct has a HasData bool field
//     (excluded from YAML/JSON serialization via "-" tags). The Set() method
//     sets HasData to true and is called whenever schema block content lines
//     are found. HasData controls auto-generation: when true (annotation
//     present), properties are NOT automatically marked required and the
//     annotation's explicit required value is used instead. When false
//     (no annotation), the upstream's default behavior applies (e.g.,
//     auto-required, auto-title, auto-default).
//
//   - Type handling: The upstream uses a custom StringOrArrayOfString type
//     with a YAML unmarshaler that handles null in type arrays (!!null tag →
//     "null" string). For bare scalar type: null (without quotes), the YAML
//     decoder produces an empty string in the single-element slice, which
//     IsEmpty() treats as "no type set", falling through to type inference
//     from the YAML value's tag. Type arrays with a single element are
//     serialized as a plain string (not an array) in JSON output via a
//     custom MarshalJSON.
//
//   - Type inference from YAML tags: When no type is specified in the
//     annotation (or IsEmpty() is true), the upstream infers the type from
//     the YAML value's tag: !!null → "null", !!bool → "boolean",
//     !!str → "string", !!int → "integer", !!float → "number",
//     !!timestamp → "string", !!seq → "array", !!map → "object".
//     The !!timestamp → "string" mapping is notable: Go's yaml.v3 uses
//     the !!timestamp tag for date/datetime values, and the upstream maps
//     this to the JSON Schema "string" type.
//
//   - const null tracking: The upstream uses an unexported constWasSet flag
//     on the Schema struct so that const: null is preserved in JSON output
//     even though the Go value is nil. A custom MarshalJSON checks this flag
//     and explicitly sets data["const"] = nil when constWasSet is true.
//
//   - default handling: The upstream does NOT track whether default was
//     explicitly set. default: null in an annotation block is treated as
//     "no default" because Go's json omitempty drops nil interface values.
//     This means the upstream silently loses default: null annotations.
//
//   - required default: The upstream marks ALL unannotated properties as
//     required in their parent's required array. The logic is: if
//     Required.Bool is true OR (no Required.Strings AND not skipping
//     required AND HasData is false), add the key to the parent's required
//     list. When a key has an @schema annotation (HasData == true), the key
//     is NOT automatically required -- the user must explicitly set
//     required: true in the block. Annotated properties use the required
//     field value (bool to signal parent inclusion via
//     [FixRequiredProperties], or string array for listing required child
//     properties on an object schema). Additionally, the upstream's
//     MarshalJSON drops the "required" key from JSON output for non-object
//     types: canDropRequired() returns true when the type is a single
//     scalar string, number, boolean, integer, null, or array. This
//     prevents the internal bool-based required signal from leaking into
//     the JSON Schema output on leaf properties.
//
//   - additionalProperties default: The upstream defaults
//     additionalProperties to false (represented as new(bool), a pointer to
//     false) for all mapping value nodes with yaml.MappingNode kind and at
//     the document root level. An explicit annotation of
//     additionalProperties: true is needed to allow extra keys. The check
//     for nested objects is: if not skipping additionalProperties AND the
//     value is a MappingNode AND (no annotation data OR the annotation
//     didn't set additionalProperties).
//
//   - title auto-generation: The upstream automatically sets title to the
//     YAML key name when no title is provided in the annotation block and
//     skipAutoGeneration.Title is false.
//
//   - default auto-generation: The upstream automatically sets default to
//     the YAML scalar value (cast via castNodeValueByType to the appropriate
//     Go type) when no default is provided in the annotation block and
//     skipAutoGeneration.Default is false.
//
//   - description auto-generation: The upstream uses non-annotation comment
//     text as the description when no description is set in the block and
//     skipAutoGeneration.Description is false. By default (without
//     --keep-full-comment), a leadingCommentsRemover regex
//     ((?s)(?m)(?:.*\n{2,})+) strips all comment text before the last
//     double-newline, keeping only the final comment group closest to the
//     key. This regex greedily matches everything up to and including the
//     last occurrence of two or more consecutive newlines. Additionally,
//     helm-docs @tags (e.g., @ignored, @default) are removed via
//     helmDocsTagsRemover regex ((?ms)(\r\n|\r|\n)?\s*@\w+(\s+--\s)?[^\n\r]*),
//     and the "-- " prefix from helm-docs style comments is stripped via
//     helmDocsPrefixRemover regex ((?m)^--\s?).
//
//   - skipAutoGeneration: The upstream CLI exposes --skip-auto-generation
//     flags for six fields: type, title, description, required, default,
//     and additionalProperties. When a field is listed, the upstream does
//     not auto-generate it from the YAML structure. This is a CLI-level
//     concern; the annotation parser itself always processes explicit
//     annotation values regardless of skip flags.
//
//   - $ref resolution: The upstream resolves relative file paths in $ref
//     values by loading and inlining the referenced JSON Schema file. It
//     splits on "#" to handle JSON Pointer fragments. When the ref path is
//     relative (determined by [util.IsRelativeFile]), the referenced file
//     is opened, parsed, and the schema is replaced with the resolved
//     content. Non-relative refs (like "#/definitions/X") are left as-is.
//     Pattern property $ref values are also resolved.
//
//   - $defs rewriting: The upstream rewrites $defs to definitions via
//     [UnmarshalJSON] and rewrites #/$defs/ ref paths to #/definitions/ via
//     [rewriteDefsRefs] for Draft 7 compatibility.
//
//   - definitions hoisting: The upstream's [HoistDefinitions] method
//     recursively collects all definitions from nested schemas and moves
//     them to the root level, since $ref paths like #/definitions/X always
//     reference the document root.
//
//   - global property: The upstream auto-injects a "global" property of
//     type object at the root level (required for Helm lint), unless
//     disabled via --dont-add-global. The global property gets an
//     auto-generated title "global" and description about global values.
//
//   - validation: The upstream validates annotation schemas via a
//     comprehensive Validate() method that checks type validity, numeric
//     constraints (minimum > maximum), string constraints (format on
//     non-string, invalid pattern regex), array constraints (items on
//     non-array), object constraints (additionalProperties on non-object),
//     and nested schema validity. Validation failures and unclosed @schema
//     blocks are treated as hard errors that stop processing.
//
//   - helm-docs compatibility: With --helm-docs-compatibility-mode, the
//     upstream additionally parses helm-docs annotations (@default, (type)
//     prefixes) using the helm-docs library's ParseComment function, setting
//     HasData and populating Default, Description, and Type fields.
//
//   - comment handling: Non-annotation comments outside @schema blocks
//     become the description. The upstream separates description lines from
//     schema block lines during [GetSchemaFromComment]: lines outside
//     @schema block delimiters are collected as description, stripped of the
//     leading "# " prefix, and joined with "\n" (preserving multi-line
//     structure). The leadingCommentsRemover regex is applied to the
//     HeadComment before both root schema and regular schema parsing
//     (see description auto-generation above for regex details).
//
//   - double-hash comments: The upstream only recognizes single-hash
//     delimiters (# @schema). Lines beginning with ## @schema are not
//     recognized as delimiters because SchemaPrefix is "# @schema" and
//     HeadComment lines from gopkg.in/yaml.v3 retain exactly one leading
//     "# " per comment line. Content stripping uses TrimPrefix for "#"
//     twice, which handles "## " content lines but not "## @schema"
//     delimiters.
//
//   - @schema.root delimiter matching: The upstream uses HasPrefix(line,
//     "# @schema.root") for root block delimiters. This means lines like
//     "# @schema.root something extra" would match and toggle the block
//     state. The upstream does not check for trailing content after the
//     marker.
//
//   - patternProperties interaction: When a key's annotation defines
//     patternProperties, the upstream excludes child keys matching any
//     pattern regex from the generated properties map. For each child key
//     in the mapping value, the upstream checks if the key matches any
//     patternProperties regex and skips it from properties if it matches.
//
//   - sequence item handling: For sequence values without predefined items,
//     the upstream creates an items schema with an anyOf array containing
//     a schema for each array element (scalar types from tags, or full
//     sub-schemas for mapping elements). It then calls FixRequiredProperties
//     on the result.
//
//   - $ref skips auto-generation: When a key has a $ref in its annotation,
//     the upstream skips all auto-generation (required, additionalProperties,
//     title, description, default) and recursive property generation.
//     The guard is: if keyNodeSchema.Ref == "" { ... all auto-generation ... }.
//
//   - custom annotation extraction: During [UnmarshalYAML], the upstream
//     iterates all YAML node keys and collects any keys not in the known
//     JSON Schema field list that start with "x-" into a CustomAnnotations
//     map. During [MarshalJSON], these are inlined into the top-level JSON
//     object and the "CustomAnnotations" wrapper key is deleted. Keys that
//     are neither known fields nor x-* prefixed are silently ignored.
//
//   - MarshalJSON required stripping: The upstream's custom MarshalJSON
//     removes the "required" key from JSON output when the schema's type
//     is a single non-object type (canDropRequired returns true for string,
//     number, boolean, integer, null, array). This prevents the internal
//     bool-based required signal (BoolOrArrayOfString.Bool) from leaking
//     into the JSON output as an empty "required":[] on leaf properties.
//
//   - castNodeValueByType: For auto-generated defaults, the upstream casts
//     YAML scalar values to typed Go values: "true"/"false" → bool for
//     boolean type, [strconv.Atoi] for integer type, [strconv.ParseFloat] for
//     number type, and raw string for all other types.
//
//   - annotate mode: The upstream's --annotate / -A flag triggers a mode
//     where @schema type annotations are written directly into the
//     values.yaml file for unannotated keys. The annotation block is three
//     lines (# @schema, # type: X, # @schema) inserted above each key.
//
// # Intentional Divergences
//
// This implementation intentionally diverges from the upstream in several
// areas to support the magicschema "fail-open" design philosophy:
//
//   - required default: We NEVER mark properties as required unless
//     explicitly annotated with required: true. This is the most significant
//     behavioral difference. The upstream's default of marking all
//     unannotated keys as required is overly strict for a best-effort
//     schema generator, as it assumes the values.yaml file is a complete
//     specification. (Upstream: unannotated → required; ours: unannotated →
//     not required.)
//
//   - additionalProperties default: We default additionalProperties to
//     true on all objects (overridable with --strict or explicit annotation).
//     The upstream defaults to false, which rejects any keys not present
//     in the values file -- problematic for partial or evolving schemas.
//     (Upstream: false; ours: true.)
//
//   - No title auto-generation: We do not auto-generate title from the
//     YAML key name. Titles should be explicitly set when meaningful;
//     auto-generating them adds noise without semantic value.
//     (Upstream: auto-generates; ours: omits.)
//
//   - No default auto-generation: We do not auto-generate default from
//     the YAML value. The YAML file represents one possible configuration,
//     not necessarily the canonical default. (Upstream: auto-generates from
//     scalar values; ours: omits.)
//
//   - No description auto-generation from comments (generator concern): The
//     upstream auto-generates description from non-schema comments when
//     skipAutoGeneration.Description is false. Our annotator extracts
//     comment-based descriptions, but the generator handles fallback
//     description extraction for unannotated nodes separately. The annotator
//     only provides descriptions when a @schema block is present and the
//     description isn't set inside it.
//
//   - default: null preserved: We preserve an explicit default: null in
//     annotation blocks as "default": null in the JSON output. The upstream
//     silently drops default: null because it lacks a "was set" flag (unlike
//     const, which uses constWasSet). Our behavior is more correct: when a
//     user explicitly writes default: null, they intend a null default.
//     We achieve this by converting annotation values through
//     [magicschema.DefaultValue] which produces json.RawMessage("null")
//     for nil values.
//
//   - $ref preserved as-is: We preserve $ref values without file
//     resolution. Schema generation should be a pure function of the input
//     YAML, not dependent on the filesystem. Users can resolve refs
//     downstream if needed. (Upstream: resolves relative file paths and
//     JSON Pointer fragments; ours: preserves verbatim.)
//
//   - No definitions hoisting: Definitions remain where annotated in the
//     schema tree. Hoisting is an output-formatting concern, not an
//     annotation-parsing concern. (Upstream: hoists to root; ours: leaves
//     in place.)
//
//   - No $defs rewriting: We do not rewrite $defs to definitions or rewrite
//     $ref paths from #/$defs/ to #/definitions/. The annotator preserves
//     whatever the user wrote. (Upstream: rewrites both; ours: preserves.)
//
//   - No global property: We do not auto-inject a "global" property.
//     This package is a generic YAML-to-schema tool, not Helm-specific.
//     (Upstream: injects global by default; ours: omits.)
//
//   - Best-effort error handling: Malformed YAML in @schema blocks
//     produces a warning (via [log/slog]) and is skipped, rather than
//     causing a fatal error. Unclosed @schema blocks are processed
//     best-effort (content up to end-of-comment is included). Unclosed
//     @schema.root blocks are silently ignored. (Upstream: treats unclosed
//     blocks and malformed YAML as hard errors.)
//
//   - Validation is not performed: We do not validate annotation schemas
//     for type/constraint consistency (e.g., minimum > maximum, pattern on
//     non-string type). Invalid annotations are passed through as-is,
//     letting downstream validators catch issues. (Upstream: validates and
//     rejects invalid schemas.)
//
//   - Single-pass comment extraction: The upstream uses a two-pass
//     approach (GetRootSchemaFromComment then GetSchemaFromComment) to
//     prevent the @schema prefix from matching @schema.root lines. Our
//     implementation uses a single pass that tracks both inBlock and
//     inRootBlock states simultaneously. We check for @schema.root first
//     (via CutPrefix), so it is matched before the shorter @schema prefix.
//     This is functionally equivalent to the upstream's two-pass approach
//     but simpler to implement.
//
//   - Double-hash support: We recognize ## @schema as a block delimiter,
//     stripping up to two leading # characters from both delimiters and
//     content lines. The upstream only recognizes single-hash # @schema
//     because gopkg.in/yaml.v3 HeadComment normalizes to "# " prefix.
//     This extension accommodates YAML files that use ## for section-level
//     comments containing schema annotations.
//
//   - Inline @schema explicitly excluded: Lines like "# @schema type:string"
//     (losisin format with content after @schema on the same line) are
//     explicitly skipped by this annotator. The upstream would erroneously
//     treat such lines as block delimiters due to its HasPrefix-based
//     detection. Our behavior is more correct for mixed-annotator scenarios.
//
//   - @schema.root trailing content rejected: We require @schema.root
//     delimiters to be bare (no trailing content). Lines like
//     "# @schema.root something" are not treated as delimiters and are
//     skipped. The upstream's HasPrefix matching would treat them as
//     delimiters. Our behavior prevents accidental block toggling from
//     comments that happen to start with @schema.root.
//
//   - Unknown keys silently ignored: Unknown keys in @schema blocks
//     (those not matching any Draft 7 field or x-* prefix) are silently
//     dropped. The upstream's validation would reject them as invalid.
//
//   - No patternProperties exclusion: We do not exclude child keys from
//     properties when patternProperties is set. The upstream checks each
//     child key against the patternProperties regexes and omits matches
//     from the properties map. Our approach is simpler: annotators only
//     process comments, and the generator handles property recursion
//     independently. If an annotation specifies explicit properties, those
//     are used; otherwise, the generator infers from the YAML structure.
//
//   - No $ref auto-generation skip: The upstream skips all auto-generation
//     when $ref is present. Our annotator does not have this special case
//     because we don't auto-generate title, default, required, or
//     additionalProperties at the annotator level.
//
//   - No skipAutoGeneration: We do not support the --skip-auto-generation
//     flag system. The upstream's six-field skip mechanism is unnecessary
//     because we already omit the auto-generated fields (title, default,
//     required, additionalProperties) by design.
//
//   - Description line joining: The upstream joins multi-line description
//     comments with newline characters ("\n"), preserving the original
//     line structure in the JSON output. Our implementation joins
//     description lines with spaces, producing single-line descriptions.
//     This is intentional: JSON Schema descriptions are typically rendered
//     as single-line prose in editor tooltips and documentation generators,
//     and collapsing to a single line produces cleaner output. Users who
//     need multi-line descriptions can use the explicit description field
//     in the @schema block with literal block scalar syntax.
//     (Upstream: "Line one\nLine two"; ours: "Line one Line two".)
//
//   - No helm-docs compatibility mode: We do not support the upstream's
//     --helm-docs-compatibility-mode flag, which uses the helm-docs
//     library's ParseComment function to extract @default, (type) prefix,
//     and description annotations. Instead, we provide a separate
//     helm-docs annotator (package norwoodj) that can be composed with
//     this annotator via the annotator priority system. This achieves the
//     same result with better separation of concerns.
//
//   - No annotate mode: The upstream supports an --annotate / -A flag
//     that writes @schema type annotations into unannotated values.yaml
//     files. This is a file-modification concern outside the scope of
//     schema generation and is not implemented.
//
// # Behavioral Alignment
//
// The following behaviors match the upstream:
//
//   - Block toggle semantics: Multiple @schema blocks on the same key
//     are concatenated via the toggle mechanism. An open delimiter starts
//     a block, a close delimiter ends it, and subsequent pairs create
//     additional concatenated content.
//
//   - @schema.root: Root schema blocks are parsed from the first key's
//     head comment only. Root blocks on non-first keys are ignored. The
//     root block is processed separately from regular @schema blocks,
//     and root content does not leak into property annotations.
//
//   - Root field propagation: The same fields propagate from root blocks
//     as in the upstream: title, description, $ref, examples, deprecated,
//     readOnly, writeOnly, additionalProperties, and x-* extensions.
//     Other fields in root blocks are silently ignored.
//
//   - CLI flag precedence: CLI flags (--title, --description, --id)
//     override root annotation values, matching the upstream's behavior
//     of applying CLI values after root schema propagation.
//
//   - Comment-based descriptions: Non-annotation comments outside @schema
//     blocks become the description when no explicit description is set
//     in the block. Only comments after the last blank line are used,
//     matching the upstream's leadingCommentsRemover regex behavior. The
//     upstream regex ((?s)(?m)(?:.*\n{2,})+) greedily strips everything
//     before the last double-newline; our implementation achieves the
//     same result by finding the last blank line in the collected
//     description lines and using only lines after it. (Note: the line
//     joining character differs -- see the "Description line joining"
//     divergence above.)
//
//   - Helm-docs prefix stripping: The "-- " prefix from helm-docs style
//     comments is stripped from descriptions. Helm-docs @tag lines (e.g.,
//     @ignored, @default, @param) are also filtered out. The upstream uses
//     a generic helmDocsTagsRemover regex matching any @\w+ pattern; our
//     implementation filters a specific set of known annotation tags via
//     [IsAnnotationComment]. The practical effect is identical for all
//     known annotation styles.
//
//   - Full Draft 7 field support: All fields supported by the upstream
//     are supported here, including composition keywords (anyOf, oneOf,
//     allOf, not), conditional schemas (if/then/else), dependencies
//     (both string-array and schema forms), definitions, $defs, and the
//     full set of constraints.
//
//   - x-* custom annotations: Custom annotation keys prefixed with x-
//     are preserved in the schema's Extra map, matching the upstream's
//     CustomAnnotations handling.
//
//   - Type arrays and null handling: The type field accepts both a single
//     string and an array of strings (e.g., [string, "null"]). Unquoted
//     null in type arrays (which YAML deserializes as Go nil) is converted
//     to the "null" type string, matching upstream's !!null tag handling.
//     Bare scalar type: null (without quotes) produces an empty string
//     from YAML unmarshal, which is treated as "no type set" and falls
//     through to type inference, consistent with upstream.
//
//   - const: null preserved: An explicit const: null annotation emits
//     "const": null in the JSON output. The upstream achieves this via a
//     constWasSet flag; we achieve it via [magicschema.ConstValue] which
//     returns a non-nil *any pointer to nil.
//
//   - Required field duality: The required field is interpreted as a
//     boolean (signals parent inclusion via HasRequired *bool) when given
//     a bool, or as a standard JSON Schema required array (set on the
//     schema's Required field) when given a list of strings.
//
//   - Single-element type array normalization: When the type field is an
//     array with exactly one element, it is stored as a single Type string
//     rather than a Types slice, matching upstream's MarshalJSON behavior
//     for StringOrArrayOfString.
//
//   - additionalItems boolean support: The additionalItems field accepts
//     both boolean values and schema objects, matching upstream's
//     SchemaOrBool type. Boolean true allows any additional items (emits
//     true), boolean false disallows them (emits false).
//
//   - Content line stripping: Content lines inside blocks are stripped
//     using the same approach as upstream: remove the "#" prefix twice
//     (handling both "# " and "## "), then remove one leading space. This
//     ensures YAML indentation inside blocks is preserved correctly.
//
//   - Alias resolution: YAML alias nodes (*name) are resolved to their
//     anchor targets before schema generation, matching upstream's alias
//     dereferencing behavior. This is handled by the generator, not the
//     annotator.
//
//   - Object property recursion: When an annotation specifies type: object
//     but no properties, the generator recurses into the YAML mapping's
//     children to infer properties. When explicit properties are set in
//     the annotation block, the YAML structure is not used. This matches
//     the upstream's behavior in YamlToSchema.
//
//   - Array items inference: When an annotation specifies type: array
//     but no items, the generator infers items from the sequence values.
//     When explicit items are set in the annotation block, the YAML
//     values are not used. This matches upstream behavior.
//
// [dadav/helm-schema]: https://github.com/dadav/helm-schema
package dadav
