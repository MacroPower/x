package jsonschema

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"reflect"
	"slices"
	"time"

	jsonv2 "encoding/json/v2"

	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/fieldset"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonprobe"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

var (
	typeTime       = reflect.TypeFor[time.Time]()
	typeSlogLevel  = reflect.TypeFor[slog.Level]()
	typeJSONNumber = reflect.TypeFor[json.Number]()
	typeBigInt     = reflect.TypeFor[big.Int]()
	typeBigRat     = reflect.TypeFor[big.Rat]()
	typeBigFloat   = reflect.TypeFor[big.Float]()
	typeProvider   = reflect.TypeFor[JSONSchemaProvider]()
	typeExtender   = reflect.TypeFor[JSONSchemaExtender]()

	// Inclusive [minimum, maximum] float64 bounds for each fixed-width integer
	// kind. Int64 and Uint64 are excluded: float64 cannot name their maxima
	// inclusively, so they use an exclusive maximum (see schemaForKind).
	inclusiveIntBounds = map[reflect.Kind][2]float64{
		reflect.Int8:   {math.MinInt8, math.MaxInt8},
		reflect.Int16:  {math.MinInt16, math.MaxInt16},
		reflect.Int32:  {math.MinInt32, math.MaxInt32},
		reflect.Uint8:  {0, math.MaxUint8},
		reflect.Uint16: {0, math.MaxUint16},
		reflect.Uint32: {0, math.MaxUint32},
	}
)

// generatorConfig is the option set one [Generator] or one-shot entry point
// applies once. Generation only reads it, so concurrent runs share one value.
type generatorConfig struct {
	typeProviders []TypeSchemaProvider
	namer         Namer
	// DefaultsFrom is the WithDefaultsFrom instance; defaultsFromSet
	// distinguishes an explicit nil instance from the option being absent.
	defaultsFrom        any
	descriptionProvider DescriptionProvider
	tagInterpreters     []tagInterpreterRegistration
	// The WithJSONOptions state: the joined [jsonv2.Options] value and the
	// refusal it resolved to (surfaced per run by generate). The three
	// honored flags probed from it sit with the other bools below.
	jsonOpts             jsonv2.Options
	jsonOptsErr          error
	typeExtenders        []TypeSchemaExtender
	profile              draftProfile // per-draft behavioral policy, resolved once from draft
	draft                Draft
	definitions          bool
	additionalProperties bool
	// The three honored WithJSONOptions flags, probed from jsonOpts at
	// option-application time.
	nilSliceNull    bool
	nilMapNull      bool
	omitZeroFields  bool
	defaultsFromSet bool
	rootTitle       bool
}

// run holds the state of a single schema generation run over one
// configuration: the caller's context, the def table, the per-type memos,
// and the v2 probe. Every phase method hangs off it.
type run struct {
	*generatorConfig

	// The caller's context for this generation run, passed to the
	// DescriptionProvider with every comment lookup.
	ctx context.Context

	typeToDef         map[reflect.Type]*defEntry
	typeOverrideCache map[reflect.Type]typeOverrideResult
	visiting          map[reflect.Type]bool
	// Fields resolves each struct type's JSON fields. It carries the in-flight
	// set that terminates the ghost recursion, so a mutually composed pair does
	// not recurse through its own shadow analysis forever.
	fields *fieldset.Collector
	// RefAliasing tracks the types whose [TypeSchema.Ref] alias is being
	// resolved, so an alias chain that reaches one of them again (a self-Ref, or
	// a mutual A -> B -> A cycle) is reported instead of recursing forever.
	refAliasing map[reflect.Type]bool
	// Probe is the [encoding/json/v2] refusal oracle for this run: it answers
	// whether v2 refuses a struct declaration, a field, or a type under the
	// run's json options, and memoizes each answer.
	probe *jsonprobe.Probe
	defs  []*defEntry // every def entry, in build order
}

// typeOverrideResult memoizes one [run.resolveTypeSchema] consultation so
// the registered type providers run at most once per type per run, even when the
// allOf-composition probe and the real build both ask about the same type.
type typeOverrideResult struct {
	err      error
	ts       TypeSchema
	resolved bool
}

// newConfig applies the options once and returns the configuration every
// run derives from through [generatorConfig.forRun]. Nil options are
// skipped, so an optional option can be passed unconditionally.
func newConfig(opts []GenerateOption) *generatorConfig {
	c := &generatorConfig{
		draft:       Draft2020,
		namer:       defaultNamerFunc(),
		definitions: true,
	}

	for _, opt := range opts {
		if opt != nil {
			opt.applyGenerate(c)
		}
	}

	// Resolve the draft profile after options settle c.draft, so every
	// generation site reads the policy through g.profile rather than comparing
	// g.draft.
	c.profile = c.draft.profile()

	return c
}

// forRun starts one generation run over the configuration. The per-run maps,
// the probe, and the context are fresh, so concurrent runs from one
// configuration never share mutable state.
func (c *generatorConfig) forRun(ctx context.Context) *run {
	g := &run{
		generatorConfig:   c,
		ctx:               ctx,
		typeToDef:         map[reflect.Type]*defEntry{},
		typeOverrideCache: map[reflect.Type]typeOverrideResult{},
		visiting:          map[reflect.Type]bool{},
		refAliasing:       map[reflect.Type]bool{},
		probe:             jsonprobe.New(c.jsonOpts),
	}

	// Bind to the run, since needsAllOfComposition reads the per-run context
	// and type-override cache.
	g.fields = fieldset.NewCollector(g.needsAllOfComposition)

	return g
}

// generate produces the root schema for the given type.
func (g *run) generate(t reflect.Type) (*Schema, error) {
	// A WithJSONOptions refusal surfaces per run, so NewGenerator-then-
	// Generate reports it the same way the one-shot entry points do.
	if g.jsonOptsErr != nil {
		return nil, g.jsonOptsErr
	}

	// A nil type carries no kind to reflect on; report it through the error
	// contract instead of panicking in numkind.DerefType.
	if t == nil {
		return nil, fmt.Errorf("%w: nil type", ErrUnsupportedType)
	}

	// Follow pointers for root type identity.
	rootType := numkind.DerefType(t)

	// Phase 1: reflect the graph. Every node records the facts of its
	// occurrence, every type-level hook runs, and the jsonschema tag's type=
	// pair rebuilds the field it overrides.
	root, err := g.schemaForType(t, false)
	if err != nil {
		return nil, err
	}

	// Phase 2: assign final $defs names (disambiguating collisions) before
	// render emits any $ref string. Names are keyed on defEntry identity, so
	// reachability and root inlining below key on identity too and need no
	// renamed-entry lookup.
	g.assignDefNames()

	// Phase 3: decide the null admission of every node from the facts the
	// build recorded and the stances the type-level hooks declared.
	g.resolveNullability(root)

	// Phase 4: the field-level hooks, which read the final decision.
	err = g.applyFieldHooks(root)
	if err != nil {
		return nil, err
	}

	// Phase 5: inline a root $ref whose def is reached from nowhere else; a
	// self- or mutually recursive root keeps its $ref so those references
	// never dangle.
	root = g.maybeInlineRoot(root)

	// Phase 6: refuse a null literal an interpreter wrote onto a canvas
	// against an occurrence that admits none.
	err = g.checkNullLiterals(root)
	if err != nil {
		return nil, err
	}

	// Phase 7: seed the WithDefaultsFrom instance onto the root object's
	// properties, on the canvases render composes.
	if g.defaultsFromSet {
		err = g.seedDefaults(root, rootType)
		if err != nil {
			return nil, err
		}
	}

	// Phase 8: render, each node once, onto fresh schemas. Only the defs
	// reachable from the final root graph are emitted. A def orphaned by a
	// type= override or by root inlining is never reached and so dropped.
	schema := g.render(root)

	reached := g.collectReferencedDefs(root)

	rendered := make(map[*defEntry]*Schema, len(reached))
	for _, e := range reached {
		rendered[e] = g.render(e.body)
	}

	// Set the root title from the type name when WithRootTitle is enabled and
	// nothing else (WithTypeSchema, JSONSchemaProvider, an extender, or tags)
	// supplied one. Unnamed roots produce an empty name even after the
	// empty-answer deferral to the default namer, and stay untitled.
	if g.rootTitle {
		target := g.rootTitleTarget(schema, root, rendered)
		if name := g.schemaName(rootType); name != "" && target.Title == "" {
			target.Title = name
		}
	}

	// Set $schema on root.
	schema.Schema = g.draft.schemaURI()

	// Attach $defs if any.
	if len(reached) > 0 {
		defs := make(map[string]*Schema, len(reached))
		for _, e := range reached {
			defs[e.name] = rendered[e]
		}

		if g.profile.definitionsKeyword {
			schema.Definitions = defs
		} else {
			schema.Defs = defs
		}
	}

	return schema, nil
}

