package jsonschema

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/annotations"
	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/format"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
	"go.jacobcolvin.com/x/jsonschema/internal/regexcache"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
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
	instancePath jsontext.Pointer
}

// instanceLocation is the position in the instance that the validation walk is
// currently at, carried in two synchronized representations: the RFC 6901
// JSON Pointer string surfaced as [ValidationError.InstancePath], and the typed
// segments surfaced as [ValidationError.InstanceSegments]. The zero value is
// the root location (empty pointer, nil segments).
type instanceLocation struct {
	// The RFC 6901-encoded JSON Pointer.
	ptr jsontext.Pointer
	// One typed [Segment] per reference token of ptr.
	segs []Segment
}

// key returns the location of the object member named name, extending both
// representations. The full slice expression caps segs so sibling descents
// append into fresh backing arrays instead of aliasing a shared one.
func (l instanceLocation) key(name string) instanceLocation {
	return instanceLocation{
		ptr:  jsonptr.AppendToken(l.ptr, name),
		segs: append(slices.Clip(l.segs), Segment{Key: name}),
	}
}

// index returns the location of the array element at index i, extending both
// representations. The full slice expression caps segs so sibling descents
// append into fresh backing arrays instead of aliasing a shared one.
func (l instanceLocation) index(i int) instanceLocation {
	return instanceLocation{
		ptr:  jsonptr.AppendToken(l.ptr, strconv.Itoa(i)),
		segs: append(slices.Clip(l.segs), Segment{Index: i, IsIndex: true}),
	}
}

// schemaLocation is the position in the schema that the validation walk is
// currently at, the schema-side counterpart of [instanceLocation]: the RFC
// 6901 JSON Pointer surfaced as [ValidationError.SchemaPath], and the typed
// segments surfaced as [ValidationError.SchemaSegments]. The zero value is
// the root location (empty pointer, nil segments).
type schemaLocation struct {
	// The RFC 6901-encoded JSON Pointer.
	ptr jsontext.Pointer
	// One typed [Segment] per reference token of ptr.
	segs []Segment
}

// kw returns the location of the keyword token named keyword, extending both
// representations. The full slice expression caps segs so sibling descents
// append into fresh backing arrays instead of aliasing a shared one.
func (l schemaLocation) kw(keyword string) schemaLocation {
	return schemaLocation{
		ptr:  jsonptr.AppendToken(l.ptr, keyword),
		segs: append(slices.Clip(l.segs), Segment{Key: keyword}),
	}
}

// key returns the location of the member named name under a map keyword
// (properties, patternProperties, dependentSchemas, ...), extending both
// representations with the aliasing discipline of [schemaLocation.kw].
func (l schemaLocation) key(name string) schemaLocation {
	return schemaLocation{
		ptr:  jsonptr.AppendToken(l.ptr, name),
		segs: append(slices.Clip(l.segs), Segment{Key: name}),
	}
}

// idx returns the location of the element at index i under a list keyword
// (allOf, anyOf, oneOf, prefixItems, ...), extending both representations
// with the aliasing discipline of [schemaLocation.kw].
func (l schemaLocation) idx(i int) schemaLocation {
	return schemaLocation{
		ptr:  jsonptr.AppendToken(l.ptr, strconv.Itoa(i)),
		segs: append(slices.Clip(l.segs), Segment{Index: i, IsIndex: true}),
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
	// carries a compile-time session for the compile reference walk;
	// forInstance and the inliner each derive their own.
	refSession *refresolve.Session

	// The remote-fetch strategy passed to refSession resolution. On the compiled
	// proto it writes the shared refReg directly (so compile-time fetches persist
	// into the compiled registry); on a per-run session it clones the registry
	// copy-on-write before its first write.
	refFetch refresolve.Fetch

	// The rootDoc is the frozen, vetted root document; root is its Root().
	rootDoc schemavet.Doc

	// The index is the node-identity index over the root document: each
	// schema reachable through sub-schema keywords has a dense id, and the
	// per-node caches below are slices indexed by that id. Built during Compile
	// (extended as remote documents are fetched) and read-only afterward;
	// forInstance shares it by reference.
	index *schemaIndex

	// The numericBounds, patternCache, and the caches below are compile-time
	// slices of derived per-node state, indexed by node id. They are populated
	// once during Compile by precompute, which runs single-threaded, and are
	// read-only afterward; forInstance shares them by reference across runs, so
	// concurrent Validate calls only read them. A nil element means the node sets
	// no keyword the cache covers. A schema reached only at validation time (a
	// remote or JSON-pointer fallback schema) is outside the index, so its cache
	// lookup misses and the validation path computes the value directly.
	numericBounds []*precomputedBounds // numeric bound keywords as rationals, by node id

	root               *Schema
	formatsForce       *bool           // explicit WithFormats override; nil if unset
	vocabOverride      map[string]bool // from WithVocabularies
	formatCheckers     map[string]FormatValidator
	metaSchemaResolver RefResolver // metaschema lookup by $schema URI (WithMetaSchemaResolver)
	visiting           map[visitKey]bool
	patternCache       []*compiledPattern           // schema.Pattern compiled (see numericBounds)
	patternProps       []map[string]compiledPattern // patternProperties keys compiled (see numericBounds)
	constVals          []*jsonvalue.Value           // document view of the const value (see documentConst)
	enumVals           [][]jsonvalue.Value          // document view of the enum members (see documentEnum)
	sortedPropertyKeys [][]string                   // schema.Properties keys, sorted (see numericBounds)
	sortedPatternKeys  [][]string                   // schema.PatternProperties keys, sorted (see numericBounds)
	itemsPlans         []*itemsPlan                 // normalized array item keywords (see numericBounds)
	depKeys            []*dependencyKeys            // dependency trigger keys, sorted (see numericBounds)

	// The lateEnums and lateConsts memoize, for one validation run, the
	// document views of schemas outside the index (JSON-pointer fallback
	// targets and documents first fetched during the run), keyed by schema
	// pointer. Such a schema is a fresh object each run but stable within it,
	// so the memo lives on the per-run copy, never on the shared proto,
	// and forInstance clears both, while constView and enumView allocate
	// each map lazily, so a run that reaches no out-of-index schema pays
	// nothing.
	lateEnums  map[*Schema][]jsonvalue.Value
	lateConsts map[*Schema]jsonvalue.Value

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

	draft   Draft
	profile draftProfile // per-draft behavioral policy, resolved once from draft
	vocabs  vocab.Set    // resolved active vocabularies

	formatsEnabled bool
	// Whether format assertion was activated by the 2020-12 format-assertion
	// vocabulary rather than the WithFormats opt-in or Draft-07's default. In
	// this mode the spec (validation section 7.2.3) mandates failure on
	// unknown formats, so evalFormat rejects a format name with no registered
	// checker instead of treating it as annotation-only.
	formatsVocabDriven bool
	contentEnabled     bool // assert contentEncoding/contentMediaType (WithContent)

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
		// metaschema lookup below, and the remote fetches the compile-time
		// reference walk makes after construction. Compile drops it before
		// the validator is cached.
		ctx: ctx,
	}
	// Register built-in format checkers.
	for name, fn := range format.Validators() {
		v.formatCheckers[name] = builtinFormat(fn)
	}

	for _, opt := range opts {
		opt.applyValidate(v)
	}

	// A configured base URI must parse before anything absolutizes against it.
	// ParseBaseURI normalizes it once here, so buildRefReg and the identifier
	// checks read the same canonical form. NewInliner calls the same helper, so
	// the two entry points accept the same bases.
	base, err := uriref.ParseBaseURI(v.baseURI)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBaseURI, err)
	}

	v.baseURI = base

	// Detect draft from $schema field; a WithDraft override wins.
	draft, err := resolveDraft(schema, v.draftOverride)
	if err != nil {
		return nil, err
	}

	v.draft = draft
	v.profile = v.draft.profile()

	// Resolve active vocabularies. The metaschema resolver reads the compile
	// context from the ctx field set above, not a threaded parameter.
	//nolint:contextcheck // See the comment above.
	err = v.resolveVocabularies()
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

	// Freeze the caller's document into a private tree and vet it up front:
	// field structure, identifiers, type names, bound domains, and (under
	// 2020-12) the items array form, so a malformed document fails compilation
	// instead of silently mis-validating. Every walk from here on reads the
	// frozen copy, so the caller's value is never held and never read again,
	// and a node reached through two paths in the caller's value is two nodes
	// here.
	frozen, err := schemavet.Freeze(schema, "the root document", v.baseURI, v.draft.vetProfile())
	if err != nil {
		//nolint:wrapcheck // The freeze error already names the document and path.
		return nil, err
	}

	rootDoc, err := frozen.Vet("")
	if err != nil {
		//nolint:wrapcheck // The vetting error already names the document and path.
		return nil, err
	}

	v.root = rootDoc.Root()
	v.rootDoc = rootDoc

	// The compile session's fetch reads the compile context from the ctx field
	// set above, not a threaded parameter.
	//nolint:contextcheck // See the comment above.
	v.buildRefReg()

	// The dynamic scope is seeded per run by forInstance, the single source for
	// the rule; the compiled validator's compile-time session (used only by the
	// compile reference walk, which resolves $dynamicRef statically) needs no
	// scope.

	return v, nil
}

