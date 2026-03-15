package jsonschema

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

// regexCache caches compiled regexps keyed by pattern string.
var regexCache sync.Map

func compileRegexp(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		re, ok := v.(*regexp.Regexp)
		if ok {
			return re, nil
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile regexp: %w", err)
	}

	regexCache.Store(pattern, re)

	return re, nil
}

// ValidateOption configures validation behavior.
type ValidateOption func(*validator)

// WithFormatValidator registers a custom format checker for a named format.
// The function receives the string value and returns nil if valid.
func WithFormatValidator(name string, fn func(string) error) ValidateOption {
	return func(v *validator) { v.formatCheckers[name] = fn }
}

// WithFormats forces built-in format validation on or off, overriding the
// draft- and vocabulary-derived default. Without this option, format is
// asserted under Draft-07 (validation §7.2 permits it) and is annotation-only
// under Draft 2020-12 unless the format-assertion vocabulary is active (per
// validation §7.2.1, which requires format-assertion to be disabled by
// default). WithFormats(true) opts in to assertion regardless of draft or
// vocabulary; WithFormats(false) disables it entirely.
func WithFormats(enabled bool) ValidateOption {
	return func(v *validator) { v.formatsForce = &enabled }
}

// WithContent enables assertion of the contentEncoding and contentMediaType
// keywords for string instances. By default these keywords are annotation-only
// (per the JSON Schema spec, which makes content assertion optional). With this
// option, a contentEncoding of base64 must decode and a contentMediaType of
// application/json must be valid JSON; other encodings and media types remain
// annotations. Non-string instances are unaffected. Mirrors [WithFormats].
func WithContent(enabled bool) ValidateOption {
	return func(v *validator) { v.contentEnabled = enabled }
}

// WithRefResolver sets a [RefResolver] for resolving remote $ref URIs during
// validation. The resolver is called when local fragment resolution fails.
// Resolved schemas are cached for the duration of the validation run.
func WithRefResolver(r RefResolver) ValidateOption {
	return func(v *validator) { v.refResolver = r }
}

// WithResolveOptions passes upstream [jsonschema.ResolveOptions] to
// Schema.Resolve for structural pre-validation. The validation walk resolves
// local fragment refs directly and remote/absolute refs via a configured
// [RefResolver] (see [WithRefResolver]).
func WithResolveOptions(opts *jsonschema.ResolveOptions) ValidateOption {
	return func(v *validator) { v.resolveOpts = opts }
}

// WithVocabularies directly specifies the active vocabulary set for validation.
// The map keys are vocabulary URIs (e.g. [VocabValidation2020]) and values
// indicate whether the vocabulary is active. This takes highest precedence,
// overriding any $vocabulary found in a metaschema registered via
// [WithMetaSchema].
func WithVocabularies(vocabs map[string]bool) ValidateOption {
	return func(v *validator) { v.vocabOverride = vocabs }
}

// WithMetaSchema registers a metaschema for vocabulary resolution. The
// metaschema's $id is used to match against the root schema's $schema field.
// If the root schema's $schema matches the metaschema's $id, the metaschema's
// $vocabulary map is used to determine active vocabularies.
func WithMetaSchema(ms *Schema) ValidateOption {
	return func(v *validator) {
		if ms.ID != "" {
			if v.metaSchemas == nil {
				v.metaSchemas = map[string]*Schema{}
			}

			v.metaSchemas[ms.ID] = ms
		}
	}
}

// visitKey identifies a unique (schema, instance path) pair for cycle detection.
// A schema may legitimately be visited multiple times for different instance
// paths (e.g. recursive $ref: "#"), so only the same schema at the same
// instance path indicates a true cycle.
type visitKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	schema *Schema
	//nolint:unused // Read via struct equality when used as a map key.
	instancePath string
}

// validator holds state for a single validation run.
type validator struct {
	refResolveErr         error              // last error from refResolver, consumed by validateRef/validateDynamicRef
	refResolver           RefResolver        // optional remote ref resolver
	metaSchemas           map[string]*Schema // $schema URI → metaschema
	visiting              map[visitKey]bool
	root                  *Schema
	resolveOpts           *jsonschema.ResolveOptions
	formatsForce          *bool           // explicit WithFormats override; nil if unset
	vocabOverride         map[string]bool // from WithVocabularies
	formatCheckers        map[string]func(string) error
	uriRegistry           map[string]*Schema         // absolute URI → schema
	anchorRegistry        map[string]*Schema         // baseURI#anchor → schema
	dynamicAnchorRegistry map[string]*Schema         // baseURI#name → schema ($dynamicAnchor only)
	baseURIs              map[*Schema]string         // schema → its base URI
	walked                map[*Schema]bool           // schemas already visited by walkSchema (cycle guard)
	jsonPointerCache      map[jsonPointerKey]*Schema // JSON-pointer fallback results, keyed by (root, pointer)
	dynamicScope          []string                   // stack of resource base URIs entered during validation
	draft                 Draft
	vocabs                vocabSet // resolved active vocabularies
	formatsEnabled        bool
	contentVocab          bool // content vocabulary active (gates validateContent)
	contentEnabled        bool // assert contentEncoding/contentMediaType (WithContent)
}

func newValidator(schema *Schema, opts []ValidateOption) (*validator, error) {
	v := &validator{
		root:           schema,
		formatCheckers: map[string]func(string) error{},
		visiting:       map[visitKey]bool{},
	}
	// Register built-in format checkers.
	maps.Copy(v.formatCheckers, builtinFormats)

	for _, opt := range opts {
		opt(v)
	}
	// Detect draft from $schema field.
	v.draft = detectDraft(schema)

	// Resolve active vocabularies.
	err := v.resolveVocabularies()
	if err != nil {
		return nil, err
	}

	// Resolve whether the format keyword is asserted (depends on draft,
	// vocabularies, and any explicit WithFormats override).
	v.resolveFormats()

	v.buildRegistry()

	// Initialize dynamic scope with the root resource's base URI.
	if v.draft == Draft2020 {
		v.dynamicScope = []string{v.baseURIs[v.root]}
	}

	return v, nil
}

// resolveVocabularies determines the active vocabulary set.
//
// Resolution priority:
//  1. WithVocabularies direct override (highest).
//  2. WithMetaSchema lookup (root $schema matches a registered metaschema $id).
//  3. Default: allVocabs (backward compatible).
//
// Draft-07 always gets allVocabs — vocabulary is a 2020-12 concept.
func (v *validator) resolveVocabularies() error {
	// Draft-07 has no vocabulary concept.
	if v.draft != Draft2020 {
		v.vocabs = allVocabs()
		v.contentVocab = true

		return nil
	}

	var rawVocabs map[string]bool

	switch {
	case v.vocabOverride != nil:
		rawVocabs = v.vocabOverride

	case v.metaSchemas != nil:
		if ms, ok := v.metaSchemas[v.root.Schema]; ok && len(ms.Vocabulary) > 0 {
			rawVocabs = ms.Vocabulary
		}
	}

	if rawVocabs == nil {
		v.vocabs = allVocabs()
		v.contentVocab = true

		return nil
	}

	err := checkUnknownVocabularies(rawVocabs)
	if err != nil {
		return err
	}
	// The core vocabulary MUST be required (true) when present in $vocabulary.
	if required, ok := rawVocabs[VocabCore2020]; ok && !required {
		return fmt.Errorf("%w: core vocabulary must be required", ErrUnknownVocabulary)
	}

	v.vocabs = resolveVocabs(rawVocabs)
	// The content vocabulary gates validateContent. vocabSet omits it (content
	// is annotation-only in the common path), so its active state is tracked
	// here directly from the raw map.
	v.contentVocab = rawVocabs[VocabContent2020]

	return nil
}

// resolveFormats determines whether the format keyword is asserted during the
// walk. An explicit WithFormats choice wins. Otherwise Draft-07 asserts format
// (validation §7.2 permits it), while Draft 2020-12 asserts only when the
// format-assertion vocabulary is active — annotation-only by default under the
// standard meta-schema, per validation §7.2.1's "MUST be disabled by default".
func (v *validator) resolveFormats() {
	switch {
	case v.formatsForce != nil:
		v.formatsEnabled = *v.formatsForce
	case v.draft == Draft7:
		v.formatsEnabled = true
	default:
		v.formatsEnabled = v.vocabs.formatAssertion
	}
}

// buildRegistry walks the entire schema tree to build URI, anchor, and
// base-URI registries for $id and $anchor resolution.
func (v *validator) buildRegistry() {
	v.uriRegistry = map[string]*Schema{}
	v.anchorRegistry = map[string]*Schema{}
	v.dynamicAnchorRegistry = map[string]*Schema{}
	v.baseURIs = map[*Schema]string{}
	v.walked = map[*Schema]bool{}
	v.walkSchema(v.root, "")
}

// walkSchema recursively walks a schema tree, registering $id and $anchor
// entries and computing base URIs.
func (v *validator) walkSchema(schema *Schema, parentBase string) {
	if schema == nil {
		return
	}
	// Cycle guard: a *Schema graph may alias or form a cycle (e.g.
	// s.AllOf = []*Schema{s}). Registering each pointer once and returning
	// early on a repeat keeps the walk from recursing without bound.
	if v.walked[schema] {
		return
	}

	v.walked[schema] = true

	currentBase := parentBase

	if schema.ID != "" {
		if isFragmentOnly(schema.ID) {
			// Draft-07: fragment-only $id acts as an anchor.
			anchor := schema.ID[1:] // strip leading '#'
			v.anchorRegistry[currentBase+"#"+anchor] = schema
		} else {
			resolved := resolveURI(currentBase, schema.ID)
			resolved = stripFragment(resolved)
			v.uriRegistry[resolved] = schema
			currentBase = resolved
		}
	}

	// 2020-12: $anchor keyword.
	if schema.Anchor != "" {
		v.anchorRegistry[currentBase+"#"+schema.Anchor] = schema
	}

	// 2020-12: $dynamicAnchor keyword.
	// Also registered as a regular anchor (accessible via $ref).
	if schema.DynamicAnchor != "" {
		key := currentBase + "#" + schema.DynamicAnchor
		v.anchorRegistry[key] = schema
		v.dynamicAnchorRegistry[key] = schema
	}

	// Store base URI for this schema (used during $ref resolution).
	// Draft-07 exception: sibling $id doesn't affect $ref resolution.
	if v.draft == Draft7 && schema.Ref != "" && schema.ID != "" && !isFragmentOnly(schema.ID) {
		v.baseURIs[schema] = parentBase
	} else {
		v.baseURIs[schema] = currentBase
	}

	// Recurse into all sub-schema fields.
	v.walkSchemaMap(schema.Properties, currentBase)
	v.walkSchemaMap(schema.PatternProperties, currentBase)
	v.walkSchemaMap(schema.Defs, currentBase)
	v.walkSchemaMap(schema.Definitions, currentBase)
	v.walkSchemaMap(schema.DependentSchemas, currentBase)
	v.walkSchemaMap(schema.DependencySchemas, currentBase)

	for _, s := range schema.AllOf {
		v.walkSchema(s, currentBase)
	}

	for _, s := range schema.AnyOf {
		v.walkSchema(s, currentBase)
	}

	for _, s := range schema.OneOf {
		v.walkSchema(s, currentBase)
	}

	for _, s := range schema.PrefixItems {
		v.walkSchema(s, currentBase)
	}

	for _, s := range schema.ItemsArray {
		v.walkSchema(s, currentBase)
	}

	v.walkSchema(schema.Items, currentBase)
	v.walkSchema(schema.AdditionalProperties, currentBase)
	v.walkSchema(schema.AdditionalItems, currentBase)
	v.walkSchema(schema.Not, currentBase)
	v.walkSchema(schema.If, currentBase)
	v.walkSchema(schema.Then, currentBase)
	v.walkSchema(schema.Else, currentBase)
	v.walkSchema(schema.Contains, currentBase)
	v.walkSchema(schema.PropertyNames, currentBase)
	v.walkSchema(schema.UnevaluatedProperties, currentBase)
	v.walkSchema(schema.UnevaluatedItems, currentBase)
	v.walkSchema(schema.ContentSchema, currentBase)
}