// rootTitleTarget resolves the schema that WithRootTitle titles. Draft-07
// readers ignore keywords beside $ref, so when a self-referential root stays
// a bare $ref into definitions, the title goes on the definitions entry it
// targets instead, shared by every occurrence of the type;
// [run.seedDefaults] redirects the same way.
func (g *run) rootTitleTarget(schema *Schema, root *node, rendered map[*defEntry]*Schema) *Schema {
	// Draft-07 readers ignore keywords beside a bare $ref, so a self-referential
	// root that stayed a $ref is titled on its $defs body instead.
	if !g.profile.honorRefSiblings && schema.Ref != "" && root.kind == kindRef {
		return rendered[root.def]
	}

	return schema
}

// schemaForType produces the IR node for the given type. Pointer reports a
// pointer or interface occurrence at any level above t, which the node records
// as a fact for [run.resolveNullability].
func (g *run) schemaForType(t reflect.Type, pointer bool) (*node, error) {
	// Follow pointers. A pointer at any level makes the occurrence nullable.
	if t.Kind() == reflect.Pointer {
		pointer = true
	}

	t = numkind.DerefType(t)

	// A nil interface marshals as null, so an interface position is nullable
	// like a pointer. The bit matters only when a later step intercepts the
	// interface with a non-empty schema (an override, a provider declaration,
	// or a TextMarshaler method set): a plain interface reflects as {}, which
	// already admits null, and render dedups the wrapper away.
	if t.Kind() == reflect.Interface {
		pointer = true
	}

	// A named type already registered in $defs is referenced again, not rebuilt:
	// re-resolving it would re-run its provider, override, extender, and
	// description hooks once per reference and discard every result after the
	// first. This must precede the provider/override dispatch (steps 1-2) so a
	// provider or override struct is not re-invoked per occurrence. It fires only
	// once the type has a def entry, so a struct mid-build (no entry yet) still
	// flows through schemaForStruct's visiting-based cycle detection.
	if t.Name() != "" {
		if e, exists := g.typeToDef[t]; exists {
			return g.refNode(e, pointer), nil
		}
	}

	// 1. Type provider override (WithTypeSchemaProvider / WithTypeSchema).
	ts, ok, err := g.resolveTypeSchema(t)
	if err != nil {
		return nil, err
	}

	if ok {
		return g.handleOverrideType(t, ts, pointer)
	}

	// 2. JSONSchemaProvider interface.
	if implementsProvider(t) {
		return g.handleProviderType(t, pointer)
	}

	// 3. Built-in overrides.
	if s, ok := g.builtinOverride(t); ok {
		return g.handleBuiltinType(t, s, pointer)
	}

	// 4. Marshaler methods promoted from an embedded field. A struct whose
	// method set includes a promoted MarshalJSONTo or MarshalJSON is
	// serialized by that method. Encoding/json/v2 resolves marshalers through
	// the method set (MarshalJSONTo outranking MarshalJSON), so the embedded
	// type's marshaler takes over the whole struct, and reflecting its fields
	// would describe a shape that never appears. A promoted json marshaler
	// can emit any JSON value, so the schema is unrestricted. A type that
	// directly implements either json interface is deliberately NOT handled
	// here: per the documented resolution priority it falls through to
	// kind-based reflection, and WithTypeSchema or JSONSchemaProvider is the
	// escape hatch for its real shape. A direct method on either interface
	// also suppresses the other's promoted form, since v2 consults the
	// higher-ranked method set entry first.
	if reflectkind.IsPromotedJSONMarshalerTo(t) {
		return g.handleBuiltinType(t, &Schema{}, pointer)
	}

	if reflectkind.IsPromotedJSONMarshaler(t) && !reflectkind.ImplementsJSONMarshalerTo(t) {
		return g.handleBuiltinType(t, &Schema{}, pointer)
	}

	// 5. Text serialization (encoding.TextAppender or encoding.TextMarshaler,
	// direct or promoted). Either method always emits a string, so the schema
	// is one; the built-in path supplies the type-level post-processing
	// (comments, extender, $defs extraction). A type that also implements a
	// json marshaler interface is not a string: encoding/json/v2 ranks
	// MarshalJSONTo and MarshalJSON above both text methods, so the text form
	// never appears in the output. Such a type falls through to kind-based
	// reflection like any other direct json marshaler.
	if reflectkind.ImplementsAnyTextMarshaler(t) &&
		!reflectkind.ImplementsAnyJSONMarshaler(t) {
		s := &Schema{Type: typename.String}

		return g.handleBuiltinType(t, s, pointer)
	}

	// 6. Cycle detection for named container types. A named type that contains
	// itself (type T []T, type M map[string]M, type A [N]A) recurses without
	// bound through schemaForKind. Tracking the type on the visiting stack lets a
	// re-entry link to its def entry, breaking the cycle exactly as
	// schemaForStruct does for self-referential structs. Struct types run their
	// own equivalent guard inside schemaForStruct, so they are excluded here.
	guarded := t.Kind() != reflect.Struct && t.Name() != "" && reflectkind.IsRecursiveContainerKind(t.Kind())
	if guarded {
		if g.visiting[t] {
			return g.refNode(g.newDefEntry(t), pointer), nil
		}

		g.visiting[t] = true
	}

	// 7. Kind-based reflection. The node records the occurrence's
	// pointer-ness as a fact; a node that becomes a $defs body is shared by
	// every reference, and the null pass ignores the fact there, so nothing
	// is withheld from the build.
	n, err := g.schemaForKind(t, pointer)
	if guarded {
		delete(g.visiting, t)
	}

	if err != nil {
		return nil, err
	}

	// Type-level post-processing for non-struct types. Struct types handle
	// comments, extend, and extraction internally in
	// buildStructSchema/schemaForStruct. Unnamed composite types ([]string,
	// map[string]int) run the extender too -- their schemas are just as
	// reflection-produced -- but skip comment lookup (keyed by named type) and
	// never extract (shouldExtract and the cyclic guard both require a name),
	// so an inline unnamed type is extended once per occurrence.
	if t.Kind() == reflect.Struct {
		return n, nil
	}

	if t.Name() != "" {
		err := g.applyTypeDescription(t, n.payload)
		if err != nil {
			return nil, err
		}
	}

	// The type's extender refines its value schema, so direct it at the bare
	// value payload. A non-pointer field of the same type presents the same
	// bare payload, so a pointer and a value field stay consistent; render
	// applies the null wrapper afterward, where a type-level keyword cannot
	// reject the permitted null. An extender may also declare a nullability
	// stance, which the null pass folds into the node's decision.
	stance, err := g.extendTypeSchema(t, n)
	if err != nil {
		return nil, err
	}

	// A cycle detected while building this type's element/value schema left a
	// placeholder def entry (created by a guarded re-entry). Fill it with the
	// now complete body and return a reference, mirroring the struct path.
	if e, cyclic := g.typeToDef[t]; cyclic {
		fillDef(e, n, stance)

		return g.refNode(e, pointer), nil
	}

	if g.shouldExtract(t) {
		return g.defineType(t, n, stance, pointer), nil
	}

	n.stance = stance

	return n, nil
}

// resolveTypeSchema consults the registered type providers for t, newest
// registration first, and returns the first schema offered, with ok
// reporting whether any provider handled the type. The order makes a later
// registration win for the types two providers both handle, which for the
// exact-match providers WithTypeSchema registers preserves its
// last-registration-wins behavior. An ErrTypeNotHandled answer passes the
// type to the next provider; any other provider error stops the
// consultation and aborts generation.
func (g *run) resolveTypeSchema(t reflect.Type) (TypeSchema, bool, error) {
	// Memoize per run so a stateful or expensive provider is consulted once per
	// type: the allOf-composition probe in needsAllOfComposition and the real
	// build both resolve the same type, and a non-deterministic provider would
	// otherwise disagree between them. The TypeSchema's Value/Verbatim is cloned
	// by finishTypeOverride before mutation, so handing the same one to both
	// callers is safe.
	if cached, ok := g.typeOverrideCache[t]; ok {
		return cached.ts, cached.resolved, cached.err
	}

	ts, resolved, err := g.resolveTypeSchemaUncached(t)
	g.typeOverrideCache[t] = typeOverrideResult{ts: ts, resolved: resolved, err: err}

	return ts, resolved, err
}

// resolveTypeSchemaUncached performs the provider consultation that
// resolveTypeSchema memoizes.
func (g *run) resolveTypeSchemaUncached(t reflect.Type) (TypeSchema, bool, error) {
	tc := TypeContext{Type: t, Draft: g.draft}
	for _, v := range slices.Backward(g.typeProviders) {
		ts, err := v.SchemaForType(g.ctx, tc)
		if errors.Is(err, ErrTypeNotHandled) {
			continue
		}

		if err != nil {
			return TypeSchema{}, false, fmt.Errorf("resolve type %s: %w", t, err)
		}

		return ts, true, nil
	}

	return TypeSchema{}, false, nil
}

