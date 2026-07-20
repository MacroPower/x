package jsonschema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/annotations"
	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/format"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
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

// newError builds a validation error at the given instance location and the
// fully-formed schema location, copying both path representations from the typed
// locations through this single constructor so the four private path fields can
// never be mismatched at a call site. It is a fresh value each call, keeping
// validation safe to run concurrently on a shared [Validator]. [leafError] and
// [wrapError] are the keyword-appending conveniences over it for the common case
// where the schema location is exactly the keyword asserted; the few call sites
// whose location names a map member (patternProperties, dependencies) or omits a
// keyword (the boolean false schema) pass the location here directly.
func newError(
	instancePath instanceLocation,
	schemaPath schemaLocation,
	keyword, msg string,
	causes []*ValidationError,
) *ValidationError {
	return &ValidationError{
		InstancePath: instancePath.ptr,
		segments:     instancePath.segs,
		SchemaPath:   schemaPath.ptr,
		schemaSegs:   schemaPath.segs,
		Keyword:      keyword,
		Message:      msg,
		Causes:       causes,
	}
}

// leafError builds a terminal (cause-free) validation error at the keyword token
// under schemaPath: the keyword-appending convenience over [newError] for the
// common case where the schema location is exactly the keyword being asserted.
func leafError(instancePath instanceLocation, schemaPath schemaLocation, keyword, msg string) *ValidationError {
	return newError(instancePath, schemaPath.kw(keyword), keyword, msg, nil)
}

// wrapError builds a validation error carrying nested causes at the keyword
// token under schemaPath, the non-terminal counterpart of [leafError]. Both
// append the keyword to schemaPath and route through [newError], so a
// cause-bearing applicator error pairs its path fields identically to a leaf.
func wrapError(
	instancePath instanceLocation,
	schemaPath schemaLocation,
	keyword, msg string,
	causes []*ValidationError,
) *ValidationError {
	return newError(instancePath, schemaPath.kw(keyword), keyword, msg, causes)
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
	refResolver RefResolver // optional remote ref resolver

	// The caller's context for the current compile or validation run, passed
	// to the resolver with every resolution call. It has the
	// same lifetime discipline as the other per-run state: Compile
	// sets it for the duration of compilation and clears it before the
	// validator is cached, and forInstance sets it per run, so a stored
	// context never outlives the call that supplied it. The Must* entry
	// points use [context.Background].
	ctx context.Context

	// The shared, compiled ref-resolution registry (URI/anchor/base-URI maps),
	// built once at Compile and shared by reference across concurrent runs. Each
	// run derives a per-run refSession from it; a run that fetches a remote ref
	// clones it copy-on-write so the shared maps stay immutable.
	refReg *refresolve.Registry

	// The per-run resolution view: ref/pointer caches, per-run fallback
	// registrations, negative cache, and dynamic scope. The compiled proto
	// carries a compile-time session for the resolve-error gate; forInstance and
	// the inliner each derive their own.
	refSession *refresolve.Session

	// The remote-fetch strategy passed to refSession resolution. On the compiled
	// proto it writes the shared refReg directly (so gate-time fetches persist
	// into the compiled registry); on a per-run session it clones the registry
	// copy-on-write before its first write.
	refFetch refresolve.Fetch

	// NumericBounds, patternCache, and patternProps below are compile-time
	// caches of derived per-schema state. They are populated once during
	// Compile by precompute, which runs single-threaded, and are read-only
	// afterward; forInstance shares them by reference across runs, so
	// concurrent Validate calls only read them. A schema reached only at
	// validation time (a remote or JSON-pointer fallback schema) is absent
	// from these maps, and the validation path falls back to computing the
	// value directly.
	numericBounds map[*Schema]*precomputedBounds // numeric bound keywords as rationals

	root               *Schema
	resolveOpts        *ResolveOptions
	formatsForce       *bool           // explicit WithFormats override; nil if unset
	vocabOverride      map[string]bool // from WithVocabularies
	formatCheckers     map[string]FormatValidator
	metaSchemaResolver RefResolver // metaschema lookup by $schema URI (WithMetaSchemaResolver)
	visiting           map[visitKey]bool
	patternCache       map[*Schema]compiledPattern            // schema.Pattern compiled (see numericBounds)
	patternProps       map[*Schema]map[string]compiledPattern // patternProperties keys compiled (see numericBounds)
	constRats          map[*Schema]*big.Rat                   // numeric const value as a rational (see numericBounds)
	enumRats           map[*Schema][]*big.Rat                 // numeric enum members as rationals by index (see numericBounds)
	sortedPropertyKeys map[*Schema][]string                   // schema.Properties keys, sorted (see numericBounds)
	sortedPatternKeys  map[*Schema][]string                   // schema.PatternProperties keys, sorted (see numericBounds)
	itemsPlans         map[*Schema]*itemsPlan                 // normalized array item keywords (see numericBounds)

	// The WithDraft override; nil leaves the draft to $schema detection.
	draftOverride *Draft

	// The root document's base URI from [WithBaseURI]; "" leaves the base
	// to the root schema's $id.
	baseURI string

	// The activeRows are the keywordTable rows this run evaluates: those whose
	// draft, vocabulary, and opt-in gates all pass. The gate reads only run-fixed
	// state (draft, vocabs, formatsEnabled, contentEnabled), so the set is constant
	// for the run and is computed once at Compile (buildActiveRows) instead of
	// re-deciding every row at every instance node. The per-run forInstance copy
	// shares the slice by reference; it is read-only after Compile.
	activeRows []*keywordEntry

	draft  Draft
	vocabs vocab.Set // resolved active vocabularies

	formatsEnabled bool
	contentEnabled bool // assert contentEncoding/contentMediaType (WithContent)

	// Treat $id as an inert annotation during the registry walk: no URI or
	// anchor registration, no base-URI change, in any form including the
	// Draft 7 fragment-only anchor form. Only the inliner sets it, for
	// [WithRetrievalBase]; Compile never does, so validation behavior is
	// unaffected.
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

	// Filter the dispatch table to the rows this run evaluates now that every
	// gate input (draft, vocabularies, format/content opt-in) is resolved, so the
	// per-node walk iterates only applicable rows and never re-runs gatePasses.
	v.buildActiveRows()

	// The gate session's fetch reads the compile context from the ctx field set
	// above, not a threaded parameter.
	//nolint:contextcheck // See the comment above.
	v.buildRefReg()

	// The dynamic scope is seeded per run by forInstance, the single source for
	// the rule; the compiled validator's compile-time session (used only by the
	// resolve-error gate) needs no scope.

	return v, nil
}

// refDeps returns the dependency-injection boundary the resolution core needs:
// the sub-schema traversal the parent owns. Deep cloning stays parent-side in
// the fetch closures, so the core needs no clone dependency.
func refDeps() refresolve.Deps {
	return refresolve.Deps{Children: schemaChildren}
}

// toRefDraft maps the parent draft to the resolution core's two-value enum,
// which drives the Draft-7 sibling-$id exception and dynamic-scope seeding.
func toRefDraft(d Draft) refresolve.Draft {
	if d == Draft7 {
		return refresolve.Draft7
	}

	return refresolve.Draft2020
}

// buildRefReg builds the compiled ref-resolution registry over the root document
// (seeded with the normalized [WithBaseURI] base) and the compile-time session
// the resolve-error gate resolves through. The gate's fetches write the shared
// refReg directly (via a copy-on-write-disabled fetch) so remote documents
// fetched while compiling persist into the registry every run shares.
func (v *validator) buildRefReg() {
	v.refReg = refresolve.NewRegistry(refDeps(), toRefDraft(v.draft), v.inertIDs)
	v.refReg.Build(v.root, uriref.NormalizeBaseURI(v.baseURI))

	v.refSession = v.refReg.NewSession()
	// The fetch reads the run's context from the ctx field, so no parameter
	// threads through the deep resolution machinery.
	//nolint:contextcheck // See the comment above.
	v.refFetch = v.remoteFetch(v.refSession, false)
}