// walkSchemaMap walks a map of named sub-schemas.
func (v *validator) walkSchemaMap(m map[string]*Schema, base string) {
	for _, s := range m {
		v.walkSchema(s, base)
	}
}

// resolveRemote calls the configured [RefResolver] to fetch a remote schema,
// registers it in the URI/anchor registries, and returns it. On error it
// stores the error in refResolveErr and returns nil. Subsequent calls for
// the same baseURI are served from the registry (cached).
func (v *validator) resolveRemote(baseURI string) *Schema {
	if v.refResolver == nil {
		return nil
	}

	schema, err := v.refResolver.ResolveRef(baseURI)
	if err != nil {
		v.refResolveErr = fmt.Errorf("%w: %w", ErrRefResolve, err)
		return nil
	}
	if schema == nil {
		return nil
	}

	// Deep-copy before registering so the resolver-owned schema is never
	// mutated by the walk and the cache holds an independent copy. This
	// matches the remoteLoader path used during Schema.Resolve, so a remote
	// ref is registered identically whichever path reaches it first.
	cp, err := cloneSchema(schema)
	if err != nil {
		v.refResolveErr = fmt.Errorf("%w: %w", ErrRefResolve, err)
		return nil
	}

	// Register and walk the copy so that $id, $anchor, and $dynamicAnchor
	// entries become available for subsequent resolution.
	v.uriRegistry[baseURI] = cp
	v.walkSchema(cp, baseURI)

	return cp
}

// remoteLoader returns a [jsonschema.Loader] for upstream Schema.Resolve.
// When a [RefResolver] is configured, resolved schemas are registered in the
// URI/anchor registries (caching them for the validation walk). If no resolver
// is configured or the resolver returns nil/error, an empty schema is returned
// so Schema.Resolve doesn't fail.
//
// Schemas returned to the upstream resolver are deep-copied via JSON
// round-trip so that Schema.Resolve's internal mutations (e.g. $schema
// inheritance) don't modify the caller's original schema objects.
func (v *validator) remoteLoader() jsonschema.Loader {
	return func(uri *url.URL) (*Schema, error) {
		uriStr := uri.String()
		// Check cache first.
		if s, ok := v.uriRegistry[uriStr]; ok {
			return s, nil
		}
		if v.refResolver != nil {
			s, err := v.refResolver.ResolveRef(uriStr)
			if err == nil && s != nil {
				// Deep-copy so the upstream resolver's mutations don't
				// affect the original schema from the RefResolver.
				cp, cpErr := cloneSchema(s)
				if cpErr != nil {
					return nil, fmt.Errorf("clone resolved schema: %w", cpErr)
				}
				// Register and walk the copy so subsequent lookups
				// during both Schema.Resolve and the validation walk
				// find it without re-calling the resolver.
				v.uriRegistry[uriStr] = cp
				v.walkSchema(cp, uriStr)

				return cp, nil
			}
		}
		// Return empty schema so Schema.Resolve can proceed.
		return &Schema{}, nil
	}
}

// cloneSchema deep-copies a [Schema] via JSON round-trip.
//
// Upstream [jsonschema.Schema.CloneSchemas] is shallow for non-sub-schema
// fields (Extra, Enum, Const, Default, Examples): it shares their backing
// maps, slices, and pointers with the original. A round-trip through JSON
// instead yields an independent copy of every serializable field, which is
// what remote-ref isolation requires so [jsonschema.Schema.Resolve]'s in-place
// mutations can't corrupt the caller's schema. The trade-off is that any field
// omitted from the JSON encoding (such as PropertyOrder) is dropped;
// TestCloneSchemaSerializableFieldCoverage forces every exported field to be
// accounted for, and TestCloneSchemaDeepIndependence verifies the copy is fully
// independent of the original.
func cloneSchema(s *Schema) (*Schema, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	var cp Schema

	err = json.Unmarshal(data, &cp)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	return &cp, nil
}

// isFragmentOnly reports whether a URI is fragment-only (e.g. "#foo").
func isFragmentOnly(uri string) bool {
	return strings.HasPrefix(uri, "#")
}

// resolveURI resolves ref against base per RFC 3986.
func resolveURI(base, ref string) string {
	if base == "" {
		return ref
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}

	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}

	return baseURL.ResolveReference(refURL).String()
}

// stripFragment removes the fragment component from a URI.
func stripFragment(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}

	parsed.Fragment = ""
	parsed.RawFragment = ""

	return parsed.String()
}

// JSON Schema type name constants.
const (
	typeNameNull    = "null"
	typeNameString  = "string"
	typeNameInteger = "integer"
	typeNameNumber  = "number"
)

// detectDraft determines the draft from the root schema's $schema field.
func detectDraft(s *Schema) Draft {
	switch s.Schema {
	case Draft7.schemaURI(),
		"http://json-schema.org/draft-07/schema",
		"https://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft-07/schema":
		return Draft7
	case Draft2020.schemaURI():
		return Draft2020
	default:
		return Draft2020
	}
}

// Validate validates a pre-parsed Go value against a JSON Schema.
//
// Accepted instance types: map[string]any, []any, string, float64,
// [json.Number], bool, nil. Go structs are not accepted; passing any other
// type returns an error (marshal to JSON or use [ValidateJSON] instead).
//
// Returns nil on success or an error that can be unwrapped to
// *[ValidationError] via [errors.As].
func Validate(schema *Schema, instance any, opts ...ValidateOption) error {
	if !acceptedInstance(instance) {
		return fmt.Errorf(
			"instance of type %T is not accepted: accepted types are map[string]any, "+
				"[]any, string, float64, json.Number, bool, and nil; marshal to JSON or use ValidateJSON",
			instance,
		)
	}

	v, err := newValidator(schema, opts)
	if err != nil {
		return err
	}

	// Structural pre-validation via Schema.Resolve.
	// A Loader is always provided so Schema.Resolve doesn't fail on remote
	// refs. When a RefResolver is configured, it is called during loading
	// and the result is cached in the URI registry so the validation walk
	// never re-calls the resolver for the same URI.
	// Copy the caller's options so assigning Loader doesn't mutate a
	// *ResolveOptions shared across concurrent Validate calls.
	var resolveOpts jsonschema.ResolveOptions
	if v.resolveOpts != nil {
		resolveOpts = *v.resolveOpts
	}
	if resolveOpts.Loader == nil {
		resolveOpts.Loader = v.remoteLoader()
	}

	_, err = schema.Resolve(&resolveOpts)
	if err != nil && !v.resolveErrorIsRefOnly(schema, resolveOpts) {
		return fmt.Errorf("schema resolve: %w", err)
	}

	errs := v.validate(schema, instance, "", "", nil)
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	return &ValidationError{Causes: errs}
}

// resolveErrorIsRefOnly reports whether a [jsonschema.Schema.Resolve] failure
// is caused solely by $ref/$dynamicRef target lookup that this package resolves
// itself.
//
// Upstream Resolve performs reference resolution as part of pre-validation and
// rejects refs it cannot follow — for example a JSON Pointer that targets an
// unknown keyword or the internals of a non-applicator keyword such as
// examples. The PRD assigns $ref/$dynamicRef target lookup to this package, not
// upstream (see [validator.resolveRef]), so such a failure must not be fatal
// when the schema is otherwise well-formed.
//
// The error is ref-only when all hold:
//
//   - The schema's sub-schemas form a tree (a JSON clone would otherwise hide
//     upstream's tree check).
//   - With every $ref and $dynamicRef removed, a deep copy resolves cleanly, so
//     the failure is not a structural or meta-schema problem.
//   - This package can resolve every reference in the schema, and each resolved
//     target is itself well-formed.
//
// Any check failing means the original error stands.
func (v *validator) resolveErrorIsRefOnly(schema *Schema, resolveOpts jsonschema.ResolveOptions) bool {
	// A non-tree schema must be rejected before the JSON-clone-based checks
	// below. The clone round-trips through JSON, which silently collapses Go
	// pointer aliasing. Upstream rejects a schema whose sub-schemas do not form
	// a tree, a check that depends on pointer identity rather than JSON content,
	// so a JSON clone would hide it.
	if !schemaFormsTree(schema) {
		return false
	}

	if !v.structureResolves(schema, resolveOpts) {
		return false
	}

	return v.refsResolveWellFormed(schema, resolveOpts)
}

// structureResolves reports whether schema resolves cleanly once every $ref and
// $dynamicRef is removed, isolating structural and meta-schema validity from
// reference target lookup. The caller must have confirmed [schemaFormsTree].
func (v *validator) structureResolves(schema *Schema, resolveOpts jsonschema.ResolveOptions) bool {
	stripped, err := cloneSchema(schema)
	if err != nil {
		return false
	}

	eachSubschema(stripped, func(s *Schema) {
		s.Ref = ""
		s.DynamicRef = ""
	})

	_, err = stripped.Resolve(&resolveOpts)

	return err == nil
}

// refsResolveWellFormed reports whether this package can resolve every $ref and
// $dynamicRef reachable from schema, and whether each resolved target is itself
// well-formed (see [validator.refTargetWellFormed]). The target check re-imposes
// the structural and meta-schema validation that upstream performs by
// dereferencing refs — which [structureResolves] skips for targets carried in
// unknown keywords or non-applicator keyword internals, since those have no
// typed Schema field. A reference this package cannot follow leaves refResolveErr
// set as a side effect; it is cleared so it does not leak into a later error.
func (v *validator) refsResolveWellFormed(schema *Schema, resolveOpts jsonschema.ResolveOptions) bool {
	ok := true

	eachSubschema(schema, func(s *Schema) {
		if !ok {
			return
		}
		if s.Ref != "" && !v.refTargetWellFormed(v.resolveRef(s, s.Ref), resolveOpts) {
			v.refResolveErr = nil
			ok = false

			return
		}
		if v.draft == Draft2020 && s.DynamicRef != "" &&
			!v.refTargetWellFormed(v.resolveDynamicRef(s, s.DynamicRef), resolveOpts) {
			v.refResolveErr = nil
			ok = false
		}
	})

	return ok
}