// handleOverrideType processes a type resolved by a registered
// TypeSchemaProvider (WithTypeSchemaProvider or WithTypeSchema). A zero
// TypeSchema marks the type unrestricted, mirroring a JSONSchemaProvider
// returning a zero TypeSchema.
func (g *run) handleOverrideType(t reflect.Type, ts TypeSchema, pointer bool) (*node, error) {
	return g.finishTypeOverride(t, ts, pointer)
}

// finishTypeOverride turns a resolved [TypeSchema] into an IR node. It is the
// single funnel for the WithTypeSchema override path and the JSONSchemaProvider
// path: it validates mutual exclusivity of Value/Verbatim/Ref
// ([ErrConflictingTypeSchema]) and dispatches on which one is set. A zero
// TypeSchema (nil Value) marks the type unrestricted ({}).
//
//   - Value (common case): clone the caller-shared schema, apply type-level
//     comments, and build a bare value node recording the
//     [TypeSchema.Nullability] stance and the occurrence's pointer-ness for the
//     null pass, never a wrapper baked into the payload.
//   - Verbatim: an opaque, fully-formed schema emitted exactly as given, with no
//     null encoding and no $defs extraction.
//   - Ref: a whole-type alias to another Go type, resolved to that type's node
//     edge so its definition stays reachable.
//
// The Value/Verbatim source is copied with the upstream shallow CloneSchemas,
// not the deep cloneSchema used for remote refs, because the generation half
// needs only sub-schema copies plus the header unaliasing below. CloneSchemas
// only deep-copies sub-schema fields, leaving the Enum, Const, Default, and
// Extra headers aliased to the caller's schema; CloneOverrideExtras copies
// those too, so a tag interpreter or JSONSchemaExtender that mutates them in
// place (appending to Enum, reassigning Const, writing into Extra) cannot reach
// back into an override or provider schema reused across Generate calls.
func (g *run) finishTypeOverride(t reflect.Type, ts TypeSchema, pointer bool) (*node, error) {
	err := checkTypeSchemaExclusive(t, ts)
	if err != nil {
		return nil, err
	}

	// Verbatim: emitted exactly as authored, no null encoding, never extracted.
	if ts.Verbatim != nil {
		v := ts.Verbatim.CloneSchemas()
		schemashape.CloneOverrideExtras(v)

		return &node{kind: kindValue, payload: v, verbatim: true, occ: occurrence{pointer: pointer}}, nil
	}

	// Ref: a whole-type alias resolved to a real node edge, keeping the target
	// definition reachable without a payload $ref-string scan.
	if ts.Ref != nil {
		return g.refTypeOverride(t, ts, pointer)
	}

	value := ts.Value
	if value == nil {
		value = &Schema{} // unrestricted
	}

	s := value.CloneSchemas()
	schemashape.CloneOverrideExtras(s)

	// Apply type-level comments.
	err = g.applyTypeDescription(t, s)
	if err != nil {
		return nil, err
	}

	vnode := &node{kind: kindValue, payload: s, occ: occurrence{pointer: pointer}}

	if g.shouldExtract(t) {
		return g.defineType(t, vnode, ts.Nullability, pointer), nil
	}

	vnode.stance = ts.Nullability

	return vnode, nil
}

// checkTypeSchemaExclusive reports an [ErrConflictingTypeSchema] when ts sets
// more than one of Value, Verbatim, or Ref: the three are mutually exclusive
// ways to describe a type's schema, so a silent precedence would hide a caller
// bug.
func checkTypeSchemaExclusive(t reflect.Type, ts TypeSchema) error {
	set := 0
	if ts.Value != nil {
		set++
	}

	if ts.Verbatim != nil {
		set++
	}

	if ts.Ref != nil {
		set++
	}

	if set > 1 {
		return fmt.Errorf("%w: type %s sets more than one of Value, Verbatim, Ref", ErrConflictingTypeSchema, t)
	}

	return nil
}

// refTypeOverride resolves a [TypeSchema.Ref] alias to the referenced type's
// node edge, so the target definition stays reachable through a node-backed $ref
// rather than a payload $ref-string scan. The referenced type must be
// extractable (a struct or a type extracted to $defs); a non-extractable target
// would inline a copy and lose the reachability guarantee, so it is rejected.
// The alias's own stance rides on the reference node, where the null pass
// applies it after the target's recorded stance and before the occurrence's
// pointer-ness.
func (g *run) refTypeOverride(t reflect.Type, ts TypeSchema, pointer bool) (*node, error) {
	// The alias resolves through schemaForType, which consults the override
	// chain again for the target; a Ref naming its own type, directly or through
	// a chain of aliases, would recurse forever. Re-entering a type whose alias
	// is already being resolved is that cycle, reported as a malformed
	// TypeSchema rather than crashing the stack.
	if g.refAliasing[t] {
		return nil, fmt.Errorf("%w: type %s Ref %s forms an alias cycle", ErrConflictingTypeSchema, t, ts.Ref)
	}

	g.refAliasing[t] = true
	defer delete(g.refAliasing, t)

	ref, err := g.schemaForType(ts.Ref, pointer)
	if err != nil {
		return nil, err
	}

	if ref.kind != kindRef {
		return nil, fmt.Errorf(
			"%w: type %s Ref %s does not name an extractable type", ErrConflictingTypeSchema, t, ts.Ref,
		)
	}

	// The precedence is target stance, then alias stance, then pointer-ness:
	// a Ref alias inherits the target type's stance, and its own Nullability
	// applies only when the target is NullFromReflection (the common case,
	// where the folded value passes through unchanged).
	ref.stance = ts.Nullability

	return ref, nil
}

// handleProviderType processes a type implementing JSONSchemaProvider.
//
// The provider's JSONSchema method returns its exact *Schema, which it may share
// across fields and Generate calls (for example a package-level singleton).
// Downstream steps mutate it: applyTypeDescription writes Description in place and
// the def body aliases the pointer into the node graph. The shared
// finishTypeOverride clones it first, so the provider's source schema is never
// corrupted.
func (g *run) handleProviderType(t reflect.Type, pointer bool) (*node, error) {
	provided, err := callProvider(g.ctx, TypeContext{Type: t, Draft: g.draft})
	if err != nil {
		return nil, err
	}

	return g.finishTypeOverride(t, provided, pointer)
}

// handleBuiltinType processes a type with a built-in override, applying
// type-level post-processing (comments, extender, $defs extraction) per
// the processing order. The node records the occurrence's pointer-ness and
// the extender's stance for the null pass.
func (g *run) handleBuiltinType(t reflect.Type, s *Schema, pointer bool) (*node, error) {
	value := &node{kind: kindValue, payload: s, occ: occurrence{pointer: pointer}}

	// Comment lookup is keyed by named type; the extender runs for unnamed
	// builtin-produced schemas too (a registered extender for []byte is just
	// as applicable as one for a named type).
	if t.Name() != "" {
		err := g.applyTypeDescription(t, s)
		if err != nil {
			return nil, err
		}
	}

	stance, err := g.extendTypeSchema(t, value)
	if err != nil {
		return nil, err
	}

	if g.shouldExtract(t) {
		return g.defineType(t, value, stance, pointer), nil
	}

	value.stance = stance

	return value, nil
}

// byteSliceNode returns the IR node for a []byte, which encoding/json/v2
// renders as a base64-encoded string (a nil one as ""). The node is a bytes
// container over a bare payload ({contentEncoding: base64}); an occurrence
// that admits null renders it as the ["null", "string"] type list. A []byte
// carries no const/enum, so the type-list encoding never has to flip to the
// anyOf form.
func (g *run) byteSliceNode(pointer bool) *node {
	return &node{
		kind:    kindValue,
		payload: &Schema{ContentEncoding: content.Base64},
		occ:     occurrence{pointer: pointer, container: containerBytes},
	}
}

// builtinOverride returns a schema for well-known types, if applicable. A byte
// slice is deliberately not here: it is a nilable container, built through
// [run.byteSliceNode] on the slice reflection path so both an exact []byte
// and a named byte-slice type share one encoding.
func (g *run) builtinOverride(t reflect.Type) (*Schema, bool) {
	switch t {
	case typeTime:
		return &Schema{Type: typename.String, Format: formatDateTime}, true
	case typeSlogLevel:
		// Slog.Level implements both direct marshalers, so the TextMarshaler
		// step does not claim it (encoding/json prefers MarshalJSON); its
		// MarshalJSON emits the same level name MarshalText produces, as a
		// JSON string, so the string schema kind-based reflection of its int
		// kind would miss is pinned here.
		return &Schema{Type: typename.String}, true
	case reflectkind.TypeJSONTextValue:
		return &Schema{}, true
	case typeJSONNumber:
		return &Schema{Type: typename.Number}, true
	case typeBigInt:
		// Big.Int.MarshalJSON emits a bare JSON number (arbitrary precision),
		// not a string, so the schema is an unbounded integer. (big.Rat and
		// big.Float marshal via MarshalText and so are strings below.)
		return &Schema{Type: typename.Integer}, true
	case typeBigRat:
		return &Schema{Type: typename.String, Pattern: `^-?[0-9]+(/[0-9]+)?$`}, true
	case typeBigFloat:
		// Big.Float can hold infinities (only NaN is unrepresentable), and its
		// MarshalText emits "+Inf"/"-Inf" for them, so the pattern admits that
		// text alongside finite decimal forms. (big.Rat cannot be infinite.)
		return &Schema{Type: typename.String, Pattern: `^([+-]Inf|-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?)$`}, true
	}

	return nil, false
}

