package jsonschema

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/format"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/regexcache"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
	"go.jacobcolvin.com/x/jsonschema/internal/vocab"
)

// ValidateOption configures validation behavior. Options are produced by
// this package's With* constructors; the interface form (rather than a func
// type) lets one option value serve several entry points, the way
// [WithRefResolver] serves both ValidateOption and [InlineOption].
type ValidateOption interface {
	applyValidate(v *validator)
}

// validateOptionFunc adapts a function to [ValidateOption].
type validateOptionFunc func(*validator)

func (f validateOptionFunc) applyValidate(v *validator) { f(v) }

// WithFormatValidator registers a custom format checker under the format
// name it checks (e.g. "uuid"), following [net/http.Handle]: the name lives
// at the registration site, so one checker implementation can serve several
// names. [FormatValidatorFunc] adapts a bare function. Registering a name
// again, including a built-in format name, replaces the previous checker. A
// nil f or an empty name is ignored.
func WithFormatValidator(name string, f FormatValidator) ValidateOption {
	return validateOptionFunc(func(v *validator) {
		if f != nil && name != "" {
			v.formatCheckers[name] = f
		}
	})
}

// WithFormats forces built-in format validation on or off, overriding the
// draft- and vocabulary-derived default. Without this option, format is
// asserted under Draft-07 (validation §7.2 permits it) and is annotation-only
// under Draft 2020-12 unless the format-assertion vocabulary is active (per
// validation §7.2.1, which requires format-assertion to be disabled by
// default). WithFormats(true) opts in to assertion regardless of draft or
// vocabulary; WithFormats(false) disables it entirely.
func WithFormats(enabled bool) ValidateOption {
	return validateOptionFunc(func(v *validator) { v.formatsForce = &enabled })
}

// WithContent enables assertion of the contentEncoding and contentMediaType
// keywords for string instances. By default these keywords are annotation-only
// (per the JSON Schema spec, which makes content assertion optional). With this
// option, a contentEncoding of base64 must decode and a contentMediaType of
// application/json must be valid JSON; other encodings and media types remain
// annotations. Non-string instances are unaffected. Mirrors [WithFormats].
func WithContent(enabled bool) ValidateOption {
	return validateOptionFunc(func(v *validator) { v.contentEnabled = enabled })
}

// WithResolveOptions passes [ResolveOptions] (an alias for the upstream
// options type) to Schema.Resolve for structural pre-validation. The
// validation walk resolves local fragment refs directly and remote/absolute
// refs via a configured [RefResolver] (see [WithRefResolver]).
func WithResolveOptions(opts *ResolveOptions) ValidateOption {
	return validateOptionFunc(func(v *validator) { v.resolveOpts = opts })
}

// WithVocabularies directly specifies the active vocabulary set for
// validation: the listed vocabulary URIs (e.g. [VocabValidation2020]) are
// active and every other vocabulary is inactive. This takes highest
// precedence, overriding any $vocabulary found in a metaschema resolved
// via [WithMetaSchemaResolver]. Calling it with no URIs is a no-op, leaving
// the metaschema or default resolution in effect.
//
// Vocabularies are a Draft 2020-12 concept: under Draft 7 the active set is
// always the full built-in set and this option has no effect.
func WithVocabularies(uris ...string) ValidateOption {
	return validateOptionFunc(func(v *validator) {
		if len(uris) == 0 {
			return
		}

		vocabs := make(map[string]bool, len(uris))
		for _, uri := range uris {
			vocabs[uri] = true
		}

		v.vocabOverride = vocabs
	})
}