// refTargetWellFormed reports whether a resolved ref target is structurally
// well-formed. A nil target (an unresolvable ref) is not. Otherwise the target
// must be structurally sound and each of its own references must resolve against
// the root document, so a malformed target — for example an uncompilable
// pattern, which upstream rejects but typed-only traversal never reaches — or a
// target whose own reference cannot be followed is rejected. The own-reference
// check is one level deep: targets reached through the typed tree are already
// validated by [structureResolves] on the root schema.
func (v *validator) refTargetWellFormed(target *Schema, resolveOpts jsonschema.ResolveOptions) bool {
	if target == nil || !schemaFormsTree(target) {
		return false
	}

	if !v.structureResolves(target, resolveOpts) {
		return false
	}

	return v.allRefsResolvable(target)
}

// allRefsResolvable reports whether this package can resolve every $ref and
// $dynamicRef directly reachable from schema, without judging the resolved
// targets. A reference this package cannot follow leaves refResolveErr set; it
// is cleared so it does not leak into a later error.
func (v *validator) allRefsResolvable(schema *Schema) bool {
	ok := true

	eachSubschema(schema, func(s *Schema) {
		if !ok {
			return
		}
		if s.Ref != "" && v.resolveRef(s, s.Ref) == nil {
			v.refResolveErr = nil
			ok = false
		}
		if v.draft == Draft2020 && s.DynamicRef != "" && v.resolveDynamicRef(s, s.DynamicRef) == nil {
			v.refResolveErr = nil
			ok = false
		}
	})

	return ok
}

// subschemaChildren returns the direct sub-schemas of schema reachable through
// the keywords that hold sub-schemas (applicators plus the reserved $defs and
// definitions locations). It includes only typed Schema fields, not sub-schemas
// carried as raw JSON in unknown keywords. The result may contain nil entries
// for absent keywords; callers skip them. This is the single source of truth for
// which fields hold sub-schemas, shared by [eachSubschema] and [schemaFormsTree].
func subschemaChildren(schema *Schema) []*Schema {
	if schema == nil {
		return nil
	}

	var children []*Schema

	for _, m := range []map[string]*Schema{
		schema.Properties, schema.PatternProperties, schema.Defs, schema.Definitions,
		schema.DependentSchemas, schema.DependencySchemas,
	} {
		for _, sub := range m {
			children = append(children, sub)
		}
	}

	for _, slice := range [][]*Schema{
		schema.AllOf, schema.AnyOf, schema.OneOf, schema.PrefixItems, schema.ItemsArray,
	} {
		children = append(children, slice...)
	}

	return append(children,
		schema.Items, schema.AdditionalProperties, schema.AdditionalItems, schema.Not,
		schema.If, schema.Then, schema.Else, schema.Contains, schema.PropertyNames,
		schema.UnevaluatedProperties, schema.UnevaluatedItems, schema.ContentSchema,
	)
}

// eachSubschema calls fn for schema and every sub-schema reachable through its
// sub-schema-bearing keywords (see [subschemaChildren]). The caller must ensure
// the schema's sub-schema pointers form a tree (see [schemaFormsTree]); an
// aliased or cyclic structure would recurse without bound.
func eachSubschema(schema *Schema, fn func(*Schema)) {
	if schema == nil {
		return
	}

	fn(schema)

	for _, child := range subschemaChildren(schema) {
		eachSubschema(child, fn)
	}
}

// schemaFormsTree reports whether schema's sub-schema pointers form a tree: no
// *Schema is reachable through more than one path, and there are no pointer
// cycles. Upstream Resolve rejects non-tree schemas, so this gates the cases
// where it is safe to traverse with [eachSubschema].
func schemaFormsTree(schema *Schema) bool {
	seen := map[*Schema]bool{}
	tree := true

	var visit func(*Schema)

	visit = func(s *Schema) {
		if s == nil || !tree {
			return
		}
		if seen[s] {
			tree = false

			return
		}

		seen[s] = true
		for _, child := range subschemaChildren(s) {
			visit(child)
		}
	}

	visit(schema)

	return tree
}

// ValidateJSON unmarshals JSON bytes using [json.Decoder] with UseNumber()
// to preserve integer vs number distinction, then validates.
//
// Returns nil on success or an error that can be unwrapped to
// *[ValidationError] via [errors.As].
func ValidateJSON(schema *Schema, data []byte, opts ...ValidateOption) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var instance any

	err := dec.Decode(&instance)
	if err != nil {
		return fmt.Errorf("JSON decode: %w", err)
	}

	return Validate(schema, instance, opts...)
}

// annotations tracks evaluated properties and items for unevaluated* keywords.
type annotations struct {
	properties    map[string]bool
	itemIndexes   map[int]bool
	itemsEnd      int
	allProperties bool
	allItems      bool
}

func newAnnotations() *annotations {
	return &annotations{
		properties:  map[string]bool{},
		itemIndexes: map[int]bool{},
	}
}

func (a *annotations) merge(other *annotations) {
	if other == nil {
		return
	}

	for k := range other.properties {
		a.properties[k] = true
	}
	if other.allProperties {
		a.allProperties = true
	}
	if other.itemsEnd > a.itemsEnd {
		a.itemsEnd = other.itemsEnd
	}

	for k := range other.itemIndexes {
		a.itemIndexes[k] = true
	}
	if other.allItems {
		a.allItems = true
	}
}

// validate performs the depth-first recursive walk.
func (v *validator) validate(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	if schema == nil {
		return nil
	}

	// Boolean schemas: empty Schema{} accepts all, Schema{Not: &Schema{}} rejects
	// all. The upstream library represents the JSON boolean `false` schema as
	// Schema{Not: &Schema{}} (its falseSchema form), which is a core construct
	// that MUST reject every instance regardless of which vocabularies are
	// active. Because that form is indistinguishable from an explicit `not`
	// keyword once parsed, the short-circuit is unconditional: gating it on the
	// applicator vocabulary would make a boolean `false` schema accept-all when
	// that vocabulary is disabled, which is worse than ignoring the much rarer
	// explicit `{"not":{}}` under the same configuration.
	if isFalseSchema(schema) {
		return []*ValidationError{{
			InstancePath: instancePath,
			SchemaPath:   schemaPath,
			Message:      "value is not allowed",
		}}
	}

	// Circular ref detection: same schema + same instance path = true cycle.
	key := visitKey{schema, instancePath}
	if v.visiting[key] {
		return nil // treat as passing to avoid infinite recursion
	}

	v.visiting[key] = true
	defer delete(v.visiting, key)

	// Dynamic scope tracking: push when entering a new resource boundary.
	// The root is already on the stack from initialization; subsequent
	// pushes happen when validation crosses into a schema whose resource
	// base URI differs from the current scope top.
	if v.draft == Draft2020 && len(v.dynamicScope) > 0 {
		base := v.baseURIs[schema]
		if base != v.dynamicScope[len(v.dynamicScope)-1] {
			v.dynamicScope = append(v.dynamicScope, base)
			defer func() { v.dynamicScope = v.dynamicScope[:len(v.dynamicScope)-1] }()
		}
	}

	// If this schema uses unevaluated* keywords but the caller didn't provide
	// annotations, create a local annotations object to track evaluated items.
	if ann == nil && (schema.UnevaluatedProperties != nil || schema.UnevaluatedItems != nil) {
		ann = newAnnotations()
	}

	var errs []*ValidationError

	// $ref resolution.
	if schema.Ref != "" {
		refErrs := v.validateRef(schema, instance, instancePath, schemaPath, ann)
		errs = append(errs, refErrs...)
		// Draft-07: ignore siblings of $ref.
		if v.draft == Draft7 {
			return errs
		}
	}

	// $dynamicRef resolution (2020-12 only).
	if v.draft == Draft2020 && schema.DynamicRef != "" {
		errs = append(errs, v.validateDynamicRef(schema, instance, instancePath, schemaPath, ann)...)
	}

	// Type validation.
	errs = append(errs, v.validateType(schema, instance, instancePath, schemaPath)...)

	// Enum.
	errs = append(errs, v.validateEnum(schema, instance, instancePath, schemaPath)...)

	// Const.
	errs = append(errs, v.validateConst(schema, instance, instancePath, schemaPath)...)

	// Numeric validations.
	errs = append(errs, v.validateNumeric(schema, instance, instancePath, schemaPath)...)

	// String validations.
	errs = append(errs, v.validateString(schema, instance, instancePath, schemaPath)...)

	// Array validations.
	errs = append(errs, v.validateArray(schema, instance, instancePath, schemaPath, ann)...)

	// Object validations.
	errs = append(errs, v.validateObject(schema, instance, instancePath, schemaPath, ann)...)

	// Composition keywords.
	errs = append(errs, v.validateComposition(schema, instance, instancePath, schemaPath, ann)...)

	// Conditional keywords.
	errs = append(errs, v.validateConditional(schema, instance, instancePath, schemaPath, ann)...)

	// Content keywords.
	errs = append(errs, v.validateContent(schema, instance, instancePath, schemaPath)...)

	// Unevaluated keywords — must run after all other applicator keywords
	// (properties, patternProperties, additionalProperties, allOf, anyOf,
	// oneOf, if/then/else, dependentSchemas) so annotations are fully merged.
	errs = append(errs, v.validateUnevaluated(schema, instance, instancePath, schemaPath, ann)...)

	return errs
}