// boundedInteger builds an integer schema with inclusive [minimum, maximum]
// bounds.
func boundedInteger(minimum, maximum float64) *Schema {
	return &Schema{Type: typename.Integer, Minimum: new(minimum), Maximum: new(maximum)}
}

// scalarNode wraps a bare scalar payload in a value node recording the
// occurrence's pointer-ness, so render applies the anyOf null wrapper if and
// only if the position admits null.
func (g *run) scalarNode(payload *Schema, pointer bool) *node {
	return &node{kind: kindValue, payload: payload, occ: occurrence{pointer: pointer}}
}

// schemaForKind handles the kind-based reflection step, producing a node. The
// v2 probe answers first for every kind but struct, whose declaration
// [run.buildStructSchema] probes on its own: a func, chan, complex, or
// [unsafe.Pointer] kind, a [time.Duration] (which v2's native codec refuses
// with no format), and a map key v2 cannot name are all refused here, with
// v2's own reason.
func (g *run) schemaForKind(t reflect.Type, pointer bool) (*node, error) {
	if t.Kind() != reflect.Struct {
		err := g.probeType(t)
		if err != nil {
			return nil, err
		}
	}

	switch t.Kind() {
	case reflect.Bool:
		return g.scalarNode(&Schema{Type: typename.Boolean}, pointer), nil

	case reflect.String:
		return g.scalarNode(&Schema{Type: typename.String}, pointer), nil

	case reflect.Int:
		// Plain int is platform-dependent (32 or 64 bit), so leave it unbounded.
		return g.scalarNode(&Schema{Type: typename.Integer}, pointer), nil

	case reflect.Int64:
		// Float64 has a 52-bit mantissa and cannot represent MaxInt64 (2^63-1)
		// exactly, so an inclusive maximum cannot name the true boundary. 2^63 is
		// exactly representable (a power of two), so an exclusive maximum of 2^63
		// admits exactly the values v <= 2^63-1 = MaxInt64, including the boundary,
		// without ever accepting an out-of-range integer. MinInt64 (-2^63) is
		// representable exactly, so the minimum stays inclusive.
		s := &Schema{
			Type:             typename.Integer,
			Minimum:          new(float64(math.MinInt64)),
			ExclusiveMaximum: new(exclusiveMaxInt64),
		}

		return g.scalarNode(s, pointer), nil

	case reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint8, reflect.Uint16, reflect.Uint32:
		// Fixed-width integers whose full range float64 can name inclusively.
		b := inclusiveIntBounds[t.Kind()]
		return g.scalarNode(boundedInteger(b[0], b[1]), pointer), nil

	case reflect.Uint, reflect.Uintptr:
		// Uint/uintptr are platform-dependent; only a lower bound is certain.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0))}
		return g.scalarNode(s, pointer), nil

	case reflect.Uint64:
		// Float64 cannot represent MaxUint64 (2^64-1) exactly; see the Int64 case.
		// 2^64 is exactly representable, so an exclusive maximum of 2^64 admits
		// exactly v <= 2^64-1 = MaxUint64, including the boundary value.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0)), ExclusiveMaximum: new(exclusiveMaxUint64)}
		return g.scalarNode(s, pointer), nil

	case reflect.Float32, reflect.Float64:
		return g.scalarNode(&Schema{Type: typename.Number}, pointer), nil

	case reflect.Interface:
		return g.scalarNode(&Schema{}, pointer), nil

	case reflect.Slice:
		return g.schemaForSlice(t, pointer)

	case reflect.Array:
		return g.schemaForArray(t, pointer)

	case reflect.Map:
		return g.schemaForMap(t, pointer)

	case reflect.Struct:
		return g.schemaForStruct(t, pointer)

	default:
		// Unreachable: the probe refused every kind v2 cannot encode.
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, t)
	}
}

// probeType maps the v2 probe's verdict on t onto the generation sentinels: a
// key v2 cannot name is [ErrUnsupportedMapKey], any other refusal is
// [ErrUnsupportedType].
func (g *run) probeType(t reflect.Type) error {
	err := g.probe.Type(t)

	switch {
	case errors.Is(err, jsonprobe.ErrMapKey):
		return fmt.Errorf("%w: %w", ErrUnsupportedMapKey, err)
	case err != nil:
		return fmt.Errorf("%w: %w", ErrUnsupportedType, err)
	}

	return nil
}

// exclusiveMaxInt64 and exclusiveMaxUint64 are the exclusive upper bounds for
// the int64 and uint64 schemas. Float64 cannot represent MaxInt64 (2^63-1) or
// MaxUint64 (2^64-1) exactly, but the next power of two above each is exactly
// representable, so an exclusive maximum names the boundary precisely:
// v < 2^63 is v <= MaxInt64, and v < 2^64 is v <= MaxUint64.
const (
	exclusiveMaxInt64  = float64(1 << 63) // 2^63
	exclusiveMaxUint64 = float64(1 << 64) // 2^64
)

// schemaForSlice generates a schema for slice types. A nil slice marshals as
// [] under encoding/json/v2's defaults (not null), so a bare slice occurrence
// admits no null; a pointer occurrence does, and so does every occurrence
// under [jsonv2.FormatNilSliceAsNull], whose marshal writes null for the nil
// slice. The node records the container kind and the null pass reads the
// flag.
func (g *run) schemaForSlice(t reflect.Type, pointer bool) (*node, error) {
	// An exact []byte marshals to a base64 string in encoding/json/v2. Only
	// the unnamed builtin element gets that encoding: a named byte element
	// makes the slice a JSON array of numbers.
	if reflectkind.IsBase64ByteSlice(t) {
		return g.byteSliceNode(pointer), nil
	}

	items, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("element type: %w", err)
	}

	return &node{
		kind:    kindList,
		payload: &Schema{},
		items:   items,
		occ:     occurrence{pointer: pointer, container: containerSlice},
	}, nil
}

// schemaForArray generates a schema for fixed-length array types as a tuple.
// Draft 2020-12 uses prefixItems with one entry per element; Draft-07 uses the
// items-as-array form. MinItems/maxItems pin the length. Each element schema is
// generated independently so the result is a tree (no shared sub-schema
// pointers), which the validator requires.
func (g *run) schemaForArray(t reflect.Type, pointer bool) (*node, error) {
	// A [N]byte with the unnamed builtin element marshals as one base64
	// string under encoding/json/v2, and unmarshaling requires the string to
	// decode to exactly N bytes, so the encoded length is pinned to the
	// padded base64 length.
	if reflectkind.IsBase64ByteArray(t) {
		enc := base64.StdEncoding.EncodedLen(t.Len())
		s := &Schema{
			Type:            typename.String,
			ContentEncoding: content.Base64,
			MinLength:       new(enc),
			MaxLength:       new(enc),
		}

		return g.scalarNode(s, pointer), nil
	}

	n := t.Len()

	prefix := make([]*node, n)

	for i := range prefix {
		item, err := g.schemaForType(t.Elem(), false)
		if err != nil {
			return nil, fmt.Errorf("element type: %w", err)
		}

		prefix[i] = item
	}

	s := &Schema{
		Type:     typename.Array,
		MinItems: new(n),
		MaxItems: new(n),
	}

	// A fixed array is not nil-able in Go, so its null (a pointer to it) uses
	// the anyOf encoding, unlike slices and maps.
	return &node{kind: kindTuple, payload: s, prefix: prefix, occ: occurrence{pointer: pointer}}, nil
}

// schemaForMap generates a schema for map types. The key was vetted by the
// probe in [run.schemaForKind]. A nil map marshals as {} under
// encoding/json/v2's defaults (not null), so a bare map occurrence admits no
// null; a pointer occurrence does, and so does every occurrence under
// [jsonv2.FormatNilMapAsNull], whose marshal writes null for the nil map. The
// node records the container kind and the null pass reads the flag.
func (g *run) schemaForMap(t reflect.Type, pointer bool) (*node, error) {
	val, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("map value type: %w", err)
	}

	return &node{
		kind:    kindMap,
		payload: &Schema{},
		items:   val,
		occ:     occurrence{pointer: pointer, container: containerMap},
	}, nil
}