// WithMetaSchemaResolver sets a [RefResolver] consulted with the root
// schema's $schema URI to look up its metaschema. The resolved metaschema's
// $vocabulary map determines the active vocabularies. The resolver decides
// the lookup's shape: a [SchemaMap] serves fixed metaschemas by exact $id,
// a [FileResolver] serves a directory of documents, and [ChainResolvers]
// composes the two with any lazily fetched set. [RefResolverFunc] adapts a
// bare function.
//
// The resolver is consulted once per compile, under the [Compile] context
// (the Must* entry points pass [context.Background]). A miss
// ([ErrNotResolved]) leaves the default vocabulary resolution in effect;
// any other resolver error fails compilation. A nil r restores the default
// (no metaschema lookup).
//
// Like [WithVocabularies], this affects only Draft 2020-12; under Draft 7 the
// metaschema's $vocabulary is ignored and the full built-in vocabulary set is
// used.
func WithMetaSchemaResolver(r RefResolver) ValidateOption {
	return validateOptionFunc(func(v *validator) { v.metaSchemaResolver = r })
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

// instanceLocation is the position in the instance that the validation walk is
// currently at, carried in two synchronized representations: the RFC 6901
// JSON Pointer string surfaced as [ValidationError.InstancePath], and the typed
// segments surfaced as [ValidationError.InstanceSegments]. The zero value is
// the root location (empty pointer, nil segments).
type instanceLocation struct {
	// The RFC 6901-encoded JSON Pointer.
	ptr string
	// One typed [Segment] per reference token of ptr.
	segs []Segment
}

// key returns the location of the object member named name, extending both
// representations. The full slice expression caps segs so sibling descents
// append into fresh backing arrays instead of aliasing a shared one.
func (l instanceLocation) key(name string) instanceLocation {
	return instanceLocation{
		ptr:  l.ptr + "/" + jsonptr.Escape(name),
		segs: append(l.segs[:len(l.segs):len(l.segs)], Segment{Key: name}),
	}
}

// index returns the location of the array element at index i, extending both
// representations. The full slice expression caps segs so sibling descents
// append into fresh backing arrays instead of aliasing a shared one.
func (l instanceLocation) index(i int) instanceLocation {
	return instanceLocation{
		ptr:  l.ptr + "/" + strconv.Itoa(i),
		segs: append(l.segs[:len(l.segs):len(l.segs)], Segment{Index: i, IsIndex: true}),
	}
}

// schemaLocation is the position in the schema that the validation walk is
// currently at, the schema-side counterpart of [instanceLocation]: the RFC
// 6901 JSON Pointer surfaced as [ValidationError.SchemaPath], and the typed
// segments surfaced as [ValidationError.SchemaSegments]. The zero value is
// the root location (empty pointer, nil segments).
type schemaLocation struct {
	// The RFC 6901-encoded JSON Pointer.
	ptr string
	// One typed [Segment] per reference token of ptr.
	segs []Segment
}

// kw returns the location of the keyword token named keyword, extending both
// representations. Keyword tokens contain no JSON Pointer specials, so no
// escaping is needed. The full slice expression caps segs so sibling
// descents append into fresh backing arrays instead of aliasing a shared
// one.
func (l schemaLocation) kw(keyword string) schemaLocation {
	return schemaLocation{
		ptr:  l.ptr + "/" + keyword,
		segs: append(l.segs[:len(l.segs):len(l.segs)], Segment{Key: keyword}),
	}
}

// key returns the location of the member named name under a map keyword
// (properties, patternProperties, dependentSchemas, ...), extending both
// representations with the aliasing discipline of [schemaLocation.kw].
func (l schemaLocation) key(name string) schemaLocation {
	return schemaLocation{
		ptr:  l.ptr + "/" + jsonptr.Escape(name),
		segs: append(l.segs[:len(l.segs):len(l.segs)], Segment{Key: name}),
	}
}

// idx returns the location of the element at index i under a list keyword
// (allOf, anyOf, oneOf, prefixItems, ...), extending both representations
// with the aliasing discipline of [schemaLocation.kw].
func (l schemaLocation) idx(i int) schemaLocation {
	return schemaLocation{
		ptr:  l.ptr + "/" + strconv.Itoa(i),
		segs: append(l.segs[:len(l.segs):len(l.segs)], Segment{Index: i, IsIndex: true}),
	}
}

// leafError builds a terminal validation error at the given instance location
// and the keyword token under the given schema location, copying both path
// representations from the typed locations so the four private path fields can
// never be mismatched at a call site. It is a fresh value each call, keeping
// validation safe to run concurrently on a shared [Validator].
func leafError(instancePath instanceLocation, schemaPath schemaLocation, keyword, msg string) *ValidationError {
	kwPath := schemaPath.kw(keyword)

	return &ValidationError{
		InstancePath: instancePath.ptr,
		segments:     instancePath.segs,
		SchemaPath:   kwPath.ptr,
		schemaSegs:   kwPath.segs,
		Keyword:      keyword,
		Message:      msg,
	}
}

// builtinFormat adapts a bare value-checking function to [FormatValidator]
// for the built-in formats, which use neither the context nor the name.
type builtinFormat func(string) error

// ValidateFormat calls f on value.
func (f builtinFormat) ValidateFormat(_ context.Context, _, value string) error {
	return f(value)
}

// validator holds state for a single validation run.
type validator struct {
	refResolveErr error       // last error from refResolver, consumed by validateRef/validateDynamicRef
	refResolver   RefResolver // optional remote ref resolver

	// The caller's context for the current compile or validation run, passed
	// to the resolver with every resolution call. It has the
	// same lifetime discipline as the other per-run state: Compile
	// sets it for the duration of compilation and clears it before the
	// validator is cached, and forInstance sets it per run, so a stored
	// context never outlives the call that supplied it. The Must* entry
	// points use [context.Background].
	ctx context.Context

	walked map[*Schema]bool // schemas already visited by walkSchema (cycle guard)

	// NumericBounds, patternCache, and patternProps below are compile-time
	// caches of derived per-schema state. They are populated once during
	// Compile by precompute, which runs single-threaded, and are read-only
	// afterward; forInstance shares them by reference across runs, so
	// concurrent Validate calls only read them. A schema reached only at
	// validation time (a remote or JSON-pointer fallback schema) is absent
	// from these maps, and the validation path falls back to computing the
	// value directly.
	numericBounds map[*Schema]*precomputedBounds // numeric bound keywords as rationals

	root                  *Schema
	resolveOpts           *ResolveOptions
	formatsForce          *bool           // explicit WithFormats override; nil if unset
	vocabOverride         map[string]bool // from WithVocabularies
	formatCheckers        map[string]FormatValidator
	uriRegistry           map[string]*Schema         // absolute URI → schema
	anchorRegistry        map[string]*Schema         // baseURI#anchor → schema
	dynamicAnchorRegistry map[string]*Schema         // baseURI#name → schema ($dynamicAnchor only)
	baseURIs              map[*Schema]string         // schema → its base URI
	metaSchemaResolver    RefResolver                // metaschema lookup by $schema URI (WithMetaSchemaResolver)
	jsonPointerCache      map[jsonPointerKey]*Schema // JSON-pointer fallback results, keyed by (root, pointer)
	refCache              map[refCacheKey]*Schema    // plain $ref resolutions, keyed by (schema, ref); successes only
	remoteMiss            map[string]error           // baseURIs the resolver could not serve, per run; nil value = plain miss
	visiting              map[visitKey]bool
	patternCache          map[*Schema]compiledPattern            // schema.Pattern compiled (see numericBounds)
	patternProps          map[*Schema]map[string]compiledPattern // patternProperties keys compiled (see numericBounds)
	constRats             map[*Schema]*big.Rat                   // numeric const value as a rational (see numericBounds)
	enumRats              map[*Schema][]*big.Rat                 // numeric enum members as rationals by index (see numericBounds)

	// Registrations for schemas materialized by the JSON-pointer fallback
	// (resolveJSONPointerViaJSON). Like jsonPointerCache they are per-run
	// scratch state, so concurrent runs never write the shared registries;
	// lookups consult the shared registry first and these second.
	fallbackURIRegistry    map[string]*Schema
	fallbackAnchorRegistry map[string]*Schema
	fallbackBaseURIs       map[*Schema]string

	// The WithDraft override; nil leaves the draft to $schema detection.
	draftOverride *Draft

	// The root document's base URI from [WithBaseURI]; "" leaves the base
	// to the root schema's $id.
	baseURI string

	dynamicScope []string // stack of resource base URIs entered during validation
	draft        Draft
	vocabs       vocab.Set // resolved active vocabularies

	formatsEnabled bool
	contentEnabled bool // assert contentEncoding/contentMediaType (WithContent)

	// True once this per-run validator holds its own copy of the five registry
	// maps. A run shares the compiled validator's maps by reference until it
	// first needs to write them (resolveRemote fetching a ref at validation
	// time), at which point ensureOwnedRegistries clones them so concurrent runs
	// never write shared state.
	registriesOwned bool

	// Treat $id as an inert annotation in walkSchema: no URI or anchor
	// registration, no base-URI change, in any form including the
	// Draft 7 fragment-only anchor form. Only the inliner's scratch
	// validators set it, for [WithRetrievalBase]; Compile never does,
	// so validation behavior is unaffected.
	inertIDs bool
}

func newValidator(ctx context.Context, schema *Schema, opts []ValidateOption) (*validator, error) {
	// A nil schema has no $schema, vocabulary, or structure to compile;
	// detectDraft and the registry walk would dereference it. Report it
	// through the error contract instead of panicking.
	if schema == nil {
		return nil, ErrNilSchema
	}

	v := &validator{
		root:           schema,
		formatCheckers: map[string]FormatValidator{},
		visiting:       map[visitKey]bool{},
		// The compile context, for resolver calls made while compiling: the
		// metaschema lookup below, and the remoteLoader and resolveRemote
		// calls Compile makes after construction. Compile drops it before the
		// validator is cached.
		ctx: ctx,
	}
	// Register built-in format checkers.
	for name, fn := range format.Validators() {
		v.formatCheckers[name] = builtinFormat(fn)
	}

	for _, opt := range opts {
		opt.applyValidate(v)
	}

	// Detect draft from $schema field; a WithDraft override wins.
	v.draft = detectDraft(schema)
	if v.draftOverride != nil {
		v.draft = *v.draftOverride
	}

	// Resolve active vocabularies.
	err := v.resolveVocabularies()
	if err != nil {
		return nil, err
	}

	// Resolve whether the format keyword is asserted (depends on draft,
	// vocabularies, and any explicit WithFormats override).
	v.resolveFormats()

	v.buildRegistry()

	// The dynamic scope is seeded per run by forInstance, the single source for
	// the rule; the compiled validator is never walked directly, so it needs no
	// scope here.

	return v, nil
}

// forInstance returns a per-validation view of a compiled validator with fresh
// mutable walk state (the visiting set, dynamic scope, JSON-pointer cache, and
// ref-resolution scratch), so a [Validator] can be reused and is safe for
// concurrent use. The immutable per-schema state (registries, resolved
// vocabularies, draft, and format configuration) is shared. The caller's ctx
// is carried on the per-run copy so a [RefResolver] resolving a remote
// ref at validation time sees the context of the run that triggered it.
//
// The five registry maps are shared from the compiled validator by reference;
// they are immutable after Compile, so concurrent runs read them safely. A run
// that fetches a remote ref at validation time (via resolveRemote) must write
// them, so it first clones them privately through ensureOwnedRegistries — a
// copy-on-write that spares the O(registry size) clone for the common run that
// resolves nothing remotely (including a resolver-configured schema whose refs
// were all cached during Compile).
func (v *validator) forInstance(ctx context.Context) *validator {
	rv := *v
	rv.ctx = ctx
	rv.registriesOwned = false
	rv.visiting = map[visitKey]bool{}
	rv.jsonPointerCache = nil
	rv.refCache = nil
	rv.remoteMiss = nil
	rv.fallbackURIRegistry = nil
	rv.fallbackAnchorRegistry = nil
	rv.fallbackBaseURIs = nil
	rv.refResolveErr = nil

	if rv.draft == Draft2020 {
		rv.dynamicScope = []string{rv.baseURIs[rv.root]}
	} else {
		rv.dynamicScope = nil
	}

	return &rv
}

// ensureOwnedRegistries gives this run its own copy of the five registry maps
// before it writes any of them, so a resolveRemote registration cannot race a
// concurrent run sharing the compiled validator's maps. It is idempotent: the
// first remote fetch in a run clones, later fetches reuse the owned copies.
func (v *validator) ensureOwnedRegistries() {
	if v.registriesOwned {
		return
	}

	v.uriRegistry = maps.Clone(v.uriRegistry)
	v.anchorRegistry = maps.Clone(v.anchorRegistry)
	v.dynamicAnchorRegistry = maps.Clone(v.dynamicAnchorRegistry)
	v.baseURIs = maps.Clone(v.baseURIs)
	v.walked = maps.Clone(v.walked)
	v.registriesOwned = true
}

// resolveVocabularies determines the active vocabulary set.
//
// Resolution priority:
//  1. WithVocabularies direct override (highest).
//  2. WithMetaSchemaResolver lookup (the resolver is consulted with the root
//     $schema URI).
//  3. Default: vocab.All (backward compatible).
//
// Draft-07 always gets vocab.All; vocabulary is a 2020-12 concept.
func (v *validator) resolveVocabularies() error {
	// Draft-07 has no vocabulary concept.
	if v.draft != Draft2020 {
		v.vocabs = vocab.All()

		return nil
	}

	rawVocabs := v.vocabOverride
	fromOverride := rawVocabs != nil

	if rawVocabs == nil && v.metaSchemaResolver != nil && v.root.Schema != "" {
		ms, err := v.metaSchemaResolver.ResolveRef(v.ctx, v.root.Schema)
		if err != nil && !errors.Is(err, ErrNotResolved) {
			return fmt.Errorf("resolve metaschema %q: %w", v.root.Schema, err)
		}

		if err == nil && ms != nil && len(ms.Vocabulary) > 0 {
			rawVocabs = ms.Vocabulary
		}
	}

	if rawVocabs == nil {
		v.vocabs = vocab.All()

		return nil
	}

	if uri := vocab.CheckUnknown(rawVocabs); uri != "" {
		return fmt.Errorf("%w: %s", ErrUnknownVocabulary, uri)
	}

	// The core vocabulary MUST be present and required (true): JSON Schema
	// 2020-12 section 8.1.2 makes a $vocabulary that omits or disables core
	// non-conformant. This constrains a metaschema's $vocabulary map, not the
	// WithVocabularies API override, which selects the active set directly and
	// carries no such requirement (its doc lists the active set, full stop).
	if !fromOverride {
		if required, ok := rawVocabs[VocabCore2020]; !ok || !required {
			return fmt.Errorf("%w: core vocabulary must be required", ErrUnknownVocabulary)
		}
	}

	v.vocabs = vocab.Resolve(rawVocabs)

	return nil
}

// resolveFormats determines whether the format keyword is asserted during the
// walk. An explicit WithFormats choice wins. Otherwise Draft-07 asserts format
// (validation §7.2 permits it), while Draft 2020-12 asserts only when the
// format-assertion vocabulary is active, annotation-only by default under the
// standard meta-schema, per validation §7.2.1's "MUST be disabled by default".
func (v *validator) resolveFormats() {
	switch {
	case v.formatsForce != nil:
		v.formatsEnabled = *v.formatsForce
	case v.draft == Draft7:
		v.formatsEnabled = true
	default:
		v.formatsEnabled = v.vocabs.FormatAssertion
	}
}

// initRegistries allocates the five empty registry maps that every fresh
// registry walk fills.
func (v *validator) initRegistries() {
	v.uriRegistry = map[string]*Schema{}
	v.anchorRegistry = map[string]*Schema{}
	v.dynamicAnchorRegistry = map[string]*Schema{}
	v.baseURIs = map[*Schema]string{}
	v.walked = map[*Schema]bool{}
}

// buildRegistry walks the entire schema tree to build URI, anchor, and
// base-URI registries for $id and $anchor resolution. The walk is seeded
// with the [WithBaseURI] base, so non-local refs absolutize against it
// exactly as they would against a root $id.
func (v *validator) buildRegistry() {
	v.initRegistries()

	base := uriref.NormalizeBaseURI(v.baseURI)
	v.walkSchema(v.root, base)

	// Register the root document under its base URI when its own $id did
	// not already claim one, so a ref that absolutizes back to the root
	// document resolves to this copy instead of being fetched.
	if base != "" {
		if _, ok := v.uriRegistry[base]; !ok {
			v.uriRegistry[base] = v.root
		}
	}
}

// walkSchema recursively walks a schema tree, registering $id and $anchor
// entries and computing base URIs. An $id/$anchor that repeats a key already in
// the registry overwrites it, which is the right behavior for the single
// authoritative document buildRegistry walks.
func (v *validator) walkSchema(schema *Schema, parentBase string) {
	v.walkSchemaInto(schema, parentBase, false)
}

// walkFetchedSchema walks a document fetched from a [RefResolver], registering
// its nested $id/$anchor/$dynamicAnchor entries only when the key is not already
// claimed. A fetched document whose nested $id resolves to an already-loaded URI
// (the root base or an earlier fetched document) must not overwrite that entry,
// so the already-loaded document keeps priority while the fetched document's own
// refs still resolve. Unlike [validator.registerFallbackSchema] these
// registrations land in the shared registry, so they survive the per-run clone
// forInstance makes for the compile-time loader path; at validation time the
// registry is already that per-run clone.
func (v *validator) walkFetchedSchema(schema *Schema, parentBase string) {
	v.walkSchemaInto(schema, parentBase, true)
}

// walkSchemaInto is the shared walk core. When onlyIfAbsent is true, a
// string-keyed registration ($id URI, $anchor, $dynamicAnchor) yields to an
// existing entry instead of overwriting it; the pointer-keyed base URI is always
// recorded so every node still resolves its own relative refs.
func (v *validator) walkSchemaInto(schema *Schema, parentBase string, onlyIfAbsent bool) {
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

	if schema.ID != "" && !v.inertIDs {
		if uriref.IsFragmentOnly(schema.ID) {
			// Draft-07: fragment-only $id acts as an anchor.
			anchor := schema.ID[1:] // strip leading '#'
			registerSchema(v.anchorRegistry, uriref.AnchorKey(currentBase, anchor), schema, onlyIfAbsent)
		} else {
			resolved := uriref.IDBase(currentBase, schema.ID)
			registerSchema(v.uriRegistry, resolved, schema, onlyIfAbsent)

			currentBase = resolved
		}
	}

	// 2020-12: $anchor keyword.
	if schema.Anchor != "" {
		registerSchema(v.anchorRegistry, uriref.AnchorKey(currentBase, schema.Anchor), schema, onlyIfAbsent)
	}

	// 2020-12: $dynamicAnchor keyword.
	// Also registered as a regular anchor (accessible via $ref).
	if schema.DynamicAnchor != "" {
		key := uriref.AnchorKey(currentBase, schema.DynamicAnchor)
		registerSchema(v.anchorRegistry, key, schema, onlyIfAbsent)
		registerSchema(v.dynamicAnchorRegistry, key, schema, onlyIfAbsent)
	}

	// Store base URI for this schema (used during $ref resolution).
	// Draft-07 exception: sibling $id doesn't affect $ref resolution.
	if v.draft == Draft7 && schema.Ref != "" && schema.ID != "" && !uriref.IsFragmentOnly(schema.ID) {
		v.baseURIs[schema] = parentBase
	} else {
		v.baseURIs[schema] = currentBase
	}

	// Recurse into all sub-schema fields. Every child inherits currentBase, so
	// iterating SubschemaEntries (the single source of truth for the field
	// list) reproduces the previous per-keyword recursion; its sorted-key map
	// order also makes registry construction deterministic.
	for _, e := range SubschemaEntries(schema) {
		v.walkSchemaInto(e.Schema, currentBase, onlyIfAbsent)
	}
}

// registerSchema stores s under key in reg. When onlyIfAbsent is true an
// existing entry is preserved, so a fetched document cannot overwrite a URI or
// anchor already claimed by the root or an earlier document.
func registerSchema(reg map[string]*Schema, key string, s *Schema, onlyIfAbsent bool) {
	if onlyIfAbsent {
		if _, ok := reg[key]; ok {
			return
		}
	}

	reg[key] = s
}

// precomputedBounds holds the numeric bound keywords of a schema as rationals,
// converted once at Compile time so validateNumeric and validateNumericUnbounded
// reuse them instead of re-parsing the float64 bounds on every numeric instance.
// A nil field denotes either an absent keyword or a NaN/Inf bound that has no
// rational form (mirroring [numrat.Float64ToRat]). The rationals are operands only:
// comparisons read them and never mutate them.
type precomputedBounds struct {
	multipleOf       *big.Rat
	minimum          *big.Rat
	maximum          *big.Rat
	exclusiveMinimum *big.Rat
	exclusiveMaximum *big.Rat
}

// compiledPattern caches the result of compiling a regular expression pattern at
// Compile time. It records the compiled regexp or, when the pattern is one Go's
// RE2 engine rejects, the compile error, so validation reproduces the same
// fail-closed behavior it would on a fresh [regexcache.Compile] call.
type compiledPattern struct {
	re  *regexp.Regexp
	err error
}

// precompute populates the read-only per-schema caches (numeric bounds and
// compiled patterns) by traversing every schema reachable from the root once.
// It runs single-threaded during Compile, before the [Validator] is shared, so
// the caches it builds are never written concurrently. The traversal delegates
// to [SubschemaEntries] for the sub-schema field list and consults only schema
// fields and its own visited set; it does not touch the URI, anchor, or
// base-URI registries,
// which keeps the validation-time fallback walk ([registerFallbackSchema]) from
// populating these caches.
func (v *validator) precompute() {
	v.numericBounds = map[*Schema]*precomputedBounds{}
	v.patternCache = map[*Schema]compiledPattern{}
	v.patternProps = map[*Schema]map[string]compiledPattern{}
	v.constRats = map[*Schema]*big.Rat{}
	v.enumRats = map[*Schema][]*big.Rat{}

	visited := map[*Schema]bool{}
	v.precomputeSchema(v.root, visited)
}

// precomputeSchema records the derived caches for one schema and recurses into
// its sub-schemas, guarding against schema graph cycles with visited.
func (v *validator) precomputeSchema(schema *Schema, visited map[*Schema]bool) {
	if schema == nil || visited[schema] {
		return
	}

	visited[schema] = true

	if b := computeBounds(schema); b != nil {
		v.numericBounds[schema] = b
	}

	if schema.Pattern != "" {
		re, err := regexcache.Compile(schema.Pattern)
		v.patternCache[schema] = compiledPattern{re: re, err: err}
	}

	if len(schema.PatternProperties) > 0 {
		compiled := make(map[string]compiledPattern, len(schema.PatternProperties))
		for pattern := range schema.PatternProperties {
			re, err := regexcache.Compile(pattern)
			compiled[pattern] = compiledPattern{re: re, err: err}
		}

		v.patternProps[schema] = compiled
	}

	if schema.Const != nil {
		if r, ok := numrat.SchemaNumberRat(*schema.Const); ok {
			v.constRats[schema] = r
		}
	}

	if rats := numrat.EnumMemberRats(schema.Enum); rats != nil {
		v.enumRats[schema] = rats
	}

	for _, e := range SubschemaEntries(schema) {
		v.precomputeSchema(e.Schema, visited)
	}
}

// computeBounds converts a schema's numeric bound keywords to rationals,
// returning nil when the schema sets none of them so the cache holds an entry
// only for schemas that constrain numbers.
func computeBounds(schema *Schema) *precomputedBounds {
	if schema.MultipleOf == nil && schema.Minimum == nil && schema.Maximum == nil &&
		schema.ExclusiveMinimum == nil && schema.ExclusiveMaximum == nil {
		return nil
	}

	b := &precomputedBounds{}
	if schema.MultipleOf != nil {
		b.multipleOf = numrat.Float64ToRat(*schema.MultipleOf)
	}

	if schema.Minimum != nil {
		b.minimum = numrat.Float64ToRat(*schema.Minimum)
	}

	if schema.Maximum != nil {
		b.maximum = numrat.Float64ToRat(*schema.Maximum)
	}

	if schema.ExclusiveMinimum != nil {
		b.exclusiveMinimum = numrat.Float64ToRat(*schema.ExclusiveMinimum)
	}

	if schema.ExclusiveMaximum != nil {
		b.exclusiveMaximum = numrat.Float64ToRat(*schema.ExclusiveMaximum)
	}

	return b
}

// runContext returns the context of the current compile or validation run
// for hook invocations (the [RefResolver], registered [FormatValidator]
// values), falling back to [context.Background] when no entry point set one.
func (v *validator) runContext() context.Context {
	if v.ctx == nil {
		return context.Background()
	}

	return v.ctx
}

// callResolver invokes the configured resolver for uri under the context of
// the current compile or validation run, with ok reporting whether the
// resolver served the URI: an ErrNotResolved answer becomes ok false with a
// nil error. A nil schema with a nil error is normalized to the
// not-resolved answer too, upholding the [RefResolver] contract that no
// caller dereferences a nil document.
func (v *validator) callResolver(uri string) (*Schema, bool, error) {
	s, err := v.refResolver.ResolveRef(v.runContext(), uri)
	if errors.Is(err, ErrNotResolved) {
		return nil, false, nil
	}

	if err != nil {
		//nolint:wrapcheck // resolveRemote wraps the error with ErrRefResolve; remoteLoader tolerates it.
		return nil, false, err
	}

	if s == nil {
		return nil, false, nil
	}

	return s, true, nil
}

// resolveRemote calls the configured [RefResolver] to fetch a remote schema,
// registers it under baseURI, and returns it. On error it stores the error in
// refResolveErr and returns nil. A success is served from the registry on later
// calls; a miss or error is recorded in a per-run negative cache (remoteMiss),
// so the resolver is consulted at most once per baseURI in a run even when many
// instance nodes reference an unresolvable URI. The recorded error is still
// re-raised into refResolveErr on each evaluation.
//
// The fetched document's own nested $ids and anchors are registered in
// only-if-absent mode (see [validator.walkFetchedSchema]), so a document whose
// nested $id resolves to an already-loaded URI (the root base or an earlier
// fetched document) keeps the already-loaded entry's priority while the fetched
// document's own refs still resolve.
func (v *validator) resolveRemote(baseURI string) *Schema {
	if v.refResolver == nil {
		return nil
	}

	if recorded, seen := v.remoteMiss[baseURI]; seen {
		if recorded != nil {
			v.refResolveErr = fmt.Errorf("%w: %w", ErrRefResolve, recorded)
		}

		return nil
	}

	schema, ok, err := v.callResolver(baseURI)
	if err != nil {
		v.refResolveErr = fmt.Errorf("%w: %w", ErrRefResolve, err)
		v.recordRemoteMiss(baseURI, err)

		return nil
	}

	if !ok {
		v.recordRemoteMiss(baseURI, nil)

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

	// Clone the registries into this run's own copies before the first remote
	// registration so the writes below cannot race a concurrent run still
	// sharing the compiled validator's maps. At validation time these
	// registrations then live only for this run.
	v.ensureOwnedRegistries()

	// Register the copy under baseURI, walking its own nested $id/$anchor
	// entries in only-if-absent mode so they cannot clobber an already-loaded
	// entry.
	v.uriRegistry[baseURI] = cp
	v.walkFetchedSchema(cp, baseURI)

	return cp
}

// recordRemoteMiss notes that the resolver could not serve baseURI this run, so
// resolveRemote skips re-calling it. A nil err records a plain miss; a non-nil
// err is replayed into refResolveErr on each later evaluation of the same ref.
func (v *validator) recordRemoteMiss(baseURI string, err error) {
	if v.remoteMiss == nil {
		v.remoteMiss = map[string]error{}
	}

	v.remoteMiss[baseURI] = err
}

// remoteLoader returns a [jsonschema.Loader] for upstream Schema.Resolve.
// When a [RefResolver] is configured, resolved schemas are registered in the
// URI/anchor registries (caching them for the validation walk). If no
// resolver is configured or the resolver misses or fails, an empty schema is
// returned so Schema.Resolve doesn't fail.
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
			s, ok, err := v.callResolver(uriStr)
			if err == nil && ok {
				// Deep-copy so the upstream resolver's mutations don't
				// affect the original schema from the RefResolver.
				cp, cpErr := cloneSchema(s)
				if cpErr != nil {
					return nil, fmt.Errorf("clone resolved schema: %w", cpErr)
				}

				// Register the copy under uriStr so subsequent lookups
				// during both Schema.Resolve and the validation walk
				// find it without re-calling the resolver. Its own nested
				// $ids/anchors are walked in only-if-absent mode so a
				// fetched doc cannot clobber an already-loaded entry. This
				// runs at compile time, so the registrations land in the
				// compiled registry every per-run validator then shares.
				v.uriRegistry[uriStr] = cp
				v.walkFetchedSchema(cp, uriStr)

				return cp, nil
			}
		}

		// Return empty schema so Schema.Resolve can proceed.
		return &Schema{}, nil
	}
}