// validateUnevaluated checks unevaluatedProperties and unevaluatedItems.
// These must run after all other applicator keywords.
func (v *validator) validateUnevaluated(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	if v.draft != Draft2020 || ann == nil || !v.vocabs.unevaluated {
		return nil
	}

	var errs []*ValidationError

	// UnevaluatedProperties.
	//nolint:nestif // Nesting tracks the annotation guards required to apply unevaluatedProperties correctly.
	if schema.UnevaluatedProperties != nil {
		if obj, ok := instance.(map[string]any); ok && !ann.allProperties {
			// isEmptySchema implies Not == nil, so the schema is not a false
			// schema: an empty (always-true) unevaluatedProperties evaluates
			// every remaining property.
			if isEmptySchema(schema.UnevaluatedProperties) {
				ann.allProperties = true
			}

			for propName, val := range obj {
				if ann.properties[propName] {
					continue
				}

				childPath := instancePath + "/" + escapeJSONPointer(propName)
				childSchemaPath := schemaPath + "/unevaluatedProperties"
				childErrs := v.validate(schema.UnevaluatedProperties, val, childPath, childSchemaPath, nil)
				if len(childErrs) == 0 {
					ann.properties[propName] = true
				} else {
					errs = append(errs, &ValidationError{
						InstancePath: childPath,
						SchemaPath:   childSchemaPath,
						Keyword:      "unevaluatedProperties",
						Message:      fmt.Sprintf("property %q is not allowed by unevaluatedProperties", propName),
						Causes:       childErrs,
					})
				}
			}
		}
	}

	// UnevaluatedItems.
	if schema.UnevaluatedItems != nil { //nolint:nestif // Validation keyword nesting is inherent.
		if arr, ok := instance.([]any); ok && !ann.allItems {
			// isEmptySchema implies Not == nil, so the schema is not a false
			// schema: an empty (always-true) unevaluatedItems evaluates every
			// remaining item.
			if isEmptySchema(schema.UnevaluatedItems) {
				ann.allItems = true
			}

			for i, item := range arr {
				if i < ann.itemsEnd {
					continue
				}
				if ann.itemIndexes[i] {
					continue
				}

				childPath := fmt.Sprintf("%s/%d", instancePath, i)
				childSchemaPath := schemaPath + "/unevaluatedItems"
				childErrs := v.validate(schema.UnevaluatedItems, item, childPath, childSchemaPath, nil)
				if len(childErrs) == 0 {
					ann.itemIndexes[i] = true
				} else {
					errs = append(errs, &ValidationError{
						InstancePath: childPath,
						SchemaPath:   childSchemaPath,
						Keyword:      "unevaluatedItems",
						Message:      fmt.Sprintf("item %d is not allowed by unevaluatedItems", i),
						Causes:       childErrs,
					})
				}
			}
		}
	}

	return errs
}

// isFalseSchema checks if a schema is equivalent to boolean false (rejects all).
func isFalseSchema(s *Schema) bool {
	return s.Not != nil && isEmptySchema(s.Not) && isSchemaTrivial(s)
}

// isEmptySchema checks if a schema is empty (no keywords set).
func isEmptySchema(s *Schema) bool {
	return s != nil &&
		s.Type == "" && s.Types == nil &&
		s.Ref == "" && s.DynamicRef == "" &&
		s.Properties == nil && s.Required == nil &&
		s.Items == nil && s.PrefixItems == nil &&
		s.AllOf == nil && s.AnyOf == nil && s.OneOf == nil && s.Not == nil &&
		s.If == nil && s.Then == nil && s.Else == nil &&
		s.Enum == nil && s.Const == nil &&
		s.Minimum == nil && s.Maximum == nil &&
		s.ExclusiveMinimum == nil && s.ExclusiveMaximum == nil &&
		s.MinLength == nil && s.MaxLength == nil &&
		s.Pattern == "" && s.Format == "" &&
		s.MinItems == nil && s.MaxItems == nil &&
		!s.UniqueItems &&
		s.MinProperties == nil && s.MaxProperties == nil &&
		s.AdditionalProperties == nil && s.AdditionalItems == nil &&
		s.PatternProperties == nil && s.PropertyNames == nil &&
		s.Contains == nil &&
		s.MultipleOf == nil &&
		s.UnevaluatedProperties == nil && s.UnevaluatedItems == nil &&
		s.DependentRequired == nil && s.DependentSchemas == nil &&
		s.DependencySchemas == nil && s.DependencyStrings == nil &&
		s.MinContains == nil && s.MaxContains == nil &&
		s.Defs == nil && s.Definitions == nil &&
		s.ContentEncoding == "" && s.ContentMediaType == "" &&
		s.ContentSchema == nil
}

// isSchemaTrivial checks if a schema has only the Not field
// set (for false schema detection).
func isSchemaTrivial(s *Schema) bool {
	// A "false" schema is {not: {}}, meaning only Not is set.
	// We need to verify no other validation/applicator keywords are set.
	return s.Type == "" && s.Types == nil &&
		s.Ref == "" && s.DynamicRef == "" &&
		s.Properties == nil && s.Required == nil &&
		s.Items == nil && s.PrefixItems == nil &&
		s.AllOf == nil && s.AnyOf == nil && s.OneOf == nil &&
		// Not is set — that's OK, it's what makes this a false schema.
		s.If == nil && s.Then == nil && s.Else == nil &&
		s.Enum == nil && s.Const == nil &&
		s.Minimum == nil && s.Maximum == nil &&
		s.ExclusiveMinimum == nil && s.ExclusiveMaximum == nil &&
		s.MinLength == nil && s.MaxLength == nil &&
		s.Pattern == "" && s.Format == "" &&
		s.MinItems == nil && s.MaxItems == nil &&
		!s.UniqueItems &&
		s.MinProperties == nil && s.MaxProperties == nil &&
		s.AdditionalProperties == nil && s.AdditionalItems == nil &&
		s.PatternProperties == nil && s.PropertyNames == nil &&
		s.Contains == nil &&
		s.MultipleOf == nil &&
		s.UnevaluatedProperties == nil && s.UnevaluatedItems == nil &&
		s.DependentRequired == nil && s.DependentSchemas == nil &&
		s.DependencySchemas == nil && s.DependencyStrings == nil &&
		s.MinContains == nil && s.MaxContains == nil &&
		s.ContentSchema == nil
}

// acceptedInstance reports whether instance is one of the JSON-compatible Go
// types [Validate] accepts: map[string]any, []any, string, float64,
// [json.Number], bool, or nil. Other types — notably Go structs, and numeric
// types such as int or [time.Time] — are not accepted, because they are not
// produced by encoding/json when unmarshaling into an any. The check is on the
// top-level value only; [ValidateJSON] always supplies accepted types.
func acceptedInstance(instance any) bool {
	switch instance.(type) {
	case nil, bool, string, float64, json.Number, map[string]any, []any:
		return true
	default:
		return false
	}
}

// jsonNumberIsIntegral reports whether a [json.Number] denotes a mathematical
// integer (e.g. "1.0" or a value far beyond the int64 range). A big.Rat parses
// the decimal literal exactly, so IsInt holds at any magnitude or precision; a
// fixed-width big.Float would round away the fraction of a very long non-integer
// and misclassify it.
func jsonNumberIsIntegral(n json.Number) bool {
	r := new(big.Rat)
	if _, ok := r.SetString(string(n)); !ok {
		return false
	}

	return r.IsInt()
}

// instanceType returns the JSON Schema type name for a Go value.
func instanceType(v any) string {
	if v == nil {
		return typeNameNull
	}

	switch val := v.(type) {
	case bool:
		return "boolean"
	case string:
		return typeNameString
	case json.Number:
		_, err := val.Int64()
		if err == nil {
			return typeNameInteger
		}
		// Handle cases like "1.0" where Int64 fails but the value
		// is mathematically an integer. A big.Rat parses the decimal
		// exactly, so its integrality test holds at any magnitude or
		// precision, unlike a fixed-width big.Float.
		if jsonNumberIsIntegral(val) {
			return typeNameInteger
		}

		return typeNameNumber

	case float64:
		// Trunc avoids the int64() saturation that misclassifies large
		// integral floats (e.g. 1e30) as non-integers.
		if !math.IsInf(val, 0) && val == math.Trunc(val) {
			return typeNameInteger
		}

		return typeNameNumber

	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return ""
	}
}

// instanceMatchesType checks if an instance matches a JSON Schema type string.
func instanceMatchesType(instance any, typ string) bool {
	switch typ {
	case typeNameNull:
		return instance == nil
	case "boolean":
		_, ok := instance.(bool)
		return ok

	case "string":
		// json.Number is a distinct type, so a string assertion already
		// excludes it; no separate numeric guard is needed.
		_, isStr := instance.(string)

		return isStr

	case "integer":
		switch val := instance.(type) {
		case float64:
			// Trunc avoids int64() saturation for large integral floats.
			return !math.IsInf(val, 0) && val == math.Trunc(val)
		case json.Number:
			_, err := val.Int64()
			if err == nil {
				return true
			}
			// Handle cases like "1.0" where Int64 fails but the value
			// is mathematically an integer. A big.Rat parses the decimal
			// exactly, so its integrality test holds at any magnitude or
			// precision, unlike a fixed-width big.Float.
			return jsonNumberIsIntegral(val)
		}

		return false

	case "number":
		switch instance.(type) {
		case float64, json.Number:
			return true
		}

		return false

	case "object":
		_, ok := instance.(map[string]any)
		return ok

	case "array":
		_, ok := instance.([]any)
		return ok
	}

	return false
}

// validateType checks the type keyword.
func (v *validator) validateType(schema *Schema, instance any, instancePath, schemaPath string) []*ValidationError {
	if !v.vocabs.validation {
		return nil
	}

	types := schema.Types
	if schema.Type != "" {
		types = []string{schema.Type}
	}
	if len(types) == 0 {
		return nil
	}

	for _, t := range types {
		if instanceMatchesType(instance, t) {
			return nil
		}
	}

	got := instanceType(instance)

	return []*ValidationError{{
		InstancePath: instancePath,
		SchemaPath:   schemaPath + "/type",
		Keyword:      "type",
		Message:      fmt.Sprintf("expected %s, got %q", formatTypes(types), got),
	}}
}

func formatTypes(types []string) string {
	if len(types) == 1 {
		return fmt.Sprintf("%q", types[0])
	}

	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = fmt.Sprintf("%q", t)
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

// validateEnum checks the enum keyword.
func (v *validator) validateEnum(schema *Schema, instance any, instancePath, schemaPath string) []*ValidationError {
	if !v.vocabs.validation {
		return nil
	}

	// A nil Enum means the keyword is absent (skip). An empty but non-nil Enum
	// ("enum": []) permits no values, so every instance fails it.
	if schema.Enum == nil {
		return nil
	}

	for _, allowed := range schema.Enum {
		if jsonschema.Equal(instance, allowed) {
			return nil
		}
	}

	return []*ValidationError{{
		InstancePath: instancePath,
		SchemaPath:   schemaPath + "/enum",
		Keyword:      "enum",
		Message:      "value does not match any enum member",
	}}
}

// validateConst checks the const keyword.
func (v *validator) validateConst(schema *Schema, instance any, instancePath, schemaPath string) []*ValidationError {
	if !v.vocabs.validation {
		return nil
	}

	if schema.Const == nil {
		return nil
	}

	constVal := *schema.Const
	if jsonschema.Equal(instance, constVal) {
		return nil
	}

	return []*ValidationError{{
		InstancePath: instancePath,
		SchemaPath:   schemaPath + "/const",
		Keyword:      "const",
		Message:      "value does not match const",
	}}
}

// toBigRat converts a numeric value to *[big.Rat] for precise comparison.
func toBigRat(v any) (*big.Rat, bool) {
	switch val := v.(type) {
	case float64:
		// Use the shortest decimal representation so that, e.g., float64(1.01)
		// compares as 101/100 rather than its exact binary expansion. This
		// matches how schema bound values are converted (float64ToRat). A
		// non-finite value yields nil, which is reported as not-a-number so
		// numeric keywords skip it rather than dereferencing a nil *big.Rat.
		r := float64ToRat(val)
		if r == nil {
			return nil, false
		}

		return r, true

	case json.Number:
		r := new(big.Rat)
		if _, ok := r.SetString(string(val)); ok {
			return r, true
		}

		return nil, false
	}

	return nil, false
}

// isNumeric reports whether a value is a numeric type
// (float64 or [json.Number]).
func isNumeric(v any) bool {
	switch v.(type) {
	case float64, json.Number:
		return true
	}

	return false
}

// float64ToRat converts a float64 to a [big.Rat] using its shortest decimal
// representation to avoid precision artifacts (e.g. float64(1.1) becoming
// 1.100000000000000088... When using [big.Rat.SetFloat64]).
func float64ToRat(f float64) *big.Rat {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		// Non-finite values have no rational form. Callers treat a nil result as
		// "not a JSON number" (JSON cannot represent Inf or NaN).
		return nil
	}

	// A finite float64 always formats to a decimal string that SetString
	// parses, so the parse cannot fail here; a nil result would only arise from
	// the non-finite guard above, which callers already treat as "not a number".
	s := strconv.FormatFloat(f, 'f', -1, 64)

	r := new(big.Rat)
	_, _ = r.SetString(s)

	return r
}