// schemaForStruct generates a schema for struct types.
func (g *run) schemaForStruct(t reflect.Type, pointer bool) (*node, error) {
	// Cycle detection: even when definitions are disabled, cyclic types must
	// emit $defs/$ref to prevent infinite recursion.
	if g.visiting[t] {
		return g.refNode(g.newDefEntry(t), pointer), nil
	}

	// Check for extraction to $defs.
	if g.shouldExtract(t) {
		// Check if already registered.
		if e, exists := g.typeToDef[t]; exists {
			return g.refNode(e, pointer), nil
		}

		g.visiting[t] = true

		obj, stance, err := g.buildStructSchema(t)
		if err != nil {
			return nil, err
		}

		delete(g.visiting, t)

		return g.defineType(t, obj, stance, pointer), nil
	}

	// Inline, but track visiting to detect cycles.
	g.visiting[t] = true

	obj, stance, err := g.buildStructSchema(t)
	if err != nil {
		return nil, err
	}

	delete(g.visiting, t)

	// If a cycle was detected during buildStructSchema (a self-reference created
	// a placeholder def entry), fill it and return a reference.
	if e, exists := g.typeToDef[t]; exists {
		fillDef(e, obj, stance)

		return g.refNode(e, pointer), nil
	}

	obj.stance = stance
	obj.occ.pointer = pointer

	return obj, nil
}

// buildStructSchema builds the object node for a struct type, including
// type-level comment extraction and JSONSchemaExtend. The field nodes hang off
// the object node's props; a field hook that reads the enclosing object
// (FieldContext.Parent) sees a view of it with every sibling's bare shape, and
// a type-level extender sees a view of the whole object, whose edits
// [node.absorbView] reads back.
func (g *run) buildStructSchema(t reflect.Type) (*node, Nullability, error) {
	s := &Schema{
		Type: typename.Object,
	}

	// Set additionalProperties: false (unless opted out).
	if !g.additionalProperties {
		s.AdditionalProperties = &Schema{Not: &Schema{}}
	}

	obj := &node{kind: kindObject, payload: s, typ: t}

	// The v2 probe answers for the declaration first, with v2's own reason.
	// A type whose method set carries a marshal interface is not probed: v2
	// never analyzes such a struct's fields, and generation reflects them by
	// policy, so the field set's own refusal stands there. Everywhere else
	// the field set only names the JSON members, and a refusal it raises
	// that the probe did not is drift between the two, reported as such.
	probed := !reflectkind.ImplementsAnyMarshaler(t)
	if probed {
		err := g.probe.Struct(t)
		if err != nil {
			return nil, NullFromReflection, fmt.Errorf("%w: %w", ErrInvalidJSONField, err)
		}
	}

	// Process fields using encoding/json rules. Only the nodes are built here;
	// the field-level hooks run in [run.applyFieldHooks] once the whole
	// graph exists and every null decision is final.
	resolved, err := g.fields.Of(t)
	if err != nil {
		if probed {
			return nil, NullFromReflection, fmt.Errorf(
				"%w: field set disagrees with encoding/json/v2: %w", ErrInvalidJSONField, err,
			)
		}

		return nil, NullFromReflection, fmt.Errorf("%w: %w", ErrInvalidJSONField, err)
	}

	fields, ghostWon := resolved.Fields, resolved.GhostWon

	// An embedded fallback's members splice into the marshaled object after
	// the named fields, so the map form's value schema becomes the object's
	// extra-member constraint below. Built before the additionalProperties
	// option is consulted, so a value type with no representation refuses
	// generation under every option, exactly where v2's marshal refuses the
	// value. The jsontext.Value form carries arbitrary object members and
	// contributes no value schema; it leaves the object open instead.
	var fallbackNode *node

	if fb := resolved.Fallback; fb != nil && fb.Type != reflectkind.TypeJSONTextValue {
		fallbackNode, err = g.schemaForType(fb.Type.Elem(), false)
		if err != nil {
			return nil, NullFromReflection, fmt.Errorf(
				"embedded fallback field %s: %w", fb.StructField.Name, err,
			)
		}
	}

	var hasAllOf, hasShadowPartial bool

	for idx := range fields {
		if fields[idx].ComposeViaAllOf {
			err := g.processAllOfField(fields[idx], obj)
			if err != nil {
				return nil, NullFromReflection, fmt.Errorf("embedded %s: %w", fields[idx].StructField.Type, err)
			}

			hasAllOf = true
			hasShadowPartial = hasShadowPartial || fields[idx].ShadowPartial

			continue
		}

		err := g.buildFieldSchema(t, fields[idx], obj)
		if err != nil {
			return nil, NullFromReflection, fmt.Errorf("field %q: %w", fields[idx].JSONName, err)
		}
	}

	// PunchGhostWon gives each ghost-won name a true property. A ghost-won
	// name appears in the marshaled object (the embed's field value is
	// carried under it) with no emitted property: the embed's allOf branch
	// holds the assertion. The branch is not guaranteed to evaluate the name
	// -- an unrestricted TypeSchema renders as true and evaluates nothing --
	// and a non-nil unevaluatedProperties would then reject the object's own
	// marshaled output, so the parent evaluates each ghost-won name itself.
	// The branch's assertions still apply through allOf, so this changes no
	// verdict a property-enumerating branch produces.
	punchGhostWon := func() {
		for _, name := range ghostWon {
			if _, ok := s.Properties[name]; ok {
				continue
			}

			if s.Properties == nil {
				s.Properties = map[string]*Schema{}
			}

			s.Properties[name] = &Schema{}
			s.PropertyOrder = append(s.PropertyOrder, name)
		}
	}

	// The extra-member slot: the fallback and allOf interactions with the
	// default close, decided here on the payload; render appends the embed
	// branches and swaps a provisional fallback payload for its rendered
	// form.
	switch {
	case g.additionalProperties:
		// WithAdditionalProperties(true) leaves the object open; a fallback's
		// members pass like any other extra member, so no value schema is
		// emitted (fallbackNode was still built, refusing a bad value type).

	case resolved.Fallback != nil:
		// The initial close never holds beside a fallback: v2 splices the
		// fallback's members into the object as extra members.
		s.AdditionalProperties = nil

		switch {
		case fallbackNode == nil:
			// The jsontext.Value form: its members are arbitrary JSON, so the
			// object stays fully open and ghost punching has nothing to
			// guard.

		case hasShadowPartial:
			// Open, dropping the value schema: the same accept-over-tighten
			// tradeoff the shadow-partial wrap makes without a fallback.

		case hasAllOf && g.profile.closeWithUnevaluated:
			// The branches evaluate the promoted names, so the fallback's
			// value schema judges exactly the unevaluated rest: the spliced
			// members (a fallback key colliding with a named field is a
			// marshal-time duplicate-name error and never appears).
			obj.items = fallbackNode
			obj.fallback = slotUnevaluated

			punchGhostWon()

		case hasAllOf:
			// Draft-07 beside allOf: open, the dialect's existing
			// degradation; additionalProperties would wrongly constrain the
			// embed-promoted names.

		default:
			obj.items = fallbackNode
			obj.fallback = slotAdditional
		}

	case hasAllOf:
		switch {
		case hasShadowPartial:
			// A partially shadowed composition can fail against the marshaled
			// object while its unshadowed names still appear there; with the
			// branch wrapped as anyOf[branch, {}], those names then carry no
			// annotation, so a closed object would reject them. Leave the
			// object open: the same accept-over-tighten tradeoff the wrap
			// itself makes.
			s.AdditionalProperties = nil
		case g.profile.closeWithUnevaluated:
			s.AdditionalProperties = nil
			s.UnevaluatedProperties = &Schema{Not: &Schema{}}

			punchGhostWon()

		default:
			// Draft-07: omit additionalProperties when allOf is in use.
			s.AdditionalProperties = nil
		}
	}

	// Type-level comment.
	err = g.applyTypeDescription(t, s)
	if err != nil {
		return nil, NullFromReflection, err
	}

	// JSONSchemaExtend, then registered extenders. An extender may declare a
	// nullability stance, returned for the caller to fold into the reference or
	// inline node's null decision.
	stance, err := g.extendTypeSchema(t, obj)
	if err != nil {
		return nil, NullFromReflection, err
	}

	return obj, stance, nil
}

// needsAllOfComposition reports whether an embedded struct type should be
// composed via allOf rather than having its fields promoted.
func (g *run) needsAllOfComposition(t reflect.Type) bool {
	// Check type provider overrides (WithTypeSchemaProvider / WithTypeSchema).
	// A provider error counts as intercepted: the embed composes via allOf and
	// the deterministic provider reports the same error when the embedded
	// type's schema is generated, where it aborts generation.
	_, resolved, err := g.resolveTypeSchema(t)
	if resolved || err != nil {
		return true
	}

	// Check JSONSchemaProvider. Only embedded structs reach this probe: an
	// embedded interface is a regular leaf field (encoding/json never flattens
	// it), so its schema resolves through schemaForType like any other field.
	if implementsProvider(t) {
		return true
	}

	// A direct TextMarshaler or a built-in override is never composed here,
	// and never promoted either. When an embedded type's promoted marshaler
	// drives the whole outer value, schemaForType intercepts the outer before
	// reflection begins. Any other struct embed carrying a marshal or
	// unmarshal method is refused by the collector (fieldset.Collect, after
	// encoding/json/v2's "must not implement marshal or unmarshal methods"
	// rule) before this probe's answer matters, so an outer that reflects as
	// an object (a direct json.Marshaler, or ambiguous same-depth marshaler
	// embeds) always receives that refusal.

	return false
}

