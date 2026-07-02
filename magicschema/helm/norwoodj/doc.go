// Package norwoodj implements the helm-docs annotator, parsing
// # -- description annotations as defined by the [norwoodj/helm-docs]
// project.
//
// Example values.yaml annotations:
//
//	# -- Number of replicas to deploy
//	replicas: 3
//
//	# -- (string) Container image repository
//	repository: nginx
//
//	# -- (int) Port to expose
//	port: 8080
//
// # Upstream Behavior
//
// The upstream helm-docs tool generates Markdown documentation from Helm
// chart values.yaml files. It extracts descriptions from YAML comments in
// two formats and renders them into a table of values, types, defaults,
// and descriptions.
//
// ## Comment Formats
//
//   - New-style comments: The "# --" prefix on a HeadComment (comment
//     block directly above a YAML key) marks the beginning of a
//     description for that key. The text after "# -- " becomes the
//     description. Example:
//
//     # -- Number of replicas
//     replicas: 3
//
//   - Old-style comments: A comment line matching "# key.path -- description"
//     anywhere in the file associates a description with that key path
//     (dotted for nested keys, a bare token for top-level keys).
//     These are parsed by a file-level line scanner (not the YAML AST) and
//     stored in a key-to-description map. The old-style scanner explicitly
//     checks that group 1 (the key) is non-empty, so new-style "# -- desc"
//     lines (where the key is empty) are skipped by the old-style scanner.
//
// ## Regex Patterns
//
// All regex patterns are defined in the upstream pkg/helm/chart_info.go
// and pkg/helm/comment.go:
//
//   - Key detection (valuesDescriptionRegex):
//     ^\s*#\s*(.*)\s+--\s*(.*)$
//     Group 1 captures the key path (empty for new-style), group 2
//     captures the description text. The \s+ before -- requires at least
//     one whitespace character before the double dash.
//
//   - Type hints (valueTypeRegex):
//     ^\((.*?)\)\s*(.*)$
//     Applied to the description text after extraction. Group 1 captures
//     the type string (lazy match), group 2 the remaining description.
//     Any string is accepted inside the parentheses. The upstream stores
//     the full type string verbatim (e.g., "list/csv", "string/email",
//     "k8s/storage/persistent-volume/access-modes"). Types are not
//     validated or mapped.
//
//   - Comment continuation (commentContinuationRegex):
//     ^\s*#(\s?)(.*)$
//     Group 1 captures at most one optional space character. Group 2
//     captures the rest of the line. For "#text" (no space), group 1 is
//     empty and group 2 is "text". For "#  text" (two spaces), group 1
//     is one space and group 2 is " text" (preserving the extra space).
//
//   - Raw mode (rawDescriptionRegex):
//     ^\s*#\s+@raw
//     Requires at least one whitespace between # and @raw. "#@raw"
//     (no space) does NOT activate raw mode.
//
//   - Default override (defaultValueRegex):
//     ^\s*# @default -- (.*)$
//     Requires exactly "# @default -- " with a single space between #
//     and @default. "# @default custom-val" (without --) does not match.
//
//   - Section (sectionRegex):
//     ^\s*# @section -- (.*)$
//     Requires the " -- " separator. "# @section Security" does not match
//     and falls through to continuation text.
//
//   - Notation type (valueNotationTypeRegex):
//     ^\s*#\s+@notationType\s+--\s+(.*)$
//     Requires \s+ between # and @notationType, and \s+--\s+ around the
//     separator. "# @notationType helm" (without --) does not match.
//
// ## ParseComment Algorithm
//
// The ParseComment function (pkg/helm/comment.go) is shared between
// old-style and new-style comment processing. The upstream defines a
// PrefixComment constant "# --" (pkg/helm/comment.go) used for prefix
// checks. Its logic:
//
//  1. Issue #96 workaround: Scans all lines for the last "# --" prefix
//     using [strings.HasPrefix] on the raw (untrimmed) line. If found at
//     index > 0, recursively calls ParseComment with only the lines from
//     the last "# --" onward, discarding everything before it (including
//     any @default, @raw, etc. on earlier lines). This handles cases
//     where a commented-out key's description bleeds into the next key's
//     HeadComment.
//
//  2. Finds the first match of valuesDescriptionRegex to extract the key
//     path (group 1) and initial description (group 2).
//
//  3. Applies valueTypeRegex to the description to extract an optional
//     type hint.
//
//  4. Processes continuation lines (after the description line) in order:
//     a. @raw check: if !isRaw and rawDescriptionRegex matches, set
//     isRaw = true. Only the first @raw match activates raw mode.
//     b. @default check: if defaultValueRegex matches, store the default.
//     If multiple @default lines exist, the last one wins.
//     c. @notationType check: if valueNotationTypeRegex matches, store it.
//     d. @section check: if sectionRegex matches, store the section name.
//     e. Comment continuation: match commentContinuationRegex and append
//     group 2 with " " (normal mode) or "\n" (raw mode) as separator.
//
//  5. Returns the key path and a ChartValueDescription containing
//     description, type, default, section, and notation type.
//
// ## @ignore Handling
//
// The upstream removes ignored nodes via a removeIgnored function called
// on the root yaml.Node immediately after unmarshaling (before any comment
// parsing). It iterates over rootNode.Content and checks each child node's
// HeadComment for the substring "@ignore" using [strings.Contains]. This is
// a simple substring check -- "@ignore" can appear anywhere in the
// HeadComment text, even embedded in other words. For MappingNode parents,
// when a key node has @ignore, both the key and its associated value node
// (the next Content element) are removed. For SequenceNode parents, only
// the matching item is removed. The function recurses into surviving nodes.
// Only HeadComment is checked -- LineComment and FootComment are not.
//
// ## @notationType Behavior
//
// In the upstream, @notationType serves dual purposes:
//
//   - Type fallback: For non-nil values, the upstream type priority
//     chain is: old-style ValueType > new-style ValueType >
//     @notationType > getTypeName(value). The getTypeName function
//     infers Go types (string, int, bool, float64, []interface{},
//     map[string]interface{}) from the unmarshaled value. So
//     @notationType overrides the inferred Go type but not an
//     explicit (type) hint. For nil values, the chain is: old-style
//     ValueType > new-style ValueType > "string" (hardcoded
//     default). @notationType does not participate in the nil type
//     chain. In practice, @notationType rarely changes the type
//     because most values have an inferrable Go type; it primarily
//     affects values where the display type should differ from the
//     inferred type (e.g., using "yaml" or "tpl" for string values).
//
//   - Rendering control: when @notationType is set, the default value is
//     rendered as raw text rather than JSON-encoded. For "yaml" notation,
//     the value is marshaled through yaml.Marshal. For "tpl" or any other
//     notation type, the raw YAML string value is used directly. This
//     affects list, object, and string scalar nodes.
//
//   - Only new-style @notationType is used: Although the old-style file
//     scanner collects @notationType lines and ParseComment extracts them,
//     the upstream rendering code only reads NotationType from the
//     new-style (HeadComment) autoDescription, not from the old-style
//     keysToDescriptions map.
//
// ## Old-Style File Scanner Details
//
// The upstream old-style scanner (parseChartValuesFileComments):
//
//  1. Scans line by line with a [bufio.Scanner].
//  2. When not collecting: looks for valuesDescriptionRegex match where
//     group 1 (key name) is non-empty. New-style lines are skipped.
//  3. When collecting: checks if the line matches defaultValueRegex,
//     sectionRegex, or commentContinuationRegex. If ANY of the three
//     match, the line is accumulated (OR logic). Note that @raw and
//     @notationType lines are NOT explicitly checked by the scanner,
//     but they match commentContinuationRegex (since any "# ..." line
//     does) and are thus collected into the block. ParseComment then
//     handles them during step 4's continuation processing.
//  4. A non-matching line terminates collection. The block is passed to
//     ParseComment and stored in the key-to-descriptions map (guarded
//     by a key != "" check so that new-style groups embedded in
//     old-style blocks are discarded).
//  5. If the file ends while collecting, the in-progress block is
//     silently dropped -- there is no EOF flush after the
//     [bufio.Scanner] loop.
//
// ## New-Style AST Parsing Details
//
// For each YAML node, the upstream reads the key node's HeadComment. If
// it is empty or does not contain "# --", no auto-description is
// generated. Otherwise, the HeadComment is split on newlines and passed
// to ParseComment. If ParseComment returns a non-empty key (indicating
// an old-style comment in the HeadComment), the auto-description is
// discarded -- the key path will be resolved via the file scanner instead.
//
// The goccy parser this package uses erases the physical blank lines when it
// merges separate comment blocks above a key into one head comment group, so
// the blank-line boundary that scopes yaml.v3's HeadComment never reaches the
// comment string. [magicschema.HeadCommentRun] reconstructs the boundaries
// from comment token positions, narrowing the head comment to the run
// physically adjacent to the key before description parsing, standalone
// @default detection, and the @ignore substring check. A detached comment
// block -- a file header, a description or @ignore for a removed key,
// separated from the key by a blank line -- therefore does not apply to the
// following key, matching the upstream HeadComment scope.
//
// ## Template Priority
//
// In the default template, old-style descriptions and defaults take
// precedence over new-style (HeadComment) values. Per-field precedence:
//
//   - Description: old-style > new-style (new-style stored as AutoDescription)
//   - Type: old-style ValueType > new-style ValueType > @notationType > inferred
//   - Default: old-style Default > new-style Default (stored as AutoDefault)
//   - Section: old-style Section > new-style Section
//
// ## Nil Value Handling
//
// When a YAML value is null/nil, the upstream defaults the display type
// to "string" (unless an explicit type hint overrides it) and sets the
// default to "`nil`" (unless @default overrides it). Additionally, for
// nil values, if the old-style description is empty, the auto-description
// (new-style) text is promoted into the Description field (not
// AutoDescription), which affects how the upstream template renders
// the value row.
//
// # Intentional Divergences
//
// This implementation intentionally diverges from the upstream in several
// areas to support the magicschema schema-generation use case:
//
//   - @section ignored: Section annotations matching the upstream regex
//     (^\s*# @section -- (.*)$) are recognized and consumed but produce
//     no schema output. Sections are a documentation-rendering concern,
//     not a schema concern.
//
//   - @section without separator consumed: Lines matching @section
//     without the " -- " separator (e.g., "# @section Security") are
//     also consumed and do not leak into descriptions. In the upstream,
//     such lines would NOT match sectionRegex and would fall through to
//     commentContinuationRegex, becoming part of the description text
//     (e.g., appending "@section Security" to the description). We
//     consume them to prevent annotation markers from polluting schema
//     descriptions.
//
//   - @notationType ignored: Notation type annotations matching the
//     upstream regex are recognized and consumed but produce no schema
//     output. In the upstream, @notationType primarily controls how
//     default values are rendered in Markdown tables and serves as a type
//     fallback. For schema generation, type information comes from
//     explicit (type) hints or structural inference from the YAML value
//     itself, making @notationType redundant. We do not use @notationType
//     as a type fallback.
//
//   - @notationType without separator consumed: Like @section, lines
//     matching @notationType without the " -- " separator (e.g.,
//     "# @notationType helm") are consumed rather than leaking into
//     descriptions. The upstream would let these fall through to
//     continuation text.
//
//   - @default produces schema default: When @default is present, its
//     value is parsed as a YAML expression and set as the JSON Schema
//     "default" field, so numbers, booleans, lists, and objects keep
//     their native JSON types (matching the bitnami annotator). Values
//     that are blank or do not parse as YAML -- notably the common
//     helm-docs display idiom of backticked defaults such as
//     "@default -- `[]` (empty list)" -- produce no default, following
//     the best-effort principle. The upstream uses @default for
//     documentation rendering only.
//
//   - Type mapping to JSON Schema: The upstream stores helm-docs display
//     types (int, float, bool, list, object, string, yaml, tpl) verbatim
//     for the documentation table. We map them to JSON Schema types:
//     int->integer, float->number, bool->boolean, list->array,
//     dict->object, object->object, string->string. Additional mappings
//     (integer, number, boolean, array) are accepted for convenience.
//     Bare "tpl" and "yaml" hints are render notations (a Go template, a
//     value rendered as a YAML block), not type assertions; charts place
//     them on mappings and sequences as readily as on strings, so they
//     contribute no type constraint and the type comes from structural
//     inference (fail open). Compound "X/Y" hints are mapped in two
//     tiers (see mapHelmDocsType and isContainerType): when the leading
//     segment X maps to a container type (array/object) and the trailing
//     segment Y is itself a known type or render notation, the CONTAINER
//     type wins, including nested hints (list/string->array,
//     list/tpl->array, dict/foo/string->object); otherwise the last
//     /-separated segment is used (tpl/string->string, tpl/array->array,
//     where "tpl" is a scalar modifier). Unrecognized types (e.g., "path",
//     "map", "list/csv") are silently ignored -- the type comes from
//     structural inference instead.
//
//   - @ignore scope extended: Matching the upstream, @ignore is detected
//     via substring check on comment text. However, the upstream checks
//     only HeadComment on all Content nodes. We check the head comment
//     run adjacent to a MappingValueNode's key, inline comments on the
//     key and value nodes, and the foot comment, using the goccy/go-yaml
//     AST. This allows @ignore to be placed as an inline comment
//     (e.g., "secret: value # @ignore") or as a trailing comment after
//     the last key, in addition to head comments. A comment stowed on a
//     sequence value (goccy attaches a comment above the first element to
//     the SequenceNode itself, on a different line than the value token)
//     is excluded: upstream removes only the matching sequence item for
//     such a comment, never the whole key, so the key stays (fail open)
//     while the item-level removal is not replicated.
//
//   - Head comment run narrowed by column: [magicschema.HeadCommentRun]
//     also discards comment lines indented past the key's column (a
//     commented-out child of the previous key), whereas yaml.v3's
//     HeadComment is indentation-insensitive, so upstream would attribute
//     such a comment to the key. The narrowing matches the core
//     structural description fallback, so the annotator and the core
//     agree on which comment block documents a key.
//
//   - No nil type defaulting: The upstream defaults nil values to "string"
//     type. We emit no type constraint for null/empty values, following
//     the magicschema fail-open principle: a null value in YAML does not
//     mean the field must be null or must be a string.
//
//   - @default preserved across issue #96 recursion: When multiple "# --"
//     groups exist and the issue #96 workaround takes only the last group,
//     any @default annotation from the earlier lines is preserved. The
//     upstream loses @default values that appear before the last "# --"
//     group during recursion because it passes only the truncated slice
//     to the recursive call.
//
//   - EOF handling for old-style comments: If the file ends while
//     collecting an old-style comment block (comment at the very end of
//     the file with no YAML key-value pair after it), we still parse and
//     store the block. The upstream's old-style scanner silently drops
//     trailing blocks because there is no EOF flush after the scan loop.
//
//   - Old-style annotation key filtering: The old-style scanner rejects
//     lines where group 1 (the key) is a recognized annotation keyword
//     (@notationType, @section, @default, @raw, @ignore). The upstream
//     does not perform this check, so a line like "# @section -- Security"
//     would be parsed as an old-style comment with key "@section" and
//     description "Security", which is almost certainly unintended. We
//     filter these to prevent annotation markers from being stored as
//     key-path descriptions.
//
//   - Old-style key shape filtering: The old-style scanner additionally
//     requires the key to look like a key path
//     ([magicschema.IsHelmDocsKeyPath]): a single token without
//     whitespace, made of dot-separated non-empty segments, none
//     beginning with a digit. The upstream accepts any non-empty group 1,
//     so prose containing " -- " (e.g. "# Use the v1.2 API -- stable")
//     would be recorded under a nonsense key. We reject such lines so
//     they remain available as ordinary fallback descriptions. Dotless
//     single tokens (e.g. "# replicas -- count") pass the filter, since
//     they are indistinguishable from top-level keys the upstream
//     documents.
//
//   - New-style precedence: When both a new-style head comment ("# -- desc")
//     and an old-style file-scanned comment ("# key.path -- desc") target
//     the same node, the new-style comment takes precedence (checked first,
//     old-style is a fallback). The upstream gives old-style priority in its
//     template rendering (Description field overrides AutoDescription).
//     In practice, having both on the same node is unusual.
//
//   - @ignore in continuation: The upstream's @ignore is a pre-processing
//     step that runs before ParseComment and only checks HeadComment.
//     ParseComment does not handle @ignore. We additionally recognize
//     @ignore in continuation lines within parseCommentBlock, setting the
//     skip flag. This allows @ignore to appear after "# --" in a comment
//     block and still trigger node skipping.
//
//   - Standalone @default without description: When a HeadComment
//     contains "# @default -- value" but no "# --" description line,
//     the upstream's getDescriptionFromNode returns empty (since
//     it requires "# --" to be present as a substring). We detect
//     standalone @default annotations and produce an AnnotationResult
//     with only the Default field set, allowing @default to function
//     independently of "# --" descriptions. Since the extension has no
//     upstream behavior to mirror, it scopes to the head comment run
//     adjacent to the key: a @default in a blank-line-detached earlier
//     block does not apply to the following key.
//
//   - Description whitespace trimming: The upstream assigns the regex
//     match groups for key path and description without trimming. We
//     apply [strings.TrimSpace] to both the key path and description
//     extracted from the regex. This strips trailing whitespace from
//     descriptions (e.g., "# -- Description   " becomes "Description"
//     instead of "Description   "). In practice this is a benign
//     improvement for schema use cases where trailing whitespace in
//     descriptions is undesirable.
//
//   - Issue #96 prefix check trims whitespace: The upstream's
//     ParseComment uses [strings.HasPrefix] to check for "# --" on raw
//     comment lines. For old-style scanner input, these raw lines may
//     contain leading whitespace from YAML indentation. A line like
//     "  # -- desc" would fail the upstream's prefix check (since it
//     starts with spaces, not "#"). We trim lines before the prefix
//     check, making the #96 workaround apply to indented "# --" lines
//     as well. In practice, old-style comments at file level are rarely
//     indented, making this a benign difference.
//
// # Behavioral Alignment
//
// The following behaviors match the upstream:
//
//   - Key regex pattern: Uses the same regex ^\s*#\s*(.*)\s+--\s*(.*)$
//     to detect both new-style and old-style comments.
//
//   - Issue #96 workaround: Multiple "# --" lines in a comment block
//     trigger recursive processing from the last "# --" line.
//
//   - @raw continuation: Raw mode uses newline joining. Only the first
//     @raw match activates raw mode. @raw requires at least one space
//     between # and @raw (matching ^\s*#\s+@raw).
//
//   - @default regex: Uses ^\s*# @default -- (.*)$ to match default
//     override annotations. Multiple @default lines in a single block
//     cause the last one to win (each match overwrites the previous).
//
//   - @section regex: Uses ^\s*# @section -- (.*)$ for standard-form
//     section annotations (with " -- " separator).
//
//   - @notationType regex: Uses ^\s*#\s+@notationType\s+--\s+(.*)$ for
//     standard-form notation type annotations (with " -- " separator).
//
//   - Comment continuation regex: Uses ^\s*#(\s?)(.*)$ to match
//     continuation lines, with the optional space after # controlling
//     the captured content.
//
//   - Type hint regex: Uses ^\((.*?)\)\s*(.*)$ to extract type hints
//     from the beginning of description text.
//
//   - Blank comment continuation: A bare "#" line in normal mode
//     produces an extra space in the description (space + empty content),
//     matching the upstream's simple concatenation behavior. In raw mode,
//     a bare "#" line produces an empty newline.
//
//   - Continuation line processing order: Checks @raw, @default,
//     @notationType, @section, then falls through to comment continuation,
//     matching the upstream's check ordering in ParseComment.
//
//   - Old-style scanner key validation: The old-style file scanner
//     requires a non-empty key path (group 1), skipping new-style
//     "# -- desc" lines where the key is empty. This matches the
//     upstream's match[1] == "" check; the key must also be key-path
//     shaped (see "Old-style key shape filtering" under Intentional
//     Divergences). Additionally, after
//     parseCommentBlock processes the collected lines, entries with an
//     empty key path (which can occur when the issue #96 workaround
//     recurses to a new-style "# --" group within an old-style block)
//     are discarded, matching the upstream's if key != "" guard.
//
//   - Old-style key discard in head comments: When a head comment
//     contains an old-style pattern ("# key.path -- desc") rather than
//     a new-style pattern ("# -- desc"), the upstream getDescriptionFromNode
//     checks whether ParseComment returned a non-empty key and discards
//     the auto-description if so, letting the old-style file scanner
//     handle it instead. We replicate this behavior: parseNewStyleComment
//     returns nil when parseCommentBlock produces a non-empty keyPath,
//     causing Annotate to fall through to the oldStyleDescs map.
//
//   - New-style detection: The upstream's getDescriptionFromNode uses
//     [strings.Contains] to check for "# --" in the full HeadComment
//     string to decide whether to attempt parsing. We check per-line
//     with [strings.HasPrefix] on trimmedLine for "# --". These are equivalent
//     in practice because HeadComment lines always start with "# ", so
//     "# --" only ever appears at the start of a line. The per-line
//     approach avoids a theoretical false positive where "# --" might
//     appear as a substring in the middle of a line.
//
// [norwoodj/helm-docs]: https://github.com/norwoodj/helm-docs
package norwoodj