// validateNumeric checks numeric keywords.
func (v *validator) validateNumeric(schema *Schema, instance any, instancePath, schemaPath string) []*ValidationError {
	if !v.vocabs.validation {
		return nil
	}

	if !isNumeric(instance) {
		return nil
	}

	val, ok := toBigRat(instance)
	if !ok {
		return nil
	}

	var errs []*ValidationError

	if schema.MultipleOf != nil {
		switch {
		case *schema.MultipleOf <= 0:
			// MultipleOf MUST be strictly greater than 0; a non-positive
			// divisor makes the schema invalid.
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/multipleOf",
				Keyword:      "multipleOf",
				Message:      fmt.Sprintf("multipleOf must be greater than 0, got %v", *schema.MultipleOf),
			})

		default:
			// A NaN/Inf divisor has no rational form (float64ToRat returns
			// nil); the constraint cannot apply, so skip it rather than
			// dividing by a nil *big.Rat.
			divisor := float64ToRat(*schema.MultipleOf)
			if divisor != nil {
				quotient := new(big.Rat).Quo(val, divisor)
				if !quotient.IsInt() {
					errs = append(errs, &ValidationError{
						InstancePath: instancePath,
						SchemaPath:   schemaPath + "/multipleOf",
						Keyword:      "multipleOf",
						Message:      fmt.Sprintf("%s is not a multiple of %v", ratString(val), *schema.MultipleOf),
					})
				}
			}
		}
	}

	// A nil bound denotes a NaN/Inf value with no rational form; such a bound
	// cannot constrain a finite instance, so the comparison is skipped.
	if schema.Minimum != nil {
		bound := float64ToRat(*schema.Minimum)
		if bound != nil && val.Cmp(bound) < 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/minimum",
				Keyword:      "minimum",
				Message:      fmt.Sprintf("%s is less than %v", ratString(val), *schema.Minimum),
			})
		}
	}

	if schema.Maximum != nil {
		bound := float64ToRat(*schema.Maximum)
		if bound != nil && val.Cmp(bound) > 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/maximum",
				Keyword:      "maximum",
				Message:      fmt.Sprintf("%s is greater than %v", ratString(val), *schema.Maximum),
			})
		}
	}

	if schema.ExclusiveMinimum != nil {
		bound := float64ToRat(*schema.ExclusiveMinimum)
		if bound != nil && val.Cmp(bound) <= 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/exclusiveMinimum",
				Keyword:      "exclusiveMinimum",
				Message:      fmt.Sprintf("%s is less than or equal to %v", ratString(val), *schema.ExclusiveMinimum),
			})
		}
	}

	if schema.ExclusiveMaximum != nil {
		bound := float64ToRat(*schema.ExclusiveMaximum)
		if bound != nil && val.Cmp(bound) >= 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/exclusiveMaximum",
				Keyword:      "exclusiveMaximum",
				Message: fmt.Sprintf(
					"%s is greater than or equal to %v",
					ratString(val),
					*schema.ExclusiveMaximum,
				),
			})
		}
	}

	return errs
}

// ratString returns a compact string representation of a [big.Rat].
func ratString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}

	f, _ := r.Float64()

	return fmt.Sprintf("%v", f)
}

// validateString checks string keywords.
func (v *validator) validateString(schema *Schema, instance any, instancePath, schemaPath string) []*ValidationError {
	str, ok := instance.(string)
	if !ok {
		// json.Number is a distinct type, so it fails this assertion and string
		// keywords correctly do not apply to numbers.
		return nil
	}

	var errs []*ValidationError

	//nolint:nestif // One branch per string validation keyword.
	if v.vocabs.validation {
		// RuneCountInString avoids allocating a []rune; only count when a
		// length keyword is present.
		if schema.MinLength != nil || schema.MaxLength != nil {
			runeLen := utf8.RuneCountInString(str)

			if schema.MinLength != nil && runeLen < *schema.MinLength {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/minLength",
					Keyword:      "minLength",
					Message:      fmt.Sprintf("string length %d is less than %d", runeLen, *schema.MinLength),
				})
			}

			if schema.MaxLength != nil && runeLen > *schema.MaxLength {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/maxLength",
					Keyword:      "maxLength",
					Message:      fmt.Sprintf("string length %d is greater than %d", runeLen, *schema.MaxLength),
				})
			}
		}

		if schema.Pattern != "" {
			re, err := compileRegexp(schema.Pattern)
			switch {
			case err != nil:
				// A pattern Go's RE2 cannot compile (e.g. an ECMA-262
				// backreference or lookaround) fails closed: the constraint
				// cannot be evaluated, so no string is accepted under it rather
				// than silently treating every string as a match.
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/pattern",
					Keyword:      "pattern",
					Message:      fmt.Sprintf("pattern %q cannot be compiled", schema.Pattern),
				})
			case !re.MatchString(str):
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/pattern",
					Keyword:      "pattern",
					Message:      fmt.Sprintf("string does not match pattern %q", schema.Pattern),
				})
			}
		}
	}

	if schema.Format != "" && v.formatsEnabled {
		if fn, exists := v.formatCheckers[schema.Format]; exists {
			err := fn(str)
			if err != nil {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/format",
					Keyword:      "format",
					Message:      fmt.Sprintf("string does not match format %q: %v", schema.Format, err),
				})
			}
		}
	}

	return errs
}

// validateArray checks array keywords.
func (v *validator) validateArray(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	arr, ok := instance.([]any)
	if !ok {
		return nil
	}

	var errs []*ValidationError

	// Applicator vocab: prefixItems, items, additionalItems, contains.
	if v.vocabs.applicator { //nolint:nestif // Vocabulary-gated applicator keywords require nesting.
		// PrefixItems (2020-12) or items as array (draft-07).
		var (
			prefixSchemas []*Schema
			prefixPath    string
		)
		if v.draft == Draft2020 && len(schema.PrefixItems) > 0 {
			prefixSchemas = schema.PrefixItems
			prefixPath = "/prefixItems"
		} else if v.draft == Draft7 && len(schema.ItemsArray) > 0 {
			prefixSchemas = schema.ItemsArray
			prefixPath = "/items"
		}

		for i, ps := range prefixSchemas {
			if i >= len(arr) {
				break
			}

			childPath := fmt.Sprintf("%s/%d", instancePath, i)
			childSchemaPath := fmt.Sprintf("%s%s/%d", schemaPath, prefixPath, i)
			childErrs := v.validate(ps, arr[i], childPath, childSchemaPath, nil)
			errs = append(errs, childErrs...)
		}

		// prefixItems annotates every index it applied a subschema to, regardless
		// of per-item success (2020-12 core §10.3.1.1: "the largest index to which
		// this keyword applied a subschema"). Because this walk collects all
		// errors instead of failing fast, the whole applied range is noted once
		// here; gating on success would leave a failed index unevaluated and let
		// unevaluatedItems re-fire on it.
		if ann != nil && len(prefixSchemas) > 0 {
			end := min(len(prefixSchemas), len(arr))
			if ann.itemsEnd < end {
				ann.itemsEnd = end
			}
		}

		// Items (single schema).
		if schema.Items != nil && len(prefixSchemas) == 0 {
			// Single-schema items: applies to all elements.
			for i, item := range arr {
				childPath := fmt.Sprintf("%s/%d", instancePath, i)
				childSchemaPath := schemaPath + "/items"
				childErrs := v.validate(schema.Items, item, childPath, childSchemaPath, nil)
				errs = append(errs, childErrs...)
			}
			if ann != nil {
				ann.allItems = true
			}
		} else if schema.Items != nil && len(prefixSchemas) > 0 {
			// In 2020-12: items after prefixItems applies to remaining elements.
			// In draft-07: additionalItems applies to remaining elements.
			if v.draft == Draft2020 {
				for i := len(prefixSchemas); i < len(arr); i++ {
					childPath := fmt.Sprintf("%s/%d", instancePath, i)
					childSchemaPath := schemaPath + "/items"
					childErrs := v.validate(schema.Items, arr[i], childPath, childSchemaPath, nil)
					errs = append(errs, childErrs...)
				}
				if ann != nil {
					ann.allItems = true
				}
			}
		}

		// AdditionalItems (draft-07 only).
		if v.draft == Draft7 && schema.AdditionalItems != nil && len(schema.ItemsArray) > 0 {
			for i := len(schema.ItemsArray); i < len(arr); i++ {
				childPath := fmt.Sprintf("%s/%d", instancePath, i)
				childSchemaPath := schemaPath + "/additionalItems"
				childErrs := v.validate(schema.AdditionalItems, arr[i], childPath, childSchemaPath, nil)
				errs = append(errs, childErrs...)
			}
		}

		// Contains (applicator vocab).
		if schema.Contains != nil {
			matchCount := 0

			var matchedIdx []int
			for i, item := range arr {
				childErrs := v.validate(
					schema.Contains,
					item,
					fmt.Sprintf("%s/%d", instancePath, i),
					schemaPath+"/contains",
					nil,
				)
				if len(childErrs) == 0 {
					matchCount++

					matchedIdx = append(matchedIdx, i)
				}
			}

			// MinContains/maxContains are 2020-12 validation-vocab keywords. They
			// do not exist in Draft-07 (any such keys are unknown and ignored),
			// and they are skipped when the validation vocabulary is disabled; the
			// defaults are then minContains=1 and no maxContains.
			minContains := 1
			if v.draft == Draft2020 && v.vocabs.validation && schema.MinContains != nil {
				minContains = *schema.MinContains
			}

			maxContains := -1
			if v.draft == Draft2020 && v.vocabs.validation && schema.MaxContains != nil {
				maxContains = *schema.MaxContains
			}

			// Record contains annotations only when the keyword as a whole
			// succeeds; otherwise the matched items must not count as evaluated.
			containsOK := matchCount >= minContains && (maxContains < 0 || matchCount <= maxContains)
			if containsOK && ann != nil {
				for _, i := range matchedIdx {
					ann.itemIndexes[i] = true
				}
			}

			if matchCount < minContains {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/contains",
					Keyword:      "contains",
					Message:      fmt.Sprintf("array has %d matching items, minimum is %d", matchCount, minContains),
				})
			}
			if maxContains >= 0 && matchCount > maxContains {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/maxContains",
					Keyword:      "maxContains",
					Message:      fmt.Sprintf("array has %d matching items, maximum is %d", matchCount, maxContains),
				})
			}
		}
	}

	// Validation vocab: minItems, maxItems, uniqueItems.
	//nolint:nestif // One branch per array validation keyword.
	if v.vocabs.validation {
		// MinItems.
		if schema.MinItems != nil && len(arr) < *schema.MinItems {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/minItems",
				Keyword:      "minItems",
				Message:      fmt.Sprintf("array has %d items, minimum is %d", len(arr), *schema.MinItems),
			})
		}

		// MaxItems.
		if schema.MaxItems != nil && len(arr) > *schema.MaxItems {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/maxItems",
				Keyword:      "maxItems",
				Message:      fmt.Sprintf("array has %d items, maximum is %d", len(arr), *schema.MaxItems),
			})
		}

		// UniqueItems.
		if schema.UniqueItems {
			if hasDuplicates(arr) {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/uniqueItems",
					Keyword:      "uniqueItems",
					Message:      "array contains duplicate items",
				})
			}
		}
	}

	return errs
}

