// Package bitnami implements an annotator for the ## @param line annotations
// used by the [bitnami/readme-generator-for-helm] project.
//
// Example values.yaml annotations:
//
//	## @param replicaCount Number of replicas
//	replicaCount: 1
//
//	## @param image.registry [string] Container image registry
//	## @param image.repository Container image name
//	image:
//	  registry: docker.io
//	  repository: bitnami/nginx
//
//	## @skip secretConfig
//	secretConfig:
//	  passwordRef: {}
//
// # Upstream Behavior
//
// The Bitnami readme-generator uses double-hash (##) comment lines with tag
// directives to annotate Helm values files. The primary tags are:
//
//   - ## @param <keyPath> [modifiers] <description>
//   - ## @skip <keyPath>
//   - ## @section <name>
//   - ## @descriptionStart / ## @descriptionEnd
//   - ## @extra <keyPath> [modifiers] <description>
//
// The comment format (##) and tag names (@param, @skip, etc.) are configurable
// upstream via config.json. The default config.json defines:
//
//	{
//	    "comments": { "format": "##" },
//	    "tags": { "param": "@param", "section": "@section",
//	              "descriptionStart": "@descriptionStart",
//	              "descriptionEnd": "@descriptionEnd",
//	              "skip": "@skip", "extra": "@extra" },
//	    "modifiers": { "array": "array", "object": "object",
//	                   "string": "string", "nullable": "nullable",
//	                   "default": "default" },
//	    "regexp": { "paramsSectionTitle": "Parameters" }
//	}
//
// These defaults are used by all known Bitnami charts. Our implementation
// hardcodes these defaults.
//
// Parameter key paths use dot notation for nesting (e.g., "image.registry")
// and bracket notation for array indices (e.g., "jobs[0].nameOverride").
//
// Supported modifiers appear in brackets as a comma-separated list:
//
//   - Type modifiers: string, array, object (these are the only type
//     modifiers supported upstream; boolean, number, and integer are NOT
//     valid upstream modifiers)
//   - nullable: marks the parameter as nullable
//   - default: VALUE: overrides the displayed default value
//
// The upstream @param regex is (with default config.json values):
//
//	^\s*##\s*@param\s*([^\s]+)\s*(\[.*?\])?\s*(.*)$
//
// Note: the upstream uses \s* (zero or more whitespace) before the key path
// capture group, so theoretically "## @paramfoo" would match. Our regex uses
// \s+ (one or more) which is stricter but functionally equivalent since all
// real-world annotations have at least one space after @param. Similarly,
// the upstream captures the key path with [^\s]+ which is equivalent to our
// \S+.
//
// The upstream @skip regex follows the same pattern:
//
//	^\s*##\s*@skip\s*([^\s]+)\s*(.*)$
//
// The upstream @extra regex is structurally identical to @param:
//
//	^\s*##\s*@extra\s*([^\s]+)\s*(\[.*?\])?\s*(.*)$
//
// Upstream parses @extra's modifier brackets and description from the regex,
// but only stores the description on the Parameter object (modifiers are
// captured by the regex but discarded). The Parameter gets extra=true
// (validate=false, readme=true), an empty string value, and is later
// filtered out from schema output by the !p.extra check in
// renderOpenAPISchema.
//
// Modifiers are extracted by stripping brackets and splitting on commas.
// Upstream extraction: paramMatch[2].split('[')[1].split(']')[0], then
// split on ',', filter empties, and trim each element. The bracket group
// in the regex is non-greedy (\[.*?\]), so brackets appearing later in
// the description (e.g., "Description with [brackets] in it") are not
// captured as modifiers -- only brackets immediately following the key path
// (with optional whitespace) are matched.
//
// Unknown modifiers cause a hard error in the upstream tool
// (throw new Error("Unknown modifier: ...") in applyModifiers).
//
// Because modifiers are comma-separated, a [default: VALUE] where VALUE
// itself contains commas will be split incorrectly. For example,
// [default: [a, b]] is parsed as two modifiers: "default: [a" and "b]".
// The first produces a default value of "[a" (which may fail YAML parsing),
// and the second is treated as an unknown modifier (hard error upstream,
// silently ignored by us). This is a limitation of the comma-separated
// format shared by both the upstream tool and our implementation. Values
// containing colons but no commas work correctly (e.g.,
// [default: https://example.com]).
//
// ## Schema Generation Pipeline
//
// The upstream tool generates OpenAPI 3.0-style schemas (not JSON Schema
// Draft 7). Its pipeline works as follows:
//
//  1. Parse YAML (createValuesObject): The YAML file is parsed and flattened
//     to dot-notation key paths using the dot-object library. Each leaf value
//     produces a Parameter with its JavaScript typeof type ("string", "number",
//     "boolean", "object") and Array.isArray for arrays. Notably, JavaScript
//     typeof does not distinguish integer from number -- all numeric values
//     become "number". YAML null values are mapped to the string 'nil'
//     internally. YAML keys containing dots (e.g., "prometheus.io/scrape")
//     cannot be resolved via lodash's standard dot-notation _.get, so the tool
//     falls back to a custom getArrayPath resolver and marks those parameters
//     with schema=false, excluding them from schema output. Plain arrays
//     (arrays containing only strings) are collapsed: instead of generating
//     separate entries for each array element (e.g., "items[0]", "items[1]"),
//     a single entry is created for the array key with the full array as its
//     value.
//
//  2. Parse comments (parseMetadataComments): The file is scanned line-by-line
//     for ## @param, ## @skip, ## @section, ## @extra, and
//     ## @descriptionStart/End annotations. Each @param creates a Parameter
//     with modifiers and description. Each @skip creates a Parameter with
//     skip=true (validate=false, readme=false). Each @extra creates a
//     Parameter with extra=true (validate=false, readme=true) and an empty
//     string value. All parsed parameters are appended to an array (not a
//     map), so duplicate key paths create multiple entries.
//
//  3. Combine (combineMetadataAndValues): Parsed comment Parameters are
//     matched to YAML-derived Parameters by key path. For each comment param
//     (except @extra), findIndex locates the first matching YAML param and
//     copies its type, value (only when the comment param's value is falsy,
//     via "if (!param.value)"), and schema flag. Only the first matching
//     YAML param is used. For @extra params, matching is skipped entirely
//     since they have no corresponding YAML key. YAML leaf keys without any
//     comment annotation are added to the parameter list with skip=true
//     (via the Parameter.skip setter, which sets validate=false,
//     readme=false) but schema=true. Despite schema=true, these params are
//     filtered out by the skip filter in step 4 (buildParamsToRenderList
//     filters !p.skip) and never reach schema generation. Note: unannotated
//     YAML keys are spliced into the metadata array at the position of the
//     first matching prefix param, which affects ordering but not schema
//     content.
//
//  4. Build render list (buildParamsToRenderList): applyModifiers processes
//     each parameter's modifier list, then the list is filtered to remove all
//     skip=true Parameters. applyModifiers handles the recognized modifiers:
//
//     Type modifiers (string, array, object) change the parameter's type AND
//     its display value: [string] sets value to '""', [array] to '[]',
//     [object] to '{}'. However, when nullable is the LAST modifier in the
//     list (determined by: findIndex(nullable)+1 === modifiers.length), the
//     type modifiers do NOT change the value (the isNullableLastModifier
//     guard skips value assignment). This is a special case for showing the
//     real YAML value in README tables while still changing the schema type.
//     The [default: VALUE] modifier overrides the value unconditionally
//     regardless of modifier ordering. It is detected via a regex match
//     (modifier.match(/default:.*/)) in a case expression, then the
//     "default:\s*" prefix is stripped via String.replace to extract the
//     raw value string. This means "default:" with any trailing content is
//     recognized, and the value is everything after "default:" with leading
//     whitespace stripped.
//     The [nullable] modifier sets the value to 'nil' if it was previously
//     undefined. This filtering is applied BEFORE schema rendering.
//
//  5. Render schema (renderOpenAPISchema): Before generating the schema tree,
//     a validation step checks for 'nil'-valued params that lack the
//     [nullable] modifier and throws a hard error if any are found
//     ("Invalid type 'nil' for the following values: ..."). Then:
//     - Nullable params with 'nil' values have their value converted to the
//     string 'null' (which becomes JSON null in output via generateSchema).
//     - All nullable params get a nullable=true property set.
//     - Type modifiers (string, array, object) are re-applied to the type
//     field (a second pass independent of applyModifiers in step 4).
//     The params are then filtered to exclude:
//     (a) @extra parameters (p.extra === true),
//     (b) parameters with the [object] modifier (summary entries for README;
//     leaf children provide the real schema),
//     (c) parameters with undefined values (e.g., modifier-only entries
//     where no YAML key matched),
//     (d) parameters with schema=false (dot-containing YAML keys).
//     The filtered list is turned into a nested schema by splitting each key
//     path on "." and building the tree via generateSchema.
//
//  6. Schema output format (generateSchema/createValuesSchema): The root
//     schema has title "Chart Values", type "object", and a properties map.
//     No $schema URI is included (the output is not a valid JSON Schema
//     document but rather an OpenAPI 3.0-style schema object). Each leaf
//     property gets type, description, and default (set to the YAML value or
//     the modifier-overridden value). Nullable params get "nullable: true"
//     (OpenAPI 3.0 style, not the Draft 7 type-array approach). Array items
//     get an items schema inferred from the first element's JavaScript typeof
//     type, or an empty items schema for empty/null arrays. When a key path
//     contains an array index (e.g., "jobs[0].name"), generateSchema creates
//     an array schema with items.type "object" and recurses into
//     items.properties with ignoreDefault=true, so properties nested inside
//     arrays of objects do NOT get a default property.
//
// The upstream tool treats JavaScript null values (from YAML null/~) as 'nil'
// internally and requires the [nullable] modifier for them. A null value
// without [nullable] causes a hard error in renderOpenAPISchema ("Invalid type
// 'nil' for the following values: ..."). With [nullable], the null is converted
// to the string 'null' as the default value (which serializes as JSON null).
// Non-null values with [nullable] keep their original value.
//
// Multi-line @param descriptions are not supported upstream; each @param must
// be on a single line. Section descriptions can span multiple lines using the
// @descriptionStart/@descriptionEnd block syntax. The @descriptionStart tag
// can optionally include text on the same line, which becomes the first line
// of the description.
//
// When multiple @param annotations target the same key path, all entries are
// added to the upstream parameter list (it is an array, not a map). For
// combining with YAML values, findIndex returns the first match, so the
// first annotation gets its type/value filled from YAML. For schema output,
// generateSchema iterates all parameters and writes to a nested tree, so
// later entries overwrite earlier ones at the same property -- effectively the
// last annotation wins for the schema.
//
// # Our Implementation
//
// This annotator parses the same ## @param and ## @skip annotations to produce
// [magicschema.AnnotationResult] values with [jsonschema.Schema] fragments.
//
// Our @param regex is:
//
//	^\s*##\s*@param\s+(\S+)\s*(?:\[(.*?)\])?\s*(.*)$
//
// Our @skip regex is:
//
//	^\s*##\s*@skip\s+(\S+)
//
// Both use \s+ (one or more whitespace) before the key path, compared to
// the upstream's \s* (zero or more). This is stricter but functionally
// equivalent since all real-world annotations include at least one space
// after the tag name. Our @param regex uses a non-capturing group for the
// optional bracket modifier, (?:\[(.*?)\]), to avoid polluting match groups
// and captures only the bracket content without the brackets themselves.
// The upstream instead captures the full bracketed string (\[.*?\]) and
// strips brackets in JavaScript via split('[')[1].split(']')[0].
//
// Tags that are only relevant for README generation (@section,
// @descriptionStart, @descriptionEnd, @extra) are matched by a separate
// ignored-tag regex and skipped during Prepare to prevent misparsing them
// as @param lines.
//
// Array indices in key paths are stripped during normalization
// ("jobs[0].nameOverride" becomes "jobs.nameOverride") so that annotations
// match the dot-separated key paths used by the magicschema generator's AST
// walker.
//
// # Intentional Divergences
//
// The following behaviors intentionally differ from the upstream tool:
//
//   - Nullable representation: We use JSON Schema Draft 7 type arrays
//     (e.g., ["string", "null"]) instead of OpenAPI 3.0's "nullable": true.
//     This produces valid Draft 7 schemas rather than OpenAPI-specific output.
//
//   - @skip semantics: Upstream removes the skipped key itself from both the
//     README and the schema (via buildParamsToRenderList filtering).
//     Unannotated children of a skipped parent are also excluded from the
//     upstream schema because they are YAML-derived parameters that get
//     skip=true from combineMetadataAndValues. However, children that have
//     their own explicit @param annotations DO appear in the upstream schema,
//     since they are added to the metadata independently and are not marked
//     as skip. Our implementation sets Skip: true on the
//     [magicschema.AnnotationResult], which causes the generator to omit the
//     key and its entire subtree from the output schema -- including children
//     with their own @param annotations, since those children are never
//     visited by the AST walker. In practice, the difference is minimal
//     because @skip is typically applied to leaf keys or to objects whose
//     children are not individually annotated.
//
//   - Unknown modifiers: We silently ignore unrecognized modifiers rather than
//     raising an error. This follows the magicschema best-effort principle of
//     extracting as much information as possible without failing.
//
//   - Extended type modifiers: We accept number, integer, and boolean as type
//     modifiers in addition to the upstream's string, array, and object. This
//     provides more precise JSON Schema types when chart authors include them.
//     Upstream only recognizes string, array, and object in the applyModifiers
//     switch; other values fall through to the default case which throws
//     "Unknown modifier".
//
//   - [object] modifier in schema: Upstream excludes parameters with the
//     [object] modifier from the schema entirely (they serve as README summary
//     entries whose leaf children provide the real schema). We include them
//     and emit type: "object" since our tool generates schema only, not README
//     tables, and the type information is useful for schema consumers.
//
//   - No default values from YAML values: Upstream sets the "default" property
//     on every leaf schema node to the YAML value (e.g., replicaCount: 1
//     produces "default": 1). We only set "default" when explicitly specified
//     via the [default: VALUE] modifier. The magicschema generator may
//     separately add defaults from YAML values during structural inference,
//     but the annotator itself does not.
//
//   - Default values inside arrays: Upstream strips default values from
//     properties nested inside arrays of objects (ignoreDefault=true in
//     generateSchema when processing array index paths). We preserve default
//     values regardless of nesting depth when specified via the [default:
//     VALUE] modifier, as they provide useful hints to schema consumers.
//
//   - Nullable-last value preservation: Upstream has a special case where if
//     nullable is the last modifier in the list, the type modifiers (string,
//     array, object) do not change the parameter's display value (they still
//     change the type). This hack preserves the real YAML value for README
//     tables while changing the schema type. Since our annotator does not set
//     default values from YAML values (only from explicit [default: VALUE]
//     modifiers), this modifier-ordering distinction has no effect on our
//     output and is not replicated.
//
//   - Null value errors: Upstream throws a hard error for YAML null values
//     that lack the [nullable] modifier ("Invalid type 'nil' for the following
//     values: ..."). We treat null values the same as any other value -- the
//     generator infers no type constraint for null/empty values, following the
//     magicschema fail-open principle.
//
//   - @extra annotations: Upstream creates README table entries for @extra
//     parameters (with an empty value column) but excludes them from the
//     schema (filtered by !p.extra in renderOpenAPISchema). We skip @extra
//     entirely during comment scanning since we only generate schema output.
//     The net effect on schema output is identical.
//
//   - Annotations for missing YAML keys: Upstream combines comment-parsed
//     params with YAML-parsed params by matching on key path
//     (combineMetadataAndValues). If a @param annotation references a key
//     path that doesn't exist in YAML, the param's value remains undefined
//     and it is later filtered out by renderOpenAPISchema's
//     "p.value !== undefined" check, so it never appears in the schema.
//     Our implementation maps annotations to AST nodes during the
//     generator's tree walk, so annotations for nonexistent YAML keys are
//     simply never matched and have no effect. The observable schema output
//     is equivalent: annotations without matching YAML structure are
//     silently ignored by both implementations.
//
//   - Dot-containing YAML keys: Upstream excludes parameters whose YAML keys
//     contain dots (e.g., "prometheus.io/scrape") from the schema due to a
//     path-resolution limitation (schema=false is set in createValuesObject
//     when lodash's dot-notation _.get fails and the tool falls back to the
//     custom getArrayPath resolver). We do not special-case these; if the
//     annotation key path happens to match a walker key path, the annotation
//     is applied normally.
//
//   - Type inference: Upstream uses JavaScript's typeof which maps all numeric
//     values to "number" (no integer distinction). Our implementation defers
//     type inference to the magicschema generator, which uses the YAML AST to
//     distinguish "integer" from "number". The annotator itself only sets
//     types when an explicit type modifier is present.
//
//   - Unannotated YAML keys included: Upstream only includes YAML keys that
//     have explicit @param annotations in the schema. Unannotated YAML leaf
//     keys are added to the parameter list with skip=true during
//     combineMetadataAndValues, then filtered out by buildParamsToRenderList
//     (which removes all skip=true entries) before schema generation. Our
//     implementation includes all YAML keys in the output schema via the
//     generator's structural inference, regardless of whether they have
//     @param annotations. This follows the magicschema principle that the
//     YAML structure itself is a valuable source of schema information.
//
//   - Duplicate key paths: When multiple @param annotations target the same
//     key path, the last annotation in the file wins during Prepare (since
//     our params map overwrites on duplicate keys). The upstream schema
//     output also ends up using the last annotation's values, since
//     generateSchema iterates all duplicate entries and later tree writes
//     overwrite earlier ones. The observable schema behavior is equivalent.
//
// [bitnami/readme-generator-for-helm]: https://github.com/bitnami/readme-generator-for-helm
package bitnami