// cloneSchema deep-copies a [Schema] via JSON round-trip, restoring the
// render-only PropertyOrder field the round-trip drops. The copy logic lives in
// [schemaclone.Clone]; the lockstep PropertyOrder restore walks this package's
// [SubschemaEntries] traversal, threaded in as [schemaChildren].
func cloneSchema(s *Schema) (*Schema, error) {
	//nolint:wrapcheck // Clone already wraps with "clone schema:".
	return schemaclone.Clone(s, schemaChildren)
}

// schemaChildren returns the direct sub-schemas of s in [SubschemaEntries]
// order, the traversal [schemaclone.Clone] walks to pair nodes when restoring
// PropertyOrder.
func schemaChildren(s *Schema) []*Schema {
	entries := SubschemaEntries(s)

	children := make([]*Schema, len(entries))
	for i, entry := range entries {
		children[i] = entry.Schema
	}

	return children
}

// detectDraft determines the draft from the root schema's $schema field.
func detectDraft(s *Schema) Draft {
	switch s.Schema {
	case Draft7.schemaURI(),
		"http://json-schema.org/draft-07/schema",
		"https://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft-07/schema":
		return Draft7
	case Draft2020.schemaURI(),
		"https://json-schema.org/draft/2020-12/schema#":
		return Draft2020
	default:
		return Draft2020
	}
}

// Validator is a schema compiled for repeated validation. Constructing it does
// the per-schema work once, so each subsequent validation only walks the
// instance. That work is walking the schema to build the URI/anchor
// registries, running [jsonschema.Schema.Resolve] for structural
// pre-validation, and detecting the draft and active vocabularies.
//
// A Validator is safe for concurrent use by multiple goroutines.
// [Validator.Schema] and [Validator.Draft] expose what it validates, so a
// compiled validator can be passed across package boundaries without the
// schema riding alongside it.
type Validator struct {
	proto *validator
}

// Schema returns the root schema the Validator was compiled for: the very
// *Schema given to [Compile], not a copy, so a consumer handed only the
// Validator can still inspect, marshal, or [Inline] what it validates.
// The compiled caches are derived from the schema at Compile time; treat the
// returned schema as read-only, and recompile after any mutation.
func (c *Validator) Schema() *Schema {
	return c.proto.root
}

// Draft returns the draft the Validator validates under: the [WithDraft]
// override when one was given, otherwise the draft detected from the root
// schema's $schema field (defaulting to [Draft2020]).
func (c *Validator) Draft() Draft {
	return c.proto.draft
}

// Compile prepares a [Validator] for schema, performing all per-schema work up
// front so the returned validator can be reused across many instances. Prefer
// it to [Validate] when validating more than one instance against the same
// schema.
//
// It returns an error when the options are invalid or the schema fails
// structural pre-validation.
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for refs
// resolved during compilation. It is not retained by the returned
// [Validator]: refs reached only at validation time resolve under the
// context passed to [Validator.Validate] or [Validator.ValidateJSON].
//
// MustCompile is Compile with [context.Background], panicking on error.
func Compile(ctx context.Context, schema *Schema, opts ...ValidateOption) (*Validator, error) {
	// The compile context rides on the validator's ctx field for resolver
	// calls made while compiling (the metaschema lookup, remoteLoader during
	// Schema.Resolve, and resolveRemote via resolveErrorIsRefOnly).
	v, err := newValidator(ctx, schema, opts)
	if err != nil {
		return nil, err
	}

	// Reject unknown type names up front. Schema.Resolve does not check the
	// type vocabulary, and a typo'd type otherwise compiles cleanly and then
	// rejects every instance: a confusing runtime failure instead of a clear
	// construction error.
	err = CheckTypeNames(schema)
	if err != nil {
		return nil, err
	}

	// Precompute derived per-schema state (numeric bounds and compiled
	// patterns) while still single-threaded, so the returned Validator only
	// reads these caches once shared across goroutines.
	v.precompute()

	// Structural pre-validation via Schema.Resolve.
	// A Loader is always provided so Schema.Resolve doesn't fail on remote
	// refs. When a RefResolver is configured, it is called during loading
	// and the result is cached in the URI registry so the validation walk
	// never re-calls the resolver for the same URI.
	// Copy the caller's options so assigning Loader doesn't mutate a
	// *ResolveOptions shared across calls.
	var resolveOpts ResolveOptions

	if v.resolveOpts != nil {
		resolveOpts = *v.resolveOpts
	}

	if resolveOpts.Loader == nil {
		// The compile context reaches the resolver through the ctx field set
		// above: the loader runs inside deep upstream Resolve machinery that
		// cannot thread a parameter.
		//nolint:contextcheck // See the comment above.
		resolveOpts.Loader = v.remoteLoader()
	}

	_, err = schema.Resolve(&resolveOpts)
	//nolint:contextcheck // The compile context rides on the ctx field set above.
	if err != nil && !v.resolveErrorIsRefOnly(schema, resolveOpts) {
		return nil, fmt.Errorf("schema resolve: %w", err)
	}

	// Drop the compile context so the cached validator never holds a stale or
	// canceled context; each validation run supplies its own via forInstance.
	v.ctx = nil

	return &Validator{proto: v}, nil
}