// hasDuplicates checks for duplicate values using JSON-semantic equality.
func hasDuplicates(arr []any) bool {
	// Use hash-then-compare optimization.
	seen := make(map[uint64][]any, len(arr))
	for _, item := range arr {
		h := hashValue(item)
		for _, existing := range seen[h] {
			if jsonschema.Equal(item, existing) {
				return true
			}
		}

		seen[h] = append(seen[h], item)
	}

	return false
}

// hashValue produces a hash for JSON-semantic equality bucketing.
func hashValue(v any) uint64 {
	switch val := v.(type) {
	case nil:
		return 0
	case bool:
		if val {
			return 1
		}

		return 2

	case string:
		return stringHash(val)
	case float64:
		// Normalize: integers hash the same regardless of representation. The
		// fast path is restricted to the int64 range — an out-of-range float to
		// int64 conversion is platform-defined (saturates or wraps), so larger
		// integers fall through to the big.Rat path and stay consistent with the
		// json.Number branch (and with jsonschema.Equal).
		if val == math.Trunc(val) && val >= math.MinInt64 && val < math.MaxInt64 {
			return numHash(int64(val))
		}

		// Non-finite floats have no big.Rat form (SetFloat64 returns nil); hash
		// them by their textual form so uniqueItems stays panic-free.
		if math.IsInf(val, 0) || math.IsNaN(val) {
			return stringHash(strconv.FormatFloat(val, 'g', -1, 64)) + 4
		}

		r := new(big.Rat).SetFloat64(val)

		return stringHash(r.RatString()) + 4

	case json.Number:
		r := new(big.Rat)
		if _, ok := r.SetString(string(val)); ok {
			// IsInt64 guards against silent truncation for integers beyond the
			// int64 range, so they hash via RatString and stay consistent with
			// the float64 branch (and with jsonschema.Equal's big.Rat compare).
			if r.IsInt() && r.Num().IsInt64() {
				return numHash(r.Num().Int64())
			}

			return stringHash(r.RatString()) + 4
		}

		return stringHash(string(val)) + 5

	case []any:
		h := uint64(6)
		for _, item := range val {
			h = h*31 + hashValue(item)
		}

		return h

	case map[string]any:
		h := uint64(7)
		for k, item := range val {
			h += stringHash(k) ^ hashValue(item)
		}

		return h
	}

	return 0
}

func stringHash(s string) uint64 {
	var h uint64
	for _, c := range s {
		h = h*31 + uint64(c)
	}

	return h
}

// numHash produces a hash for integer values, avoiding gosec G115.
//
//nolint:gosec // Overflow is intentional for hash distribution.
func numHash(n int64) uint64 {
	return uint64(n)*2654435761 + 3
}

// validateObject checks object keywords.
func (v *validator) validateObject(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	obj, ok := instance.(map[string]any)
	if !ok {
		return nil
	}

	var errs []*ValidationError

	// Track locally evaluated properties for additionalProperties.
	localEvaluated := map[string]bool{}

	// Applicator vocab: properties, patternProperties, additionalProperties,
	// propertyNames, dependentSchemas.
	//nolint:nestif // One branch per object applicator keyword; flattening would not reduce the inherent fan-out.
	if v.vocabs.applicator {
		// Properties.
		for propName, propSchema := range schema.Properties {
			val, exists := obj[propName]
			if !exists {
				continue
			}

			localEvaluated[propName] = true
			if ann != nil {
				ann.properties[propName] = true
			}

			childPath := instancePath + "/" + escapeJSONPointer(propName)
			childSchemaPath := schemaPath + "/properties/" + escapeJSONPointer(propName)
			childErrs := v.validate(propSchema, val, childPath, childSchemaPath, nil)
			errs = append(errs, childErrs...)
		}

		// PatternProperties.
		for pattern, patternSchema := range schema.PatternProperties {
			re, err := compileRegexp(pattern)
			if err != nil {
				// A pattern Go's RE2 cannot compile fails closed: the keyword
				// cannot decide which properties it governs, so the object is
				// rejected rather than silently dropping the subschema.
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/patternProperties/" + escapeJSONPointer(pattern),
					Keyword:      "patternProperties",
					Message:      fmt.Sprintf("pattern %q cannot be compiled", pattern),
				})

				continue
			}

			for propName, val := range obj {
				if !re.MatchString(propName) {
					continue
				}

				localEvaluated[propName] = true
				if ann != nil {
					ann.properties[propName] = true
				}

				childPath := instancePath + "/" + escapeJSONPointer(propName)
				childSchemaPath := schemaPath + "/patternProperties/" + escapeJSONPointer(pattern)
				childErrs := v.validate(patternSchema, val, childPath, childSchemaPath, nil)
				errs = append(errs, childErrs...)
			}
		}

		// AdditionalProperties — only considers sibling properties and patternProperties.
		if schema.AdditionalProperties != nil {
			for propName, val := range obj {
				if localEvaluated[propName] {
					continue
				}
				if ann != nil {
					ann.properties[propName] = true
				}

				childPath := instancePath + "/" + escapeJSONPointer(propName)
				childSchemaPath := schemaPath + "/additionalProperties"
				childErrs := v.validate(schema.AdditionalProperties, val, childPath, childSchemaPath, nil)
				errs = append(errs, childErrs...)
			}
			if ann != nil {
				ann.allProperties = true
			}
		}

		// PropertyNames. The constraint is on the key, not its value, and a key
		// has no JSON Pointer of its own, so a violation is reported at the
		// containing object's instance path rather than at the property value.
		if schema.PropertyNames != nil {
			for propName := range obj {
				childSchemaPath := schemaPath + "/propertyNames"
				childErrs := v.validate(
					schema.PropertyNames,
					propName,
					instancePath,
					childSchemaPath,
					nil,
				)
				errs = append(errs, childErrs...)
			}
		}

		// DependentSchemas (2020-12).
		if v.draft == Draft2020 {
			for prop, depSchema := range schema.DependentSchemas {
				if _, exists := obj[prop]; !exists {
					continue
				}

				depAnn := newAnnotations()
				childSchemaPath := schemaPath + "/dependentSchemas/" + escapeJSONPointer(prop)
				childErrs := v.validate(depSchema, instance, instancePath, childSchemaPath, depAnn)
				errs = append(errs, childErrs...)
				if len(childErrs) == 0 && ann != nil {
					ann.merge(depAnn)
				}
			}
		}
	}

	// Validation vocab: required, minProperties, maxProperties, dependentRequired.
	//nolint:nestif // One branch per object validation keyword.
	if v.vocabs.validation {
		// Required.
		for _, reqProp := range schema.Required {
			if _, exists := obj[reqProp]; !exists {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/required",
					Keyword:      "required",
					Message:      fmt.Sprintf("missing required property %q", reqProp),
				})
			}
		}

		// MinProperties.
		if schema.MinProperties != nil && len(obj) < *schema.MinProperties {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/minProperties",
				Keyword:      "minProperties",
				Message:      fmt.Sprintf("object has %d properties, minimum is %d", len(obj), *schema.MinProperties),
			})
		}

		// MaxProperties.
		if schema.MaxProperties != nil && len(obj) > *schema.MaxProperties {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/maxProperties",
				Keyword:      "maxProperties",
				Message:      fmt.Sprintf("object has %d properties, maximum is %d", len(obj), *schema.MaxProperties),
			})
		}

		// DependentRequired (2020-12).
		if v.draft == Draft2020 {
			for prop, deps := range schema.DependentRequired {
				if _, exists := obj[prop]; !exists {
					continue
				}

				for _, dep := range deps {
					if _, exists := obj[dep]; !exists {
						errs = append(errs, &ValidationError{
							InstancePath: instancePath,
							SchemaPath:   schemaPath + "/dependentRequired/" + escapeJSONPointer(prop),
							Keyword:      "dependentRequired",
							Message:      fmt.Sprintf("property %q requires property %q", prop, dep),
						})
					}
				}
			}
		}
	}

	// Dependencies (legacy): DependencySchemas and DependencyStrings, both
	// derived from the draft-07 `dependencies` keyword. Honored under Draft 2020-12
	// too for backward compatibility (the keyword was split into dependentSchemas
	// and dependentRequired there, but accepting the legacy form aids migration and
	// matches the optional dependencies-compatibility suite). Ungated by vocabulary:
	// vocabulary is a 2020-12 concept and the legacy keyword predates it.
	for prop, depSchema := range schema.DependencySchemas {
		if _, exists := obj[prop]; !exists {
			continue
		}

		depAnn := newAnnotations()
		childSchemaPath := schemaPath + "/dependencies/" + escapeJSONPointer(prop)
		childErrs := v.validate(depSchema, instance, instancePath, childSchemaPath, depAnn)
		errs = append(errs, childErrs...)
		if len(childErrs) == 0 && ann != nil {
			ann.merge(depAnn)
		}
	}

	for prop, deps := range schema.DependencyStrings {
		if _, exists := obj[prop]; !exists {
			continue
		}

		for _, dep := range deps {
			if _, exists := obj[dep]; !exists {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/dependencies/" + escapeJSONPointer(prop),
					Keyword:      "dependencies",
					Message:      fmt.Sprintf("property %q requires property %q", prop, dep),
				})
			}
		}
	}

	return errs
}

