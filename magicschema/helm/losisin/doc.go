// Package losisin implements an annotator for the inline annotation format
// defined by [helm-values-schema-json].
//
// Example values.yaml annotations:
//
//	# @schema type:string;required;minLength:1
//	name: my-release
//
//	# @schema type:[integer, null];minimum:0;maximum:100
//	replicas: 3
//
//	# @schema type:string;enum:[ClusterIP, NodePort, LoadBalancer]
//	serviceType: ClusterIP
//
// # Upstream Behavior
//
// helm-values-schema-json is a Helm plugin that generates JSON Schema from
// Helm values.yaml files using inline comment annotations. Annotations use
// the format:
//
//	# @schema key:value;key:value;...
//
// Semicolons separate key:value pairs; colons separate keys from values.
// Values containing colons are split only on the first colon, so patterns
// like "pattern:^https?://" work correctly.
//
// Only single-hash comments are recognized. The upstream parser calls
// [strings.TrimPrefix] with "#" (removing exactly one hash character), then
// [strings.TrimSpace] on the result, then checks for the "@schema" prefix.
// A double-hash comment like "## @schema type:string" becomes "# @schema
// type:string" after the single-hash strip, which retains a leading "#" and
// fails the "@schema" prefix check. This means bitnami-style "## @param"
// comments never accidentally trigger the inline annotator. A bare "#@schema
// type:string" (no space between hash and @) works because TrimSpace removes
// the leading space that TrimPrefix left behind.
//
// The "@schema" keyword must be followed by whitespace (or end of string)
// to avoid matching prefixes like "@schemafoo". The upstream checks this by
// comparing the length of the content before and after TrimSpace -- if no
// whitespace was trimmed, the match is rejected. A bare "# @schema" line
// with no key:value content is also rejected by this check (both before and
// after TrimSpace are empty strings with equal length). A line like
// "# @schema " (trailing space only) is similarly rejected.
//
// Boolean keys (required, hidden, readOnly, uniqueItems, skipProperties,
// mergeProperties) may omit the ":true" suffix -- a bare keyword like
// "# @schema required" is equivalent to "# @schema required:true". The
// upstream only accepts the exact lowercase strings "true" and "false";
// anything else (including "TRUE", "True", "yes", "1") causes a hard error.
// An empty value or whitespace-only value (e.g., from a bare keyword with
// no colon) defaults to true.
//
// Multiple "# @schema" comment lines for the same YAML key are collected
// and processed together. The upstream gathers comments from YAML AST nodes
// in this order:
//
//  1. keyNode.HeadComment: split by [SplitHelmDocsComment] which finds
//     the last comment group (delimited by blank lines, i.e., "\n\n" in the
//     raw comment string). Only the final group is considered for @schema
//     annotations. When helm-docs mode is enabled, lines after the "# --"
//     helm-docs delimiter are separated out; otherwise they remain in the
//     schema comment list.
//  2. keyNode.LineComment: appended as a single comment line.
//  3. valNode.LineComment: appended as a single comment line.
//  4. keyNode.FootComment: split by "\n" and appended as individual lines.
//
// Note: the last-comment-group behavior only applies to head comments. Foot
// comments, key line comments, and value line comments are not subject to
// grouping.
//
// If a key appears multiple times across collected annotations (e.g.,
// "type:string" in one line and "type:integer" in another), the last value
// wins (overwrite semantics). The upstream processes all annotations
// sequentially, with each key:value pair directly mutating the schema
// struct, so later assignments naturally override earlier ones.
//
// The upstream splits annotation lines on semicolons naively (no bracket
// awareness), meaning a value containing a literal semicolon like
// "patternProperties:{\"^x-\": {type: string; pattern: foo}}" would be
// incorrectly split at the inner semicolons. See the Intentional Divergences
// section for our improvement.
//
// After all key:value pairs are parsed, the upstream splits on ";" first,
// then on ":" for each pair. Trailing semicolons produce empty key:value
// pairs which fall through to the "unknown annotation" error handler,
// causing a hard error for input like "# @schema type:string;".
//
// ## List Parsing
//
// The upstream tool uses a two-phase list parser (processList) for type,
// enum, examples, item, and itemEnum values:
//
//  1. If the value starts with "[", try YAML array parsing first.
//  2. If YAML parsing fails (or the value does not start with "["), fall
//     back to comma-separated splitting. Brackets are stripped first if
//     present, then the result is split on commas.
//
// For "stringsOnly" lists (type and item), all parsed values from the YAML
// path are coerced to strings via convertScalarsToString -- nil becomes the
// string "null", numbers and booleans become their [fmt.Sprint] representation,
// and nested []any slices are recursively converted. For "any" lists (enum,
// examples, itemEnum), native YAML types are preserved during YAML parsing,
// while the comma-split fallback preserves null as JSON null (only when not
// in stringsOnly mode) and treats other values as strings.
//
// The comma-split fallback path handles quoted strings: values starting with
// a double quote are unquoted via [strconv.Unquote]; other values have
// surrounding double quotes stripped. Importantly, the fallback does NOT
// skip empty items -- a trailing comma like "a, b," produces ["a", "b", ""],
// including the trailing empty string as an element.
//
// This means both bracket and non-bracket forms are valid:
//
//	# @schema type:[string, null]     # bracket form (YAML parsed)
//	# @schema type:string, null       # comma-separated form (fallback)
//	# @schema enum:[ClusterIP, null]  # bracket form (types preserved)
//	# @schema enum:ClusterIP, null    # comma form (null preserved, rest strings)
//
// Type arrays with a single element are unwrapped to a plain string.
//
// ## Numeric Parsing
//
// The upstream uses [strconv.ParseFloat] for float64 constraints (minimum,
// maximum, multipleOf) and [strconv.ParseUint] with base 10 for unsigned
// integer constraints (minLength, maxLength, minItems, maxItems,
// minProperties, maxProperties). This means:
//
//   - Float constraints accept standard decimal notation only. YAML-specific
//     float forms like .inf, -.inf, and .nan are NOT accepted (they produce
//     hard parse errors).
//   - Integer constraints accept base-10 digits only. Hex (0x1F), octal
//     (0o17), and binary (0b1111) are NOT accepted. Negative values are
//     explicitly rejected with a "negative values not allowed" error before
//     reaching ParseUint. Float values (e.g., "1.5") are also rejected.
//   - Both float and integer constraints accept "null" as a special value
//     that clears the constraint (sets the pointer to nil), allowing a
//     later annotation to reset an earlier one.
//   - Empty strings and non-numeric values produce hard errors.
//
// ## processObjectComment
//
// The upstream uses a generic processObjectComment[T] function for fields
// that accept arbitrary YAML values: default, const, patternProperties,
// itemProperties, additionalProperties (non-empty), unevaluatedProperties
// (non-empty), allOf, anyOf, oneOf, and not. This function:
//
//   - Rejects empty strings with a "missing value" error.
//   - YAML-unmarshals the string into a new T value.
//   - Performs a full replacement (not merge) of the destination.
//
// Since it YAML-unmarshals into the upstream's *Schema type which implements
// yaml.Unmarshaler, boolean YAML values ("true"/"false") are correctly
// decoded as boolean schemas (SchemaTrue/SchemaFalse) for fields like
// additionalProperties. Invalid YAML produces a hard error.
//
// ## Supported Annotation Keys
//
// The upstream tool supports the following annotation keys:
//
//   - type: JSON Schema type string, bracket-delimited array (e.g.,
//     "[string, null]"), or comma-separated list (e.g., "string, null").
//     Type strings are validated against the set of valid JSON Schema types
//     (array, boolean, integer, null, number, object, string) during the
//     compliance pass; invalid types cause a hard error. Duplicate types in
//     a type array are also rejected.
//   - title, description: metadata strings (direct assignment, no parsing).
//   - default: default value parsed via processObjectComment. Supports
//     scalars, objects, and arrays. An empty value (bare "default:" with
//     nothing after the colon) is a hard error.
//   - enum: array of allowed values. Supports both bracket-delimited YAML
//     arrays and comma-separated fallback. Preserves native types: integers
//     remain integers, booleans remain booleans, null is preserved as JSON
//     null.
//   - const: constant value parsed via processObjectComment. JSON Schema
//     allows any value including null. An empty value is a hard error.
//   - examples: array of example values. Same list parsing as enum
//     (preserves native types).
//   - pattern: regex pattern string for string validation (direct
//     assignment).
//   - minimum, maximum: numeric range constraints (float64 via
//     [strconv.ParseFloat]). Accept "null" to clear the constraint.
//     Invalid numbers are hard errors.
//   - multipleOf: numeric multiple constraint (float64). Must be strictly
//     greater than zero. Accepts "null" to clear. Values <= 0 and invalid
//     numbers are hard errors.
//   - minLength, maxLength: string length constraints (uint64 via
//     [strconv.ParseUint] base 10). Negative values, non-integers, and
//     empty strings are hard errors. Accept "null" to clear.
//   - minItems, maxItems: array size constraints. Same uint64 parsing as
//     length constraints.
//   - minProperties, maxProperties: object size constraints. Same uint64
//     parsing as length constraints.
//   - uniqueItems: boolean, whether array items must be unique.
//   - required: boolean, marks this property as required in its parent's
//     required array.
//   - readOnly: boolean.
//   - $id: schema identifier URI (direct assignment, no validation).
//   - $ref: schema reference URI (direct assignment). Upstream supports
//     $k8s shorthand expansion (see below) and Draft 7 allOf wrapping
//     (see below).
//   - additionalProperties: when the value is empty or whitespace-only,
//     sets to true (SchemaTrue). Otherwise parsed via processObjectComment
//     which YAML-unmarshals the value -- "true" and "false" strings produce
//     boolean schemas, and YAML objects like {type: string} produce schema
//     objects. Invalid YAML is a hard error.
//   - unevaluatedProperties: same handling as additionalProperties.
//   - patternProperties: map of regex pattern to schema object, parsed via
//     processObjectComment (YAML unmarshal). Empty value is a hard error.
//   - allOf, anyOf, oneOf: arrays of sub-schemas for composition, parsed
//     via processObjectComment (YAML unmarshal into []*Schema). Empty
//     values are hard errors.
//   - not: sub-schema for negation, parsed via processObjectComment.
//   - item: convenience shortcut to set items.type on array schemas.
//     Uses the same list parsing as type (stringsOnly). Supports both
//     single types and type arrays. Creates the Items schema if nil.
//   - itemProperties: shortcut to set items.properties from a YAML object
//     via processObjectComment. Creates the Items schema if nil.
//   - itemEnum: shortcut to set items.enum. Uses the same list parsing as
//     enum (preserves native types). Creates the Items schema if nil.
//   - itemRef: shortcut to set items.$ref (direct assignment). Creates
//     the Items schema if nil.
//   - hidden: boolean, excludes the property (and its children) from schema
//     output entirely. Works on any node type (scalars, arrays, objects).
//   - skipProperties: boolean, strips the properties map from an object
//     schema while preserving all other fields (type, additionalProperties,
//     constraints, etc.). Only effective when the type is "object". Applied
//     both during child processing and after comment processing.
//   - mergeProperties: boolean, merges all child property schemas together
//     (using the upstream's mergeSchemas algorithm) and assigns the merged
//     result to additionalProperties, then removes the properties map.
//     Useful for generating additionalProperties schemas from example
//     entries (e.g., labels maps). Only effective when there are ordered
//     child properties.
//
// The upstream tool treats unknown annotation keys as hard errors
// (fmt.Errorf("unknown annotation %q", key)).
//
// ## Type and Composition Interaction
//
// When composition keywords (allOf, anyOf, oneOf, not) or const are present,
// the upstream tool removes the type field from the schema during a
// compliance pass (ensureCompliantRec). This is done after all annotations
// are parsed and the full schema tree is built. The rationale is that type
// and these keywords are considered to collide. The same compliance pass
// validates type strings against the allowed set, checks for duplicate types,
// and optionally sets additionalProperties:false when the
// --no-additional-properties flag is used.
//
// ## Draft 7 $ref Compliance
//
// In JSON Schema Draft 7, a $ref keyword causes all sibling keywords to be
// ignored. The upstream tool detects schemas that have both $ref and other
// non-zero fields (checked via IsZero on a clone with $ref cleared), and
// wraps them in allOf to produce compliant output:
//
//	{"$ref": "...", "type": "string"}
//
// becomes:
//
//	{"allOf": [{"type": "string"}, {"$ref": "..."}]}
//
// During this wrapping, $defs and definitions are preserved at the root
// level (not moved into allOf), and internal #/ references (except those
// targeting #/$defs/ or #/definitions/) are updated to prepend #/allOf/0/.
// This wrapping only applies to drafts 4, 6, and 7; drafts 2019 and 2020
// allow $ref to coexist with other keywords.
//
// ## $k8s Shorthand
//
// The upstream tool supports $k8s shorthand in $ref values. A reference like
// "$k8s/_definitions.json#/definitions/io.k8s..." is expanded using a Go
// text/template with a configurable URL pattern (default: yannh/kubernetes-
// json-schema on GitHub) and schema version from the --k8s-schema-version
// flag. The expansion requires this flag to be set; otherwise it is a hard
// error. The $k8s prefix must be followed by a path (bare "$k8s" or "$k8s/"
// alone are hard errors).
//
// ## Schema Bundling
//
// The upstream tool can bundle referenced schemas into $defs (via --bundle
// flag), resolving file-based and URL-based $ref values and inlining the
// referenced schemas. This involves loading schemas from disk or URLs
// (with caching), assigning $id values, moving any $defs/$definitions from
// loaded schemas to the root, and rewriting $ref to use local #/$defs/name
// pointers. Unused $defs entries are removed after bundling.
//
// ## Global Property Injection
//
// When the root schema's additionalProperties is restricted (not nil, not
// true, and not a schema that allows objects), the upstream tool
// automatically adds a "global" property with type ["object", "null"] and a
// $comment explaining it was auto-generated. This prevents conflicts when
// the chart is used as a Helm dependency (Helm's special "global" values
// key). The injection is skipped when:
//
//   - The "global" property already exists in the root schema.
//   - additionalProperties is nil (not set) or true.
//   - additionalProperties is a non-boolean schema with type "object" or
//     no type (which allows any type).
//
// This can be disabled with --no-default-global.
//
// ## Unsupported Upstream Annotation Keys
//
// The following JSON Schema fields exist in the upstream's Schema struct and
// are handled during schema merging and the compliance pass, but cannot be
// set via @schema comment annotations:
//
//   - format
//   - deprecated (note: the upstream JSON tag is misspelled as "decrecated",
//     though the YAML tag is correct)
//   - writeOnly
//   - exclusiveMinimum, exclusiveMaximum
//   - if, then, else (though these can appear in nested schemas passed to
//     composition keywords like allOf)
//   - $comment, $anchor, $dynamicAnchor, $dynamicRef
//   - contentEncoding, contentMediaType, contentSchema
//   - maxContains, minContains, contains
//   - prefixItems, additionalItems, unevaluatedItems
//   - propertyNames, dependentRequired, dependentSchemas, dependencies
//   - $defs, definitions (populated only by the bundling system)
//
// These fields can appear in the output only when set via nested schema
// objects (e.g., inside allOf, additionalProperties, or other composition
// keywords that accept full schema objects via processObjectComment).
//
// # Intentional Divergences
//
// This implementation follows the magicschema design principles of failing
// open and providing best-effort schema generation. The following behaviors
// intentionally differ from the upstream tool:
//
//   - Unknown annotation keys produce a warning log and are skipped, rather
//     than causing a hard error. This follows the magicschema principle of
//     silently handling unparseable annotations.
//
//   - Empty key:value pairs from trailing semicolons are silently skipped.
//     Upstream treats trailing semicolons as producing an empty key which
//     falls through to the "unknown annotation" error. Our implementation
//     skips empty pairs entirely.
//
//   - Invalid parse values (non-numeric strings for numeric fields, negative
//     values for unsigned fields, empty strings for processObjectComment
//     fields) are silently skipped rather than returning errors. The upstream
//     tool produces hard errors for these. Our fail-open approach means a
//     typo like "minLength:abc" simply does not set the constraint rather
//     than aborting generation.
//
//   - Boolean values are matched case-insensitively ("TRUE" and "True" are
//     accepted as "true"). The upstream strictly requires lowercase "true"
//     and "false" only; all other values including case variants produce
//     hard errors. For unrecognized non-boolean values (anything other than
//     true/false case-insensitively), our implementation treats them as
//     false (fail-open: don't add restrictions for garbage input).
//
//   - Numeric constraints (minLength, maxLength, minItems, maxItems,
//     minProperties, maxProperties) are parsed via YAML unmarshal into int
//     rather than [strconv.ParseUint] with base 10. This means YAML-native
//     integer formats like hex (0x1F), octal (0o17), and binary (0b1111)
//     are accepted, whereas the upstream only accepts base-10 integers.
//     Float values are also accepted and truncated to integers by
//     goccy/go-yaml (e.g., "1.5" becomes 1), whereas upstream would reject
//     them with a hard parse error.
//
//   - Float constraints (minimum, maximum, multipleOf) are parsed via YAML
//     unmarshal rather than [strconv.ParseFloat]. This means YAML-native
//     float forms like .inf, -.inf, and .nan are accepted, whereas the
//     upstream would reject them with a parse error.
//
//   - Type is preserved alongside composition keywords (allOf, anyOf, oneOf,
//     not) and const. The upstream strips type in these cases during the
//     compliance pass. We keep type because it is valid in Draft 7 and
//     provides more useful information to consumers (e.g., editors can
//     provide better completions when they know the base type even with
//     composition constraints).
//
//   - additionalProperties defaults to true on objects (fail-open) when set
//     by the magicschema generator. The upstream tool does not set
//     additionalProperties by default (nil/omitted), but optionally sets it
//     to false on all object schemas when the --no-additional-properties
//     flag is used. The magicschema design mandates fail-open behavior:
//     generated schemas should help users, not block them.
//
//   - default and const with empty values are silently treated as null.
//     The upstream uses processObjectComment which rejects empty strings
//     with a "missing value" error. Our implementation treats an empty
//     value as YAML null, which is a valid JSON Schema value for both
//     fields.
//
//   - $ref values are preserved as-is. The upstream's $k8s shorthand
//     expansion, file-based $ref resolution, schema bundling, and Draft 7
//     allOf wrapping are not performed. These are features of the upstream
//     CLI tool rather than the annotation format itself.
//
//   - Type strings are not validated against the allowed JSON Schema type
//     set. The upstream validates types during the compliance pass and
//     rejects invalid type strings. Invalid types in our output will pass
//     through and may be caught by downstream schema consumers. This avoids
//     blocking schema generation over minor annotation typos.
//
//   - Semicolons inside bracket-delimited values ({} or []) are not treated
//     as pair separators. The upstream naively splits on all semicolons via
//     [strings.SplitSeq], which means complex values like
//     "patternProperties:{\"^x-\": {type: string; pattern: foo}}" would be
//     incorrectly split at the inner semicolons. Our bracket-aware splitting
//     preserves these values correctly.
//
//   - Empty items from trailing commas in list values are skipped. The
//     upstream's comma-split fallback produces empty string elements for
//     trailing commas (e.g., "a, b," → ["a", "b", ""]). Our implementation
//     filters out empty items.
//
//   - The enum comma-split fallback treats all non-null values as strings.
//     The upstream comma-split fallback also treats non-null non-quoted
//     values as strings (stripping surrounding quotes), so the behavior is
//     consistent for common cases. Numeric and boolean enum values are only
//     preserved as native types when using the bracket form (which triggers
//     YAML parsing in both implementations).
//
//   - The global property injection (adding "global" with type
//     ["object", "null"]) is not performed. This is a Helm-specific
//     convenience that is outside the scope of a generic annotation parser.
//
// [helm-values-schema-json]: https://github.com/losisin/helm-values-schema-json
package losisin