// MustCompile is [Compile] with [context.Background] but panics on error;
// intended for package-scope validators, where for a static schema and fixed
// options compilation either always succeeds or always fails, so a failure
// is a programming error best surfaced at startup. It follows
// [regexp.MustCompile] and [MustGenerateFor].
func MustCompile(schema *Schema, opts ...ValidateOption) *Validator {
	v, err := Compile(context.Background(), schema, opts...)
	if err != nil {
		panic(err)
	}

	return v
}

// ParseSchemaValue converts an already-decoded JSON schema document to a
// [*Schema]. The document doc must be a bool (true is the empty schema;
// false is the schema that rejects every instance) or a map[string]any. Any
// other dynamic type returns an error wrapping [ErrInvalidSchemaDocument]
// naming the Go type. This includes nil, the decoding of a top-level JSON
// null, which [Schema.UnmarshalJSON] silently coerces to the false schema.
// Values produced by [Normalize] ([json.Number] leaves) convert correctly.
func ParseSchemaValue(doc any) (*Schema, error) {
	switch d := doc.(type) {
	case bool:
		if d {
			return &Schema{}, nil
		}

		// The boolean false schema form {"not": {}} (see [IsFalseSchema]),
		// matching what the upstream produces when unmarshaling JSON false.
		return &Schema{Not: &Schema{}}, nil

	case map[string]any:
		// Round-trip through encoding/json, delegating keyword parsing to the
		// upstream UnmarshalJSON. A [json.Number] leaf marshals verbatim as a
		// JSON number, so a [Normalize]d document converts exactly.
		data, err := json.Marshal(d)
		if err != nil {
			return nil, fmt.Errorf("encode schema document: %w", err)
		}

		var s Schema

		err = json.Unmarshal(data, &s)
		if err != nil {
			return nil, fmt.Errorf("decode schema document: %w", err)
		}

		return &s, nil

	default:
		return nil, fmt.Errorf("%w: got %T", ErrInvalidSchemaDocument, doc)
	}
}

// ParseSchema decodes data as a single JSON schema document and returns it
// as a [*Schema] without compiling it, for consumers that work with the schema
// itself, through [Inline], [Walk], or programmatic editing, rather than
// validating instances against it. It applies the same decode
// discipline as [CompileJSON] (which is equivalent to compiling its result):
// numbers decode as [json.Number] so large integer keywords survive the
// round-trip into the Schema, and trailing data after the document is rejected.
// A top-level value that is not an object or boolean returns an error wrapping
// [ErrInvalidSchemaDocument]; this includes JSON null, which unmarshaling into
// a [Schema] directly silently coerces to the false schema. Malformed JSON
// returns the wrapped decode error without the sentinel.
func ParseSchema(data []byte) (*Schema, error) {
	doc, err := normalize.DecodeJSONInstance(data)
	if err != nil {
		return nil, err //nolint:wrapcheck // DecodeJSONInstance already wraps with "JSON decode:".
	}

	return ParseSchemaValue(doc)
}

// CompileJSON decodes data as a single JSON schema document with
// [ParseSchema] and compiles it with [Compile]. It is the schema-side
// counterpart of [Validator.ValidateJSON]: numbers decode as [json.Number], and trailing
// data after the document is rejected. A top-level value that is not an object
// or boolean returns an error wrapping [ErrInvalidSchemaDocument]; this
// includes JSON null, which unmarshaling into a [Schema] directly silently
// coerces to the false schema. Malformed JSON returns the wrapped decode error
// without the sentinel.
//
// The context is passed to the [RefResolver] for refs resolved during
// compilation (see [Compile]).
func CompileJSON(ctx context.Context, data []byte, opts ...ValidateOption) (*Validator, error) {
	schema, err := ParseSchema(data)
	if err != nil {
		return nil, err
	}

	return Compile(ctx, schema, opts...)
}

// MustCompileJSON is [CompileJSON] with [context.Background] but panics on
// error; intended for package-scope validators compiled from static schema
// documents, such as files brought in with go:embed, following
// [MustCompile].
func MustCompileJSON(data []byte, opts ...ValidateOption) *Validator {
	v, err := CompileJSON(context.Background(), data, opts...)
	if err != nil {
		panic(err)
	}

	return v
}

// CheckTypeNames verifies that every type keyword reachable from schema
// names one of the seven JSON Schema type names ("null", "boolean",
// "string", "integer", "number", "object", "array"). It returns nil or an
// error wrapping [ErrInvalidType] that includes the schema path of the
// first offending keyword. It is the standalone form of the check [Compile]
// runs before resolution, for vetting structurally messy schemas without
// compiling them: it needs no registry, resolves no references, follows
// only typed sub-schema fields, and tolerates cyclic schema graphs. A nil
// schema returns nil.
func CheckTypeNames(schema *Schema) error {
	return checkTypeNames(schema, "", map[*Schema]bool{})
}