// validateComposition checks allOf, anyOf, oneOf, not.
func (v *validator) validateComposition(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	if !v.vocabs.applicator {
		return nil
	}

	var errs []*ValidationError

	// AllOf. Annotations from individual subschemas are merged only when the
	// allOf as a whole succeeds; a single failing branch discards them all so
	// unevaluatedProperties/Items do not observe partial evaluation.
	if len(schema.AllOf) > 0 {
		var (
			allCauses []*ValidationError
			subAnns   []*annotations
		)
		for i, sub := range schema.AllOf {
			subAnn := newAnnotations()
			childSchemaPath := fmt.Sprintf("%s/allOf/%d", schemaPath, i)
			childErrs := v.validate(sub, instance, instancePath, childSchemaPath, subAnn)
			if len(childErrs) > 0 {
				allCauses = append(allCauses, childErrs...)
			} else {
				subAnns = append(subAnns, subAnn)
			}
		}
		if len(allCauses) > 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/allOf",
				Keyword:      "allOf",
				Message:      "did not validate against all subschemas",
				Causes:       allCauses,
			})
		} else if ann != nil {
			for _, subAnn := range subAnns {
				ann.merge(subAnn)
			}
		}
	}

	// AnyOf.
	if len(schema.AnyOf) > 0 { //nolint:nestif // Composition keyword with annotation merging requires nesting.
		matched := false

		var allCauses []*ValidationError
		for i, sub := range schema.AnyOf {
			subAnn := newAnnotations()
			childSchemaPath := fmt.Sprintf("%s/anyOf/%d", schemaPath, i)
			childErrs := v.validate(sub, instance, instancePath, childSchemaPath, subAnn)
			if len(childErrs) == 0 {
				matched = true
				if ann != nil {
					ann.merge(subAnn)
				}
			} else {
				allCauses = append(allCauses, childErrs...)
			}
		}
		if !matched {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/anyOf",
				Keyword:      "anyOf",
				Message:      "did not validate against any subschema",
				Causes:       allCauses,
			})
		}
	}

	// OneOf.
	if len(schema.OneOf) > 0 {
		matchCount := 0

		var (
			allCauses  []*ValidationError
			matchedAnn *annotations
		)

		for i, sub := range schema.OneOf {
			subAnn := newAnnotations()
			childSchemaPath := fmt.Sprintf("%s/oneOf/%d", schemaPath, i)
			childErrs := v.validate(sub, instance, instancePath, childSchemaPath, subAnn)
			if len(childErrs) == 0 {
				matchCount++
				matchedAnn = subAnn
			} else {
				allCauses = append(allCauses, childErrs...)
			}
		}

		switch {
		case matchCount == 0:
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/oneOf",
				Keyword:      "oneOf",
				Message:      "did not validate against any subschema",
				Causes:       allCauses,
			})

		case matchCount > 1:
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/oneOf",
				Keyword:      "oneOf",
				Message:      fmt.Sprintf("validated against %d subschemas, expected exactly one", matchCount),
			})

		default:
			if ann != nil && matchedAnn != nil {
				ann.merge(matchedAnn)
			}
		}
	}

	// Not.
	if schema.Not != nil {
		// Not never contributes annotations.
		childErrs := v.validate(schema.Not, instance, instancePath, schemaPath+"/not", nil)
		if len(childErrs) == 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/not",
				Keyword:      "not",
				Message:      "should not validate against the schema",
			})
		}
	}

	return errs
}

// validateConditional checks if/then/else.
func (v *validator) validateConditional(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	if !v.vocabs.applicator || schema.If == nil {
		return nil
	}

	var errs []*ValidationError

	ifAnn := newAnnotations()
	ifErrs := v.validate(schema.If, instance, instancePath, schemaPath+"/if", ifAnn)
	ifPassed := len(ifErrs) == 0

	if ifPassed { //nolint:nestif // Conditional branching with annotation tracking requires nesting.
		if ann != nil {
			ann.merge(ifAnn)
		}
		if schema.Then != nil {
			thenAnn := newAnnotations()
			thenErrs := v.validate(schema.Then, instance, instancePath, schemaPath+"/then", thenAnn)
			if len(thenErrs) > 0 {
				errs = append(errs, &ValidationError{
					InstancePath: instancePath,
					SchemaPath:   schemaPath + "/then",
					Keyword:      "then",
					Message:      "if condition was true but then validation failed",
					Causes:       thenErrs,
				})
			} else if ann != nil {
				ann.merge(thenAnn)
			}
		}
	} else if schema.Else != nil {
		elseAnn := newAnnotations()
		elseErrs := v.validate(schema.Else, instance, instancePath, schemaPath+"/else", elseAnn)
		if len(elseErrs) > 0 {
			errs = append(errs, &ValidationError{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/else",
				Keyword:      "else",
				Message:      "if condition was false but else validation failed",
				Causes:       elseErrs,
			})
		} else if ann != nil {
			ann.merge(elseAnn)
		}
	}

	return errs
}

// validateContent applies content keywords.
//
// Per spec, content keywords (contentEncoding, contentMediaType, contentSchema)
// are annotations only by default. [WithContent] opts in to asserting
// contentEncoding and contentMediaType for string instances; that assertion runs
// first and short-circuits on failure.
//
// For contentSchema: when a media type or encoding is present this package does
// not decode the content, so contentSchema is left as an annotation. When
// neither is present, contentSchema is the schema's only constraint and is
// applied directly to the instance so the schema is not treated as accept-all.
func (v *validator) validateContent(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
) []*ValidationError {
	// Gated on the content vocabulary, consistent with the other keyword
	// groups. When it is inactive the content keywords are inert.
	if !v.contentVocab {
		return nil
	}

	if v.contentEnabled {
		if errs := v.assertContent(schema, instance, instancePath, schemaPath); errs != nil {
			return errs
		}
	}

	if schema.ContentSchema == nil {
		return nil
	}
	if schema.ContentMediaType != "" || schema.ContentEncoding != "" {
		return nil
	}

	return v.validate(schema.ContentSchema, instance, instancePath, schemaPath+"/contentSchema", nil)
}

// assertContent asserts contentEncoding and contentMediaType for a string
// instance. Content lives only in strings, so non-string instances carry no
// content and pass. Only base64 encoding and the application/json media type are
// asserted; unrecognized encodings and media types remain annotations.
func (v *validator) assertContent(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
) []*ValidationError {
	str, ok := instance.(string)
	if !ok {
		return nil
	}

	decoded := []byte(str)

	if schema.ContentEncoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			return []*ValidationError{{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/contentEncoding",
				Keyword:      "contentEncoding",
				Message:      fmt.Sprintf("string is not valid base64: %v", err),
			}}
		}

		decoded = b
	}

	if schema.ContentMediaType == "application/json" && !json.Valid(decoded) {
		return []*ValidationError{{
			InstancePath: instancePath,
			SchemaPath:   schemaPath + "/contentMediaType",
			Keyword:      "contentMediaType",
			Message:      "string is not a valid application/json document",
		}}
	}

	return nil
}

// validateRef resolves and validates a $ref.
func (v *validator) validateRef(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	ref := schema.Ref
	if ref == "" {
		return nil
	}

	target := v.resolveRef(schema, ref)
	if target == nil {
		if v.refResolveErr != nil {
			err := v.refResolveErr
			v.refResolveErr = nil

			return []*ValidationError{{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/$ref",
				Keyword:      "$ref",
				Message:      err.Error(),
				err:          err,
			}}
		}
		// A non-local (remote/absolute) ref that cannot be resolved is an
		// error rather than silently passing. Unresolvable local fragment refs
		// are already rejected by Schema.Resolve before the walk begins.
		if !isFragmentOnly(ref) {
			return []*ValidationError{{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/$ref",
				Keyword:      "$ref",
				Message:      fmt.Sprintf("cannot resolve $ref %q", ref),
			}}
		}
		// Unresolvable local fragment ref: silently skip.
		return nil
	}

	refAnn := newAnnotations()
	childErrs := v.validate(target, instance, instancePath, schemaPath+"/$ref", refAnn)
	if len(childErrs) > 0 {
		return []*ValidationError{{
			InstancePath: instancePath,
			SchemaPath:   schemaPath + "/$ref",
			Keyword:      "$ref",
			Causes:       childErrs,
		}}
	}
	if ann != nil {
		ann.merge(refAnn)
	}

	return nil
}

// validateDynamicRef resolves and validates a $dynamicRef.
func (v *validator) validateDynamicRef(
	schema *Schema,
	instance any,
	instancePath, schemaPath string,
	ann *annotations,
) []*ValidationError {
	ref := schema.DynamicRef
	if ref == "" {
		return nil
	}

	target := v.resolveDynamicRef(schema, ref)
	if target == nil {
		if v.refResolveErr != nil {
			err := v.refResolveErr
			v.refResolveErr = nil

			return []*ValidationError{{
				InstancePath: instancePath,
				SchemaPath:   schemaPath + "/$dynamicRef",
				Keyword:      "$dynamicRef",
				Message:      err.Error(),
				err:          err,
			}}
		}

		return nil // unresolvable: silently skip
	}

	refAnn := newAnnotations()
	childErrs := v.validate(target, instance, instancePath, schemaPath+"/$dynamicRef", refAnn)
	if len(childErrs) > 0 {
		return []*ValidationError{{
			InstancePath: instancePath,
			SchemaPath:   schemaPath + "/$dynamicRef",
			Keyword:      "$dynamicRef",
			Causes:       childErrs,
		}}
	}
	if ann != nil {
		ann.merge(refAnn)
	}

	return nil
}

// resolveDynamicRef resolves a $dynamicRef string to a target schema.
// Two-phase: static resolution first, then dynamic scope walk if bookended.
func (v *validator) resolveDynamicRef(schema *Schema, ref string) *Schema {
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil
	}

	fragment := parsed.Fragment

	// Phase 1: Static resolution (same as $ref).
	staticTarget := v.resolveRef(schema, ref)
	if staticTarget == nil {
		return nil
	}

	// JSON Pointer fragments bypass dynamic resolution.
	if strings.HasPrefix(fragment, "/") || fragment == "" {
		return staticTarget
	}

	// Phase 2: Bookending check — the static target must have a
	// $dynamicAnchor matching the fragment name.
	staticBase := v.baseURIs[staticTarget]
	if _, ok := v.dynamicAnchorRegistry[staticBase+"#"+fragment]; !ok {
		return staticTarget // no bookend → behave like $ref
	}

	// Phase 3: Walk dynamic scope outermost→innermost for first matching
	// $dynamicAnchor.
	for _, scopeBase := range v.dynamicScope {
		if target, ok := v.dynamicAnchorRegistry[scopeBase+"#"+fragment]; ok {
			return target
		}
	}

	return staticTarget
}