// forInstance returns a per-validation view of a compiled validator with fresh
// mutable walk state (the visiting set and a fresh per-run refSession), so a
// [Validator] can be reused and is safe for concurrent use. The immutable
// per-schema state (the compiled refReg, resolved vocabularies, draft, and
// format configuration) is shared. The caller's ctx is carried on the per-run
// copy so a [RefResolver] resolving a remote ref at validation time sees the
// context of the run that triggered it.
//
// The compiled refReg is shared by reference; it is immutable after Compile, so
// concurrent runs read it safely. A run that fetches a remote ref at validation
// time clones it privately via the session's copy-on-write (see
// [refresolve.Session.EnsureOwned] in the run's refFetch), sparing the clone for
// the common run that resolves nothing remotely.
func (v *validator) forInstance(ctx context.Context) *validator {
	rv := *v
	rv.ctx = ctx
	rv.visiting = map[visitKey]bool{}

	rv.refSession = v.refReg.NewSession()
	if rv.draft == Draft2020 {
		rv.refSession.SeedDynamicScope(rv.refSession.SchemaBase(rv.root))
	}

	// The fetch reads the run's context from the ctx field set above, so no
	// parameter threads through the deep resolution machinery.
	//nolint:contextcheck // See the comment above.
	rv.refFetch = rv.remoteFetch(rv.refSession, true)

	return &rv
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
// base-URI registries, which keeps the validation-time fallback walk (the
// session's RegisterFallback) from populating these caches.
func (v *validator) precompute() map[*Schema]bool {
	v.numericBounds = map[*Schema]*precomputedBounds{}
	v.patternCache = map[*Schema]compiledPattern{}
	v.patternProps = map[*Schema]map[string]compiledPattern{}
	v.constRats = map[*Schema]*big.Rat{}
	v.enumRats = map[*Schema][]*big.Rat{}
	v.sortedPropertyKeys = map[*Schema][]string{}
	v.sortedPatternKeys = map[*Schema][]string{}
	v.itemsPlans = map[*Schema]*itemsPlan{}

	visited := map[*Schema]bool{}
	v.precomputeSchema(v.root, visited)

	return visited
}

// precomputeSchema records the derived caches for one schema and recurses into
// its sub-schemas, guarding against schema graph cycles with visited. It is the
// Compile-time counterpart of the dispatch loop: it runs each active keyword
// row's compile step (nil for rows that precompute nothing), so the per-schema
// caches a row's eval consults are populated by the same row that reads them. A
// row the run's draft, vocabulary, or opt-in gates disabled (buildActiveRows
// runs earlier in newValidator) never evaluates, so its caches are never read
// and are not built.
func (v *validator) precomputeSchema(schema *Schema, visited map[*Schema]bool) {
	if schema == nil || visited[schema] {
		return
	}

	visited[schema] = true

	for _, e := range v.activeRows {
		if e.compile != nil {
			e.compile(v, schema)
		}
	}

	for _, e := range SubschemaEntries(schema) {
		v.precomputeSchema(e.Schema, visited)
	}
}

// numericCompile caches a schema's numeric bound keywords as rationals for the
// numeric row's eval.
func numericCompile(v *validator, s *Schema) {
	if b := computeBounds(s); b != nil {
		v.numericBounds[s] = b
	}
}

// enumCompile caches a schema's numeric enum members as rationals by index for
// the enum row's eval.
func enumCompile(v *validator, s *Schema) {
	if rats := numrat.EnumMemberRats(s.Enum); rats != nil {
		v.enumRats[s] = rats
	}
}

// constCompile caches a schema's numeric const value as a rational for the const
// row's eval.
func constCompile(v *validator, s *Schema) {
	if s.Const != nil {
		if r, ok := numrat.SchemaNumberRat(*s.Const); ok {
			v.constRats[s] = r
		}
	}
}

// stringCompile caches a schema's compiled Pattern for the string row's eval.
func stringCompile(v *validator, s *Schema) {
	if s.Pattern != "" {
		re, err := regexcache.Compile(s.Pattern)
		v.patternCache[s] = compiledPattern{re: re, err: err}
	}
}

// objectApplicatorsCompile caches a schema's sorted property keys, compiled
// patternProperties, and sorted pattern keys for the object.applicators row's
// eval. The key sets are fixed per schema, so the sorts and compiles happen
// once at Compile time instead of on every object instance node.
func objectApplicatorsCompile(v *validator, s *Schema) {
	if len(s.Properties) > 0 {
		v.sortedPropertyKeys[s] = slices.Sorted(maps.Keys(s.Properties))
	}

	if len(s.PatternProperties) > 0 {
		compiled := make(map[string]compiledPattern, len(s.PatternProperties))
		for pattern := range s.PatternProperties {
			re, err := regexcache.Compile(pattern)
			compiled[pattern] = compiledPattern{re: re, err: err}
		}

		v.patternProps[s] = compiled
		v.sortedPatternKeys[s] = slices.Sorted(maps.Keys(s.PatternProperties))
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

// callResolver invokes resolver for uri under ctx, with ok reporting whether
// the resolver served the URI: an ErrNotResolved answer becomes ok false with a
// nil error. A nil schema with a nil error is normalized to the not-resolved
// answer too, upholding the [RefResolver] contract that no caller dereferences a
// nil document. The validator's remote fetch and loader and the inliner's fetch
// all share it, so the three sites invoke the resolver identically.
func callResolver(ctx context.Context, resolver RefResolver, uri string) (*Schema, bool, error) {
	s, err := resolver.ResolveRef(ctx, uri)
	if errors.Is(err, ErrNotResolved) {
		return nil, false, nil
	}

	if err != nil {
		//nolint:wrapcheck // Callers wrap the error with ErrRefResolve or tolerate it.
		return nil, false, err
	}

	if s == nil {
		return nil, false, nil
	}

	return s, true, nil
}

// remoteFetch returns the [refresolve.Fetch] the resolution core calls when a
// non-fragment ref's document is not yet registered. It fetches the document
// through the configured [RefResolver], deep-copies it, registers the copy under
// baseURI, and walks its nested $id/$anchor entries in only-if-absent mode so
// they cannot clobber an already-loaded entry.
//
// A per-run session (cow true) clones the compiled registry copy-on-write before
// its first write via [refresolve.Session.EnsureOwned], so the registrations
// live only for that run and never race a concurrent run. The compile-time gate
// session (cow false) writes the shared refReg directly, so a remote document
// fetched while compiling persists into the registry every run shares.
//
// A resolver miss or error is recorded in the session's per-run negative cache,
// so the resolver is consulted at most once per baseURI in a run even when many
// nodes reference an unresolvable URI. The recorded error is replayed on each
// later evaluation, and only a resolver-reported failure carries an error;
// a plain not-resolved answer returns (nil, nil).
func (v *validator) remoteFetch(sess *refresolve.Session, cow bool) refresolve.Fetch {
	return func(baseURI string) (*Schema, error) {
		if v.refResolver == nil {
			return nil, nil //nolint:nilnil // A missing resolver is a plain miss, not an error.
		}

		if recorded, seen := sess.RemoteMiss(baseURI); seen {
			if recorded != nil {
				return nil, fmt.Errorf("%w: %w", ErrRefResolve, recorded)
			}

			return nil, nil //nolint:nilnil // A recorded plain miss stays a plain miss.
		}

		schema, ok, err := callResolver(v.runContext(), v.refResolver, baseURI)
		if err != nil {
			sess.RecordRemoteMiss(baseURI, err)

			return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
		}

		if !ok {
			sess.RecordRemoteMiss(baseURI, nil)

			return nil, nil //nolint:nilnil // A resolver miss is not an error.
		}

		// Deep-copy before registering so the resolver-owned schema is never
		// mutated by the walk and the cache holds an independent copy.
		cp, err := cloneSchema(schema)
		if err != nil {
			// Record the miss like the paths above, so a document that resolves
			// but fails to deep-copy is not re-fetched on every node referencing
			// it (the "at most once per baseURI in a run" contract).
			sess.RecordRemoteMiss(baseURI, err)

			return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
		}

		if cow {
			// Clone the registry into this run's own copy before the first
			// remote registration so the writes below cannot race a concurrent
			// run still sharing the compiled registry.
			sess.EnsureOwned()
		}

		reg := sess.Registry()
		reg.URI[baseURI] = cp
		reg.WalkFetched(cp, baseURI)

		return cp, nil
	}
}

// remoteLoader returns a [jsonschema.Loader] for upstream Schema.Resolve.
// When a [RefResolver] is configured, resolved schemas are registered in the
// shared compiled registry (caching them for the validation walk). If no
// resolver is configured or the resolver misses or fails, an empty schema is
// returned so Schema.Resolve doesn't fail.
//
// Schemas returned to the upstream resolver are deep-copied via JSON
// round-trip so that Schema.Resolve's internal mutations (e.g. $schema
// inheritance) don't modify the caller's original schema objects. This runs at
// compile time single-threaded, so the registrations land directly in the
// compiled registry every per-run validator then shares.
func (v *validator) remoteLoader() jsonschema.Loader {
	return func(uri *url.URL) (*Schema, error) {
		uriStr := uri.String()
		// Check cache first.
		if s, ok := v.refReg.URI[uriStr]; ok {
			return s, nil
		}

		if v.refResolver != nil {
			s, ok, err := callResolver(v.runContext(), v.refResolver, uriStr)
			if err == nil && ok {
				// Deep-copy so the upstream resolver's mutations don't
				// affect the original schema from the RefResolver.
				cp, cpErr := cloneSchema(s)
				if cpErr != nil {
					return nil, fmt.Errorf("clone resolved schema: %w", cpErr)
				}

				// Register the copy under uriStr so subsequent lookups during
				// both Schema.Resolve and the validation walk find it without
				// re-calling the resolver. Its own nested $ids/anchors are
				// walked in only-if-absent mode so a fetched doc cannot clobber
				// an already-loaded entry.
				v.refReg.URI[uriStr] = cp
				v.refReg.WalkFetched(cp, uriStr)

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
	// construction error. The visited set is shared with the post-Resolve remote
	// pass below, so a node reached both locally and through a remote URI is
	// checked once.
	typeVisited := map[*Schema]bool{}

	err = checkTypeNames(schema, "", typeVisited)
	if err != nil {
		return nil, err
	}

	// Reject a negative length or count bound up front, for the same reason as
	// the type-name check: Schema.Resolve does not enforce the spec's
	// non-negative-integer requirement, so a negative bound otherwise compiles
	// and then silently mis-validates. The visited set is shared with the
	// post-Resolve remote pass below.
	boundsVisited := map[*Schema]bool{}

	err = checkNonNegativeBounds(schema, "", boundsVisited)
	if err != nil {
		return nil, err
	}

	// Reject the Draft-7 array form of items under Draft 2020-12, where it has
	// no meaning and the validation walk would otherwise drop it silently,
	// accepting every element. Draft-7 spells tuples this way, so the check is
	// gated on the resolved draft rather than applied unconditionally.
	itemsVisited := map[*Schema]bool{}

	if v.draft == Draft2020 {
		err = checkItemsArrayDraft2020(schema, "", itemsVisited)
		if err != nil {
			return nil, err
		}
	}

	// Precompute derived per-schema state (numeric bounds and compiled
	// patterns) while still single-threaded, so the returned Validator only
	// reads these caches once shared across goroutines.
	precomputeVisited := v.precompute()

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

	// Resolve may have fetched and registered remote documents in uriRegistry
	// after the passes above ran over the root subtree. Two things must extend to
	// them, in key-sorted order so a reported violation locates a stable document:
	//
	//   - The structural checks (type names always, the Draft-7 items array under
	//     Draft 2020-12). The root pass walks typed sub-schemas only and never
	//     crosses a $ref into a remote, so a remote carrying an invalid type or an
	//     items array would otherwise compile cleanly and then silently
	//     mis-validate. The shared visited sets skip the root (also registered
	//     under its base URI) and any node reached through several URIs, so each is
	//     checked once; the base URI prefixes the path so a violation names the
	//     offending remote.
	//   - The precompute caches (numeric bounds and compiled patterns), so a
	//     numeric, pattern, const, or enum keyword in a fetched remote hits the
	//     cache instead of being recomputed on every validation.
	for _, uri := range slices.Sorted(maps.Keys(v.refReg.URI)) {
		s := v.refReg.URI[uri]

		err = checkTypeNames(s, uri+"#", typeVisited)
		if err != nil {
			return nil, err
		}

		err = checkNonNegativeBounds(s, uri+"#", boundsVisited)
		if err != nil {
			return nil, err
		}

		if v.draft == Draft2020 {
			err = checkItemsArrayDraft2020(s, uri+"#", itemsVisited)
			if err != nil {
				return nil, err
			}
		}

		v.precomputeSchema(s, precomputeVisited)
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

// checkItemsArrayDraft2020 rejects the Draft-7 array form of the items keyword
// (ItemsArray, what upstream parses a JSON `"items": [ ... ]` into) when
// compiling under [Draft2020], where it has no meaning. Without this the
// 2020-12 array walk drops the constraint silently and validates every element
// against nothing. The traversal mirrors [checkTypeNames]: it uses
// [SubschemaEntries] for the field list and each entry's Pointer for the
// location, with visited guarding schema-graph cycles. Compile runs it only
// under Draft 2020-12, so Draft-7 schemas pay nothing.
func checkItemsArrayDraft2020(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	if len(schema.ItemsArray) > 0 {
		return fmt.Errorf("%w; use prefixItems at %s/items", ErrItemsArrayUnderDraft2020, schemaPath)
	}

	for _, entry := range SubschemaEntries(schema) {
		err := checkItemsArrayDraft2020(entry.Schema, schemaPath+entry.Pointer, visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkNonNegativeBounds rejects a negative value on a length or count keyword,
// each of which the spec defines as a non-negative integer. Schema.Resolve does
// not enforce it, so a negative bound otherwise compiles cleanly and then
// silently mis-validates: a negative maximum rejects every instance and a
// negative minimum never fires. The traversal mirrors [checkTypeNames]: it uses
// [SubschemaEntries] for the field list and each entry's Pointer for the
// location, with visited guarding schema-graph cycles. It is draft-agnostic;
// every draft defines these keywords as non-negative.
func checkNonNegativeBounds(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	for _, bound := range []struct {
		value   *int
		keyword string
	}{
		{schema.MinLength, "minLength"},
		{schema.MaxLength, "maxLength"},
		{schema.MinItems, "minItems"},
		{schema.MaxItems, "maxItems"},
		{schema.MinProperties, "minProperties"},
		{schema.MaxProperties, "maxProperties"},
		{schema.MinContains, "minContains"},
		{schema.MaxContains, "maxContains"},
	} {
		if bound.value != nil && *bound.value < 0 {
			return fmt.Errorf("%w: %s is %d at %s/%s",
				ErrNegativeBound, bound.keyword, *bound.value, schemaPath, bound.keyword)
		}
	}

	for _, entry := range SubschemaEntries(schema) {
		err := checkNonNegativeBounds(entry.Schema, schemaPath+entry.Pointer, visited)
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
// entry points cannot drift. Normalization and the acceptance check share one
// tree walk via [normalize.ValueChecked].
func normalizeAndCheck(instance any) (any, error) {
	instance, ok := normalize.ValueChecked(instance)
	if !ok {
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
// have no typed Schema field. A resolution reports failure through its
// [refresolve.Result]; the gate reads only the target, so a resolver error
// does not leak into a later validation error.
func (v *validator) refsResolveWellFormed(schema *Schema, resolveOpts ResolveOptions) bool {
	ok := true

	eachSubschema(schema, func(s *Schema) {
		if !ok {
			return
		}

		if s.Ref != "" && !v.refTargetWellFormed(v.resolveRef(s, s.Ref).Target, resolveOpts) {
			ok = false

			return
		}

		if v.draft == Draft2020 && s.DynamicRef != "" &&
			!v.refTargetWellFormed(v.resolveDynamicRef(s, s.DynamicRef).Target, resolveOpts) {
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
// targets. A resolution reports failure through its [refresolve.Result]; the
// gate reads only the target, so a resolver error does not leak into a later
// error.
func (v *validator) allRefsResolvable(schema *Schema) bool {
	ok := true

	eachSubschema(schema, func(s *Schema) {
		if !ok {
			return
		}

		if s.Ref != "" && v.resolveRef(s, s.Ref).Target == nil {
			ok = false
		}

		if v.draft == Draft2020 && s.DynamicRef != "" && v.resolveDynamicRef(s, s.DynamicRef).Target == nil {
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

// validate performs the depth-first recursive walk.
func (v *validator) validate(
	schema *Schema,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations.Set,
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
		// it via labelFalseSchemaKeyword. The schema location is bare (no keyword
		// token) for the same reason, so the error is built through newError
		// directly rather than the keyword-appending leafError.
		return []*ValidationError{
			newError(instancePath, schemaPath, "", "value is not allowed", nil),
		}
	}

	// Circular ref detection: same schema + same instance path = true cycle.
	key := visitKey{schema, instancePath.ptr}
	if v.visiting[key] {
		return nil // treat as passing to avoid infinite recursion
	}

	v.visiting[key] = true
	defer delete(v.visiting, key)

	// Dynamic scope tracking: push when entering a new resource boundary.
	// The root is already on the stack from seeding; subsequent pushes happen
	// when validation crosses into a schema whose resource base URI differs
	// from the current scope top. EnterScope returns nil when the scope is empty
	// (Draft 7, where it is never seeded) or unchanged, so the defer is
	// registered only on the rarer boundary crossing, not on every node.
	if v.draft == Draft2020 {
		if leave := v.refSession.EnterScope(v.refSession.SchemaBase(schema)); leave != nil {
			defer leave()
		}
	}

	// If this schema uses unevaluated* keywords but the caller didn't provide
	// annotations, create a local annotations object to track evaluated items.
	if ann == nil && (schema.UnevaluatedProperties != nil || schema.UnevaluatedItems != nil) {
		ann = annotations.New()
	}

	// Drive keyword evaluation from the run's active dispatch rows (filtered once
	// at Compile by buildActiveRows), evaluated in table order -- the
	// deterministic error-slice order the tests pin.
	ctx := evalContext{
		v: v, schema: schema, instance: instance,
		instancePath: instancePath, schemaPath: schemaPath, ann: ann,
	}

	// Draft-07: a $ref with siblings ignores the siblings, so only the $ref row
	// runs. Under Draft 2020-12 a $ref evaluates alongside its siblings, and a
	// schema with no $ref runs every active row regardless of draft.
	onlyRef := v.draft == Draft7 && schema.Ref != ""

	var errs []*ValidationError

	for _, e := range v.activeRows {
		if onlyRef && !e.isRef {
			continue
		}

		errs = append(errs, e.eval(ctx)...)
	}

	return errs
}

// evalUnevaluatedProperties checks unevaluatedProperties. It runs in
// phaseUnevaluated, after every applicator has recorded the properties it
// evaluated, so the annotation set it consults is complete. The draft and
// vocabulary gate lives on the table row; the nil-annotation short-circuit
// stays here because it is a per-node runtime condition, not a static fact.
//
//nolint:nestif // Nesting tracks the annotation guards required to apply unevaluatedProperties correctly.
func evalUnevaluatedProperties(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if ann == nil || schema.UnevaluatedProperties == nil {
		return nil
	}

	obj, ok := ctx.instance.(map[string]any)
	if !ok || ann.AllPropertiesSet() {
		return nil
	}

	// IsEmptySchema implies Not == nil, so the schema is not a false schema: an
	// empty (always-true) unevaluatedProperties evaluates every remaining
	// property and can never fail. The saturation flag fully captures that
	// outcome, so the per-property loop is skipped: it would only re-validate
	// each property against the empty schema (always passing) and re-record what
	// SetAllProperties subsumes.
	if schemashape.IsEmpty(schema.UnevaluatedProperties) {
		ann.SetAllProperties()

		return nil
	}

	v := ctx.v

	var errs []*ValidationError

	childSchemaPath := ctx.schemaPath.kw("unevaluatedProperties")

	// Iterate in sorted key order so the emitted cause errors are deterministic,
	// matching the sibling object keywords (properties, patternProperties,
	// additionalProperties, propertyNames).
	for _, propName := range slices.Sorted(maps.Keys(obj)) {
		if ann.Evaluated(propName) {
			continue
		}

		val := obj[propName]

		childPath := ctx.instancePath.key(propName)
		childErrs := v.validate(schema.UnevaluatedProperties, val, childPath, childSchemaPath, nil)
		if len(childErrs) == 0 {
			ann.RecordProperty(propName)
		} else {
			errs = append(errs, newError(
				childPath, childSchemaPath, KeywordUnevaluatedProperties,
				fmt.Sprintf("property %q is not allowed by unevaluatedProperties", propName),
				childErrs,
			))
		}
	}

	return errs
}

// evalUnevaluatedItems checks unevaluatedItems, the array counterpart of
// [evalUnevaluatedProperties]; it runs last for the same annotation-completeness
// reason.
//
//nolint:nestif // Validation keyword nesting is inherent.
func evalUnevaluatedItems(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if ann == nil || schema.UnevaluatedItems == nil {
		return nil
	}

	arr, ok := ctx.instance.([]any)
	if !ok || ann.AllItemsSet() {
		return nil
	}

	// IsEmptySchema implies Not == nil, so the schema is not a false schema: an
	// empty (always-true) unevaluatedItems evaluates every remaining item and
	// can never fail. The saturation flag fully captures that outcome, so the
	// per-item loop is skipped: it would only re-validate each item against the
	// empty schema (always passing) and re-record what SetAllItems subsumes.
	if schemashape.IsEmpty(schema.UnevaluatedItems) {
		ann.SetAllItems()

		return nil
	}

	v := ctx.v

	var errs []*ValidationError

	childSchemaPath := ctx.schemaPath.kw("unevaluatedItems")

	for i, item := range arr {
		if ann.ItemEvaluated(i) {
			continue
		}

		childPath := ctx.instancePath.index(i)
		childErrs := v.validate(schema.UnevaluatedItems, item, childPath, childSchemaPath, nil)
		if len(childErrs) == 0 {
			ann.RecordItem(i)
		} else {
			errs = append(errs, newError(
				childPath, childSchemaPath, KeywordUnevaluatedItems,
				fmt.Sprintf("item %d is not allowed by unevaluatedItems", i),
				childErrs,
			))
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

// evalType checks the type keyword.
func evalType(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	types := schema.Types
	if schema.Type != "" {
		types = []string{schema.Type}
	}

	if len(types) == 0 {
		return nil
	}

	for _, t := range types {
		if normalize.MatchesType(ctx.instance, t) {
			return nil
		}
	}

	got := normalize.TypeName(ctx.instance)

	return []*ValidationError{
		leafError(ctx.instancePath, ctx.schemaPath, KeywordType,
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

// evalEnum checks the enum keyword.
func evalEnum(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	// A nil Enum means the keyword is absent (skip). An empty but non-nil Enum
	// ("enum": []) permits no values, so every instance fails it.
	if schema.Enum == nil {
		return nil
	}

	rats := ctx.v.enumRats[schema]

	for i, allowed := range schema.Enum {
		var allowedRat *big.Rat

		if rats != nil {
			allowedRat = rats[i]
		}

		if jsonequal.EqualWithRat(allowed, allowedRat, ctx.instance) {
			return nil
		}
	}

	return []*ValidationError{
		leafError(ctx.instancePath, ctx.schemaPath, KeywordEnum, "value does not match any enum member"),
	}
}

// evalConst checks the const keyword.
func evalConst(ctx evalContext) []*ValidationError {
	schema := ctx.schema
	if schema.Const == nil {
		return nil
	}

	constVal := *schema.Const
	if jsonequal.EqualWithRat(constVal, ctx.v.constRats[schema], ctx.instance) {
		return nil
	}

	return []*ValidationError{
		leafError(ctx.instancePath, ctx.schemaPath, KeywordConst, "value does not match const"),
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

// propertyKeysFor returns schema.Properties' keys in sorted order, preferring
// the Compile-time cache and sorting on the fly for a schema absent from it (a
// remote or JSON-pointer fallback schema reached only at validation time).
func (v *validator) propertyKeysFor(schema *Schema) []string {
	if keys, ok := v.sortedPropertyKeys[schema]; ok {
		return keys
	}

	return slices.Sorted(maps.Keys(schema.Properties))
}

// patternKeysFor returns schema.PatternProperties' keys in sorted order, with
// the same Compile-time cache and on-the-fly fallback as [propertyKeysFor].
func (v *validator) patternKeysFor(schema *Schema) []string {
	if keys, ok := v.sortedPatternKeys[schema]; ok {
		return keys
	}

	return slices.Sorted(maps.Keys(schema.PatternProperties))
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

// evalNumeric checks numeric keywords.
func evalNumeric(ctx evalContext) []*ValidationError {
	v, schema, instance := ctx.v, ctx.schema, ctx.instance
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

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

// evalString checks the string length and pattern keywords (minLength,
// maxLength, pattern). The format keyword is a separate row ([evalFormat]): it
// belongs to core rather than the validation vocabulary and has its own opt-in
// gate, so splitting it keeps each row's gate a single declarative fact.
func evalString(ctx evalContext) []*ValidationError {
	schema := ctx.schema
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	str, ok := ctx.instance.(string)
	if !ok {
		// Json.Number is a distinct type, so it fails this assertion and string
		// keywords correctly do not apply to numbers.
		return nil
	}

	var errs []*ValidationError

	// RuneCountInString avoids allocating a []rune; only count when a length
	// keyword is present.
	if schema.MinLength != nil || schema.MaxLength != nil {
		runeLen := utf8.RuneCountInString(str)

		if schema.MinLength != nil && runeLen < *schema.MinLength {
			errs = append(errs, leafError(instancePath, schemaPath, KeywordMinLength,
				fmt.Sprintf("string length %d is less than %d", runeLen, *schema.MinLength)))
		}

		if schema.MaxLength != nil && runeLen > *schema.MaxLength {
			errs = append(errs, leafError(instancePath, schemaPath, KeywordMaxLength,
				fmt.Sprintf("string length %d is greater than %d", runeLen, *schema.MaxLength)))
		}
	}

	if schema.Pattern != "" {
		cp := ctx.v.patternFor(schema)
		switch {
		case cp.err != nil:
			// A pattern Go's RE2 cannot compile (e.g. an ECMA-262 backreference
			// or lookaround) fails closed: the constraint cannot be evaluated, so
			// no string is accepted under it rather than silently treating every
			// string as a match.
			errs = append(errs, leafError(instancePath, schemaPath, KeywordPattern,
				fmt.Sprintf("pattern %q cannot be compiled", schema.Pattern)))

		case !cp.re.MatchString(str):
			errs = append(errs, leafError(instancePath, schemaPath, KeywordPattern,
				fmt.Sprintf("string does not match pattern %q", schema.Pattern)))
		}
	}

	return errs
}

// evalFormat asserts the format keyword against a string instance. Format is
// annotation-only unless the run enables assertion (the row's optInFormat gate),
// so this is reached only when assertion is on.
func evalFormat(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	str, ok := ctx.instance.(string)
	if !ok {
		return nil
	}

	if schema.Format == "" {
		return nil
	}

	fv, exists := ctx.v.formatCheckers[schema.Format]
	if !exists {
		return nil
	}

	err := fv.ValidateFormat(ctx.v.runContext(), schema.Format, str)
	if err == nil {
		return nil
	}

	e := leafError(ctx.instancePath, ctx.schemaPath, KeywordFormat,
		fmt.Sprintf("string does not match format %q: %v", schema.Format, err))
	// Attach the checker's error so a sentinel it returns stays reachable via
	// errors.Is/As on the validation result, matching the $ref-resolution path
	// (validateResolvedRef).
	e.err = err

	return []*ValidationError{e}
}

// evalArrayItems checks the array item applicator keywords (prefixItems, items,
// additionalItems) by iterating the Compile-time [itemsPlan], which normalizes
// the two draft spellings of tuple and trailing items away, so this eval carries
// no per-node draft branch. The annotation watermark it records is exact: the
// tuple marks every index it applied a subschema to regardless of per-item
// success, and the rest marks all items only when it is the 2020-12 items
// keyword (restMarksAllItems) and actually reached a trailing element.
func evalArrayItems(ctx evalContext) []*ValidationError {
	arr, ok := ctx.instance.([]any)
	if !ok {
		return nil
	}

	plan := ctx.v.itemsPlanFor(ctx.schema)
	if plan == nil {
		return nil
	}

	v, ann := ctx.v, ctx.ann
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	for i, ps := range plan.tuple {
		if i >= len(arr) {
			break
		}

		childPath := instancePath.index(i)
		childSchemaPath := schemaPath.kw(plan.tupleLabel).idx(i)
		childErrs := v.validate(ps, arr[i], childPath, childSchemaPath, nil)
		labelFalseSchemaKeyword(childErrs, ps, plan.tupleLabel)

		errs = append(errs, childErrs...)
	}

	// The tuple annotates every index it applied a subschema to, regardless of
	// per-item success (2020-12 core §10.3.1.1: "the largest index to which this
	// keyword applied a subschema"). Because this walk collects all errors
	// instead of failing fast, the whole applied range is noted once here;
	// gating on success would leave a failed index unevaluated and let
	// unevaluatedItems re-fire on it.
	if len(plan.tuple) > 0 {
		ann.ExtendItems(min(len(plan.tuple), len(arr)))
	}

	if plan.rest != nil {
		// The rest applies to indexes at or beyond the tuple length (every index
		// when the tuple is empty). The schema location is invariant across
		// elements; build it once.
		childSchemaPath := schemaPath.kw(plan.restLabel)

		for i := len(plan.tuple); i < len(arr); i++ {
			childPath := instancePath.index(i)
			childErrs := v.validate(plan.rest, arr[i], childPath, childSchemaPath, nil)
			labelFalseSchemaKeyword(childErrs, plan.rest, plan.restLabel)

			errs = append(errs, childErrs...)
		}

		// Mark all items evaluated only for the 2020-12 items rest, and only when
		// it actually covered a trailing element: an empty tuple means it covered
		// every index (unconditional), otherwise only when the array outran the
		// tuple. Draft-07 additionalItems records no watermark (restMarksAllItems
		// is false).
		if plan.restMarksAllItems && (len(plan.tuple) == 0 || len(arr) > len(plan.tuple)) {
			ann.SetAllItems()
		}
	}

	return errs
}

// evalContains checks the contains keyword and its count assertion (the default
// minContains=1 floor and the optional minContains/maxContains bounds). The
// match loop records the contains annotation for every matching item
// unconditionally (so unevaluatedItems sees them even when the count fails); the
// count assertion is sub-gated on the validation vocabulary, since it belongs
// there while the match/annotation belongs to the applicator vocabulary that
// gates this whole row.
func evalContains(ctx evalContext) []*ValidationError {
	schema := ctx.schema
	if schema.Contains == nil {
		return nil
	}

	arr, ok := ctx.instance.([]any)
	if !ok {
		return nil
	}

	v, ann := ctx.v, ctx.ann
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	matchCount := 0

	// The contains schema location is invariant across elements; build it once
	// rather than rebuilding it on every iteration.
	containsSchemaPath := schemaPath.kw("contains")

	for i, item := range arr {
		childErrs := v.validate(schema.Contains, item, instancePath.index(i), containsSchemaPath, nil)
		if len(childErrs) == 0 {
			matchCount++

			// Record the matched index as the contains annotation (JSON Schema
			// 2020-12 core 10.3.1.3) as it is found, independent of min/maxContains,
			// so a matched item stays evaluated for unevaluatedItems even when the
			// count violates those separate assertions emitted below. RecordItem is
			// nil-safe and order independent, so recording here needs no second pass.
			ann.RecordItem(i)
		}
	}

	// The contains-count assertion belongs to the 2020-12 validation vocabulary,
	// so under Draft 2020-12 it is skipped when that vocabulary is disabled;
	// contains then only records its match annotation above. Under Draft-07
	// vocabActive(vocabValidation) is always true, so the default contains
	// assertion always applies there.
	if !v.vocabActive(vocabValidation) {
		return errs
	}

	minContains := 1
	if v.draft == Draft2020 && schema.MinContains != nil {
		minContains = *schema.MinContains
	}

	maxContains := -1
	if v.draft == Draft2020 && schema.MaxContains != nil {
		maxContains = *schema.MaxContains
	}

	if matchCount < minContains {
		// An explicit minContains owns the violation; without it the shortfall is
		// a plain contains failure (default minContains=1).
		keyword := KeywordContains
		if v.draft == Draft2020 && schema.MinContains != nil {
			keyword = KeywordMinContains
		}

		errs = append(errs, leafError(instancePath, schemaPath, keyword,
			fmt.Sprintf("array has %d matching items, minimum is %d", matchCount, minContains)))
	}

	if maxContains >= 0 && matchCount > maxContains {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordMaxContains,
			fmt.Sprintf("array has %d matching items, maximum is %d", matchCount, maxContains)))
	}

	return errs
}

// evalArrayLength checks the array length and uniqueness keywords (minItems,
// maxItems, uniqueItems).
func evalArrayLength(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	arr, ok := ctx.instance.([]any)
	if !ok {
		return nil
	}

	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	if schema.MinItems != nil && len(arr) < *schema.MinItems {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordMinItems,
			fmt.Sprintf("array has %d items, minimum is %d", len(arr), *schema.MinItems)))
	}

	if schema.MaxItems != nil && len(arr) > *schema.MaxItems {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordMaxItems,
			fmt.Sprintf("array has %d items, maximum is %d", len(arr), *schema.MaxItems)))
	}

	if schema.UniqueItems && jsonequal.HasDuplicates(arr) {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordUniqueItems,
			"array contains duplicate items"))
	}

	return errs
}

// evalObjectApplicators checks the object applicator cluster (properties,
// patternProperties, additionalProperties, propertyNames). These stay one row
// because additionalProperties reads the set of properties matched by this
// schema's own properties/patternProperties -- a per-node local, deliberately
// not the cross-subschema annotation rollup -- and that set plus the sorted
// instance keys are locals the four keywords share. The dependentSchemas
// keyword is a separate row: it needs no local matched-set and carries a
// 2020-12-only gate.
//
//nolint:nestif // One branch per object applicator keyword; flattening would not reduce the inherent fan-out.
func evalObjectApplicators(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann

	obj, ok := ctx.instance.(map[string]any)
	if !ok {
		return nil
	}

	v := ctx.v
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	// Track locally evaluated properties for additionalProperties. Only that
	// keyword reads the map, so allocate it lazily and leave it nil otherwise.
	var localEvaluated map[string]bool

	if schema.AdditionalProperties != nil {
		localEvaluated = map[string]bool{}
	}

	// Properties. Iterate in sorted-key order so the emitted error order is
	// deterministic; Go map iteration is randomized. The key set is fixed
	// per schema, so the sort is precomputed at Compile time.
	for _, propName := range v.propertyKeysFor(schema) {
		propSchema := schema.Properties[propName]
		val, exists := obj[propName]
		if !exists {
			continue
		}

		if localEvaluated != nil {
			localEvaluated[propName] = true
		}

		ann.RecordProperty(propName)

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

	// PatternProperties. Sorted iteration keeps the error order
	// deterministic; the key set is fixed, so the sort is precomputed.
	for _, pattern := range v.patternKeysFor(schema) {
		patternSchema := schema.PatternProperties[pattern]

		// One schema-path location per pattern, shared by the error branch
		// and every matching property rather than rebuilt for each.
		patternSchemaPath := schemaPath.kw("patternProperties").key(pattern)

		cp := v.patternPropertyFor(schema, pattern)
		if cp.err != nil {
			// A pattern Go's RE2 cannot compile fails closed: the keyword
			// cannot decide which properties it governs, so the object is
			// rejected rather than silently dropping the subschema. The
			// location names the pattern member, so the keyword token and
			// path differ: build through newError with the full location.
			errs = append(errs, newError(instancePath, patternSchemaPath, KeywordPatternProperties,
				fmt.Sprintf("pattern %q cannot be compiled", pattern), nil))

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

			ann.RecordProperty(propName)

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

			ann.RecordProperty(propName)

			childPath := instancePath.key(propName)
			childErrs := v.validate(schema.AdditionalProperties, val, childPath, childSchemaPath, nil)
			labelFalseSchemaKeyword(childErrs, schema.AdditionalProperties, KeywordAdditionalProperties)

			errs = append(errs, childErrs...)
		}

		ann.SetAllProperties()
	}

	// PropertyNames. The constraint is on the key, not its value, and RFC
	// 6901 gives a key no JSON Pointer of its own, so a violation borrows
	// the property's location: the wrapping error (and its causes) carry
	// the property's instance path, with Keyword "propertyNames" and the
	// offending name in the message identifying which key failed and which
	// object it belongs to.
	if schema.PropertyNames != nil {
		// The propertyNames schema location is invariant across keys; build it
		// once rather than rebuilding it on every iteration.
		childSchemaPath := schemaPath.kw("propertyNames")

		for _, propName := range sortedObjKeys {
			childPath := instancePath.key(propName)
			childErrs := v.validate(schema.PropertyNames, propName, childPath, childSchemaPath, nil)
			if len(childErrs) > 0 {
				errs = append(errs, newError(
					childPath, childSchemaPath, KeywordPropertyNames,
					fmt.Sprintf("property name %q is invalid", propName), childErrs,
				))
			}
		}
	}

	return errs
}

// evalDependentSchemas checks the 2020-12 dependentSchemas keyword. Its table
// row gates it on the applicator vocabulary and the 2020-12-and-up draft range.
func evalDependentSchemas(ctx evalContext) []*ValidationError {
	obj, ok := ctx.instance.(map[string]any)
	if !ok {
		return nil
	}

	return ctx.v.validateSchemaDependencies(
		ctx.schema.DependentSchemas, KeywordDependentSchemas,
		ctx.instance, obj, ctx.instancePath, ctx.schemaPath, ctx.ann)
}

// evalObjectCount checks the object count keywords (required, minProperties,
// maxProperties).
func evalObjectCount(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	obj, ok := ctx.instance.(map[string]any)
	if !ok {
		return nil
	}

	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	for _, reqProp := range schema.Required {
		if _, exists := obj[reqProp]; !exists {
			errs = append(errs, leafError(instancePath, schemaPath, KeywordRequired,
				fmt.Sprintf("missing required property %q", reqProp)))
		}
	}

	if schema.MinProperties != nil && len(obj) < *schema.MinProperties {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordMinProperties,
			fmt.Sprintf("object has %d properties, minimum is %d", len(obj), *schema.MinProperties)))
	}

	if schema.MaxProperties != nil && len(obj) > *schema.MaxProperties {
		errs = append(errs, leafError(instancePath, schemaPath, KeywordMaxProperties,
			fmt.Sprintf("object has %d properties, maximum is %d", len(obj), *schema.MaxProperties)))
	}

	return errs
}

// evalDependentRequired checks the 2020-12 dependentRequired keyword, gated on
// the validation vocabulary and the 2020-12-and-up draft range by its table row.
func evalDependentRequired(ctx evalContext) []*ValidationError {
	obj, ok := ctx.instance.(map[string]any)
	if !ok {
		return nil
	}

	return ctx.v.validateRequiredDependencies(
		ctx.schema.DependentRequired, KeywordDependentRequired, obj, ctx.instancePath, ctx.schemaPath)
}

// evalLegacyDependencies checks the legacy draft-07 dependencies keyword, which
// upstream splits into DependencySchemas and DependencyStrings. It is honored
// under Draft 2020-12 too for backward compatibility (the keyword was split into
// dependentSchemas and dependentRequired there, but accepting the legacy form
// aids migration and matches the optional dependencies-compatibility suite).
// Ungated by vocabulary: vocabulary is a 2020-12 concept and the legacy keyword
// predates it, so its table row carries the always-active core group.
func evalLegacyDependencies(ctx evalContext) []*ValidationError {
	obj, ok := ctx.instance.(map[string]any)
	if !ok {
		return nil
	}

	v, schema := ctx.v, ctx.schema
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	errs = append(errs, v.validateSchemaDependencies(
		schema.DependencySchemas, KeywordDependencies, ctx.instance, obj, instancePath, schemaPath, ctx.ann)...)
	errs = append(errs, v.validateRequiredDependencies(
		schema.DependencyStrings, KeywordDependencies, obj, instancePath, schemaPath)...)

	return errs
}

// validateSchemaDependencies validates the schema-valued dependency keywords:
// Draft 2020-12 dependentSchemas and the legacy draft-07 dependencies form.
// For each trigger property present in obj, the whole instance must validate
// against the dependency subschema. Annotations are merged only on success, so
// a failing dependency does not let unevaluated* observe its partial
// evaluation. The keyword argument names the keyword for the error schema path.
func (v *validator) validateSchemaDependencies(
	deps map[string]*Schema,
	keyword string,
	instance any,
	obj map[string]any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations.Set,
) []*ValidationError {
	var errs []*ValidationError

	for _, prop := range slices.Sorted(maps.Keys(deps)) {
		if _, exists := obj[prop]; !exists {
			continue
		}

		depAnn := ann.Child()
		childSchemaPath := schemaPath.kw(keyword).key(prop)
		childErrs := v.validate(deps[prop], instance, instancePath, childSchemaPath, depAnn)
		errs = append(errs, childErrs...)

		if len(childErrs) == 0 {
			ann.Merge(depAnn)
		}
	}

	return errs
}

// validateRequiredDependencies validates the string-array dependency keywords:
// Draft 2020-12 dependentRequired and the legacy draft-07 dependencies form.
// When a trigger property is present in obj, each property it names must be
// present too. The keyword argument names both the error schema path token and
// the Keyword field, which coincide for these keywords.
func (v *validator) validateRequiredDependencies(
	deps map[string][]string,
	keyword string,
	obj map[string]any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	var errs []*ValidationError

	for _, prop := range slices.Sorted(maps.Keys(deps)) {
		if _, exists := obj[prop]; !exists {
			continue
		}

		for _, dep := range deps[prop] {
			if _, exists := obj[dep]; !exists {
				errs = append(errs, newError(instancePath, schemaPath.kw(keyword).key(prop), keyword,
					fmt.Sprintf("property %q requires property %q", prop, dep), nil))
			}
		}
	}

	return errs
}

// evalAllOf checks the allOf keyword. Annotations from individual subschemas are
// merged only when the allOf as a whole succeeds; a single failing branch
// discards them all so unevaluatedProperties/Items do not observe partial
// evaluation.
func evalAllOf(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if len(schema.AllOf) == 0 {
		return nil
	}

	v := ctx.v
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var (
		allCauses []*ValidationError
		subAnns   []*annotations.Set
	)

	for i, sub := range schema.AllOf {
		subAnn := ann.Child()
		childSchemaPath := schemaPath.kw("allOf").idx(i)
		childErrs := v.validate(sub, ctx.instance, instancePath, childSchemaPath, subAnn)
		if len(childErrs) > 0 {
			allCauses = append(allCauses, childErrs...)
		} else {
			subAnns = append(subAnns, subAnn)
		}
	}

	if len(allCauses) > 0 {
		return []*ValidationError{
			wrapError(instancePath, schemaPath, KeywordAllOf, "did not validate against all subschemas", allCauses),
		}
	}

	for _, subAnn := range subAnns {
		ann.Merge(subAnn)
	}

	return nil
}

// evalAnyOf checks the anyOf keyword, merging the annotations of every matching
// branch.
func evalAnyOf(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if len(schema.AnyOf) == 0 {
		return nil
	}

	v := ctx.v
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	matched := false

	var allCauses []*ValidationError

	for i, sub := range schema.AnyOf {
		subAnn := ann.Child()
		childSchemaPath := schemaPath.kw("anyOf").idx(i)
		childErrs := v.validate(sub, ctx.instance, instancePath, childSchemaPath, subAnn)
		if len(childErrs) == 0 {
			matched = true

			ann.Merge(subAnn)
		} else {
			allCauses = append(allCauses, childErrs...)
		}
	}

	if !matched {
		return []*ValidationError{
			wrapError(instancePath, schemaPath, KeywordAnyOf, "did not validate against any subschema", allCauses),
		}
	}

	return nil
}

// evalOneOf checks the oneOf keyword, merging the single matching branch's
// annotations and failing when zero or more than one branch matches.
func evalOneOf(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if len(schema.OneOf) == 0 {
		return nil
	}

	v := ctx.v
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	matchCount := 0

	var (
		allCauses  []*ValidationError
		matchedAnn *annotations.Set
	)

	for i, sub := range schema.OneOf {
		subAnn := ann.Child()
		childSchemaPath := schemaPath.kw("oneOf").idx(i)
		childErrs := v.validate(sub, ctx.instance, instancePath, childSchemaPath, subAnn)
		if len(childErrs) == 0 {
			matchCount++
			matchedAnn = subAnn
		} else {
			allCauses = append(allCauses, childErrs...)
		}
	}

	switch {
	case matchCount == 0:
		return []*ValidationError{
			wrapError(instancePath, schemaPath, KeywordOneOf, "did not validate against any subschema", allCauses),
		}

	case matchCount > 1:
		return []*ValidationError{
			leafError(instancePath, schemaPath, KeywordOneOf,
				fmt.Sprintf("validated against %d subschemas, expected exactly one", matchCount)),
		}

	default:
		ann.Merge(matchedAnn)

		return nil
	}
}

// evalNot checks the not keyword. Not never contributes annotations.
func evalNot(ctx evalContext) []*ValidationError {
	schema := ctx.schema
	if schema.Not == nil {
		return nil
	}

	childErrs := ctx.v.validate(schema.Not, ctx.instance, ctx.instancePath, ctx.schemaPath.kw("not"), nil)
	if len(childErrs) == 0 {
		return []*ValidationError{
			leafError(ctx.instancePath, ctx.schemaPath, KeywordNot, "should not validate against the schema"),
		}
	}

	return nil
}

// evalIfThenElse checks the if/then/else conditional keywords.
//
//nolint:nestif // Conditional branching with annotation tracking requires nesting.
func evalIfThenElse(ctx evalContext) []*ValidationError {
	schema, ann := ctx.schema, ctx.ann
	if schema.If == nil {
		return nil
	}

	v, instance := ctx.v, ctx.instance
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	ifAnn := ann.Child()
	ifErrs := v.validate(schema.If, instance, instancePath, schemaPath.kw("if"), ifAnn)
	ifPassed := len(ifErrs) == 0

	if ifPassed {
		ann.Merge(ifAnn)

		if schema.Then != nil {
			thenAnn := ann.Child()
			thenErrs := v.validate(schema.Then, instance, instancePath, schemaPath.kw("then"), thenAnn)
			if len(thenErrs) > 0 {
				errs = append(errs, wrapError(instancePath, schemaPath, KeywordThen,
					"if condition was true but then validation failed", thenErrs))
			} else {
				ann.Merge(thenAnn)
			}
		}
	} else if schema.Else != nil {
		elseAnn := ann.Child()
		elseErrs := v.validate(schema.Else, instance, instancePath, schemaPath.kw("else"), elseAnn)
		if len(elseErrs) > 0 {
			errs = append(errs, wrapError(instancePath, schemaPath, KeywordElse,
				"if condition was false but else validation failed", elseErrs))
		} else {
			ann.Merge(elseAnn)
		}
	}

	return errs
}

// evalContent applies content keywords.
//
// Per 2020-12 spec section 8.5, content keywords (contentEncoding,
// contentMediaType, contentSchema) are annotations only and never affect
// validity. ContentSchema describes the decoded content, which this package
// does not decode, so it is never asserted regardless of the other keywords.
//
// [WithContent] opts in to asserting contentEncoding and contentMediaType for
// string instances only; non-string instances carry no content and pass. The
// content vocabulary and that opt-in are both the table row's gate, so this
// reaches here only when assertion is on.
func evalContent(ctx evalContext) []*ValidationError {
	return ctx.v.assertContent(ctx.schema, ctx.instance, ctx.instancePath, ctx.schemaPath)
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

	switch kw, decodeErr := content.Assert(schema.ContentEncoding, schema.ContentMediaType, str); kw {
	case KeywordContentEncoding:
		return []*ValidationError{leafError(
			instancePath, schemaPath, KeywordContentEncoding,
			fmt.Sprintf("string is not valid base64: %v", decodeErr),
		)}

	case KeywordContentMediaType:
		return []*ValidationError{leafError(
			instancePath, schemaPath, KeywordContentMediaType,
			"string is not a valid application/json document",
		)}
	}

	return nil
}

// evalRef resolves and validates a $ref. Under Draft-07 a $ref suppresses its
// siblings; the dispatch loop enforces that (via the row's isRef marker), so
// this eval carries no draft branch.
func evalRef(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	ref := schema.Ref
	if ref == "" {
		return nil
	}

	return ctx.v.validateResolvedRef(
		ctx.v.resolveRef(schema, ref), ref, KeywordRef,
		ctx.instance, ctx.instancePath, ctx.schemaPath, ctx.ann)
}

// evalDynamicRef resolves and validates a $dynamicRef. Its table row carries the
// 2020-12-and-up draft range, so the 2020-12 draft gate lives on the row rather
// than in this eval.
func evalDynamicRef(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	ref := schema.DynamicRef
	if ref == "" {
		return nil
	}

	return ctx.v.validateResolvedRef(
		ctx.v.resolveDynamicRef(schema, ref), ref, KeywordDynamicRef,
		ctx.instance, ctx.instancePath, ctx.schemaPath, ctx.ann)
}

// validateResolvedRef validates the instance against a resolved reference
// target, sharing the resolution-error and annotation handling between $ref
// and $dynamicRef. The keyword names the reference keyword for error paths.
// The [refresolve.Result] carries the target and, on a resolver-reported
// failure, the error to surface.
func (v *validator) validateResolvedRef(
	res refresolve.Result,
	ref, keyword string,
	instance any,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations.Set,
) []*ValidationError {
	if res.Target == nil {
		if res.Err != nil {
			// Built through leafError to share the path pairing; the private err
			// field (the unwrappable resolution cause) is set on the result. The
			// error is carried by value in the Result, so it surfaces once per
			// failing node without a shared side channel to clear.
			e := leafError(instancePath, schemaPath, keyword, res.Err.Error())
			e.err = res.Err

			return []*ValidationError{e}
		}

		// A non-local (remote/absolute) ref that cannot be resolved is an
		// error rather than silently passing. Unresolvable local fragment refs
		// are already rejected by Schema.Resolve before the walk begins.
		if !uriref.IsFragmentOnly(ref) {
			return []*ValidationError{
				leafError(instancePath, schemaPath, keyword, fmt.Sprintf("cannot resolve %s %q", keyword, ref)),
			}
		}

		// Unresolvable local fragment ref: silently skip.
		return nil
	}

	refAnn := ann.Child()
	childErrs := v.validate(res.Target, instance, instancePath, schemaPath.kw(keyword), refAnn)
	if len(childErrs) > 0 {
		return []*ValidationError{
			wrapError(instancePath, schemaPath, keyword, "", childErrs),
		}
	}

	ann.Merge(refAnn)

	return nil
}

// resolveRef resolves a $ref string against the shared resolution core, keyed to
// this run's session. The [refresolve.Result] carries the target and, on a
// resolver-reported failure, the error to surface.
func (v *validator) resolveRef(schema *Schema, ref string) refresolve.Result {
	return v.refSession.ResolveRef(schema, ref, v.refFetch)
}

// resolveDynamicRef resolves a $dynamicRef string against the shared resolution
// core, applying the static-then-dynamic-scope walk.
func (v *validator) resolveDynamicRef(schema *Schema, ref string) refresolve.Result {
	return v.refSession.ResolveDynamicRef(schema, ref, v.refFetch)
}