// buildFieldSchema generates a struct field's node, applies the json:",string"
// override and the jsonschema tag's type= pair, and registers the node in the
// parent's props, PropertyOrder, and required list. The other field-level
// hooks run in [run.applyFieldHooks] once the graph is complete.
func (g *run) buildFieldSchema(
	parentType reflect.Type,
	fi fieldset.Field,
	parent *node,
) error {
	fieldType := fi.StructField.Type
	isPointer := fieldType.Kind() == reflect.Pointer

	// 1. JSON ",string" override. When it coerces the field schema to a string,
	// encoding/json also serializes the value as a quoted string, so the tag's
	// scalars must compare against that text rather than the number: a numeric
	// const on an int field would be {"type":"string","const":5}, which the
	// string-encoded "5" can never satisfy.
	//
	// The tag keeps the field's real Go type and learns the coercion from the
	// type schema instead, which is what lets it parse a scalar at the real kind
	// (so const=200 on an int8 is out of range) before re-serializing it. For a
	// pointer the string lives on the node's null-branch base rather than the
	// payload, so the tag is handed a view that states the coerced type outright.
	//
	// On a stringifiable type the override fully replaces the field schema, so
	// generating the field's own type is skipped: it would be wasted work and,
	// for a type extracted to $defs (a provider or extender), would register an
	// orphan definition and drop the provider's constraints.
	//
	// The v2 probe decides both halves. It marshals a full value of the field
	// and reports whether v2 refuses the declaration and whether the value it
	// writes is a JSON string: the integer and float kinds and
	// [encoding/json.Number] are quoted at any pointer depth, a type whose
	// method set carries a marshal interface is written by the method, which
	// ignores the flag, and every other type is a SemanticError. A refusal is
	// the field's answer only where the type resolves through kind-based
	// reflection: a type a [WithTypeSchemaFor] override or a provider declares
	// is the caller's to describe, and a type v2 refuses with or without the
	// flag ([time.Duration]) is answered as it is unflagged, so the field
	// builds through [run.schemaForType] first and the flag's refusal
	// stands only when that build was kind-based.
	var (
		fieldNode      *node
		stringOverride bool
		err            error
	)

	if fi.JSONString {
		fieldNode, stringOverride, err = g.stringOptionField(fi)
		if err != nil {
			return err
		}
	}

	switch {
	case fieldNode != nil:
		// Built above: a hook declared the type behind a refused flag.
	case stringOverride:
		// The coerced field is a quoted-number container: its base declares
		// the string, so a hook dispatches on the coerced type, and a pointer
		// occurrence renders its null as the ["null", "string"] type list the
		// way a slice or map does.
		fieldNode = &node{
			kind:    kindValue,
			payload: &Schema{Type: typename.String},
			occ:     occurrence{pointer: isPointer, container: containerQuoted},
		}

	default:
		fieldNode, err = g.schemaForType(fieldType, false)
		if err != nil {
			return err
		}
	}

	fieldNode.isField = true

	// Allocate the authored canvas for the field and every sequence/map element
	// beneath it. Field-level processing (the comment provider, the jsonschema
	// tag, tag interpreters) declares its facts on the canvas rather than mutating
	// the type-derived payload, so which schema a keyword lives in is its
	// provenance and reconcileField composes the two at render.
	allocCanvasTree(fieldNode, g.draft)

	// Record the field position on the node and on every element beneath it, so
	// checkNullLiterals can name the field a late-refused null literal sits in.
	assignFieldOrigins(fieldNode, &fieldOrigin{
		parent: parentType,
		typ:    numkind.DerefType(fieldType),
		field:  fi.JSONName,
	})

	// The jsonschema tag's type= pair replaces the field's type wholesale, a
	// fact the null pass needs, so it applies here; the tag's other
	// directives wait for the pass.
	fieldNode = g.applyTypeOverrideDirective(fieldNode, fi)

	// Encoding/json/v2's omitempty omits a field only when its encoded value	// Encoding/json/v2's omitempty omits a field only when its encoded value
	// is an empty JSON value (null, "", {}, []), so a field whose type never
	// encodes one stays required even under the option. The probe answers
	// from the field's zero value, the emptiest value its type encodes. A
	// field promoted through a pointer embed (Optional) is omitted whole when
	// the embed is nil, whatever its kind. [jsonv2.OmitZeroStructFields]
	// makes every field behave as ,omitzero, and every Go type has a zero
	// value, so no required entry survives it.
	required := (!fi.Omitempty || !g.omitsZero(fi)) &&
		!fi.Omitzero && !fi.Optional && !g.omitZeroFields
	if required {
		parent.payload.Required = append(parent.payload.Required, fi.JSONName)
	}

	parent.payload.PropertyOrder = append(parent.payload.PropertyOrder, fi.JSONName)
	parent.props = append(parent.props, nodeProp{
		name:   fi.JSONName,
		schema: fieldNode,
		fi:     fi,
		quoted: stringOverride,
	})

	return nil
}

// applyTypeOverrideDirective applies the type= pairs of a field's jsonschema
// tag to a view of the field's base and rebuilds the node over it, so the
// override is a fact of the graph before the null pass runs. The rebuilt node
// keeps the node it replaced, whose decision the tag's other directives read
// in [run.applyFieldTag]. A tag with no valid type= pair, or one the
// parser refuses, leaves the node as built.
func (g *run) applyTypeOverrideDirective(fieldNode *node, fi fieldset.Field) *node {
	tag, ok := fi.StructField.Tag.Lookup("jsonschema")
	if !ok {
		return fieldNode
	}

	// A malformed tag is reported where the tag is applied, in
	// [run.applyFieldTag], which parses it again.
	directives, _, err := tagparse.Parse(tag)
	if err != nil {
		return fieldNode
	}

	var view *Schema

	for _, d := range directives {
		if d.Key != keyword.Type || !typename.Valid(d.Value) {
			continue
		}

		if view == nil {
			view = fieldNode.view(g.draft)
		}

		tagparse.ApplyTypeOverride(view, d.Value)
	}

	if view == nil {
		return fieldNode
	}

	rebuilt := rebuildOverriddenField(fieldNode, view)
	rebuilt.overrode = fieldNode

	return rebuilt
}

// applyFieldHooks runs the field-level hooks over every struct in the graph
// once every null decision is final: the description provider and the
// jsonschema tag's remaining directives for each field, then the tag
// interpreters, each seeing a view of the enclosing object with every sibling
// in place. A struct is visited once, in walk order from the root, then each
// def not reached from it. An error names the struct whose schema carries the
// field, behind the field path from the root.
func (g *run) applyFieldHooks(root *node) error {
	seen := map[*defEntry]bool{}

	err := g.hookNodes(root, "", seen)
	if err != nil {
		return err
	}

	for _, e := range g.defs {
		if seen[e] {
			continue
		}

		seen[e] = true

		err := g.hookNodes(e.body, "", seen)
		if err != nil {
			return err
		}
	}

	return nil
}

// hookNodes walks the graph beneath n, running the hooks of every object node
// it reaches; prefix is the field path to n, for error reports.
func (g *run) hookNodes(n *node, prefix string, seen map[*defEntry]bool) error {
	if n == nil {
		return nil
	}

	if n.def != nil && !seen[n.def] {
		seen[n.def] = true

		err := g.hookNodes(n.def.body, prefix, seen)
		if err != nil {
			return err
		}
	}

	if n.kind == kindObject {
		err := g.hookStruct(n, prefix)
		if err != nil {
			return err
		}
	}

	err := g.hookNodes(n.items, prefix, seen)
	if err != nil {
		return err
	}

	for i := range n.props {
		err := g.hookNodes(n.props[i].schema, prefix+fmt.Sprintf("field %q: ", n.props[i].name), seen)
		if err != nil {
			return err
		}
	}

	for _, c := range n.prefix {
		err := g.hookNodes(c, prefix, seen)
		if err != nil {
			return err
		}
	}

	for _, e := range n.embeds {
		err := g.hookNodes(e.branch, prefix, seen)
		if err != nil {
			return err
		}
	}

	return nil
}