// resolveRef resolves a $ref string to a target schema using the URI and
// anchor registries.
func (v *validator) resolveRef(schema *Schema, ref string) *Schema {
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil
	}

	// Fragment-only refs (e.g. "#", "#/$defs/foo", "#anchor").
	//nolint:nestif // Resolution walks distinct fragment forms (pointer, anchor, root).
	if isFragmentOnly(ref) {
		fragment := parsed.Fragment

		// Find the root of the current resource.
		resourceRoot := v.root
		base := v.baseURIs[schema]
		if base != "" {
			if target, ok := v.uriRegistry[base]; ok {
				resourceRoot = target
			}
		}

		if fragment == "" {
			return resourceRoot
		}

		// JSON Pointer. Pass the still-encoded fragment so a member name
		// escaped as %2F is not mistaken for a pointer separator.
		if strings.HasPrefix(fragment, "/") {
			raw, encoded := rawFragment(parsed)

			return v.resolveJSONPointer(resourceRoot, raw, encoded)
		}

		// Anchor reference.
		if target, ok := v.anchorRegistry[base+"#"+fragment]; ok {
			return target
		}

		return nil
	}

	// Non-fragment ref: resolve against current schema's base URI.
	base := v.baseURIs[schema]
	absRef := resolveURI(base, ref)

	parsedAbs, err := url.Parse(absRef)
	if err != nil {
		return nil
	}

	fragment := parsedAbs.Fragment
	rawFrag, fragEncoded := rawFragment(parsedAbs)
	parsedAbs.Fragment = ""
	parsedAbs.RawFragment = ""
	baseURI := parsedAbs.String()

	target, ok := v.uriRegistry[baseURI]
	if !ok {
		// Try remote resolution as fallback.
		target = v.resolveRemote(baseURI)
		if target == nil {
			return nil
		}
	}

	if fragment == "" {
		return target
	}

	// JSON Pointer within resolved schema. Pass the still-encoded fragment so a
	// member name escaped as %2F is not mistaken for a pointer separator.
	if strings.HasPrefix(fragment, "/") {
		return v.resolveJSONPointer(target, rawFrag, fragEncoded)
	}

	// Anchor within resolved schema.
	if anchorTarget, ok := v.anchorRegistry[baseURI+"#"+fragment]; ok {
		return anchorTarget
	}

	return nil
}

// resolveJSONPointer resolves a JSON Pointer fragment against a schema.
//
// Typed traversal handles the common case, matching pointer segments to known
// Schema fields. When that fails the pointer may still target a referenceable
// location that has no typed field — a sub-schema carried as raw JSON in an
// unknown keyword, or the internals of a non-applicator keyword such as
// examples — so resolution falls back to walking the schema's JSON form.
func (v *validator) resolveJSONPointer(root *Schema, fragment string, encoded bool) *Schema {
	path := fragment[1:] // strip leading '/'
	segments := strings.Split(path, "/")

	// When the fragment is still percent-encoded (the caller had a RawFragment),
	// RFC 6901 requires splitting on '/' first — so a member name escaped as
	// %2F survives as one segment rather than splitting the pointer — then
	// percent-decoding each segment. When url.Parse already decoded the fragment
	// (RawFragment empty), a second decode would corrupt a name that legitimately
	// contains '%', so only the ~0/~1 unescape is applied.
	for i, seg := range segments {
		if encoded {
			decoded, err := url.PathUnescape(seg)
			if err == nil {
				seg = decoded
			}
			// On an invalid percent-escape the segment is left as-is; resolution
			// then simply does not match.
		}

		segments[i] = unescapeJSONPointer(seg)
	}

	if target := v.traverseSchema(root, segments); target != nil {
		return target
	}

	return v.resolveJSONPointerViaJSON(root, segments)
}

// rawFragment returns the JSON Pointer fragment to resolve plus whether it is
// still percent-encoded. url.Parse populates RawFragment only when the fragment
// carries an encoding it could not canonicalize (e.g. a %2F separator escape);
// that form must be split before decoding. Otherwise Fragment is already the
// single-decoded value and must not be decoded again.
func rawFragment(u *url.URL) (string, bool) {
	if u.RawFragment != "" {
		return u.RawFragment, true
	}

	return u.Fragment, false
}

// jsonPointerKey identifies a JSON-pointer fallback lookup, used to cache its
// result within a validation run.
type jsonPointerKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	root *Schema
	//nolint:unused // Read via struct equality when used as a map key.
	pointer string
}

// resolveJSONPointerViaJSON resolves a JSON Pointer by walking the schema's JSON
// encoding rather than its typed fields, so it reaches locations that typed
// traversal cannot: sub-schemas held in unknown keywords (the Extra map) and
// the internals of non-applicator keywords such as examples. The target
// resolves only when it is itself a schema — a JSON object or boolean. Any
// other target (a string, number, or missing member) yields nil, so a pointer
// into a non-schema value or a typo stays unresolved.
//
// Results are cached per (root, pointer): the same fallback is reached once per
// ref during gate checking and again for each instance node the ref is
// evaluated against, and the root is marshaled at most once per distinct
// pointer. This path runs only for the uncommon untyped-location pointer.
func (v *validator) resolveJSONPointerViaJSON(root *Schema, segments []string) *Schema {
	if v.jsonPointerCache == nil {
		v.jsonPointerCache = map[jsonPointerKey]*Schema{}
	}

	key := jsonPointerKey{root: root, pointer: strings.Join(segments, "\x00")}
	if cached, ok := v.jsonPointerCache[key]; ok {
		return cached
	}

	target := schemaAtJSONPointer(root, segments)
	v.jsonPointerCache[key] = target

	return target
}

// schemaAtJSONPointer navigates root's JSON encoding by segments and returns the
// located value as a Schema when it is itself a schema (a JSON object or
// boolean), or nil otherwise.
func schemaAtJSONPointer(root *Schema, segments []string) *Schema {
	data, err := json.Marshal(root)
	if err != nil {
		return nil
	}

	var node any

	err = json.Unmarshal(data, &node)
	if err != nil {
		return nil
	}

	for _, seg := range segments {
		switch container := node.(type) {
		case map[string]any:
			next, ok := container[seg]
			if !ok {
				return nil
			}

			node = next

		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(container) {
				return nil
			}

			node = container[idx]

		default:
			return nil
		}
	}

	switch node.(type) {
	case map[string]any, bool:
		target, err := json.Marshal(node)
		if err != nil {
			return nil
		}

		var schema Schema

		err = json.Unmarshal(target, &schema)
		if err != nil {
			return nil
		}

		return &schema

	default:
		return nil
	}
}

// traverseSchema navigates the schema tree by matching segment names to
// JSON tag names.
func (v *validator) traverseSchema(schema *Schema, segments []string) *Schema {
	if len(segments) == 0 || schema == nil {
		return schema
	}

	seg := segments[0]
	rest := segments[1:]

	// Try map fields first: $defs, definitions, properties, patternProperties,
	// dependentSchemas, DependencySchemas.
	switch seg {
	case "$defs":
		if schema.Defs != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Defs[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "definitions":
		if schema.Definitions != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Definitions[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "properties":
		if schema.Properties != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Properties[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "patternProperties":
		if schema.PatternProperties != nil {
			if len(rest) > 0 {
				if sub, ok := schema.PatternProperties[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "dependentSchemas":
		if schema.DependentSchemas != nil {
			if len(rest) > 0 {
				if sub, ok := schema.DependentSchemas[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "dependencies":
		// Draft-07: dependencies is marshaled from DependencySchemas.
		if schema.DependencySchemas != nil {
			if len(rest) > 0 {
				if sub, ok := schema.DependencySchemas[rest[0]]; ok {
					return v.traverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "items":
		if schema.Items != nil {
			return v.traverseSchema(schema.Items, rest)
		}
		// Array form (Draft-07 items as array): requires an index in rest.
		if len(rest) > 0 && len(schema.ItemsArray) > 0 {
			idx, err := strconv.Atoi(rest[0])
			if err == nil && idx >= 0 && idx < len(schema.ItemsArray) {
				return v.traverseSchema(schema.ItemsArray[idx], rest[1:])
			}
		}

		return nil

	case "additionalProperties":
		if schema.AdditionalProperties != nil {
			return v.traverseSchema(schema.AdditionalProperties, rest)
		}

		return nil

	case "additionalItems":
		if schema.AdditionalItems != nil {
			return v.traverseSchema(schema.AdditionalItems, rest)
		}

		return nil

	case "not":
		if schema.Not != nil {
			return v.traverseSchema(schema.Not, rest)
		}

		return nil

	case "if":
		if schema.If != nil {
			return v.traverseSchema(schema.If, rest)
		}

		return nil

	case "then":
		if schema.Then != nil {
			return v.traverseSchema(schema.Then, rest)
		}

		return nil

	case "else":
		if schema.Else != nil {
			return v.traverseSchema(schema.Else, rest)
		}

		return nil

	case "contains":
		if schema.Contains != nil {
			return v.traverseSchema(schema.Contains, rest)
		}

		return nil

	case "propertyNames":
		if schema.PropertyNames != nil {
			return v.traverseSchema(schema.PropertyNames, rest)
		}

		return nil

	case "unevaluatedProperties":
		if schema.UnevaluatedProperties != nil {
			return v.traverseSchema(schema.UnevaluatedProperties, rest)
		}

		return nil

	case "unevaluatedItems":
		if schema.UnevaluatedItems != nil {
			return v.traverseSchema(schema.UnevaluatedItems, rest)
		}

		return nil

	case "contentSchema":
		if schema.ContentSchema != nil {
			return v.traverseSchema(schema.ContentSchema, rest)
		}

		return nil
	}

	// Slice fields: allOf, anyOf, oneOf, prefixItems.
	idx := -1
	if len(rest) > 0 {
		n, err := strconv.Atoi(rest[0])
		if err == nil {
			idx = n
		}
	}

	switch seg {
	case "allOf":
		if idx >= 0 && idx < len(schema.AllOf) {
			return v.traverseSchema(schema.AllOf[idx], rest[1:])
		}

		return nil

	case "anyOf":
		if idx >= 0 && idx < len(schema.AnyOf) {
			return v.traverseSchema(schema.AnyOf[idx], rest[1:])
		}

		return nil

	case "oneOf":
		if idx >= 0 && idx < len(schema.OneOf) {
			return v.traverseSchema(schema.OneOf[idx], rest[1:])
		}

		return nil

	case "prefixItems":
		if idx >= 0 && idx < len(schema.PrefixItems) {
			return v.traverseSchema(schema.PrefixItems[idx], rest[1:])
		}

		return nil
	}

	return nil
}

// jsonPointerEscaper and jsonPointerUnescaper apply the RFC 6901 ~0/~1
// transforms in a single pass. NewReplacer matches leftmost-longest without
// rescanning its own output, so unescaping "~1" before "~0" is order-correct.
var (
	jsonPointerEscaper   = strings.NewReplacer("~", "~0", "/", "~1")
	jsonPointerUnescaper = strings.NewReplacer("~1", "/", "~0", "~")
)

// escapeJSONPointer escapes a string per RFC 6901.
func escapeJSONPointer(s string) string {
	return jsonPointerEscaper.Replace(s)
}

// unescapeJSONPointer unescapes a JSON Pointer segment per RFC 6901.
func unescapeJSONPointer(s string) string {
	return jsonPointerUnescaper.Replace(s)
}