// checkTypeNames implements [CheckTypeNames], verifying that every type keyword
// reachable from schema names one of the seven JSON Schema types and returning
// an error wrapping [ErrInvalidType] for the first violation. The traversal
// uses [SubschemaEntries] for the sub-schema field list, appending each entry's
// Pointer so the error locates the offending keyword; visited guards
// against schema graph cycles. The check is draft-agnostic: neither draft
// defines type names beyond the canonical seven.
func checkTypeNames(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	if schema.Type != "" && !typename.Valid(schema.Type) {
		return fmt.Errorf("%w: %q at %s/type", ErrInvalidType, schema.Type, schemaPath)
	}

	for _, name := range schema.Types {
		if !typename.Valid(name) {
			return fmt.Errorf("%w: %q at %s/type", ErrInvalidType, name, schemaPath)
		}
	}

	// SubschemaEntries is the single source of truth for the sub-schema field
	// list, and its Pointer reproduces the "/keyword[/key-or-index]" tokens
	// this check previously built by hand (member keys carry ~0/~1 escaping,
	// map children come in sorted-key order for deterministic violations).
	for _, entry := range SubschemaEntries(schema) {
		err := checkTypeNames(entry.Schema, schemaPath+entry.Pointer, visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// Validate validates a pre-parsed Go value against the compiled schema.
//
// Accepted instance types: map[string]any, []any, string, float64,
// [json.Number], bool, nil. The Go numeric kinds that encoding/json does not
// produce are accepted too, namely the signed and unsigned integer types and
// float32; they are normalized via [Normalize], so values decoded from YAML or
// TOML or built by hand validate directly (integers exactly, at any
// magnitude). Go structs are not accepted; passing any other type returns an
// error (marshal to JSON or use [Validator.ValidateJSON] instead).
//
// Returns nil on success or an error that can be unwrapped to [*ValidationError]
// via [errors.AsType].
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for remote
// refs reached during this validation run, so a resolver that fetches over
// the network can honor cancellation and deadlines. The context is held only
// for the duration of the run, never by the [Validator] itself.
func (c *Validator) Validate(ctx context.Context, instance any) error {
	instance, err := normalizeAndCheck(instance)
	if err != nil {
		return err
	}

	return c.validateNormalized(ctx, instance)
}

// normalizeAndCheck normalizes instance and reports an error if, after
// normalization, its type or a nested container leaf is not one the validation
// walk accepts. The message lists the accepted types in one place so the two
// entry points cannot drift.
func normalizeAndCheck(instance any) (any, error) {
	instance = Normalize(instance)
	if !normalize.Accepted(instance) {
		return nil, fmt.Errorf(
			"instance of type %T is not accepted: accepted types are map[string]any, "+
				"[]any, string, bool, nil, and the numeric types; marshal to JSON or use Validator.ValidateJSON",
			instance,
		)
	}

	return instance, nil
}

// validateNormalized validates an already-normalized, accepted instance,
// returning nil on success or the assembled *ValidationError. The one-shot
// [Validate] entry point calls it directly so an instance it already normalized
// is not walked a second time.
func (c *Validator) validateNormalized(ctx context.Context, instance any) error {
	v := c.proto.forInstance(ctx)

	// The run context reaches the resolver through the per-run ctx field set
	// by forInstance: the recursive walk cannot thread a parameter.
	//nolint:contextcheck // See the comment above.
	errs := v.validate(v.root, instance, instanceLocation{}, schemaLocation{}, nil)
	if len(errs) == 0 {
		return nil
	}

	if len(errs) == 1 {
		return errs[0]
	}

	return &ValidationError{Causes: errs}
}

// ValidateJSON decodes data as a JSON instance (numbers as [json.Number]) and
// validates it against the compiled schema.
//
// The context is passed to the [RefResolver] for remote refs reached during
// this validation run (see [Validator.Validate]).
func (c *Validator) ValidateJSON(ctx context.Context, data []byte) error {
	instance, err := normalize.DecodeJSONInstance(data)
	if err != nil {
		return err //nolint:wrapcheck // DecodeJSONInstance already wraps with "JSON decode:".
	}

	return c.Validate(ctx, instance)
}

// ValidateValue marshals v with encoding/json and validates its JSON form
// against the compiled schema. It accepts the Go values [Validator.Validate]
// rejects, namely structs and other types encoding/json can marshal, so an
// instance of the very type a schema was generated for validates in one
// call. What is validated is the value's marshaled form, exactly what a JSON
// consumer of the value would see: json tags, omitempty and omitzero, and
// MarshalJSON implementations all apply. The bytes are decoded back with the
// [Validator.ValidateJSON] discipline (numbers as [json.Number]).
//
// Returns nil on success or an error that can be unwrapped to
// [*ValidationError] via [errors.AsType]. A value encoding/json cannot marshal
// returns the wrapped marshal error, which does not unwrap to
// [*ValidationError]; this covers channels, cyclic values, and unsupported
// floats.
//
// The context is passed to the [RefResolver] for remote refs reached during
// this validation run (see [Validator.Validate]).
func (c *Validator) ValidateValue(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal instance: %w", err)
	}

	return c.ValidateJSON(ctx, data)
}

// Validate validates a pre-parsed Go value against a JSON Schema. It compiles
// schema and validates instance in one call; to validate many instances against
// the same schema, call [Compile] once and reuse the returned [Validator].
//
// Accepted instance types: map[string]any, []any, string, float64,
// [json.Number], bool, nil. The Go numeric kinds that encoding/json does not
// produce are accepted too, namely the signed and unsigned integer types and
// float32; they are normalized via [Normalize]. Go structs are not accepted;
// passing any other type returns an error (marshal to JSON or use
// [Validator.ValidateJSON] instead).
//
// Returns nil on success or an error that can be unwrapped to
// [*ValidationError] via [errors.AsType].
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for refs
// resolved both while compiling schema and during the validation run.
func Validate(ctx context.Context, schema *Schema, instance any, opts ...ValidateOption) error {
	// Check the instance type before compiling so an unaccepted instance is
	// reported without the cost of (or any error from) schema preparation.
	instance, err := normalizeAndCheck(instance)
	if err != nil {
		return err
	}

	c, err := Compile(ctx, schema, opts...)
	if err != nil {
		return err
	}

	// Call validateNormalized, not c.Validate, so the instance normalized just
	// above is not walked by Normalize a second time.
	return c.validateNormalized(ctx, instance)
}

// resolveErrorIsRefOnly reports whether a [jsonschema.Schema.Resolve] failure
// is caused solely by $ref/$dynamicRef target lookup that this package resolves
// itself.
//
// Upstream Resolve performs reference resolution as part of pre-validation and
// rejects refs it cannot follow. One example is a JSON Pointer that targets an
// unknown keyword or the internals of a non-applicator keyword such as
// examples. This package resolves $ref/$dynamicRef targets itself (see
// [validator.resolveRef]), so such a failure must not be fatal when the schema
// is otherwise well-formed.
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
func (v *validator) resolveErrorIsRefOnly(schema *Schema, resolveOpts ResolveOptions) bool {
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
func (v *validator) structureResolves(schema *Schema, resolveOpts ResolveOptions) bool {
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
// dereferencing refs. [structureResolves] skips that validation for targets
// carried in unknown keywords or non-applicator keyword internals, since those
// have no typed Schema field. A reference this package cannot follow leaves
// refResolveErr set as a side effect; it is cleared so it does not leak into a
// later error.
func (v *validator) refsResolveWellFormed(schema *Schema, resolveOpts ResolveOptions) bool {
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
// the root document. Two kinds of target are therefore rejected: a malformed
// one and a target whose own reference cannot be followed. A malformed target
// is, for example, a schema with an uncompilable pattern that upstream rejects
// but typed-only traversal never reaches. The own-reference check is one level
// deep: targets reached through the typed tree are already validated by
// [structureResolves] on the root schema.
func (v *validator) refTargetWellFormed(target *Schema, resolveOpts ResolveOptions) bool {
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

// eachSubschema calls fn for schema and every sub-schema reachable through its
// sub-schema-bearing keywords (see [SubschemaEntries]). The caller must ensure
// the schema's sub-schema pointers form a tree (see [schemaFormsTree]); an
// aliased or cyclic structure would recurse without bound. [Walk] is the
// exported, cycle-safe form.
func eachSubschema(schema *Schema, fn func(*Schema)) {
	if schema == nil {
		return
	}

	fn(schema)

	for _, entry := range SubschemaEntries(schema) {
		eachSubschema(entry.Schema, fn)
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
		for _, entry := range SubschemaEntries(s) {
			visit(entry.Schema)
		}
	}

	visit(schema)

	return tree
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

// childAnnotations returns a fresh annotations set when the parent tracks
// annotations, or nil when it does not. A composition, conditional, reference,
// or dependency keyword collects a subschema's evaluation only to merge it into
// the parent; when the parent is nil that result is discarded, so skipping the
// allocation avoids two maps per subschema on schemas with no
// unevaluatedProperties/Items to satisfy. Passing nil to [validator.validate]
// is safe: it self-allocates a local set if the subschema itself needs one.
func childAnnotations(parent *annotations) *annotations {
	if parent == nil {
		return nil
	}

	return newAnnotations()
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
	instancePath instanceLocation,
	schemaPath schemaLocation,
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
		// Keyword is left empty here: this point cannot know which applicator
		// (if any) handed it the false schema. The applicator call sites stamp
		// it via labelFalseSchemaKeyword.
		return []*ValidationError{{
			InstancePath: instancePath.ptr,
			segments:     instancePath.segs,
			SchemaPath:   schemaPath.ptr,
			schemaSegs:   schemaPath.segs,
			Message:      "value is not allowed",
		}}
	}

	// Circular ref detection: same schema + same instance path = true cycle.
	key := visitKey{schema, instancePath.ptr}
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
		base := v.schemaBase(schema)
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

	// Unevaluated keywords: must run after all other applicator keywords
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
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	if v.draft != Draft2020 || ann == nil || !v.vocabs.Unevaluated {
		return nil
	}

	var errs []*ValidationError

	// UnevaluatedProperties.
	//nolint:nestif // Nesting tracks the annotation guards required to apply unevaluatedProperties correctly.
	if schema.UnevaluatedProperties != nil {
		if obj, ok := instance.(map[string]any); ok && !ann.allProperties {
			// IsEmptySchema implies Not == nil, so the schema is not a false
			// schema: an empty (always-true) unevaluatedProperties evaluates
			// every remaining property.
			if schemashape.IsEmpty(schema.UnevaluatedProperties) {
				ann.allProperties = true
			}

			childSchemaPath := schemaPath.kw("unevaluatedProperties")

			// Iterate in sorted key order so the emitted cause errors are
			// deterministic, matching the sibling object keywords (properties,
			// patternProperties, additionalProperties, propertyNames).
			for _, propName := range slices.Sorted(maps.Keys(obj)) {
				if ann.properties[propName] {
					continue
				}

				val := obj[propName]

				childPath := instancePath.key(propName)
				childErrs := v.validate(schema.UnevaluatedProperties, val, childPath, childSchemaPath, nil)
				if len(childErrs) == 0 {
					ann.properties[propName] = true
				} else {
					errs = append(errs, &ValidationError{
						InstancePath: childPath.ptr,
						segments:     childPath.segs,
						SchemaPath:   childSchemaPath.ptr,
						schemaSegs:   childSchemaPath.segs,
						Keyword:      KeywordUnevaluatedProperties,
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
			// IsEmptySchema implies Not == nil, so the schema is not a false
			// schema: an empty (always-true) unevaluatedItems evaluates every
			// remaining item.
			if schemashape.IsEmpty(schema.UnevaluatedItems) {
				ann.allItems = true
			}

			childSchemaPath := schemaPath.kw("unevaluatedItems")

			for i, item := range arr {
				if i < ann.itemsEnd {
					continue
				}

				if ann.itemIndexes[i] {
					continue
				}

				childPath := instancePath.index(i)
				childErrs := v.validate(schema.UnevaluatedItems, item, childPath, childSchemaPath, nil)
				if len(childErrs) == 0 {
					ann.itemIndexes[i] = true
				} else {
					errs = append(errs, &ValidationError{
						InstancePath: childPath.ptr,
						segments:     childPath.segs,
						SchemaPath:   childSchemaPath.ptr,
						schemaSegs:   childSchemaPath.segs,
						Keyword:      KeywordUnevaluatedItems,
						Message:      fmt.Sprintf("item %d is not allowed by unevaluatedItems", i),
						Causes:       childErrs,
					})
				}
			}
		}
	}

	return errs
}

// labelFalseSchemaKeyword stamps keyword on the leaf error a false subschema
// emitted, so a consumer can tell an additionalProperties:false violation (or
// a false property/item subschema) apart from other failures without parsing
// SchemaPath. The false-schema short-circuit in [validator.validate] cannot
// know which applicator handed it the schema, so the applicator call sites
// label the result; a root or standalone boolean false schema has no
// applicator context and its leaf keeps an empty Keyword.
func labelFalseSchemaKeyword(errs []*ValidationError, sub *Schema, keyword string) {
	if !isFalseSchema(sub) {
		return
	}

	for _, e := range errs {
		if e.Keyword == "" {
			e.Keyword = keyword
		}
	}
}

// isFalseSchema reports whether a schema is equivalent to boolean false (rejects
// all). It delegates to the exported [IsFalseSchema] so the single field
// enumeration in [IsTrueSchema] governs both halves of the package: the boolean
// false form is {"not": {}} with no other keyword, and any sibling at all — an
// unknown keyword (Extra) or an annotation such as a title or $id — defeats the
// form, since the schema then marshals to an object rather than to bare false.
// Such a schema is validated through its `not` keyword (which still rejects every
// instance), and the error names that keyword instead of the bare false-schema
// message. A nil schema is not the false form. ([schemashape.IsEmpty], which
// ignores annotations, intentionally answers a different question for the
// always-true unevaluated* subschema checks and is not used here.)
func isFalseSchema(s *Schema) bool {
	return IsFalseSchema(s)
}

// validateType checks the type keyword.
func (v *validator) validateType(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	if !v.vocabs.Validation {
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
		if normalize.MatchesType(instance, t) {
			return nil
		}
	}

	got := normalize.TypeName(instance)

	return []*ValidationError{
		leafError(instancePath, schemaPath, KeywordType,
			fmt.Sprintf("expected %s, got %q", formatTypes(types), got)),
	}
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
func (v *validator) validateEnum(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	if !v.vocabs.Validation {
		return nil
	}

	// A nil Enum means the keyword is absent (skip). An empty but non-nil Enum
	// ("enum": []) permits no values, so every instance fails it.
	if schema.Enum == nil {
		return nil
	}

	rats := v.enumRats[schema]

	for i, allowed := range schema.Enum {
		var allowedRat *big.Rat

		if rats != nil {
			allowedRat = rats[i]
		}

		if jsonequal.EqualWithRat(allowed, allowedRat, instance) {
			return nil
		}
	}

	return []*ValidationError{
		leafError(instancePath, schemaPath, KeywordEnum, "value does not match any enum member"),
	}
}

// validateConst checks the const keyword.
func (v *validator) validateConst(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	if !v.vocabs.Validation {
		return nil
	}

	if schema.Const == nil {
		return nil
	}

	constVal := *schema.Const
	if jsonequal.EqualWithRat(constVal, v.constRats[schema], instance) {
		return nil
	}

	return []*ValidationError{
		leafError(instancePath, schemaPath, KeywordConst, "value does not match const"),
	}
}

// boundsFor returns the numeric bound rationals for schema, preferring the
// Compile-time cache and converting on the fly for a schema absent from it
// (a remote or JSON-pointer fallback schema reached only at validation time).
// The returned rationals are operands only; callers must not mutate them.
func (v *validator) boundsFor(schema *Schema) *precomputedBounds {
	if b, ok := v.numericBounds[schema]; ok {
		return b
	}

	return computeBounds(schema)
}

// patternFor returns the compiled form of schema.Pattern, preferring the
// Compile-time cache and compiling on the fly for a schema absent from it
// (a remote or JSON-pointer fallback schema reached only at validation time).
// The compile error, when present, is reported by the caller exactly as a fresh
// [regexcache.Compile] call would, preserving the fail-closed behavior.
func (v *validator) patternFor(schema *Schema) compiledPattern {
	if cp, ok := v.patternCache[schema]; ok {
		return cp
	}

	re, err := regexcache.Compile(schema.Pattern)

	return compiledPattern{re: re, err: err}
}

// patternPropertyFor returns the compiled form of one patternProperties key on
// schema, preferring the Compile-time cache and compiling on the fly for a
// schema absent from it.
func (v *validator) patternPropertyFor(schema *Schema, pattern string) compiledPattern {
	if byPattern, ok := v.patternProps[schema]; ok {
		if cp, ok := byPattern[pattern]; ok {
			return cp
		}
	}

	re, err := regexcache.Compile(pattern)

	return compiledPattern{re: re, err: err}
}

// validateNumeric checks numeric keywords.
func (v *validator) validateNumeric(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	if !v.vocabs.Validation {
		return nil
	}

	if !numrat.IsNumeric(instance) {
		return nil
	}

	// Decompose a JSON number exactly once. An over-cap literal (the DoS guard)
	// takes the magnitude-class comparison without a second scan of the literal;
	// an unparseable one has no value to compare and skips the numeric keywords;
	// an exactly-comparable one yields the rational the bounded checks use. A
	// float64 (the default) converts through its shortest decimal, and a
	// non-finite float yields no rational and is likewise skipped.
	var val *big.Rat

	switch n := instance.(type) {
	case json.Number:
		d, ok := numrat.ParseDecNumber(string(n))
		if !ok {
			return nil
		}

		if !d.ExactlyComparable() {
			return v.validateNumericUnbounded(schema, d, string(n), instancePath, schemaPath)
		}

		val = d.Rat()

	default:
		var ok bool

		val, ok = numrat.ToBigRat(instance)
		if !ok {
			return nil
		}
	}

	var errs []*ValidationError

	// One error per failed bound, sharing the instance path and keyword
	// schema-path location.
	add := func(keyword, msg string) {
		errs = append(errs, leafError(instancePath, schemaPath, keyword, msg))
	}

	bounds := v.boundsFor(schema)

	if schema.MultipleOf != nil {
		switch {
		case *schema.MultipleOf <= 0:
			// MultipleOf MUST be strictly greater than 0; a non-positive
			// divisor makes the schema invalid.
			add(KeywordMultipleOf, fmt.Sprintf("multipleOf must be greater than 0, got %v", *schema.MultipleOf))

		default:
			// A NaN/Inf divisor has no rational form (numrat.Float64ToRat returns
			// nil); the constraint cannot apply, so skip it rather than
			// dividing by a nil *big.Rat. Quo writes its own receiver, so the
			// cached divisor stays an operand and is never mutated.
			divisor := bounds.multipleOf
			if divisor != nil {
				quotient := new(big.Rat).Quo(val, divisor)
				if !quotient.IsInt() {
					add(
						KeywordMultipleOf,
						fmt.Sprintf("%s is not a multiple of %v", numrat.RatString(val), *schema.MultipleOf),
					)
				}
			}
		}
	}

	// A nil bound denotes a NaN/Inf value with no rational form; such a bound
	// cannot constrain a finite instance, so the comparison is skipped.
	if schema.Minimum != nil {
		if bound := bounds.minimum; bound != nil && val.Cmp(bound) < 0 {
			add(KeywordMinimum, fmt.Sprintf("%s is less than %v", numrat.RatString(val), *schema.Minimum))
		}
	}

	if schema.Maximum != nil {
		if bound := bounds.maximum; bound != nil && val.Cmp(bound) > 0 {
			add(KeywordMaximum, fmt.Sprintf("%s is greater than %v", numrat.RatString(val), *schema.Maximum))
		}
	}

	if schema.ExclusiveMinimum != nil {
		if bound := bounds.exclusiveMinimum; bound != nil && val.Cmp(bound) <= 0 {
			add(
				KeywordExclusiveMinimum,
				fmt.Sprintf("%s is less than or equal to %v", numrat.RatString(val), *schema.ExclusiveMinimum),
			)
		}
	}

	if schema.ExclusiveMaximum != nil {
		if bound := bounds.exclusiveMaximum; bound != nil && val.Cmp(bound) >= 0 {
			add(
				KeywordExclusiveMaximum,
				fmt.Sprintf("%s is greater than or equal to %v", numrat.RatString(val), *schema.ExclusiveMaximum),
			)
		}
	}

	return errs
}

// validateNumericUnbounded checks the numeric bound keywords for a
// [json.Number] whose exact expansion is too expensive (see numrat.MaxNumberLen): a
// huge magnitude (exponent above the cap), a tiny magnitude (exponent below
// the negative cap), or a significand longer than the cap. Every such value
// still orders deterministically against any float64 bound via
// [numrat.DecNumber.CmpRat], and equality with a bound is impossible, so the
// inclusive and exclusive variants of each bound coincide. An over-cap integer
// still has its multipleOf divisibility enforced through modular arithmetic
// (see [numrat.IntegerMultipleOf]); only an over-cap non-integer skips it, since
// expanding its fractional part is unbounded. The schema-validity check (a
// non-positive divisor) fires regardless. A zero value is always
// ExactlyComparable, so it never reaches this path.
func (v *validator) validateNumericUnbounded(
	schema *Schema,
	d numrat.DecNumber,
	literal string,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	num := numrat.TruncateNumber(literal)

	var errs []*ValidationError

	add := func(keyword, msg string) {
		errs = append(errs, leafError(instancePath, schemaPath, keyword, msg))
	}

	bounds := v.boundsFor(schema)

	// A non-positive multipleOf makes the schema invalid independent of the
	// instance value. For a positive divisor, an over-cap integer's
	// divisibility is still decidable at bounded cost via modular arithmetic
	// (see numrat.IntegerMultipleOf), so it is enforced. A non-integral over-cap value
	// keeps the documented skip: expanding its fractional part is unbounded.
	if schema.MultipleOf != nil {
		switch {
		case *schema.MultipleOf <= 0:
			add(KeywordMultipleOf, fmt.Sprintf("multipleOf must be greater than 0, got %v", *schema.MultipleOf))
		case bounds.multipleOf != nil && d.IsIntegral() &&
			!numrat.IntegerMultipleOf(d, literal, bounds.multipleOf):
			add(KeywordMultipleOf, fmt.Sprintf("%s is not a multiple of %v", num, *schema.MultipleOf))
		}
	}

	// A nil bound denotes a NaN/Inf value with no rational form; such a bound
	// cannot constrain a finite instance, so the comparison is skipped. The
	// comparison reads the bound and never mutates it, so the cached rational
	// stays shared.
	if schema.Minimum != nil {
		if b := bounds.minimum; b != nil && d.CmpRat(b) < 0 {
			add(KeywordMinimum, fmt.Sprintf("%s is less than %v", num, *schema.Minimum))
		}
	}

	if schema.Maximum != nil {
		if b := bounds.maximum; b != nil && d.CmpRat(b) > 0 {
			add(KeywordMaximum, fmt.Sprintf("%s is greater than %v", num, *schema.Maximum))
		}
	}

	if schema.ExclusiveMinimum != nil {
		// On the unbounded path CmpRat never reports equality (an over-cap value
		// cannot equal the finite float64 bound), so the violation is always a
		// strict inequality; the message omits the "or equal to" the bounded path
		// uses, where equality is reachable.
		if b := bounds.exclusiveMinimum; b != nil && d.CmpRat(b) < 0 {
			add(KeywordExclusiveMinimum, fmt.Sprintf("%s is less than %v", num, *schema.ExclusiveMinimum))
		}
	}

	if schema.ExclusiveMaximum != nil {
		if b := bounds.exclusiveMaximum; b != nil && d.CmpRat(b) > 0 {
			add(KeywordExclusiveMaximum, fmt.Sprintf("%s is greater than %v", num, *schema.ExclusiveMaximum))
		}
	}

	return errs
}

// validateString checks string keywords.
func (v *validator) validateString(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	str, ok := instance.(string)
	if !ok {
		// Json.Number is a distinct type, so it fails this assertion and string
		// keywords correctly do not apply to numbers.
		return nil
	}

	var errs []*ValidationError

	//nolint:nestif // One branch per string validation keyword.
	if v.vocabs.Validation {
		// RuneCountInString avoids allocating a []rune; only count when a
		// length keyword is present.
		if schema.MinLength != nil || schema.MaxLength != nil {
			runeLen := utf8.RuneCountInString(str)

			if schema.MinLength != nil && runeLen < *schema.MinLength {
				kwPath := schemaPath.kw("minLength")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordMinLength,
					Message:      fmt.Sprintf("string length %d is less than %d", runeLen, *schema.MinLength),
				})
			}

			if schema.MaxLength != nil && runeLen > *schema.MaxLength {
				kwPath := schemaPath.kw("maxLength")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordMaxLength,
					Message:      fmt.Sprintf("string length %d is greater than %d", runeLen, *schema.MaxLength),
				})
			}
		}

		if schema.Pattern != "" {
			cp := v.patternFor(schema)
			switch {
			case cp.err != nil:
				// A pattern Go's RE2 cannot compile (e.g. an ECMA-262
				// backreference or lookaround) fails closed: the constraint
				// cannot be evaluated, so no string is accepted under it rather
				// than silently treating every string as a match.
				kwPath := schemaPath.kw("pattern")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordPattern,
					Message:      fmt.Sprintf("pattern %q cannot be compiled", schema.Pattern),
				})

			case !cp.re.MatchString(str):
				kwPath := schemaPath.kw("pattern")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordPattern,
					Message:      fmt.Sprintf("string does not match pattern %q", schema.Pattern),
				})
			}
		}
	}

	if schema.Format != "" && v.formatsEnabled {
		if fv, exists := v.formatCheckers[schema.Format]; exists {
			err := fv.ValidateFormat(v.runContext(), schema.Format, str)
			if err != nil {
				kwPath := schemaPath.kw("format")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordFormat,
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
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	arr, ok := instance.([]any)
	if !ok {
		return nil
	}

	var errs []*ValidationError

	// Applicator vocab: prefixItems, items, additionalItems, contains.
	if v.vocabs.Applicator { //nolint:nestif // Vocabulary-gated applicator keywords require nesting.
		// PrefixItems (2020-12) or items as array (draft-07).
		var (
			prefixSchemas []*Schema
			prefixKeyword string
		)

		if v.draft == Draft2020 && len(schema.PrefixItems) > 0 {
			prefixSchemas = schema.PrefixItems
			prefixKeyword = KeywordPrefixItems
		} else if v.draft == Draft7 && len(schema.ItemsArray) > 0 {
			prefixSchemas = schema.ItemsArray
			prefixKeyword = KeywordItems
		}

		for i, ps := range prefixSchemas {
			if i >= len(arr) {
				break
			}

			childPath := instancePath.index(i)
			childSchemaPath := schemaPath.kw(prefixKeyword).idx(i)
			childErrs := v.validate(ps, arr[i], childPath, childSchemaPath, nil)
			labelFalseSchemaKeyword(childErrs, ps, prefixKeyword)

			errs = append(errs, childErrs...)
		}

		// PrefixItems annotates every index it applied a subschema to, regardless
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
			childSchemaPath := schemaPath.kw("items")

			for i, item := range arr {
				childPath := instancePath.index(i)
				childErrs := v.validate(schema.Items, item, childPath, childSchemaPath, nil)
				labelFalseSchemaKeyword(childErrs, schema.Items, KeywordItems)

				errs = append(errs, childErrs...)
			}

			if ann != nil {
				ann.allItems = true
			}
		} else if schema.Items != nil && len(prefixSchemas) > 0 {
			// In 2020-12: items after prefixItems applies to remaining elements.
			// In draft-07: additionalItems applies to remaining elements.
			if v.draft == Draft2020 {
				childSchemaPath := schemaPath.kw("items")

				for i := len(prefixSchemas); i < len(arr); i++ {
					childPath := instancePath.index(i)
					childErrs := v.validate(schema.Items, arr[i], childPath, childSchemaPath, nil)
					labelFalseSchemaKeyword(childErrs, schema.Items, KeywordItems)

					errs = append(errs, childErrs...)
				}

				// Mark all items evaluated only when items actually applied to a
				// trailing element; for an array no longer than prefixItems the
				// prefix already covers every index via itemsEnd.
				if ann != nil && len(arr) > len(prefixSchemas) {
					ann.allItems = true
				}
			}
		}

		// AdditionalItems (draft-07 only).
		if v.draft == Draft7 && schema.AdditionalItems != nil && len(schema.ItemsArray) > 0 {
			childSchemaPath := schemaPath.kw("additionalItems")

			for i := len(schema.ItemsArray); i < len(arr); i++ {
				childPath := instancePath.index(i)
				childErrs := v.validate(schema.AdditionalItems, arr[i], childPath, childSchemaPath, nil)
				labelFalseSchemaKeyword(childErrs, schema.AdditionalItems, KeywordAdditionalItems)

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
					instancePath.index(i),
					schemaPath.kw("contains"),
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
			if v.draft == Draft2020 && v.vocabs.Validation && schema.MinContains != nil {
				minContains = *schema.MinContains
			}

			maxContains := -1
			if v.draft == Draft2020 && v.vocabs.Validation && schema.MaxContains != nil {
				maxContains = *schema.MaxContains
			}

			// The contains annotation is the set of indexes the subschema matched,
			// produced per element independently of min/maxContains (JSON Schema
			// 2020-12 core 10.3.1.3). Record it unconditionally so a matched item
			// stays evaluated for unevaluatedItems even when the count violates
			// min/maxContains; those are separate assertions, emitted below.
			if ann != nil {
				for _, i := range matchedIdx {
					ann.itemIndexes[i] = true
				}
			}

			if matchCount < minContains {
				// An explicit minContains owns the violation; without it the
				// shortfall is a plain contains failure (default minContains=1).
				keyword := KeywordContains
				if v.draft == Draft2020 && v.vocabs.Validation && schema.MinContains != nil {
					keyword = KeywordMinContains
				}

				kwPath := schemaPath.kw(keyword)

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      keyword,
					Message:      fmt.Sprintf("array has %d matching items, minimum is %d", matchCount, minContains),
				})
			}

			if maxContains >= 0 && matchCount > maxContains {
				kwPath := schemaPath.kw("maxContains")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordMaxContains,
					Message:      fmt.Sprintf("array has %d matching items, maximum is %d", matchCount, maxContains),
				})
			}
		}
	}

	// Validation vocab: minItems, maxItems, uniqueItems.
	//nolint:nestif // One branch per array validation keyword.
	if v.vocabs.Validation {
		// MinItems.
		if schema.MinItems != nil && len(arr) < *schema.MinItems {
			kwPath := schemaPath.kw("minItems")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordMinItems,
				Message:      fmt.Sprintf("array has %d items, minimum is %d", len(arr), *schema.MinItems),
			})
		}

		// MaxItems.
		if schema.MaxItems != nil && len(arr) > *schema.MaxItems {
			kwPath := schemaPath.kw("maxItems")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordMaxItems,
				Message:      fmt.Sprintf("array has %d items, maximum is %d", len(arr), *schema.MaxItems),
			})
		}

		// UniqueItems.
		if schema.UniqueItems {
			if jsonequal.HasDuplicates(arr) {
				kwPath := schemaPath.kw("uniqueItems")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordUniqueItems,
					Message:      "array contains duplicate items",
				})
			}
		}
	}

	return errs
}