// refDeps returns the dependency-injection boundary the resolution core needs:
// the decoded-document materializer the JSON-pointer fallback builds its
// targets through. [ParseSchemaValue] serves as the materializer so a fallback
// target's const and enum numbers stay exact [jsonv1.Number] literals, like
// every other path a schema document takes into the engines.
func refDeps() refresolve.Deps {
	return refresolve.Deps{Materialize: ParseSchemaValue}
}

// buildRefReg builds the compiled ref-resolution registry over the frozen
// root document (frozen against the [WithBaseURI] base, normalized by
// newValidator) and the compile-time session the compile reference walk
// resolves through. The walk's fetches write the shared refReg directly (via
// a copy-on-write-disabled fetch) so remote documents fetched while compiling
// persist into the registry every run shares.
func (v *validator) buildRefReg() {
	v.refReg = refresolve.NewRegistry(refDeps(), v.inertIDs)
	v.refReg.Build(v.rootDoc)

	// The compile-time session freezes and vets each JSON-pointer fallback
	// target at materialization, so the walk meets a malformed target at that
	// point rather than in a later pass. The inliner's session vets at the
	// same point, which is what makes the two engines report whichever fault
	// the closure walk reaches first. The vet does not wrap, so Compile
	// reports the bare vetting sentinel and refWalkError supplies the
	// reference framing.
	v.refSession = v.refReg.NewSession(newFallbackVet(v.draft.vetProfile(), false))
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
	rv.lateEnums = nil
	rv.lateConsts = nil

	// A JSON-pointer fallback target materialized during this run is a fresh
	// object no compile-time pass saw (a run re-materializes every target, and
	// a target inside a late-fetched document has no compile-time counterpart
	// at all), so this session vets at materialization the way the
	// compile-time one does. A violation surfaces through the referencing ref
	// as an error wrapping [ErrRefResolve], matching the
	// late-fetched-document vet.
	rv.refSession = v.refReg.NewSession(newFallbackVet(rv.draft.vetProfile(), true))
	if rv.profile.dynamicRef {
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
	if !v.profile.vocabularies {
		v.vocabs = vocab.All()

		return nil
	}

	rawVocabs := v.vocabOverride
	fromOverride := rawVocabs != nil

	if rawVocabs == nil && v.metaSchemaResolver != nil && v.root.Schema != "" {
		// The lookup goes through runContext, not the raw ctx field: hook
		// invocations always receive a normalized context, matching the
		// remote-fetch and loader call sites.
		ms, err := v.metaSchemaResolver.ResolveRef(v.runContext(), v.root.Schema)
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
	case v.profile.formatAssertsByDefault:
		v.formatsEnabled = true
	default:
		v.formatsEnabled = v.vocabs.FormatAssertion
		// Only vocabulary-driven assertion is spec-bound to fail on unknown
		// formats (2020-12 validation section 7.2.3); the WithFormats opt-in
		// and Draft-07's default assertion keep unknown names annotation-only.
		v.formatsVocabDriven = v.formatsEnabled
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

// precompute populates the read-only per-node caches (numeric bounds and
// compiled patterns) indexed by node id. It sizes the cache slices to the
// index and runs each active keyword row's compile step over every node,
// single-threaded during Compile before the [Validator] is shared, so the caches
// are never written concurrently. The node index already deduped every distinct
// pointer, so precompute needs no visited set of its own. It does not touch the
// URI, anchor, or base-URI registries, which keeps the validation-time fallback
// walk (the session's RegisterFallback) from populating these caches.
func (v *validator) precompute() {
	v.sizeCaches(v.index.len())
	v.precomputeRange(0, v.index.len())
}

// sizeCaches grows every per-node cache slice to length n (allocating on the
// first call, extending with nil elements when Compile later folds a fetched
// remote's nodes into the index). A nil element means the node sets no
// keyword the cache covers.
func (v *validator) sizeCaches(n int) {
	v.numericBounds = growSlice(v.numericBounds, n)
	v.patternCache = growSlice(v.patternCache, n)
	v.patternProps = growSlice(v.patternProps, n)
	v.constVals = growSlice(v.constVals, n)
	v.enumVals = growSlice(v.enumVals, n)
	v.sortedPropertyKeys = growSlice(v.sortedPropertyKeys, n)
	v.sortedPatternKeys = growSlice(v.sortedPatternKeys, n)
	v.itemsPlans = growSlice(v.itemsPlans, n)
	v.depKeys = growSlice(v.depKeys, n)
}

// growSlice returns s extended to length n with zero-value elements, or s
// unchanged when it is already at least that long.
func growSlice[T any](s []T, n int) []T {
	if n <= len(s) {
		return s
	}

	return append(s, make([]T, n-len(s))...)
}

// precomputeRange records the derived caches for the indexed nodes in id range
// [from, to). It is the Compile-time counterpart of the dispatch loop: it runs
// each active keyword row's compile step (nil for rows that precompute nothing)
// under each node's id, so the per-node caches a row's eval consults are
// populated by the same row that reads them. A row the run's draft, vocabulary,
// or opt-in gates disabled (buildActiveRows runs earlier in newValidator) never
// evaluates, so its caches are never read and are not built. The range form lets
// Compile precompute only a fetched remote's freshly indexed nodes.
func (v *validator) precomputeRange(from, to int) {
	for id := from; id < to; id++ {
		schema := v.index.schemas[id]
		for _, e := range v.activeRows {
			if e.compile != nil {
				e.compile(v, id, schema)
			}
		}
	}
}

// numericCompile caches a schema's numeric bound keywords as rationals for the
// numeric row's eval, under the schema's node id.
func numericCompile(v *validator, id int, s *Schema) {
	if b := computeBounds(s); b != nil {
		v.numericBounds[id] = b
	}
}

// enumCompile caches the document view of a schema's enum members for the
// enum row's eval, under the schema's node id.
func enumCompile(v *validator, id int, s *Schema) {
	if s.Enum == nil {
		return
	}

	v.enumVals[id] = documentEnum(s.Enum)
}

// constCompile caches the document view of a schema's const value for the
// const row's eval, under the schema's node id.
func constCompile(v *validator, id int, s *Schema) {
	if s.Const == nil {
		return
	}

	val := documentConst(*s.Const)
	v.constVals[id] = &val
}

// documentConst returns the value a const compares as: the JSON value the
// emitted document renders for it ([jsonvalue.FromDocument]), so a hand-built
// typed nil container, struct, or raw [jsontext.Value] validates the way its
// document does. A value the document view refuses (a func, a cyclic value)
// is the Invalid value, which the comparison treats as unequal to
// everything. The view shares nothing with the schema.
func documentConst(val any) jsonvalue.Value {
	dv, _ := jsonvalue.FromDocument(val)

	return dv
}

// documentEnum returns a fresh slice holding [documentConst] of each enum
// member.
func documentEnum(members []any) []jsonvalue.Value {
	out := make([]jsonvalue.Value, len(members))
	for i, member := range members {
		out[i] = documentConst(member)
	}

	return out
}

// stringCompile caches a schema's compiled Pattern for the string row's eval,
// under the schema's node id.
func stringCompile(v *validator, id int, s *Schema) {
	if s.Pattern != "" {
		re, err := regexcache.Compile(s.Pattern)
		v.patternCache[id] = &compiledPattern{re: re, err: err}
	}
}

// objectApplicatorsCompile caches a schema's sorted property keys, compiled
// patternProperties, and sorted pattern keys for the object.applicators row's
// eval, under the schema's node id. The key sets are fixed per schema, so
// the sorts and compiles happen once at Compile time instead of on every object
// instance node.
func objectApplicatorsCompile(v *validator, id int, s *Schema) {
	if len(s.Properties) > 0 {
		v.sortedPropertyKeys[id] = slices.Sorted(maps.Keys(s.Properties))
	}

	if len(s.PatternProperties) > 0 {
		compiled := make(map[string]compiledPattern, len(s.PatternProperties))
		for pattern := range s.PatternProperties {
			re, err := regexcache.Compile(pattern)
			compiled[pattern] = compiledPattern{re: re, err: err}
		}

		v.patternProps[id] = compiled
		v.sortedPatternKeys[id] = slices.Sorted(maps.Keys(s.PatternProperties))
	}
}

// dependencyKeys carries a node's dependency trigger keys in sorted order,
// one slice per keyword form. The key sets are fixed per schema, so the sorts
// happen once at Compile time instead of on every object instance evaluation,
// the same hoist [objectApplicatorsCompile] gives properties and
// patternProperties. A nil slice means the node sets no keys for that form.
type dependencyKeys struct {
	dependentSchemas  []string
	dependentRequired []string
	legacySchemas     []string
	legacyStrings     []string
}

// depKeysAt returns the node's dependencyKeys cache entry, allocating it on
// first use so the three dependency rows' compile steps share one entry.
func (v *validator) depKeysAt(id int) *dependencyKeys {
	if v.depKeys[id] == nil {
		v.depKeys[id] = &dependencyKeys{}
	}

	return v.depKeys[id]
}

// dependentSchemasCompile caches a schema's sorted dependentSchemas trigger
// keys for the dependentSchemas row's eval, under the schema's node id.
func dependentSchemasCompile(v *validator, id int, s *Schema) {
	if len(s.DependentSchemas) > 0 {
		v.depKeysAt(id).dependentSchemas = slices.Sorted(maps.Keys(s.DependentSchemas))
	}
}

// dependentRequiredCompile caches a schema's sorted dependentRequired trigger
// keys for the dependentRequired row's eval, under the schema's node id.
func dependentRequiredCompile(v *validator, id int, s *Schema) {
	if len(s.DependentRequired) > 0 {
		v.depKeysAt(id).dependentRequired = slices.Sorted(maps.Keys(s.DependentRequired))
	}
}

// legacyDependenciesCompile caches a schema's sorted legacy dependencies
// trigger keys, both the schema-valued and the string-array forms, for the
// dependencies.legacy row's eval, under the schema's node id.
func legacyDependenciesCompile(v *validator, id int, s *Schema) {
	if len(s.DependencySchemas) > 0 {
		v.depKeysAt(id).legacySchemas = slices.Sorted(maps.Keys(s.DependencySchemas))
	}

	if len(s.DependencyStrings) > 0 {
		v.depKeysAt(id).legacyStrings = slices.Sorted(maps.Keys(s.DependencyStrings))
	}
}

// dependencyKeysFor returns deps' keys in sorted order, preferring the cached
// slice pick selects from the node's [dependencyKeys] entry and sorting on
// the fly for a schema outside the index (a remote or JSON-pointer fallback
// schema reached only at validation time).
func dependencyKeysFor[T any](
	v *validator, id int, deps map[string]T, pick func(*dependencyKeys) []string,
) []string {
	if v.inIndex(id) {
		if dk := v.depKeys[id]; dk != nil {
			if keys := pick(dk); keys != nil {
				return keys
			}
		}
	}

	return slices.Sorted(maps.Keys(deps))
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

// fetchAndFreeze is the shared fetch skeleton the validator's
// [validator.remoteFetch] and the inliner's [inliner.fetchDoc] both run: it
// consults the session's per-run negative cache, calls the resolver through
// [callResolver], freezes the resolved document into a private tree so the
// resolver-owned schema is never held and every cache reads an independent
// copy, settles an identifier collision against the session's registry, and
// vets the document. Every failure path records the outcome in the negative
// cache, upholding the at-most-once-per-baseURI-in-a-run contract even when
// many nodes reference the same URI.
//
// The order is load-bearing. The collision settles ahead of the vet, so a
// document carrying both faults fails with one sentinel wherever it is read;
// a caller that vetted first would report the structural cause instead. The
// two result shapes the callers distinguish are: missed true, a plain
// not-resolved answer (a resolver miss or a replayed plain miss) that each
// caller shapes into its own miss behavior; and a non-nil err, already
// wrapped with [ErrRefResolve] (a resolver-reported failure, a replayed
// recorded error, a cyclic document, a collision, or a vetting violation). On
// success the minted document is returned with missed false and a nil error,
// and the caller registers it.
func fetchAndFreeze(
	ctx context.Context, resolver RefResolver, sess *refresolve.Session,
	baseURI string, profile schemavet.Profile,
) (schemavet.Doc, bool, error) {
	if recorded, seen := sess.RemoteMiss(baseURI); seen {
		if recorded != nil {
			return schemavet.Doc{}, false, fmt.Errorf("%w: %w", ErrRefResolve, recorded)
		}

		return schemavet.Doc{}, true, nil
	}

	schema, ok, err := callResolver(ctx, resolver, baseURI)
	if err != nil {
		sess.RecordRemoteMiss(baseURI, err)

		return schemavet.Doc{}, false, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	if !ok {
		sess.RecordRemoteMiss(baseURI, nil)

		return schemavet.Doc{}, true, nil
	}

	frozen, err := schemavet.Freeze(schema, fmt.Sprintf("document %q", baseURI), baseURI, profile)
	if err != nil {
		return schemavet.Doc{}, false, refuseFetched(sess, baseURI, err)
	}

	err = sess.CheckFetched(frozen)
	if err != nil {
		return schemavet.Doc{}, false, refuseFetched(sess, baseURI, err)
	}

	doc, err := frozen.Vet(baseURI + "#")
	if err != nil {
		return schemavet.Doc{}, false, refuseFetched(sess, baseURI, err)
	}

	return doc, false, nil
}

// refuseFetched records the refusal of a document the resolver served under
// baseURI and returns the error the fetch reports for it. The cause rides
// inside a [refresolve.RefusedError], which the resolution reads as a settled
// answer rather than a miss, so the reference-closure walk fails on it in
// every pass where a plain miss would be deferred; the negative cache holds
// the same marked error, so a replay settles the same way. The whole is
// wrapped in [ErrRefResolve], as every fetch failure is.
func refuseFetched(sess *refresolve.Session, baseURI string, cause error) error {
	refused := &refresolve.RefusedError{Err: cause}
	sess.RecordRemoteMiss(baseURI, refused)

	return fmt.Errorf("%w: %w", ErrRefResolve, refused)
}

// remoteFetch returns the [refresolve.Fetch] the resolution core calls when a
// non-fragment ref's document is not yet registered. It fetches the document
// through the configured [RefResolver], freezes and vets it, and registers the
// frozen tree under baseURI along with its nested $id/$anchor entries. A
// nested identifier another document already holds fails the registration
// with [ErrIDCollision]; a duplicate within the document itself resolves to
// the first entry the walk reaches.
//
// A per-run session (cow true) clones the compiled registry copy-on-write before
// its first write via [refresolve.Session.EnsureOwned], so the registrations
// live only for that run and never race a concurrent run. The compile-time
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

		doc, missed, err := fetchAndFreeze(v.runContext(), v.refResolver, sess, baseURI, v.draft.vetProfile())
		if err != nil {
			return nil, err
		}

		if missed {
			return nil, nil //nolint:nilnil // A resolver miss is not an error for the validator.
		}

		if cow {
			// Clone the registry into this run's own copy before the first
			// remote registration so the write below cannot race a concurrent
			// run still sharing the compiled registry.
			sess.EnsureOwned()
		}

		// The collision settled at the fetch, so this call only merges; the
		// check is repeated because the registration is the one path both
		// engines take for a fetched document.
		err = sess.RegisterFetched(doc)
		if err != nil {
			return nil, refuseFetched(sess, baseURI, err)
		}

		return doc.Root(), nil
	}
}

// newFallbackVet returns the [refresolve.FallbackVet] a session applies to
// each JSON-pointer fallback target it materializes. A target carved out of
// raw JSON in an unknown keyword never passed through a document-level vet,
// so it is frozen and checked at materialization, where the compile-time
// session, the per-run sessions, and the inliner all run it.
//
// The per-run vet wraps a violation in [ErrRefResolve], so it surfaces
// through the referencing ref exactly like a malformed-document violation.
// The compile-time vet passes wrap false, since refWalkError frames the bare
// sentinel under the failing reference.
func newFallbackVet(profile schemavet.Profile, wrap bool) refresolve.FallbackVet {
	return func(sc *Schema, base, locator string) (schemavet.Node, error) {
		node, err := schemavet.FreezeNode(sc, locator, base, profile)
		if err != nil {
			if wrap {
				return schemavet.Node{}, fmt.Errorf("%w: %w", ErrRefResolve, err)
			}

			//nolint:wrapcheck // The vetting error already names the target and path.
			return schemavet.Node{}, err
		}

		return node, nil
	}
}

// cloneSchema deep-copies a [Schema] structurally, field by field, through
// [schemaclone.Clone]. The copy reproduces the source's pointer graph, so an
// aliased node stays one node and a cyclic document copies as a cycle, and no
// graph shape makes the copy fail. The inliner clones its working copies and
// its memoized expansions through it, all of which are trees already.
func cloneSchema(s *Schema) *Schema {
	return schemaclone.Clone(s)
}

// resolveDraft returns the draft a validation or inlining run operates under: a
// [WithDraft] override when one was given, otherwise the draft detected from the
// root schema's $schema field. It is the single detect-then-override site the
// validator and inliner share. Returning the override without reading $schema is
// behavior-preserving because [detectDraft] is a pure read with no side effect.
func resolveDraft(s *Schema, override *Draft) (Draft, error) {
	if override != nil {
		return *override, nil
	}

	return detectDraft(s)
}

// detectDraft determines the draft from the root schema's $schema field. A
// declared official dialect this package does not implement is an error
// rather than a guess (see [ErrUnsupportedDraft]); any other unrecognized URI
// is a custom metaschema and keeps the [Draft2020] default.
func detectDraft(s *Schema) (Draft, error) {
	switch s.Schema {
	case Draft7.schemaURI(),
		"http://json-schema.org/draft-07/schema",
		"https://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft-07/schema":
		return Draft7, nil
	case Draft2020.schemaURI(),
		"https://json-schema.org/draft/2020-12/schema#":
		return Draft2020, nil
	}

	if unsupportedDialect(s.Schema) {
		return Draft2020, fmt.Errorf("%w: %q", ErrUnsupportedDraft, s.Schema)
	}

	return Draft2020, nil
}

// unsupportedDialect reports whether uri names an official json-schema.org
// dialect this package does not implement, in any of its published spellings
// (http or https scheme, with or without the trailing empty fragment).
func unsupportedDialect(uri string) bool {
	uri = strings.TrimSuffix(uri, "#")
	uri = strings.TrimPrefix(uri, "http://")
	uri = strings.TrimPrefix(uri, "https://")

	switch uri {
	case "json-schema.org/draft/2019-09/schema",
		"json-schema.org/draft-06/schema",
		"json-schema.org/draft-04/schema",
		"json-schema.org/draft-03/schema":
		return true
	default:
		return false
	}
}

// Validator is a schema compiled for repeated validation. Constructing it does
// the per-schema work once, so each subsequent validation only walks the
// instance. That work is walking the schema to build the URI/anchor
// registries, running the compile-time structure, identifier, and reference
// checks, and detecting the draft and active vocabularies.
//
// A Validator is safe for concurrent use by multiple goroutines.
// [Validator.Schema] and [Validator.Draft] expose what it validates, so a
// compiled validator can be passed across package boundaries without the
// schema riding alongside it.
type Validator struct {
	proto *validator
}

// Schema returns the root schema the Validator validates: a private tree
// copy of the *Schema given to [Compile], so a consumer handed only the
// Validator can still inspect, marshal, or [Inline] what it validates. The
// copy shares nothing with the caller's value, and a node the caller's value
// reached through two paths is two nodes in it. The compiled caches are
// derived from it at Compile time; treat the returned schema as read-only,
// and recompile after any mutation.
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
// It returns an error when the options are invalid or the schema fails the
// compile-time structure, identifier, or reference checks.
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for refs
// resolved during compilation. It is not retained by the returned
// [Validator]: refs reached only at validation time resolve under the
// context passed to [Validator.Validate] or [Validator.ValidateJSON].
//
// MustCompile is Compile with [context.Background], panicking on error.
func Compile(ctx context.Context, schema *Schema, opts ...ValidateOption) (*Validator, error) {
	// The compile context rides on the validator's ctx field for resolver
	// calls made while compiling (the metaschema lookup, and the remote
	// fetches the compile-time reference walk triggers).
	v, err := newValidator(ctx, schema, opts)
	if err != nil {
		return nil, err
	}

	// Build the node-identity index over the frozen, vetted root document.
	// The per-node precompute caches are slices indexed by the ids it
	// assigns, and the validation walk descends the same frozen nodes, so
	// nodeID hits for every one of them.
	v.index = newSchemaIndex()
	v.index.extend(v.rootDoc)

	// Precompute derived per-node state (numeric bounds and compiled patterns)
	// into the id-indexed caches while still single-threaded, so the
	// returned Validator only reads these caches once shared across goroutines.
	v.precompute()

	// Resolve every reference reachable from the root, fetching and vetting
	// each remote document the walk uncovers. The session vets each fallback
	// target as it materializes it, so the walk needs no pass of its own.
	//nolint:contextcheck // The compile context rides on the ctx field set above.
	err = v.compileRefPasses()
	if err != nil {
		return nil, err
	}

	// Drop the compile-only state. The cached validator then holds no stale or
	// canceled context, and releases the walk's session. Each validation run
	// supplies its own through forInstance, which overwrites the session and
	// the fetch before any walk reads them. The fetch goes too, since it
	// closes over both the session and the context.
	v.ctx = nil
	v.refSession = nil
	v.refFetch = nil

	return &Validator{proto: v}, nil
}

// compileRefPasses runs the compile-time reference walk over the root and every
// document it reaches, supplying the document hook [refClosure] leaves to its
// caller. Every document the closure reaches was frozen and vetted at its
// fetch, so the hook only folds it into the node index, once per document,
// so its nodes hit the precompute caches; the root document is indexed
// already and adds nothing, since extend returns from == len().
//
// A JSON-pointer fallback target needs no hook of its own. The compile-time
// session vets each one where it materializes it, and the caches would not
// serve it either way, since a validation run re-materializes targets as fresh
// objects and pointer-keyed caches built here could never be hit.
func (v *validator) compileRefPasses() error {
	return refClosure{
		session:    v.refSession,
		fetch:      v.refFetch,
		root:       v.root,
		dynamicRef: v.profile.dynamicRef,
		strictRef:  true,
		strictDyn:  true,
		onDoc: func(doc schemavet.Doc) error {
			from := v.index.extend(doc)
			if from < v.index.len() {
				v.sizeCaches(v.index.len())
				v.precomputeRange(from, v.index.len())
			}

			return nil
		},
	}.run()
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
// Values produced by [Normalize] ([jsonv1.Number] leaves) convert correctly.
// A document string holding invalid UTF-8 (reachable only from a hand-built
// or non-JSON-sourced document) returns a wrapped encode error rather than
// being silently rewritten with U+FFFD, matching the package's RFC 7493
// treatment of the byte-decoding entry points.
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
		// upstream UnmarshalJSON. A [jsonv1.Number] leaf marshals verbatim as a
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

		restoreExactValues(&s, d)
		dropUnderflowedMultipleOf(&s, d)

		return &s, nil

	default:
		return nil, fmt.Errorf("%w: got %T", ErrInvalidSchemaDocument, doc)
	}
}

// restoreExactValues re-copies each decoded node's any-typed value members
// (const, enum, examples, and the unknown-keyword Extra map, mirroring the set
// internal/schemaclone deep-copies) from the source document. The upstream
// UnmarshalJSON decodes those any-typed members without UseNumber, so a number
// beyond float64 precision comes back rounded and the validator would compare
// instances against the rounded neighbor of what the author wrote; a sub-schema
// carried inside an unknown keyword would likewise reach the JSON-pointer
// fallback with its numbers already rounded. Each node's typed [Location]
// segments resolve its source map, and each member is re-copied via
// [jsonvalue.Exact], keeping numbers as exact [jsonv1.Number] literals
// while staying unaliased from the caller's document. Restoration is gated
// on what upstream parsed (a node whose members are unset stays unset), so
// the two trees stay shape-aligned; a member that fails the re-copy keeps
// its round-tripped value.
func restoreExactValues(s *Schema, doc map[string]any) {
	//nolint:errcheck // The walk callback never returns an error.
	_ = Walk(s, func(loc Location, node *Schema) error {
		if node.Const == nil && node.Enum == nil && node.Examples == nil && node.Extra == nil {
			return nil
		}

		src, ok := resolveDocValue(doc, loc.Segments).(map[string]any)
		if !ok {
			return nil
		}

		if node.Const != nil {
			if cp, copied := copySourceMember(src, "const"); copied {
				node.Const = &cp
			}
		}

		if node.Enum != nil {
			if cp, copied := copySourceMember(src, "enum"); copied {
				if list, isList := cp.([]any); isList {
					node.Enum = list
				}
			}
		}

		if node.Examples != nil {
			if cp, copied := copySourceMember(src, "examples"); copied {
				if list, isList := cp.([]any); isList {
					node.Examples = list
				}
			}
		}

		for key := range node.Extra {
			if cp, copied := copySourceMember(src, key); copied {
				node.Extra[key] = cp
			}
		}

		return nil
	})
}

// dropUnderflowedMultipleOf clears each multipleOf whose authored literal is
// strictly positive but decoded to float64 zero. A literal below the smallest
// positive float64 (about 4.9e-324) underflows gradually, so
// [strconv.ParseFloat] reports an exact 0 with no range error and the upstream
// decode stores it silently. The spec fixes the keyword's domain as a number
// strictly greater than zero, so the authored schema is valid and must not
// trip [Compile]'s [ErrNonPositiveMultipleOf] vet, which by then cannot tell
// the underflow from an authored zero. At float64 precision such a quantum
// constrains nothing, so the keyword is dropped, the multiplicative limit of
// the rounding the other float64-typed bound keywords already document. An
// authored zero or negative literal keeps its decoded value and stays for the
// vet to reject.
func dropUnderflowedMultipleOf(s *Schema, doc map[string]any) {
	//nolint:errcheck // The walk callback never returns an error.
	_ = Walk(s, func(loc Location, node *Schema) error {
		if node.MultipleOf == nil || *node.MultipleOf != 0 {
			return nil
		}

		src, ok := resolveDocValue(doc, loc.Segments).(map[string]any)
		if !ok {
			return nil
		}

		// Only a [jsonv1.Number] source carries the authored literal; a float64
		// source lost the sign of an underflowed value before this package saw
		// the document, so it keeps the decoded zero.
		lit, ok := src[KeywordMultipleOf].(jsonv1.Number)
		if !ok {
			return nil
		}

		if d, parsed := numrat.ParseDecNumber(lit.String()); parsed && d.Sig() != "" && !d.Neg() {
			node.MultipleOf = nil
		}

		return nil
	})
}

// resolveDocValue resolves a schema node's typed path into the decoded source
// document, returning nil on any shape mismatch. The walk's segment keys are
// schema keywords and member keys carried verbatim, matching the document's
// own JSON structure.
func resolveDocValue(doc any, segments []Segment) any {
	cur := doc
	for _, seg := range segments {
		switch val := cur.(type) {
		case map[string]any:
			if seg.IsIndex {
				return nil
			}

			cur = val[seg.Key]

		case []any:
			if !seg.IsIndex || seg.Index >= len(val) {
				return nil
			}

			cur = val[seg.Index]

		default:
			return nil
		}
	}

	return cur
}

// copySourceMember returns an exact copy of a member of the source map,
// reporting false when the member is absent or the copy fails.
func copySourceMember(src map[string]any, key string) (any, bool) {
	v, present := src[key]
	if !present {
		return nil, false
	}

	return jsonvalue.Exact(v)
}

// ParseSchema decodes data as a single JSON schema document and returns it
// as a [*Schema] without compiling it, for consumers that work with the schema
// itself, through [Inline], [Walk], or programmatic editing, rather than
// validating instances against it. It applies the same decode
// discipline as [CompileJSON] (which is equivalent to compiling its result):
// numbers decode as [jsonv1.Number] so large integer keywords survive the
// round-trip into the Schema, and trailing data after the document is rejected.
// A top-level value that is not an object or boolean returns an error wrapping
// [ErrInvalidSchemaDocument]; this includes JSON null, which unmarshaling into
// a [Schema] directly silently coerces to the false schema. Malformed JSON
// returns the wrapped decode error without the sentinel.
func ParseSchema(data []byte) (*Schema, error) {
	doc, err := jsonvalue.Decode(data)
	if err != nil {
		return nil, err //nolint:wrapcheck // Decode already wraps with "JSON decode:".
	}

	return ParseSchemaValue(doc.Interface())
}

// CompileJSON decodes data as a single JSON schema document with
// [ParseSchema] and compiles it with [Compile]. It is the schema-side
// counterpart of [Validator.ValidateJSON]: numbers decode as [jsonv1.Number], and trailing
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
	//nolint:wrapcheck // The vetting error already names the offending path.
	return schemavet.CheckTypeNames(schema)
}

// Validate validates a pre-parsed Go value against the compiled schema.
//
// Accepted instance types: map[string]any, []any, string, float64,
// [jsonv1.Number], bool, nil. The Go numeric kinds that encoding/json does not
// produce are accepted too, namely the signed and unsigned integer types and
// float32; they are normalized via [Normalize], so values decoded from YAML or
// TOML or built by hand validate directly (integers exactly, at any
// magnitude). Go structs are not accepted; passing any other type returns an
// error (marshal to JSON or use [Validator.ValidateJSON] instead). A
// self-referential instance (a map or slice that contains itself) is rejected
// rather than walked.
//
// Returns nil on success or an error that can be unwrapped to [*ValidationError]
// via [errors.AsType].
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for remote
// refs reached during this validation run, so a resolver that fetches over
// the network can honor cancellation and deadlines. The context is held only
// for the duration of the run, never by the [Validator] itself.
func (c *Validator) Validate(ctx context.Context, instance any) error {
	value, err := normalizeAndCheck(instance)
	if err != nil {
		return err
	}

	return c.validateNormalized(ctx, value)
}

// normalizeAndCheck converts instance through [jsonvalue.FromGo] and reports
// an error if its type, a nested container leaf, or a self-referential
// container is not one the validation walk accepts. The message lists the
// accepted shapes in one place so the two entry points cannot drift.
func normalizeAndCheck(instance any) (jsonvalue.Value, error) {
	value, ok := jsonvalue.FromGo(instance)
	if !ok {
		return jsonvalue.Value{}, fmt.Errorf(
			"instance of type %T is not accepted: instances must contain only map[string]any, "+
				"[]any, string, bool, nil, and numeric values, with no self-referential containers; "+
				"marshal to JSON or use Validator.ValidateJSON",
			instance,
		)
	}

	return value, nil
}

// validateNormalized validates a converted instance, returning nil on
// success or the assembled *ValidationError. Every entry point converts its
// instance exactly once, through [jsonvalue.FromGo] for a Go value or
// [jsonvalue.Decode] for JSON text, and hands the Value here.
func (c *Validator) validateNormalized(ctx context.Context, instance jsonvalue.Value) error {
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

// ValidateJSON decodes data as a JSON instance, keeping every number as its
// exact literal, and validates it against the compiled schema.
//
// The context is passed to the [RefResolver] for remote refs reached during
// this validation run (see [Validator.Validate]).
func (c *Validator) ValidateJSON(ctx context.Context, data []byte) error {
	instance, err := jsonvalue.Decode(data)
	if err != nil {
		return err //nolint:wrapcheck // Decode already wraps with "JSON decode:".
	}

	return c.validateNormalized(ctx, instance)
}

// ValidateReader applies [Validator.ValidateJSON]'s discipline to an
// [io.Reader], reading r to EOF, keeping every number as its exact literal
// while rejecting data after the single top-level value, duplicate object
// member names, and invalid UTF-8, then validating against the compiled
// schema. A read error returns wrapped, without a validation verdict, and
// does not unwrap to [*ValidationError].
//
// The context is passed to the [RefResolver] for remote refs reached during
// this validation run (see [Validator.Validate]).
func (c *Validator) ValidateReader(ctx context.Context, r io.Reader) error {
	instance, err := jsonvalue.DecodeReader(r)
	if err != nil {
		return err //nolint:wrapcheck // DecodeReader already wraps with "JSON decode:".
	}

	return c.validateNormalized(ctx, instance)
}

// ValidateValue marshals v with [encoding/json/v2] and validates its JSON
// form against the compiled schema. It accepts the Go values
// [Validator.Validate] rejects, namely structs and other types it can
// marshal, so an instance of the very type a schema was generated for
// validates in one call. What is validated is the value's marshaled form,
// exactly what a JSON consumer of the value would see: json tags, omitempty
// and omitzero, and MarshalJSON implementations all apply. A non-pointer v
// is marshaled through a pointer to a copy, so pointer-receiver MarshalJSON
// and MarshalText implementations (big.Int's, for example) apply exactly as
// they would for &v -- generation resolves marshalers through the type's
// full method set, and ValidateValue(ctx, v) and ValidateValue(ctx, &v)
// validate the same JSON form. The bytes are decoded back with the
// [Validator.ValidateJSON] discipline (numbers as [jsonv1.Number]).
//
// Returns nil on success or an error that can be unwrapped to
// [*ValidationError] via [errors.AsType]. A value [encoding/json/v2] cannot
// marshal returns the wrapped marshal error, which does not unwrap to
// [*ValidationError]; this covers channels, cyclic values, and unsupported
// floats.
//
// The context is passed to the [RefResolver] for remote refs reached during
// this validation run (see [Validator.Validate]).
func (c *Validator) ValidateValue(ctx context.Context, v any) error {
	data, err := json.Marshal(addressableInstance(v))
	if err != nil {
		return fmt.Errorf("marshal instance: %w", err)
	}

	return c.ValidateJSON(ctx, data)
}

// addressableInstance returns v, or a pointer to a copy of v when v is not
// already a pointer. A value arrives in the interface non-addressable, and
// encoding/json only uses a pointer-receiver MarshalJSON/MarshalText on an
// addressable value, so marshaling v directly would fall back to struct
// reflection for a type like [big.Int] held by value -- a shape generation
// never describes, since it resolves marshalers through the type's full
// method set. Marshaling through a pointer makes the copy (and every field
// under it) addressable, so the pointer-receiver methods apply as they do
// for &v; encoding/json dereferences the added pointer, leaving the output
// otherwise unchanged.
func addressableInstance(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() == reflect.Pointer {
		return v
	}

	p := reflect.New(rv.Type())
	p.Elem().Set(rv)

	return p.Interface()
}

// Validate validates a pre-parsed Go value against a JSON Schema. It compiles
// schema and validates instance in one call; to validate many instances against
// the same schema, call [Compile] once and reuse the returned [Validator].
//
// Accepted instance types: map[string]any, []any, string, float64,
// [jsonv1.Number], bool, nil. The Go numeric kinds that encoding/json does not
// produce are accepted too, namely the signed and unsigned integer types and
// float32; they are normalized via [Normalize]. Go structs are not accepted;
// passing any other type returns an error (marshal to JSON or use
// [Validator.ValidateJSON] instead). A self-referential instance (a map or
// slice that contains itself) is rejected rather than walked.
//
// Returns nil on success or an error that can be unwrapped to
// [*ValidationError] via [errors.AsType].
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) for refs
// resolved both while compiling schema and during the validation run.
func Validate(ctx context.Context, schema *Schema, instance any, opts ...ValidateOption) error {
	// Check the instance type before compiling so an unaccepted instance is
	// reported without the cost of (or any error from) schema preparation.
	value, err := normalizeAndCheck(instance)
	if err != nil {
		return err
	}

	c, err := Compile(ctx, schema, opts...)
	if err != nil {
		return err
	}

	return c.validateNormalized(ctx, value)
}

// validate performs the depth-first recursive walk.
func (v *validator) validate(
	schema *Schema,
	instance jsonvalue.Value,
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
	if v.profile.dynamicRef {
		if leave := v.refSession.EnterScope(v.refSession.SchemaBase(schema)); leave != nil {
			defer leave()
		}
	}

	// If this schema uses unevaluated* keywords but the caller didn't provide
	// annotations, create a local annotations object to track evaluated items.
	if ann == nil && (schema.UnevaluatedProperties != nil || schema.UnevaluatedItems != nil) {
		ann = annotations.New()
	}

	// Resolve the schema's node id once for this node so the per-keyword
	// eval steps index their caches by it. A schema outside the index (a
	// remote or JSON-pointer fallback target reached only at validation time)
	// takes id -1, and the cache accessors recompute for it.
	nodeID := -1
	if id, ok := v.index.nodeID(schema); ok {
		nodeID = id
	}

	// Drive keyword evaluation from the run's active dispatch rows (filtered once
	// at Compile by buildActiveRows), evaluated in table order -- the
	// deterministic error-slice order the tests pin.
	ctx := evalContext{
		v: v, schema: schema, nodeID: nodeID, instance: instance,
		instancePath: instancePath, schemaPath: schemaPath, ann: ann,
	}

	// Draft-07: a $ref with siblings ignores the siblings, so only the $ref row
	// runs. Under Draft 2020-12 a $ref evaluates alongside its siblings, and a
	// schema with no $ref runs every active row regardless of draft.
	onlyRef := !v.profile.honorRefSiblings && schema.Ref != ""

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

	if ctx.instance.Kind() != jsonvalue.Object || ann.AllPropertiesSet() {
		return nil
	}

	obj := ctx.instance.Members()

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

	childSchemaPath := ctx.schemaPath.kw(KeywordUnevaluatedProperties)

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

	if ctx.instance.Kind() != jsonvalue.Array || ann.AllItemsSet() {
		return nil
	}

	arr := ctx.instance.Elements()

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

	childSchemaPath := ctx.schemaPath.kw(KeywordUnevaluatedItems)

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

	if slices.ContainsFunc(types, ctx.instance.MatchesType) {
		return nil
	}

	got := ctx.instance.TypeName()

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

	for _, allowed := range ctx.v.enumView(ctx.nodeID, schema) {
		if allowed.Equal(ctx.instance) {
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

	if ctx.v.constView(ctx.nodeID, schema).Equal(ctx.instance) {
		return nil
	}

	return []*ValidationError{
		leafError(ctx.instancePath, ctx.schemaPath, KeywordConst, "value does not match const"),
	}
}

// inIndex reports whether id addresses an indexed node whose cache slot may hold a
// precomputed value: a non-negative id within the sized slices. A negative id
// marks a schema outside the index (a fallback target reached only at
// validation time), whose caches were never built; constView and enumView
// memoize such a schema's views per run instead. The bound is index.len()
// because sizeCaches keeps every cache slice invariantly that long, so a
// non-negative in-index id never over-indexes a shorter slice.
func (v *validator) inIndex(id int) bool {
	return id >= 0 && id < v.index.len()
}

// constView returns the document view of schema's const value, preferring
// the per-node cache. For a schema outside the index (a remote or
// JSON-pointer fallback schema reached only at validation time) it converts
// on first sight and memoizes the result in the run's lateConsts, so an
// instance with many nodes against one such schema converts once. The caller
// checks that the schema sets const.
func (v *validator) constView(id int, schema *Schema) jsonvalue.Value {
	if v.inIndex(id) && v.constVals[id] != nil {
		return *v.constVals[id]
	}

	if view, ok := v.lateConsts[schema]; ok {
		return view
	}

	val := documentConst(*schema.Const)

	if v.lateConsts == nil {
		v.lateConsts = map[*Schema]jsonvalue.Value{}
	}

	v.lateConsts[schema] = val

	return val
}

// enumView returns the document view of schema's enum members, on the same
// terms as [validator.constView] (the per-node cache first, then the run's
// lateEnums memo). The returned slice is an operand only; callers must not
// mutate it.
func (v *validator) enumView(id int, schema *Schema) []jsonvalue.Value {
	if v.inIndex(id) && v.enumVals[id] != nil {
		return v.enumVals[id]
	}

	if view, ok := v.lateEnums[schema]; ok {
		return view
	}

	members := documentEnum(schema.Enum)

	if v.lateEnums == nil {
		v.lateEnums = map[*Schema][]jsonvalue.Value{}
	}

	v.lateEnums[schema] = members

	return members
}

// boundsFor returns the numeric bound rationals for schema, preferring the
// per-node cache and converting on the fly for a schema outside the index
// (a remote or JSON-pointer fallback schema reached only at validation time).
// The returned rationals are operands only; callers must not mutate them.
func (v *validator) boundsFor(id int, schema *Schema) *precomputedBounds {
	if v.inIndex(id) && v.numericBounds[id] != nil {
		return v.numericBounds[id]
	}

	return computeBounds(schema)
}

// propertyKeysFor returns schema.Properties' keys in sorted order, preferring
// the per-node cache and sorting on the fly for a schema outside the index
// (a remote or JSON-pointer fallback schema reached only at validation time).
func (v *validator) propertyKeysFor(id int, schema *Schema) []string {
	if v.inIndex(id) && v.sortedPropertyKeys[id] != nil {
		return v.sortedPropertyKeys[id]
	}

	return slices.Sorted(maps.Keys(schema.Properties))
}

// patternKeysFor returns schema.PatternProperties' keys in sorted order, with
// the same per-node cache and on-the-fly fallback as [propertyKeysFor].
func (v *validator) patternKeysFor(id int, schema *Schema) []string {
	if v.inIndex(id) && v.sortedPatternKeys[id] != nil {
		return v.sortedPatternKeys[id]
	}

	return slices.Sorted(maps.Keys(schema.PatternProperties))
}

// patternFor returns the compiled form of schema.Pattern, preferring the per-node
// cache and compiling on the fly for a schema outside the index (a remote
// or JSON-pointer fallback schema reached only at validation time). The compile
// error, when present, is reported by the caller exactly as a fresh
// [regexcache.Compile] call would, preserving the fail-closed behavior.
func (v *validator) patternFor(id int, schema *Schema) compiledPattern {
	if v.inIndex(id) && v.patternCache[id] != nil {
		return *v.patternCache[id]
	}

	re, err := regexcache.Compile(schema.Pattern)

	return compiledPattern{re: re, err: err}
}

// patternPropertyFor returns the compiled form of one patternProperties key on
// schema, preferring the per-node cache and compiling on the fly for a schema
// outside the index.
func (v *validator) patternPropertyFor(id int, pattern string) compiledPattern {
	if v.inIndex(id) && v.patternProps[id] != nil {
		if cp, ok := v.patternProps[id][pattern]; ok {
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
	nodeID := ctx.nodeID

	if instance.Kind() != jsonvalue.Number {
		return nil
	}

	// The value carries its decomposition from the parse. An over-cap literal
	// (the DoS guard) takes the magnitude-class comparison without a second
	// scan of the literal; a value with none (an unparseable literal, or a
	// non-finite float, which yields no rational) fails every present bound
	// keyword closed; an exactly comparable one yields the rational the
	// bounded checks use.
	literal := instance.Literal()

	if !instance.Comparable() {
		desc := literal
		if !instance.FromFloat() {
			desc = fmt.Sprintf("%q", literal)
		}

		return validateNumericNonComparable(schema, desc, instancePath, schemaPath)
	}

	val, ok := instance.Rat()
	if !ok {
		d, _ := instance.Dec()

		return v.validateNumericUnbounded(nodeID, schema, d, literal, instancePath, schemaPath)
	}

	var errs []*ValidationError

	// One error per failed bound, sharing the instance path and keyword
	// schema-path location.
	add := func(keyword, msg string) {
		errs = append(errs, leafError(instancePath, schemaPath, keyword, msg))
	}

	bounds := v.boundsFor(nodeID, schema)

	if schema.MultipleOf != nil {
		switch {
		case *schema.MultipleOf <= 0:
			// MultipleOf MUST be strictly greater than 0; a non-positive
			// divisor makes the schema invalid. Compile rejects it with
			// [ErrNonPositiveMultipleOf] wherever its vetting reaches, so
			// this is a backstop for a schema outside that coverage.
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
// [jsonv1.Number] whose exact expansion is too expensive (see numrat.MaxNumberLen): a
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
	id int,
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

	bounds := v.boundsFor(id, schema)

	// A non-positive multipleOf makes the schema invalid independent of the
	// instance value. For a positive divisor, an over-cap integer's
	// divisibility is still decidable via modular arithmetic at cost linear in
	// the literal (see numrat.IntegerMultipleOf), so it is enforced. A
	// non-integral over-cap value keeps the documented skip: expanding its
	// fractional part is unbounded.
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

// validateNumericNonComparable reports every numeric bound keyword present on
// the schema as violated by an instance that carries no numeric value to
// compare: a non-finite float64 (NaN or an infinity, which JSON cannot
// represent) or a [jsonv1.Number] whose literal is not a valid JSON number. Such
// a value still passes a bare type assertion (normalize admits both as
// number-shaped leaves), but a bound written to constrain a number fails
// closed against it rather than being silently skipped: +Inf under a maximum,
// or an unparseable literal under a minimum, must not validate. A
// non-positive multipleOf keeps its schema-validity message, which is
// independent of the instance value.
func validateNumericNonComparable(
	schema *Schema,
	desc string,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	var errs []*ValidationError

	add := func(keyword, msg string) {
		errs = append(errs, leafError(instancePath, schemaPath, keyword, msg))
	}

	noValue := func(keyword string) {
		add(keyword, fmt.Sprintf("%s has no numeric value to compare with %s", desc, keyword))
	}

	if schema.MultipleOf != nil {
		if *schema.MultipleOf <= 0 {
			add(KeywordMultipleOf, fmt.Sprintf("multipleOf must be greater than 0, got %v", *schema.MultipleOf))
		} else {
			noValue(KeywordMultipleOf)
		}
	}

	if schema.Minimum != nil {
		noValue(KeywordMinimum)
	}

	if schema.Maximum != nil {
		noValue(KeywordMaximum)
	}

	if schema.ExclusiveMinimum != nil {
		noValue(KeywordExclusiveMinimum)
	}

	if schema.ExclusiveMaximum != nil {
		noValue(KeywordExclusiveMaximum)
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

	if ctx.instance.Kind() != jsonvalue.String {
		return nil
	}

	str := ctx.instance.Str()

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
		cp := ctx.v.patternFor(ctx.nodeID, schema)
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

	if ctx.instance.Kind() != jsonvalue.String || schema.Format == "" {
		return nil
	}

	str := ctx.instance.Str()

	fv, exists := ctx.v.formatCheckers[schema.Format]
	if !exists {
		// When the format-assertion vocabulary drives assertion, 2020-12
		// validation section 7.2.3 mandates failure on unknown formats, so a
		// name with no registered checker rejects the instance. Assertion via
		// WithFormats(true) or Draft-07's default stays lenient: those are the
		// package's own opt-in contracts, and an unknown name asserts nothing.
		if ctx.v.formatsVocabDriven {
			return []*ValidationError{
				leafError(ctx.instancePath, ctx.schemaPath, KeywordFormat,
					fmt.Sprintf("format %q has no registered checker", schema.Format)),
			}
		}

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
	if ctx.instance.Kind() != jsonvalue.Array {
		return nil
	}

	arr := ctx.instance.Elements()

	plan := ctx.v.itemsPlanFor(ctx.nodeID, ctx.schema)
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
// unconditionally (so unevaluatedItems sees them even when the count fails).
// The at-least-one rule with its default floor belongs to contains itself
// (2020-12 core section 10.3.1.3, the applicator vocabulary that gates this
// row), so it applies whenever the row runs; only the explicit
// minContains/maxContains keyword reads are sub-gated on the validation
// vocabulary, which owns those keywords.
func evalContains(ctx evalContext) []*ValidationError {
	schema := ctx.schema
	if schema.Contains == nil {
		return nil
	}

	if ctx.instance.Kind() != jsonvalue.Array {
		return nil
	}

	arr := ctx.instance.Elements()

	v, ann := ctx.v, ctx.ann
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	matchCount := 0

	// The contains schema location is invariant across elements; build it once
	// rather than rebuilding it on every iteration.
	containsSchemaPath := schemaPath.kw(KeywordContains)

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

	// Only the explicit minContains/maxContains keywords belong to the 2020-12
	// validation vocabulary; with it disabled they are skipped (not in effect),
	// while the default minContains=1 floor below still applies, since the
	// at-least-one rule is contains' own (applicator-vocabulary) assertion.
	// Under Draft-07 vocabActive(vocabValidation) is always true.
	validationActive := v.vocabActive(vocabValidation)

	minContains := 1
	if validationActive && v.profile.containsCounts && schema.MinContains != nil {
		minContains = *schema.MinContains
	}

	maxContains := -1
	if validationActive && v.profile.containsCounts && schema.MaxContains != nil {
		maxContains = *schema.MaxContains
	}

	if matchCount < minContains {
		// An explicit (and in-effect) minContains owns the violation; otherwise
		// the shortfall is a plain contains failure (default minContains=1). A
		// skipped minContains must not label a default-floor failure.
		keyword := KeywordContains
		if validationActive && v.profile.containsCounts && schema.MinContains != nil {
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

	if ctx.instance.Kind() != jsonvalue.Array {
		return nil
	}

	arr := ctx.instance.Elements()

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

	if schema.UniqueItems && jsonvalue.HasDuplicates(arr) {
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

	if ctx.instance.Kind() != jsonvalue.Object {
		return nil
	}

	obj := ctx.instance.Members()

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
	for _, propName := range v.propertyKeysFor(ctx.nodeID, schema) {
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
		childSchemaPath := schemaPath.kw(KeywordProperties).key(propName)
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
	for _, pattern := range v.patternKeysFor(ctx.nodeID, schema) {
		patternSchema := schema.PatternProperties[pattern]

		// One schema-path location per pattern, shared by the error branch
		// and every matching property rather than rebuilt for each.
		patternSchemaPath := schemaPath.kw(KeywordPatternProperties).key(pattern)

		cp := v.patternPropertyFor(ctx.nodeID, pattern)
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
		childSchemaPath := schemaPath.kw(KeywordAdditionalProperties)

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
		childSchemaPath := schemaPath.kw(KeywordPropertyNames)

		for _, propName := range sortedObjKeys {
			childPath := instancePath.key(propName)
			childErrs := v.validate(
				schema.PropertyNames, jsonvalue.NewString(propName), childPath, childSchemaPath, nil,
			)
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
	if ctx.instance.Kind() != jsonvalue.Object {
		return nil
	}

	obj := ctx.instance.Members()

	triggers := dependencyKeysFor(ctx.v, ctx.nodeID, ctx.schema.DependentSchemas,
		func(dk *dependencyKeys) []string { return dk.dependentSchemas })

	return ctx.v.validateSchemaDependencies(
		ctx.schema.DependentSchemas, triggers, KeywordDependentSchemas,
		ctx.instance, obj, ctx.instancePath, ctx.schemaPath, ctx.ann,
	)
}

// evalObjectCount checks the object count keywords (required, minProperties,
// maxProperties).
func evalObjectCount(ctx evalContext) []*ValidationError {
	schema := ctx.schema

	if ctx.instance.Kind() != jsonvalue.Object {
		return nil
	}

	obj := ctx.instance.Members()

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
	if ctx.instance.Kind() != jsonvalue.Object {
		return nil
	}

	obj := ctx.instance.Members()

	triggers := dependencyKeysFor(ctx.v, ctx.nodeID, ctx.schema.DependentRequired,
		func(dk *dependencyKeys) []string { return dk.dependentRequired })

	return ctx.v.validateRequiredDependencies(
		ctx.schema.DependentRequired, triggers, KeywordDependentRequired,
		obj, ctx.instancePath, ctx.schemaPath,
	)
}

// evalLegacyDependencies checks the legacy draft-07 dependencies keyword, which
// upstream splits into DependencySchemas and DependencyStrings. It is honored
// under Draft 2020-12 too for backward compatibility (the keyword was split into
// dependentSchemas and dependentRequired there, but accepting the legacy form
// aids migration and matches the optional dependencies-compatibility suite).
// Ungated by vocabulary: vocabulary is a 2020-12 concept and the legacy keyword
// predates it, so its table row carries the always-active core group.
func evalLegacyDependencies(ctx evalContext) []*ValidationError {
	if ctx.instance.Kind() != jsonvalue.Object {
		return nil
	}

	obj := ctx.instance.Members()

	v, schema := ctx.v, ctx.schema
	instancePath, schemaPath := ctx.instancePath, ctx.schemaPath

	var errs []*ValidationError

	schemaTriggers := dependencyKeysFor(v, ctx.nodeID, schema.DependencySchemas,
		func(dk *dependencyKeys) []string { return dk.legacySchemas })
	stringTriggers := dependencyKeysFor(v, ctx.nodeID, schema.DependencyStrings,
		func(dk *dependencyKeys) []string { return dk.legacyStrings })

	errs = append(errs, v.validateSchemaDependencies(
		schema.DependencySchemas, schemaTriggers, KeywordDependencies,
		ctx.instance, obj, instancePath, schemaPath, ctx.ann,
	)...)
	errs = append(errs, v.validateRequiredDependencies(
		schema.DependencyStrings, stringTriggers, KeywordDependencies, obj, instancePath, schemaPath,
	)...)

	return errs
}

// validateSchemaDependencies validates the schema-valued dependency keywords:
// Draft 2020-12 dependentSchemas and the legacy draft-07 dependencies form.
// For each trigger property present in obj, the whole instance must validate
// against the dependency subschema. Annotations are merged only on success, so
// a failing dependency does not let unevaluated* observe its partial
// evaluation. The keyword argument names the keyword for the error schema
// path, and triggers carries deps' keys in sorted order, precomputed at
// Compile for an indexed node (see [dependencyKeysFor]).
func (v *validator) validateSchemaDependencies(
	deps map[string]*Schema,
	triggers []string,
	keyword string,
	instance jsonvalue.Value,
	obj map[string]jsonvalue.Value,
	instancePath instanceLocation,
	schemaPath schemaLocation,
	ann *annotations.Set,
) []*ValidationError {
	var errs []*ValidationError

	for _, prop := range triggers {
		if _, exists := obj[prop]; !exists {
			continue
		}

		depAnn := ann.Child()
		childSchemaPath := schemaPath.kw(keyword).key(prop)
		childErrs := v.validate(deps[prop], instance, instancePath, childSchemaPath, depAnn)
		// Stamp the dependency keyword on a boolean-false subschema's leaf,
		// mirroring the other applicator call sites, so the error contract (a
		// false subschema failure carries the applying keyword) holds here too.
		labelFalseSchemaKeyword(childErrs, deps[prop], keyword)

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
// the Keyword field, which coincide for these keywords, and triggers carries
// deps' keys in sorted order, precomputed at Compile for an indexed node (see
// [dependencyKeysFor]).
func (v *validator) validateRequiredDependencies(
	deps map[string][]string,
	triggers []string,
	keyword string,
	obj map[string]jsonvalue.Value,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	var errs []*ValidationError

	for _, prop := range triggers {
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
		childSchemaPath := schemaPath.kw(KeywordAllOf).idx(i)
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
		childSchemaPath := schemaPath.kw(KeywordAnyOf).idx(i)
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
		childSchemaPath := schemaPath.kw(KeywordOneOf).idx(i)
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

	childErrs := ctx.v.validate(schema.Not, ctx.instance, ctx.instancePath, ctx.schemaPath.kw(KeywordNot), nil)
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
	ifErrs := v.validate(schema.If, instance, instancePath, schemaPath.kw(KeywordIf), ifAnn)
	ifPassed := len(ifErrs) == 0

	if ifPassed {
		ann.Merge(ifAnn)

		if schema.Then != nil {
			thenAnn := ann.Child()
			thenErrs := v.validate(schema.Then, instance, instancePath, schemaPath.kw(KeywordThen), thenAnn)
			if len(thenErrs) > 0 {
				errs = append(errs, wrapError(instancePath, schemaPath, KeywordThen,
					"if condition was true but did not validate against then subschema", thenErrs))
			} else {
				ann.Merge(thenAnn)
			}
		}
	} else if schema.Else != nil {
		elseAnn := ann.Child()
		elseErrs := v.validate(schema.Else, instance, instancePath, schemaPath.kw(KeywordElse), elseAnn)
		if len(elseErrs) > 0 {
			errs = append(errs, wrapError(instancePath, schemaPath, KeywordElse,
				"if condition was false but did not validate against else subschema", elseErrs))
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
	instance jsonvalue.Value,
	instancePath instanceLocation,
	schemaPath schemaLocation,
) []*ValidationError {
	if instance.Kind() != jsonvalue.String {
		return nil
	}

	str := instance.Str()

	switch kw, decodeErr := content.Assert(
		schema.ContentEncoding, schema.ContentMediaType, str,
		v.draft == Draft2020,
	); kw {
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
		ctx.v.resolveRef(schema, ref), schema, ref, KeywordRef,
		ctx.instance, ctx.instancePath, ctx.schemaPath, ctx.ann,
	)
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
		ctx.v.resolveDynamicRef(schema, ref), schema, ref, KeywordDynamicRef,
		ctx.instance, ctx.instancePath, ctx.schemaPath, ctx.ann,
	)
}

// validateResolvedRef validates the instance against a resolved reference
// target, sharing the resolution-error and annotation handling between $ref
// and $dynamicRef. The schema is the node bearing the reference keyword;
// the keyword names that keyword for error paths.
// The [refresolve.Result] carries the target and, on a resolver-reported
// failure, the error to surface.
func (v *validator) validateResolvedRef(
	res refresolve.Result,
	schema *Schema,
	ref, keyword string,
	instance jsonvalue.Value,
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

		// An unresolvable ref is an error rather than silently passing: always
		// for a non-local (remote/absolute) ref, and for a local fragment ref
		// whose bearing node entered the graph only during this run (inside a
		// document first fetched at validation time, or inside a JSON-pointer
		// fallback target), where no compile-time pass ever vetted it. Only a
		// fragment ref borne by a node the compiled registry knows is silently
		// skipped: there the compile-time reference walk already rejected
		// genuinely broken fragments before this walk began, so this branch
		// is benign.
		if !uriref.IsFragmentOnly(ref) || !v.refReg.KnownSchema(schema) {
			return []*ValidationError{
				leafError(instancePath, schemaPath, keyword, fmt.Sprintf("cannot resolve %s %q", keyword, ref)),
			}
		}

		// Unresolvable local fragment ref in a compile-vetted document:
		// silently skip.
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