// hookStruct runs the field-level hooks of one object node's fields. A field
// hooked already (reached through another node sharing its props) is skipped.
func (g *run) hookStruct(obj *node, prefix string) error {
	parentView := obj.view(g.draft)

	for i := range obj.props {
		p := &obj.props[i]
		if p.schema.hooked {
			continue
		}

		err := g.applyFieldDescription(obj.typ, p.fi, p.schema, parentView)
		if err != nil {
			return err
		}

		err = g.applyFieldTag(*p)
		if err != nil {
			return fmt.Errorf("%s%s field %q: %w", prefix, obj.typ, p.name, err)
		}
	}

	for i := range obj.props {
		p := &obj.props[i]
		if p.schema.hooked {
			continue
		}

		p.schema.hooked = true

		err := g.applyFieldInterpreters(obj.typ, p.fi, p.schema, parentView, obj, p.quoted)
		if err != nil {
			return fmt.Errorf("%s%s field %q: %w", prefix, obj.typ, p.name, err)
		}
	}

	return nil
}

// applyFieldTag applies the jsonschema struct tag to a field: facts land on
// the authored canvas, and the tag's type= pair, applied to the node in
// [run.applyTypeOverrideDirective], replays on a discarded view of the
// occurrence the pair replaced, so the fold keeps its ordering rules and a
// directive before the pair classifies against that occurrence and reads its
// null decision.
func (g *run) applyFieldTag(p nodeProp) error {
	tag, ok := p.fi.StructField.Tag.Lookup("jsonschema")
	if !ok {
		return nil
	}

	fieldNode := p.schema

	decided := fieldNode
	if fieldNode.overrode != nil {
		decided = fieldNode.overrode
	}

	err := tagparse.Apply(tagparse.Input{
		Tag:       tag,
		FieldType: p.fi.StructField.Type,
		Canvas:    fieldNode.authored,
		Payload:   decided.view(g.draft),
		Quoted:    p.quoted,
		// FieldContext.Shape reads this same decision, so a field's null
		// admission is one answer whichever site classifies it.
		Nullable: decided.null.admit,
	})
	if err != nil {
		// Tagparse carries its own ErrInvalidType sentinel; map it onto the
		// package's exported ErrInvalidType so errors.Is keeps working.
		if errors.Is(err, tagparse.ErrInvalidType) {
			err = fmt.Errorf("%w: %w", ErrInvalidType, err)
		}

		// Tagparse errors already carry the "jsonschema tag:" prefix.
		return err
	}

	return nil
}

// stringOptionField answers a field carrying json:",string". It returns the
// node a hook built for a type v2 refuses under the flag (nil otherwise),
// whether the field takes the string override, and the refusal when neither
// applies; see the override discussion in [run.buildFieldSchema].
func (g *run) stringOptionField(fi fieldset.Field) (*node, bool, error) {
	stringified, probeErr := g.probe.Field(fi.StructField)
	if probeErr == nil {
		return nil, stringified, nil
	}

	fieldType := fi.StructField.Type

	fieldNode, err := g.schemaForType(fieldType, false)
	if err != nil {
		return nil, false, err
	}

	if !g.hookDeclares(numkind.DerefType(fieldType)) {
		return nil, false, fmt.Errorf("%w: %w", ErrInvalidJSONField, probeErr)
	}

	return fieldNode, false, nil
}

// omitsZero reports whether json:",omitempty" ever omits the field: the v2
// probe marshals the field's zero value and reports whether the member was
// dropped. A zero value v2 refuses belongs to a type a hook declared (the
// kind-based build refused it already), and v2 never marshals the struct at
// all, so the field stays required.
func (g *run) omitsZero(fi fieldset.Field) bool {
	omitted, err := g.probe.OmitsZero(fi.StructField)

	return err == nil && omitted
}

// rebuildOverriddenField rebuilds a field node after a type= override replaced
// its type wholesale: a plain value node over the overridden view, dropping
// the def link, children, and null bits in one place, while carrying the
// authored canvas across. Reachability drops any def the detached ref
// orphaned. A type=array override on a sequence field keeps the element
// structure (applyTypeOverride drops it for every other type, which the
// view's cleared element slot reports), and the authored element canvases can
// carry a redirected element enum; those element nodes are kept so reconcile
// still composes them per child instead of silently dropping the author's
// enum. The view's own element slots are cleared, since render fills them from
// the kept nodes.
func rebuildOverriddenField(fieldNode *node, payload *Schema) *node {
	rebuilt := &node{
		kind:     kindValue,
		payload:  payload,
		authored: fieldNode.authored,
		isField:  true,
	}

	switch {
	case fieldNode.kind == kindList && payload.Items != nil:
		rebuilt.kind = kindList
		rebuilt.items = fieldNode.items
		payload.Items = nil

	case fieldNode.kind == kindTuple && (len(payload.PrefixItems) > 0 || len(payload.ItemsArray) > 0):
		rebuilt.kind = kindTuple
		rebuilt.prefix = fieldNode.prefix
		payload.PrefixItems = nil
		payload.ItemsArray = nil

	case fieldNode.kind == kindObject && fieldNode.items != nil:
		// An inline struct field carrying an embedded fallback keeps the
		// fallback value node, so render still fills the extra-member slot
		// from it (null wrap and final $ref name included).
		rebuilt.kind = kindObject
		rebuilt.items = fieldNode.items
		rebuilt.fallback = fieldNode.fallback
		payload.AdditionalProperties = nil
		payload.UnevaluatedProperties = nil
	}

	return rebuilt
}

// fieldContext builds the FieldContext passed to tag interpreters and the
// description provider for one struct field, computing the declaring type once.
// Canvas is the field's authored canvas (where a hook declares its facts) and
// Base is a private view of the type-derived base; the accessor exposing
// element canvases reads the field node's element children. Parent is the
// caller's view of the enclosing object.
func (g *run) fieldContext(
	parentType reflect.Type,
	fi fieldset.Field,
	fieldNode *node,
	parent *Schema,
	quoted bool,
) FieldContext {
	return FieldContext{
		Name:        fi.JSONName,
		Type:        fi.StructField.Type,
		Owner:       reflectkind.DeclaringType(parentType, fi.StructField),
		Canvas:      fieldNode.authored,
		Base:        fieldNode.view(g.draft),
		Parent:      parent,
		StructField: fi.StructField,
		Draft:       g.draft,
		node:        fieldNode,
		quoted:      quoted,
	}
}

// applyFieldInterpreters runs the registered tag interpreters for a field on its
// authored canvas. It runs after every field node is built and tagged, so an
// interpreter sees the full sibling set through parentView. The one write an
// interpreter makes through the parent, a name appended to Required, is read
// back onto the object node after each call. Const/enum placement and the
// Draft-07 $ref wrap are handled by render, from the complete graph.
func (g *run) applyFieldInterpreters(
	parentType reflect.Type,
	fi fieldset.Field,
	fieldNode *node,
	parentView *Schema,
	parent *node,
	quoted bool,
) error {
	for _, reg := range g.tagInterpreters {
		tag, ok := fi.StructField.Tag.Lookup(reg.key)
		if !ok {
			continue
		}

		fc := g.fieldContext(parentType, fi, fieldNode, parentView, quoted)

		err := reg.interp.Interpret(g.ctx, fc, Tag{Key: reg.key, Value: tag})
		if err != nil {
			return fmt.Errorf("tag interpreter %q: %w", reg.key, err)
		}

		for _, name := range parentView.Required {
			if !slices.Contains(parent.payload.Required, name) {
				parent.payload.Required = append(parent.payload.Required, name)
			}
		}
	}

	return nil
}

// wrapRefForDraft7 wraps a bare $ref with allOf if sibling keywords were added
// and the draft is Draft-07 (where $ref siblings are ignored). It serves a
// property a hook declared as a literal, where a seeded default lands beside
// the $ref the hook wrote; render's renderRef handles the field path itself.
func (g *run) wrapRefForDraft7(s *Schema) {
	if g.profile.honorRefSiblings || s.Ref == "" {
		return
	}

	if !schemashape.HasRefSiblings(s) {
		return
	}

	inner := &Schema{Ref: s.Ref}
	s.AllOf = append(s.AllOf, inner)
	s.Ref = ""
}

// processAllOfField handles embedded structs that need allOf composition.
//
// An embed reached through a pointer (fi.Optional) contributes nothing to the
// marshaled object when the pointer is nil, so its schema cannot be an
// unconditional allOf branch. Such a branch would require the embed's
// properties in every instance and reject the nil-embed serialization. The
// branch is wrapped as anyOf[embedded, {}] instead: a non-nil embed matches
// the schema (and its annotations still flow to unevaluatedProperties), while
// a nil embed satisfies the empty alternative. The empty branch also admits a
// partial embed serialization under Draft-07 (which lacks unevaluated
// semantics); accepting those is the price of not rejecting valid documents.
//
// A shadowed embed (fi.Shadowed) takes the same wrap for the same reason: a
// real field wins one of the embed's promoted names, so the marshaled object
// carries the winner's value where the branch asserts the embed's
// constraints, and an unconditional branch would reject the type's own
// marshaled JSON.
func (g *run) processAllOfField(fi fieldset.Field, parent *node) error {
	// Collect composes only a struct or an unnamed pointer to one, so
	// IndirectType names the embed's type exactly as Collect recorded it.
	ft := reflectkind.IndirectType(fi.StructField.Type)

	branch, err := g.schemaForType(ft, false)
	if err != nil {
		return err
	}

	// The embed is composition, not nullability: render emits the optional
	// anyOf[branch, {}] from embedNode.optional, and the null pass gives a
	// composed branch no null (a pointer embed's optionality rides on
	// embedNode.optional, not the node's decision).
	branch.composed = true

	parent.embeds = append(parent.embeds, embedNode{branch: branch, optional: fi.Optional || fi.Shadowed})

	return nil
}