// validateObject checks object keywords.
func (v *validator) validateObject(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	obj, ok := instance.(map[string]any)
	if !ok {
		return nil
	}

	var errs []*ValidationError

	// Track locally evaluated properties for additionalProperties. Only that
	// keyword reads the map, so allocate it lazily and leave it nil otherwise.
	var localEvaluated map[string]bool

	if schema.AdditionalProperties != nil {
		localEvaluated = map[string]bool{}
	}

	// Applicator vocab: properties, patternProperties, additionalProperties,
	// propertyNames, dependentSchemas.
	//nolint:nestif // One branch per object applicator keyword; flattening would not reduce the inherent fan-out.
	if v.vocabs.Applicator {
		// Properties. Iterate in sorted-key order so the emitted error order is
		// deterministic; Go map iteration is randomized.
		for _, propName := range slices.Sorted(maps.Keys(schema.Properties)) {
			propSchema := schema.Properties[propName]
			val, exists := obj[propName]
			if !exists {
				continue
			}

			if localEvaluated != nil {
				localEvaluated[propName] = true
			}

			if ann != nil {
				ann.properties[propName] = true
			}

			childPath := instancePath.key(propName)
			childSchemaPath := schemaPath.kw("properties").key(propName)
			childErrs := v.validate(propSchema, val, childPath, childSchemaPath, nil)
			labelFalseSchemaKeyword(childErrs, propSchema, KeywordProperties)

			errs = append(errs, childErrs...)
		}

		// PatternProperties, additionalProperties, and propertyNames all iterate
		// the instance keys in sorted order; compute that ordering once and share
		// it rather than re-sorting per pattern and per keyword. The applicator
		// walk never mutates obj.
		var sortedObjKeys []string

		if len(schema.PatternProperties) > 0 || schema.AdditionalProperties != nil || schema.PropertyNames != nil {
			sortedObjKeys = slices.Sorted(maps.Keys(obj))
		}

		// PatternProperties. Sorted iteration keeps the error order deterministic.
		for _, pattern := range slices.Sorted(maps.Keys(schema.PatternProperties)) {
			patternSchema := schema.PatternProperties[pattern]

			// One schema-path location per pattern, shared by the error branch
			// and every matching property rather than rebuilt for each.
			patternSchemaPath := schemaPath.kw("patternProperties").key(pattern)

			cp := v.patternPropertyFor(schema, pattern)
			if cp.err != nil {
				// A pattern Go's RE2 cannot compile fails closed: the keyword
				// cannot decide which properties it governs, so the object is
				// rejected rather than silently dropping the subschema.
				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   patternSchemaPath.ptr,
					schemaSegs:   patternSchemaPath.segs,
					Keyword:      KeywordPatternProperties,
					Message:      fmt.Sprintf("pattern %q cannot be compiled", pattern),
				})

				continue
			}

			for _, propName := range sortedObjKeys {
				val := obj[propName]
				if !cp.re.MatchString(propName) {
					continue
				}

				if localEvaluated != nil {
					localEvaluated[propName] = true
				}

				if ann != nil {
					ann.properties[propName] = true
				}

				childPath := instancePath.key(propName)
				childErrs := v.validate(patternSchema, val, childPath, patternSchemaPath, nil)
				labelFalseSchemaKeyword(childErrs, patternSchema, KeywordPatternProperties)

				errs = append(errs, childErrs...)
			}
		}

		// AdditionalProperties: only considers sibling properties and patternProperties.
		if schema.AdditionalProperties != nil {
			childSchemaPath := schemaPath.kw("additionalProperties")

			for _, propName := range sortedObjKeys {
				val := obj[propName]
				if localEvaluated[propName] {
					continue
				}

				if ann != nil {
					ann.properties[propName] = true
				}

				childPath := instancePath.key(propName)
				childErrs := v.validate(schema.AdditionalProperties, val, childPath, childSchemaPath, nil)
				labelFalseSchemaKeyword(childErrs, schema.AdditionalProperties, KeywordAdditionalProperties)

				errs = append(errs, childErrs...)
			}

			if ann != nil {
				ann.allProperties = true
			}
		}

		// PropertyNames. The constraint is on the key, not its value, and RFC
		// 6901 gives a key no JSON Pointer of its own, so a violation borrows
		// the property's location: the wrapping error (and its causes) carry
		// the property's instance path, with Keyword "propertyNames" and the
		// offending name in the message identifying which key failed and which
		// object it belongs to.
		if schema.PropertyNames != nil {
			for _, propName := range sortedObjKeys {
				childPath := instancePath.key(propName)
				childSchemaPath := schemaPath.kw("propertyNames")
				childErrs := v.validate(
					schema.PropertyNames,
					propName,
					childPath,
					childSchemaPath,
					nil,
				)
				if len(childErrs) > 0 {
					errs = append(errs, &ValidationError{
						InstancePath: childPath.ptr,
						segments:     childPath.segs,
						SchemaPath:   childSchemaPath.ptr,
						schemaSegs:   childSchemaPath.segs,
						Keyword:      KeywordPropertyNames,
						Message:      fmt.Sprintf("property name %q is invalid", propName),
						Causes:       childErrs,
					})
				}
			}
		}

		// DependentSchemas (2020-12).
		if v.draft == Draft2020 {
			for _, prop := range slices.Sorted(maps.Keys(schema.DependentSchemas)) {
				depSchema := schema.DependentSchemas[prop]
				if _, exists := obj[prop]; !exists {
					continue
				}

				depAnn := childAnnotations(ann)
				childSchemaPath := schemaPath.kw("dependentSchemas").key(prop)
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
	if v.vocabs.Validation {
		// Required.
		for _, reqProp := range schema.Required {
			if _, exists := obj[reqProp]; !exists {
				kwPath := schemaPath.kw("required")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordRequired,
					Message:      fmt.Sprintf("missing required property %q", reqProp),
				})
			}
		}

		// MinProperties.
		if schema.MinProperties != nil && len(obj) < *schema.MinProperties {
			kwPath := schemaPath.kw("minProperties")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordMinProperties,
				Message:      fmt.Sprintf("object has %d properties, minimum is %d", len(obj), *schema.MinProperties),
			})
		}

		// MaxProperties.
		if schema.MaxProperties != nil && len(obj) > *schema.MaxProperties {
			kwPath := schemaPath.kw("maxProperties")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordMaxProperties,
				Message:      fmt.Sprintf("object has %d properties, maximum is %d", len(obj), *schema.MaxProperties),
			})
		}

		// DependentRequired (2020-12).
		if v.draft == Draft2020 {
			for _, prop := range slices.Sorted(maps.Keys(schema.DependentRequired)) {
				deps := schema.DependentRequired[prop]
				if _, exists := obj[prop]; !exists {
					continue
				}

				for _, dep := range deps {
					if _, exists := obj[dep]; !exists {
						kwPath := schemaPath.kw("dependentRequired").key(prop)

						errs = append(errs, &ValidationError{
							InstancePath: instancePath.ptr,
							segments:     instancePath.segs,
							SchemaPath:   kwPath.ptr,
							schemaSegs:   kwPath.segs,
							Keyword:      KeywordDependentRequired,
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
	for _, prop := range slices.Sorted(maps.Keys(schema.DependencySchemas)) {
		depSchema := schema.DependencySchemas[prop]
		if _, exists := obj[prop]; !exists {
			continue
		}

		depAnn := childAnnotations(ann)
		childSchemaPath := schemaPath.kw("dependencies").key(prop)
		childErrs := v.validate(depSchema, instance, instancePath, childSchemaPath, depAnn)
		errs = append(errs, childErrs...)
		if len(childErrs) == 0 && ann != nil {
			ann.merge(depAnn)
		}
	}

	for _, prop := range slices.Sorted(maps.Keys(schema.DependencyStrings)) {
		deps := schema.DependencyStrings[prop]
		if _, exists := obj[prop]; !exists {
			continue
		}

		for _, dep := range deps {
			if _, exists := obj[dep]; !exists {
				kwPath := schemaPath.kw("dependencies").key(prop)

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordDependencies,
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
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	if !v.vocabs.Applicator {
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
			subAnn := childAnnotations(ann)
			childSchemaPath := schemaPath.kw("allOf").idx(i)
			childErrs := v.validate(sub, instance, instancePath, childSchemaPath, subAnn)
			if len(childErrs) > 0 {
				allCauses = append(allCauses, childErrs...)
			} else {
				subAnns = append(subAnns, subAnn)
			}
		}

		if len(allCauses) > 0 {
			kwPath := schemaPath.kw("allOf")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordAllOf,
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
			subAnn := childAnnotations(ann)
			childSchemaPath := schemaPath.kw("anyOf").idx(i)
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
			kwPath := schemaPath.kw("anyOf")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordAnyOf,
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
			subAnn := childAnnotations(ann)
			childSchemaPath := schemaPath.kw("oneOf").idx(i)
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
			kwPath := schemaPath.kw("oneOf")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordOneOf,
				Message:      "did not validate against any subschema",
				Causes:       allCauses,
			})

		case matchCount > 1:
			kwPath := schemaPath.kw("oneOf")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordOneOf,
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
		childErrs := v.validate(schema.Not, instance, instancePath, schemaPath.kw("not"), nil)
		if len(childErrs) == 0 {
			kwPath := schemaPath.kw("not")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordNot,
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
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	if !v.vocabs.Applicator || schema.If == nil {
		return nil
	}

	var errs []*ValidationError

	ifAnn := childAnnotations(ann)
	ifErrs := v.validate(schema.If, instance, instancePath, schemaPath.kw("if"), ifAnn)
	ifPassed := len(ifErrs) == 0

	if ifPassed { //nolint:nestif // Conditional branching with annotation tracking requires nesting.
		if ann != nil {
			ann.merge(ifAnn)
		}

		if schema.Then != nil {
			thenAnn := childAnnotations(ann)
			thenErrs := v.validate(schema.Then, instance, instancePath, schemaPath.kw("then"), thenAnn)
			if len(thenErrs) > 0 {
				kwPath := schemaPath.kw("then")

				errs = append(errs, &ValidationError{
					InstancePath: instancePath.ptr,
					segments:     instancePath.segs,
					SchemaPath:   kwPath.ptr,
					schemaSegs:   kwPath.segs,
					Keyword:      KeywordThen,
					Message:      "if condition was true but then validation failed",
					Causes:       thenErrs,
				})
			} else if ann != nil {
				ann.merge(thenAnn)
			}
		}
	} else if schema.Else != nil {
		elseAnn := childAnnotations(ann)
		elseErrs := v.validate(schema.Else, instance, instancePath, schemaPath.kw("else"), elseAnn)
		if len(elseErrs) > 0 {
			kwPath := schemaPath.kw("else")

			errs = append(errs, &ValidationError{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordElse,
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
// Per 2020-12 spec section 8.5, content keywords (contentEncoding,
// contentMediaType, contentSchema) are annotations only and never affect
// validity. ContentSchema describes the decoded content, which this package
// does not decode, so it is never asserted regardless of the other keywords.
//
// [WithContent] opts in to asserting contentEncoding and contentMediaType for
// string instances only; non-string instances carry no content and pass.
func (v *validator) validateContent(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	// Gated on the content vocabulary, consistent with the other keyword
	// groups. When it is inactive the content keywords are inert.
	if !v.vocabs.Content {
		return nil
	}

	if v.contentEnabled {
		if errs := v.assertContent(schema, instance, instancePath, schemaPath); errs != nil {
			return errs
		}
	}

	return nil
}

// assertContent asserts contentEncoding and contentMediaType for a string
// instance. Content lives only in strings, so non-string instances carry no
// content and pass. Only base64 encoding and the application/json media type are
// asserted; unrecognized encodings and media types remain annotations.
func (v *validator) assertContent(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	str, ok := instance.(string)
	if !ok {
		return nil
	}

	decoded := []byte(str)
	decodedKnown := true

	switch schema.ContentEncoding {
	case "":
		// No encoding: the instance string is the content itself.
	case contentEncodingBase64:
		b, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			kwPath := schemaPath.kw("contentEncoding")

			return []*ValidationError{{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      KeywordContentEncoding,
				Message:      fmt.Sprintf("string is not valid base64: %v", err),
			}}
		}

		decoded = b

	default:
		// An unrecognized encoding cannot be decoded, so the media type
		// cannot be asserted against the decoded form; both keywords remain
		// annotations rather than running the assertion on still-encoded text.
		decodedKnown = false
	}

	if decodedKnown && mediaTypeIsJSON(schema.ContentMediaType) && !json.Valid(decoded) {
		kwPath := schemaPath.kw("contentMediaType")

		return []*ValidationError{{
			InstancePath: instancePath.ptr,
			segments:     instancePath.segs,
			SchemaPath:   kwPath.ptr,
			schemaSegs:   kwPath.segs,
			Keyword:      KeywordContentMediaType,
			Message:      "string is not a valid application/json document",
		}}
	}

	return nil
}

// mediaTypeIsJSON reports whether a contentMediaType denotes application/json.
// Per RFC 2045 the type/subtype is case-insensitive and any parameters (for
// example "; charset=utf-8") are not part of it, so "Application/JSON" and
// "application/json; charset=utf-8" both match.
func mediaTypeIsJSON(mediaType string) bool {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err == nil {
		// ParseMediaType lowercases the type/subtype and strips parameters.
		return parsed == "application/json"
	}

	// ParseMediaType rejects some malformed-but-recognizable values, so fall
	// back to stripping parameters and folding case by hand.
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}

	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}

// validateRef resolves and validates a $ref.
func (v *validator) validateRef(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	ref := schema.Ref
	if ref == "" {
		return nil
	}

	return v.validateResolvedRef(v.resolveRef(schema, ref), ref, KeywordRef, instance, instancePath, schemaPath, ann)
}

// validateDynamicRef resolves and validates a $dynamicRef.
func (v *validator) validateDynamicRef(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	ref := schema.DynamicRef
	if ref == "" {
		return nil
	}

	return v.validateResolvedRef(
		v.resolveDynamicRef(schema, ref),
		ref,
		KeywordDynamicRef,
		instance,
		instancePath,
		schemaPath,
		ann,
	)
}

// validateResolvedRef validates the instance against a resolved reference
// target, sharing the resolution-error and annotation handling between $ref
// and $dynamicRef. The keyword names the reference keyword for error paths.
func (v *validator) validateResolvedRef(
	target *Schema,
	ref, keyword string,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations,
) []*ValidationError {
	if target == nil {
		if v.refResolveErr != nil {
			err := v.refResolveErr
			v.refResolveErr = nil

			kwPath := schemaPath.kw(keyword)

			return []*ValidationError{{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      keyword,
				Message:      err.Error(),
				err:          err,
			}}
		}

		// A non-local (remote/absolute) ref that cannot be resolved is an
		// error rather than silently passing. Unresolvable local fragment refs
		// are already rejected by Schema.Resolve before the walk begins.
		if !uriref.IsFragmentOnly(ref) {
			kwPath := schemaPath.kw(keyword)

			return []*ValidationError{{
				InstancePath: instancePath.ptr,
				segments:     instancePath.segs,
				SchemaPath:   kwPath.ptr,
				schemaSegs:   kwPath.segs,
				Keyword:      keyword,
				Message:      fmt.Sprintf("cannot resolve %s %q", keyword, ref),
			}}
		}

		// Unresolvable local fragment ref: silently skip.
		return nil
	}

	refAnn := childAnnotations(ann)
	childErrs := v.validate(target, instance, instancePath, schemaPath.kw(keyword), refAnn)
	if len(childErrs) > 0 {
		kwPath := schemaPath.kw(keyword)

		return []*ValidationError{{
			InstancePath: instancePath.ptr,
			segments:     instancePath.segs,
			SchemaPath:   kwPath.ptr,
			schemaSegs:   kwPath.segs,
			Keyword:      keyword,
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

	// Phase 2: Bookending check. Dynamic resolution engages only when static
	// resolution actually landed on the schema that bears a $dynamicAnchor of the
	// fragment name, not merely when the static target's resource defines one
	// somewhere. Otherwise a plain $anchor that wins static resolution would be
	// treated dynamically just because a same-named $dynamicAnchor sits elsewhere
	// in the resource.
	staticBase := v.schemaBase(staticTarget)
	if anchored, ok := v.lookupDynamicAnchor(uriref.AnchorKey(staticBase, fragment)); !ok || anchored != staticTarget {
		return staticTarget // no bookend → behave like $ref
	}

	// Phase 3: Walk dynamic scope outermost→innermost for first matching
	// $dynamicAnchor.
	for _, scopeBase := range v.dynamicScope {
		if target, ok := v.lookupDynamicAnchor(uriref.AnchorKey(scopeBase, fragment)); ok {
			return target
		}
	}

	return staticTarget
}

// refCacheKey identifies a plain $ref resolution, used to cache its result
// within a validation run. The containing schema fixes the base URI the ref
// resolves against, so the pair is sufficient to key the lookup.
type refCacheKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	schema *Schema
	//nolint:unused // Read via struct equality when used as a map key.
	ref string
}

// resolveRef resolves a $ref string to a target schema using the URI and anchor
// registries, caching the result per (schema, ref) for the validation run.
//
// The same ref is re-resolved for every instance node it is evaluated against,
// and resolution is deterministic within a run because the registries are fixed
// at compile time (or cloned once per run when a [RefResolver] is configured).
// Only successful resolutions are cached: an unresolved remote ref records its
// failure in refResolveErr as a side effect that the caller consumes once, so
// caching a nil would suppress that error on later evaluations.
func (v *validator) resolveRef(schema *Schema, ref string) *Schema {
	key := refCacheKey{schema: schema, ref: ref}
	if cached, ok := v.refCache[key]; ok {
		return cached
	}

	target := v.resolveRefUncached(schema, ref)
	if target != nil {
		if v.refCache == nil {
			v.refCache = map[refCacheKey]*Schema{}
		}

		v.refCache[key] = target
	}

	return target
}

// resolveRefUncached performs the actual $ref resolution behind resolveRef's
// per-run cache.
func (v *validator) resolveRefUncached(schema *Schema, ref string) *Schema {
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil
	}

	// Fragment-only refs (e.g. "#", "#/$defs/foo", "#anchor").
	//nolint:nestif // Resolution walks distinct fragment forms (pointer, anchor, root).
	if uriref.IsFragmentOnly(ref) {
		fragment := parsed.Fragment

		// Find the root of the current resource.
		resourceRoot := v.root
		base := v.schemaBase(schema)
		if base != "" {
			if target, ok := v.lookupURI(base); ok {
				resourceRoot = target
			}
		}

		if fragment == "" {
			return resourceRoot
		}

		// JSON Pointer. Pass the still-encoded fragment so a member name
		// escaped as %2F is not mistaken for a pointer separator.
		if strings.HasPrefix(fragment, "/") {
			raw, encoded := uriref.RawFragment(parsed)

			return v.resolveJSONPointer(resourceRoot, raw, encoded)
		}

		// Anchor reference.
		if target, ok := v.lookupAnchor(uriref.AnchorKey(base, fragment)); ok {
			return target
		}

		return nil
	}

	// Non-fragment ref: resolve against current schema's base URI.
	base := v.schemaBase(schema)
	absRef := uriref.ResolveURI(base, ref)

	parsedAbs, err := url.Parse(absRef)
	if err != nil {
		return nil
	}

	fragment := parsedAbs.Fragment
	rawFrag, fragEncoded := uriref.RawFragment(parsedAbs)
	parsedAbs.Fragment = ""
	parsedAbs.RawFragment = ""
	baseURI := parsedAbs.String()

	target, ok := v.lookupURI(baseURI)
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

	// Anchor within resolved schema. A fetched document registers its anchors
	// under its own canonical base, which is the retrieval URI unless the
	// document declares a distinct $id, so try the canonical base before
	// falling back to the retrieval URI.
	if anchorTarget, ok := v.lookupAnchor(uriref.AnchorKey(baseURI, fragment)); ok {
		return anchorTarget
	}

	if canonBase := v.schemaBase(target); canonBase != "" && canonBase != baseURI {
		if anchorTarget, ok := v.lookupAnchor(uriref.AnchorKey(canonBase, fragment)); ok {
			return anchorTarget
		}
	}

	return nil
}

// resolveJSONPointer resolves a JSON Pointer fragment against a schema.
//
// Typed traversal handles the common case, matching pointer segments to known
// Schema fields. When that fails the pointer may still target a referenceable
// location that has no typed field, so resolution falls back to walking the
// schema's JSON form. Such locations are a sub-schema carried as raw JSON in an
// unknown keyword, or the internals of a non-applicator keyword such as
// examples.
func (v *validator) resolveJSONPointer(root *Schema, fragment string, encoded bool) *Schema {
	segments, ok := jsonptr.FragmentSegments(fragment, encoded)
	if !ok {
		return nil
	}

	if target := jsonptr.TraverseSchema(root, segments); target != nil {
		return target
	}

	return v.resolveJSONPointerViaJSON(root, segments)
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
// resolves only when it is itself a schema: a JSON object or boolean. Any
// other target (a string, number, or missing member) yields nil, so a pointer
// into a non-schema value or a typo stays unresolved.
//
// A located schema is freshly unmarshaled and so unknown to the registries
// built at compile time; it is registered through the per-run fallback
// registries with the base URI in effect at its location, so any $ref,
// $anchor, or $id inside it resolves correctly instead of against an empty
// base.
//
// Results are cached per (root, pointer): the same fallback is reached once per
// ref during gate checking and again for each instance node the ref is
// evaluated against, and the root is marshaled at most once per distinct
// pointer. This path runs only for the uncommon untyped-location pointer.
func (v *validator) resolveJSONPointerViaJSON(root *Schema, segments []string) *Schema {
	if v.jsonPointerCache == nil {
		v.jsonPointerCache = map[jsonPointerKey]*Schema{}
	}

	key := jsonPointerKey{root: root, pointer: jsonptr.SegmentsKey(segments)}
	if cached, ok := v.jsonPointerCache[key]; ok {
		return cached
	}

	target, base := jsonptr.SchemaAtJSONPointer(root, segments, v.schemaBase(root))
	if target != nil {
		v.registerFallbackSchema(target, base)
	}

	v.jsonPointerCache[key] = target

	return target
}

// registerFallbackSchema walks a schema materialized by the JSON-pointer
// fallback and records its subtree's base URIs, $ids, and anchors in the
// per-run fallback registries. A scratch validator collects the walk output so
// the shared registries stay untouched and concurrent runs cannot race on
// them.
func (v *validator) registerFallbackSchema(s *Schema, base string) {
	scratch := &validator{draft: v.draft, inertIDs: v.inertIDs}
	scratch.initRegistries()
	scratch.walkSchema(s, base)

	if v.fallbackBaseURIs == nil {
		v.fallbackURIRegistry = map[string]*Schema{}
		v.fallbackAnchorRegistry = map[string]*Schema{}
		v.fallbackBaseURIs = map[*Schema]string{}
	}

	// $dynamicAnchor registrations are deliberately not carried into the fallback
	// scope: lookupDynamicAnchor resolves only against the shared registry, so a
	// dynamic anchor a fallback materialized cannot pollute an unrelated
	// $dynamicRef's dynamic scope.
	//
	// Merge first-write-wins so two distinct fallback schemas registering the
	// same absolute $id/$anchor key (only reachable from a malformed duplicate-key
	// schema) resolve deterministically to the earliest-materialized one, matching
	// registerSchema's onlyIfAbsent precedence rather than a map-iteration race.
	for k, s := range scratch.uriRegistry {
		if _, ok := v.fallbackURIRegistry[k]; !ok {
			v.fallbackURIRegistry[k] = s
		}
	}

	for k, s := range scratch.anchorRegistry {
		if _, ok := v.fallbackAnchorRegistry[k]; !ok {
			v.fallbackAnchorRegistry[k] = s
		}
	}

	// Base URIs key on the schema pointer, which is unique per node, so no
	// cross-schema collision is possible and a plain copy is correct.
	maps.Copy(v.fallbackBaseURIs, scratch.baseURIs)
}

// schemaBase returns the base URI registered for s, consulting the shared
// registry first and the per-run fallback registrations second.
func (v *validator) schemaBase(s *Schema) string {
	if base, ok := v.baseURIs[s]; ok {
		return base
	}

	return v.fallbackBaseURIs[s]
}

// lookupURI resolves an absolute URI to its schema, consulting the shared
// registry first and the per-run fallback registrations second.
func (v *validator) lookupURI(uri string) (*Schema, bool) {
	if s, ok := v.uriRegistry[uri]; ok {
		return s, true
	}

	s, ok := v.fallbackURIRegistry[uri]

	return s, ok
}

// lookupAnchor resolves a baseURI#anchor key, consulting the shared registry
// first and the per-run fallback registrations second.
func (v *validator) lookupAnchor(key string) (*Schema, bool) {
	if s, ok := v.anchorRegistry[key]; ok {
		return s, true
	}

	s, ok := v.fallbackAnchorRegistry[key]

	return s, ok
}

// lookupDynamicAnchor resolves a baseURI#name key against $dynamicAnchor
// registrations in the shared compile-time registry only. Unlike URI and anchor
// lookups it does not consult the per-run JSON-pointer fallback: a $dynamicAnchor
// that a fallback materialized for an unrelated ref is outside the dynamic scope
// of any $dynamicRef and must not be selectable as its target.
func (v *validator) lookupDynamicAnchor(key string) (*Schema, bool) {
	s, ok := v.dynamicAnchorRegistry[key]

	return s, ok
}