// hookDeclares reports whether a caller-registered hook declares t's schema: a
// type provider registered through [WithTypeSchemaProvider] or
// [WithTypeSchemaFor], or the type's own [JSONSchemaProvider] method. Such a
// type is exempt from the v2 probes, since the caller took over describing
// what it marshals to.
func (g *run) hookDeclares(t reflect.Type) bool {
	_, ok, err := g.resolveTypeSchema(t)

	return (ok && err == nil) || implementsProvider(t)
}

// implementsProvider checks if a type (or pointer to type) implements
// JSONSchemaProvider directly (not just via an embedded field).
func implementsProvider(t reflect.Type) bool {
	if !t.Implements(typeProvider) && !reflect.PointerTo(t).Implements(typeProvider) {
		return false
	}

	return reflectkind.HasDirectMethod(t, "JSONSchema")
}

// implementsExtender checks if a type (or pointer to type) implements
// JSONSchemaExtender directly (not just via an embedded field).
func implementsExtender(t reflect.Type) bool {
	if !t.Implements(typeExtender) && !reflect.PointerTo(t).Implements(typeExtender) {
		return false
	}

	return reflectkind.HasDirectMethod(t, "JSONSchemaExtend")
}

// callProvider calls JSONSchema on a zero value of the type. For interface
// types it returns a zero [TypeSchema] (unrestricted {}), since a nil interface
// cannot be called. An error the method itself returns is wrapped with the type
// and method so it locates the failing provider, matching [callExtender]. The
// user method runs against a zero value whose pointer fields are nil, so a
// method that dereferences such a field panics; the panic is recovered and
// returned as an error wrapping [ErrProviderPanic] so it surfaces from Generate
// rather than crashing the caller.
//
//nolint:nonamedreturns // Recover needs named returns.
func callProvider(ctx context.Context, tc TypeContext) (ts TypeSchema, err error) {
	t := tc.Type
	if t.Kind() == reflect.Interface {
		return TypeSchema{}, nil
	}

	defer func() {
		if r := recover(); r != nil {
			ts = TypeSchema{}
			err = fmt.Errorf("%w: %s.JSONSchema: %v", ErrProviderPanic, t, r)
		}
	}()

	var v reflect.Value

	if t.Implements(typeProvider) {
		v = reflect.New(t).Elem()
	} else {
		v = reflect.New(t)
	}

	results := v.MethodByName("JSONSchema").
		Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(tc)})

	provErr, ok := reflect.TypeAssert[error](results[1])
	if ok && provErr != nil {
		return TypeSchema{}, fmt.Errorf("%s.JSONSchema: %w", t, provErr)
	}

	res, ok := reflect.TypeAssert[TypeSchema](results[0])
	if !ok {
		return TypeSchema{}, nil
	}

	return res, nil
}

// callExtender calls JSONSchemaExtend on a zero value of the type. For
// interface types it returns nil without calling anything, since a nil
// interface cannot be called (reflect panics on a nil interface's Method);
// the reflected schema stands unextended, matching [callProvider]'s handling
// of an interface declaring JSONSchema. As with [callProvider], the method
// runs against a zero value, so a panic (for example dereferencing a nil
// pointer field) is recovered and returned as an error wrapping
// [ErrProviderPanic]. An error the method itself returns is wrapped with the
// type and method so it locates the failing extender.
func callExtender(ctx context.Context, tc TypeContext, ts *TypeSchema) (err error) {
	t := tc.Type
	if t.Kind() == reflect.Interface {
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %s.JSONSchemaExtend: %v", ErrProviderPanic, t, r)
		}
	}()

	var v reflect.Value

	if t.Implements(typeExtender) {
		v = reflect.New(t).Elem()
	} else {
		v = reflect.New(t)
	}

	results := v.MethodByName("JSONSchemaExtend").
		Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(tc), reflect.ValueOf(ts)})

	extErr, ok := reflect.TypeAssert[error](results[0])
	if ok && extErr != nil {
		return fmt.Errorf("%s.JSONSchemaExtend: %w", t, extErr)
	}

	return nil
}

// extendType runs type-level schema extension for a reflection-produced
// schema: the type's own JSONSchemaExtend when implemented, then the
// registered [TypeSchemaExtender] values ([WithTypeSchemaExtender]) in
// registration order, so a registered extender sees what the type's author
// produced and can adjust it. It is called from each reflection path
// (structs, built-in overrides, non-struct kinds named or not) and never from
// the provider paths (registered or on-type), which replace reflection
// entirely.
// The extenders mutate ts.Value in place and may set ts.Nullability to declare a
// nullability stance.
func (g *run) extendType(t reflect.Type, ts *TypeSchema) error {
	tc := TypeContext{Type: t, Draft: g.draft}

	if implementsExtender(t) {
		err := callExtender(g.ctx, tc, ts)
		if err != nil {
			return err
		}
	}

	for _, e := range g.typeExtenders {
		err := e.ExtendSchemaForType(g.ctx, tc, ts)
		if err != nil {
			return fmt.Errorf("extend type %s: %w", t, err)
		}
	}

	return nil
}

// extendTypeSchema runs [run.extendType] over a view of the node's
// type-derived base, wrapping it in a [TypeSchema] the extenders mutate. It
// returns the declared [Nullability] stance (NullFromReflection when no
// extender sets one) for the call site to fold into the node's null decision.
// The extenders' edits reach the node through [node.absorbView], which reads
// the view back against a pristine copy; an extender that clears ts.Value to
// nil leaves the node unchanged, since there is nothing to read back. With no
// extender registered for the run, nothing is built.
//
// The envelope enters with Verbatim and Ref nil: those declare a replacement
// schema, which only a provider supplies. An extender honors only Value and
// Nullability, so one that sets Verbatim or Ref is reported as a malformed
// TypeSchema rather than having the declaration silently ignored.
func (g *run) extendTypeSchema(t reflect.Type, n *node) (Nullability, error) {
	if len(g.typeExtenders) == 0 && !implementsExtender(t) {
		return NullFromReflection, nil
	}

	view := n.view(g.draft)
	pristine := n.view(g.draft)
	ts := &TypeSchema{Value: view}

	err := g.extendType(t, ts)
	if err != nil {
		return NullFromReflection, err
	}

	if ts.Verbatim != nil || ts.Ref != nil {
		return NullFromReflection, fmt.Errorf(
			"%w: extender for type %s sets Verbatim or Ref; an extender declares only Value and Nullability",
			ErrConflictingTypeSchema, t,
		)
	}

	if ts.Value != nil {
		n.absorbView(ts.Value, pristine, g.draft)
	}

	return ts.Nullability, nil
}

// jsonTagInfo holds parsed json tag information.
// ApplyTypeDescription sets the description from the comment provider on a
// type's schema. An empty comment leaves the description unset; a provider
// error aborts generation.
func (g *run) applyTypeDescription(t reflect.Type, s *Schema) error {
	if g.descriptionProvider == nil {
		return nil
	}

	comment, err := g.descriptionProvider.TypeDescription(g.ctx, TypeContext{Type: t, Draft: g.draft})
	if err != nil {
		return fmt.Errorf("describe type %s: %w", t, err)
	}

	if comment != "" {
		s.Description = comment
	}

	return nil
}

// applyFieldDescription sets the description from the comment provider on a
// field's authored canvas (the field description is wrapper-scoped, as reconcile
// places it). The provider receives the [FieldContext] tag interpreters get,
// with the tag pair empty and Owner the type declaring the field (see
// [reflectkind.DeclaringType]); an empty comment leaves the description unset, and a
// provider error aborts generation.
func (g *run) applyFieldDescription(
	parentType reflect.Type, fi fieldset.Field, fieldNode *node, parent *Schema,
) error {
	if g.descriptionProvider == nil {
		return nil
	}

	fc := g.fieldContext(parentType, fi, fieldNode, parent, fi.JSONString)

	comment, err := g.descriptionProvider.FieldDescription(g.ctx, fc)
	if err != nil {
		return fmt.Errorf("describe field %q of %s: %w", fi.JSONName, parentType, err)
	}

	if comment != "" {
		fieldNode.authored.Description = comment
	}

	return nil
}
