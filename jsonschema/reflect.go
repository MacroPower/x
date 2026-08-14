package jsonschema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

var (
	typeJSONRawMessage = reflect.TypeFor[json.RawMessage]()
	typeTime           = reflect.TypeFor[time.Time]()
	typeSlogLevel      = reflect.TypeFor[slog.Level]()
	typeJSONNumber     = reflect.TypeFor[json.Number]()
	typeBigInt         = reflect.TypeFor[big.Int]()
	typeBigRat         = reflect.TypeFor[big.Rat]()
	typeBigFloat       = reflect.TypeFor[big.Float]()
	typeProvider       = reflect.TypeFor[JSONSchemaProvider]()
	typeExtender       = reflect.TypeFor[JSONSchemaExtender]()

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

// generator holds the state for a single schema generation run.
type generator struct {
	// The caller's context for this generation run, passed to the
	// DescriptionProvider with every comment lookup.
	ctx context.Context

	typeProviders     []TypeSchemaProvider
	namer             Namer
	typeToDef         map[reflect.Type]*defEntry
	defs              []*defEntry
	typeOverrideCache map[reflect.Type]typeOverrideResult
	visiting          map[reflect.Type]bool
	// ShadowScan tracks the composed embed types whose promoted names are
	// being collected for shadow analysis, so a mutually-composed pair does
	// not recurse through its own analysis forever.
	shadowScan map[reflect.Type]bool
	// RefAliasing tracks the types whose [TypeSchema.Ref] alias is being
	// resolved, so an alias chain that reaches one of them again (a self-Ref, or
	// a mutual A -> B -> A cycle) is reported instead of recursing forever.
	refAliasing map[reflect.Type]bool
	// DefaultsFrom is the WithDefaultsFrom instance; defaultsFromSet
	// distinguishes an explicit nil instance from the option being absent.
	defaultsFrom         any
	descriptionProvider  DescriptionProvider
	tagInterpreters      []tagInterpreterRegistration
	typeExtenders        []TypeSchemaExtender
	draft                Draft
	profile              draftProfile // per-draft behavioral policy, resolved once from draft
	definitions          bool
	additionalProperties bool
	nullable             bool
	defaultsFromSet      bool
	rootTitle            bool
}

// typeOverrideResult memoizes one [generator.resolveTypeSchema] consultation so
// the registered type providers run at most once per type per run, even when the
// allOf-composition probe and the real build both ask about the same type.
type typeOverrideResult struct {
	err      error
	ts       TypeSchema
	resolved bool
}

// newGenerator returns a configuration-only generator prototype: the options
// are applied, but no per-run state is initialized. Each generation run
// derives its state from the prototype via [generator.forRun]. Nil options
// are skipped, so an optional option can be passed unconditionally.
func newGenerator(opts []GenerateOption) *generator {
	g := &generator{
		draft:       Draft2020,
		namer:       defaultNamerFunc(),
		definitions: true,
		nullable:    true,
	}

	for _, opt := range opts {
		if opt != nil {
			opt.applyGenerate(g)
		}
	}

	// Resolve the draft profile after options settle g.draft, so every
	// generation site reads the policy through g.profile rather than comparing
	// g.draft. Each run copies the prototype by value via forRun, carrying it
	// along.
	g.profile = g.draft.profile()

	return g
}

// forRun derives one generation run from the prototype. The configuration
// (providers, extenders, interpreters, namer, and flags) is shared, since
// generation only reads it. The per-run maps and the context are fresh, so
// concurrent runs from one prototype never share mutable state.
func (g *generator) forRun(ctx context.Context) *generator {
	run := *g
	run.ctx = ctx
	run.typeToDef = map[reflect.Type]*defEntry{}
	run.defs = nil
	run.typeOverrideCache = map[reflect.Type]typeOverrideResult{}
	run.visiting = map[reflect.Type]bool{}
	run.refAliasing = map[reflect.Type]bool{}
	run.shadowScan = map[reflect.Type]bool{}

	return &run
}

// generate produces the root schema for the given type.
func (g *generator) generate(t reflect.Type) (*Schema, error) {
	// A nil type carries no kind to reflect on; report it through the error
	// contract instead of panicking in numkind.DerefType.
	if t == nil {
		return nil, fmt.Errorf("%w: nil type", ErrUnsupportedType)
	}

	// Follow pointers for root type identity.
	rootType := numkind.DerefType(t)

	root, err := g.schemaForType(t, false)
	if err != nil {
		return nil, err
	}

	// Assign final $defs names (disambiguating collisions) before render emits
	// any $ref string. Names are keyed on defEntry identity, so reachability and
	// root inlining below key on identity too and need no renamed-entry lookup.
	g.assignDefNames()

	// Inline a root $ref whose def is reached from nowhere else; a self- or
	// mutually recursive root keeps its $ref so those references never dangle.
	root = g.maybeInlineRoot(root)

	schema := g.render(root)

	// Emit only the defs reachable from the final root graph. A def orphaned by
	// a type= override or by root inlining is never reached and so dropped.
	reached := g.collectReferencedDefs(root)
	for _, e := range reached {
		g.renderDef(e)
	}

	// Seed property defaults from the WithDefaultsFrom instance, resolving the
	// produced root schema to the object that carries the properties (through a
	// nullable wrapper, or to the $defs body of a recursive root).
	if g.defaultsFromSet {
		err := g.applyInstanceDefaults(g.defaultsFrom, rootType, g.rootDefaultsTarget(schema, root))
		if err != nil {
			return nil, err
		}
	}

	// Set the root title from the type name when WithRootTitle is enabled and
	// nothing else (WithTypeSchema, JSONSchemaProvider, an extender, or tags)
	// supplied one. Unnamed roots produce an empty name even after the
	// empty-answer deferral to the default namer, and stay untitled.
	if g.rootTitle {
		target := g.rootTitleTarget(schema, root)
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
			defs[e.name] = e.rendered
		}

		if g.profile.definitionsKeyword {
			schema.Definitions = defs
		} else {
			schema.Defs = defs
		}
	}

	return schema, nil
}

// rootDefaultsTarget resolves the schema that WithDefaultsFrom seeds. A
// pointer root under WithNullable generates an anyOf nullable wrapper whose
// value branch holds the object schema, so the target resolves through the
// wrapper first. When the resolved target is a $ref to a $defs entry (a
// pointer root's value branch, or a self-reference or mutual recursion kept
// the root from being inlined), the defaults land on that definition's
// properties, shared by every occurrence of the type.
func (g *generator) rootDefaultsTarget(schema *Schema, root *node) *Schema {
	// A self- or mutually recursive root stayed a $ref: seed the shared $defs
	// body so every occurrence of the type carries the defaults.
	if root.kind == kindRef {
		return root.def.rendered
	}

	// A pointer root under WithNullable renders as anyOf[value, {null}]; applyNull
	// always emits the null branch second, so the value branch is AnyOf[0]. No
	// hook wrapper reaches here, so no other ordering is possible.
	if root.nullable && len(schema.AnyOf) == 2 {
		return schema.AnyOf[0]
	}

	return schema
}

// rootTitleTarget resolves the schema that WithRootTitle titles. Draft-07
// readers ignore keywords beside $ref, so when a self-referential root stays
// a bare $ref into definitions, the title goes on the definitions entry it
// targets instead, shared by every occurrence of the type;
// [generator.rootDefaultsTarget] redirects the same way.
func (g *generator) rootTitleTarget(schema *Schema, root *node) *Schema {
	// Draft-07 readers ignore keywords beside a bare $ref, so a self-referential
	// root that stayed a $ref is titled on its $defs body instead.
	if !g.profile.honorRefSiblings && schema.Ref != "" && root.kind == kindRef {
		return root.def.rendered
	}

	return schema
}

// schemaForType produces the IR node for the given type. If nullable is true,
// the node carries the deferred null decision (render wraps a pointer/scalar in
// an anyOf with a null branch, and a slice or map in a ["null", base] type
// list); see [generator.applyNull].
//
//nolint:unparam // nullable is part of the API contract; callers pass false but the parameter is used internally after pointer unwrapping.
func (g *generator) schemaForType(t reflect.Type, nullable bool) (*node, error) {
	// Follow pointers. A pointer at any level makes the schema nullable.
	if t.Kind() == reflect.Pointer {
		nullable = g.nullable
	}

	t = numkind.DerefType(t)

	// A nil interface marshals as null, so an interface position is nullable
	// like a pointer. The bit matters only when a later step intercepts the
	// interface with a non-empty schema (an override, a provider declaration,
	// or a TextMarshaler method set): a plain interface reflects as {}, which
	// already admits null, and render dedups the wrapper away.
	if t.Kind() == reflect.Interface {
		nullable = g.nullable
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
			return g.refNode(e, nullable), nil
		}
	}

	// 1. Type provider override (WithTypeSchemaProvider / WithTypeSchema).
	ts, ok, err := g.resolveTypeSchema(t)
	if err != nil {
		return nil, err
	}

	if ok {
		return g.handleOverrideType(t, ts, nullable)
	}

	// 2. JSONSchemaProvider interface.
	if implementsProvider(t) {
		return g.handleProviderType(t, nullable)
	}

	// 3. Built-in overrides.
	if s, ok := g.builtinOverride(t); ok {
		return g.handleBuiltinType(t, s, nullable)
	}

	// 4. Marshaler methods promoted from an embedded field. A struct whose
	// method set includes a promoted MarshalJSON or MarshalText is serialized
	// by that method. Encoding/json resolves marshalers through the method set,
	// so the embedded type's marshaler takes over the whole struct, and
	// reflecting its fields would describe a shape that never appears.
	// A promoted json.Marshaler can emit any JSON value, so the schema is
	// unrestricted; a promoted TextMarshaler always emits a string. A type
	// that directly implements json.Marshaler is deliberately NOT handled
	// here: per the documented resolution priority it falls through to
	// kind-based reflection, and WithTypeSchema or JSONSchemaProvider is the
	// escape hatch for its real shape.
	if reflectkind.IsPromotedJSONMarshaler(t) {
		return g.handleBuiltinType(t, &Schema{}, nullable)
	}

	if reflectkind.IsPromotedTextMarshaler(t) && !reflectkind.ImplementsJSONMarshaler(t) {
		return g.handleBuiltinType(t, &Schema{Type: typename.String}, nullable)
	}

	// 5. TextMarshaler (direct implementation). A direct TextMarshaler
	// serializes as a string and shares the built-in path's type-level
	// post-processing (comments, extender, $defs extraction). A type that
	// also implements json.Marshaler is not a string: encoding/json prefers
	// MarshalJSON over MarshalText, so the text form never appears in the
	// output. Such a type falls through to kind-based reflection like any
	// other direct json.Marshaler, mirroring the guard on step 4's promoted
	// TextMarshaler branch.
	if reflectkind.IsDirectTextMarshaler(t) && !reflectkind.ImplementsJSONMarshaler(t) {
		s := &Schema{Type: typename.String}
		return g.handleBuiltinType(t, s, nullable)
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
			return g.refNode(g.newDefEntry(t), nullable), nil
		}

		g.visiting[t] = true
	}

	// 7. Kind-based reflection. A named non-struct type bound for $defs builds its
	// body bare: the definition is shared by every reference, so nullability
	// belongs on each reference node, not baked into the single shared body.
	// Baking it in would let whichever reference is processed first decide the
	// body's nullability for all of them, making the output depend on field
	// declaration order. Inlined types keep nullability on the node itself.
	//
	// A guarded (potentially cyclic) type is built bare too: a cycle detected
	// while building it routes it to $defs via the cyclic-fill path below. When
	// no cycle materializes and it stays inline, the withheld nullability is
	// restored on the node. This is a no-op for slices and maps (their builders
	// fold g.nullable into the node regardless of the build-time flag); only a
	// named array, which honors the flag, is affected, fixing an order-dependent
	// null in its shared body.
	extractBare := t.Kind() != reflect.Struct && t.Name() != "" && (g.shouldExtract(t) || guarded)

	kindNullable := nullable
	if extractBare {
		kindNullable = false
	}

	n, err := g.schemaForKind(t, kindNullable)
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
	//nolint:nestif // Sequential post-processing steps; flattening adds no clarity.
	if t.Kind() != reflect.Struct {
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
		// stance, folded into the node's null decision below.
		stance, err := g.extendTypeSchema(t, n.payload)
		if err != nil {
			return nil, err
		}

		// A cycle detected while building this type's element/value schema left a
		// placeholder def entry (created by a guarded re-entry). Fill it with the
		// now complete body and return a reference, mirroring the struct path.
		if e, cyclic := g.typeToDef[t]; cyclic {
			if e.body == nil {
				e.body = n
			}

			e.nullability = stance

			return g.refNode(e, nullable), nil
		}

		if g.shouldExtract(t) {
			return g.defineType(t, n, stance, nullable), nil
		}

		// Built bare as a guarded type but neither cyclic nor extracted, so it
		// stays inline: restore the nullability withheld during the bare build
		// (only the anyOf-styled kinds, a named array, honor it; slices and maps
		// carry their own container nullability on the node already), then fold
		// the extender's stance.
		base := n.nullable
		if extractBare && !n.nilableContainer() {
			base = nullable
		}

		n.nullable = combineNullable(stance, base)
	}

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
func (g *generator) resolveTypeSchema(t reflect.Type) (TypeSchema, bool, error) {
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
func (g *generator) resolveTypeSchemaUncached(t reflect.Type) (TypeSchema, bool, error) {
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
func (g *generator) handleOverrideType(t reflect.Type, ts TypeSchema, nullable bool) (*node, error) {
	return g.finishTypeOverride(t, ts, nullable)
}

// finishTypeOverride turns a resolved [TypeSchema] into an IR node. It is the
// single funnel for the WithTypeSchema override path and the JSONSchemaProvider
// path: it validates mutual exclusivity of Value/Verbatim/Ref
// ([ErrConflictingTypeSchema]) and dispatches on which one is set. A zero
// TypeSchema (nil Value) marks the type unrestricted ({}).
//
//   - Value (common case): clone the caller-shared schema, apply type-level
//     comments, and build a bare value node; nullability is the [TypeSchema.Nullability]
//     stance combined with the occurrence's pointer-ness, never a wrapper baked
//     into the payload.
//   - Verbatim: an opaque, fully-formed schema emitted exactly as given, with no
//     null encoding and no $defs extraction.
//   - Ref: a whole-type alias to another Go type, resolved to that type's node
//     edge so its definition stays reachable.
//
// The Value/Verbatim source is copied with the upstream shallow CloneSchemas,
// not the JSON round-trip cloneSchema used for remote refs: CloneSchemas
// preserves the caller's exact any-typed Enum/Const/Default values, whereas a
// round-trip would rewrite them (a Go int enum value would decode back as
// float64). CloneSchemas only deep-copies sub-schema fields, leaving the Enum,
// Const, Default, and Extra headers aliased to the caller's schema;
// CloneOverrideExtras copies those too, so a tag interpreter or
// JSONSchemaExtender that mutates them in place (appending to Enum, reassigning
// Const, writing into Extra) cannot reach back into an override or provider
// schema reused across Generate calls.
func (g *generator) finishTypeOverride(t reflect.Type, ts TypeSchema, nullable bool) (*node, error) {
	err := checkTypeSchemaExclusive(t, ts)
	if err != nil {
		return nil, err
	}

	// Verbatim: emitted exactly as authored, no null encoding, never extracted.
	if ts.Verbatim != nil {
		v := ts.Verbatim.CloneSchemas()
		schemashape.CloneOverrideExtras(v)

		return &node{kind: kindValue, payload: v, verbatim: true}, nil
	}

	// Ref: a whole-type alias resolved to a real node edge, keeping the target
	// definition reachable without a payload $ref-string scan.
	if ts.Ref != nil {
		return g.refTypeOverride(t, ts, nullable)
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

	vnode := &node{kind: kindValue, payload: s}

	if g.shouldExtract(t) {
		return g.defineType(t, vnode, ts.Nullability, nullable), nil
	}

	vnode.nullable = combineNullable(ts.Nullability, nullable)

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
// The node's nullability is the stance combined with the occurrence's
// pointer-ness (schemaForType computes null from ts.Ref's own pointer-ness and
// knows nothing of the stance, so the combine is applied here).
func (g *generator) refTypeOverride(t reflect.Type, ts TypeSchema, nullable bool) (*node, error) {
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

	ref, err := g.schemaForType(ts.Ref, nullable)
	if err != nil {
		return nil, err
	}

	if ref.kind != kindRef {
		return nil, fmt.Errorf(
			"%w: type %s Ref %s does not name an extractable type", ErrConflictingTypeSchema, t, ts.Ref)
	}

	// Fold the alias's own stance with the occurrence's pointer-ness into the
	// reference's ptrNullable, which nullableDecision reads. NullableDecision then
	// applies the aliased type's own recorded stance outermost, so the precedence
	// is target stance, then alias stance, then pointer-ness: a Ref alias inherits
	// the target type's stance, and its own Nullability applies only when the target
	// is NullFromReflection (the common case, where the folded value passes through
	// unchanged).
	ref.ptrNullable = combineNullable(ts.Nullability, nullable)

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
func (g *generator) handleProviderType(t reflect.Type, nullable bool) (*node, error) {
	provided, err := callProvider(g.ctx, TypeContext{Type: t, Draft: g.draft})
	if err != nil {
		return nil, err
	}

	return g.finishTypeOverride(t, provided, nullable)
}

// combineNullable resolves a [Nullability] stance against an occurrence's
// pointer-ness: NullAllowed always admits null, NullForbidden never does, and
// NullFromReflection defers to the pointer-ness (a pointer or interface position
// is nullable, everything else is not).
func combineNullable(stance Nullability, ptr bool) bool {
	switch stance {
	case NullAllowed:
		return true
	case NullForbidden:
		return false
	default:
		return ptr
	}
}

// handleBuiltinType processes a type with a built-in override, applying
// type-level post-processing (comments, extender, $defs extraction) per
// the processing order. The null encoding rides on the returned node's nullable
// bit; a schema that already admits null (the []byte override folds null into
// its type list) is deduped by render, never double-wrapped.
func (g *generator) handleBuiltinType(t reflect.Type, s *Schema, nullable bool) (*node, error) {
	value := &node{kind: kindValue, payload: s}

	// Comment lookup is keyed by named type; the extender runs for unnamed
	// builtin-produced schemas too (a registered extender for []byte is just
	// as applicable as one for a named type).
	if t.Name() != "" {
		err := g.applyTypeDescription(t, s)
		if err != nil {
			return nil, err
		}
	}

	stance, err := g.extendTypeSchema(t, s)
	if err != nil {
		return nil, err
	}

	if g.shouldExtract(t) {
		return g.defineType(t, value, stance, nullable), nil
	}

	value.nullable = combineNullable(stance, nullable)

	return value, nil
}

// byteSliceNode returns the IR node for a byte slice, which encoding/json
// renders as a base64-encoded string. It is a real nilable container: the node
// carries base "string" and a bare payload ({contentEncoding: base64}), so
// render encodes the container null as the ["null", "string"] type list (or the
// bare {type: string} when WithNullable is off), the same shape a slice or map
// uses. A []byte carries no const/enum, so the type-list encoding never has to
// flip to the anyOf form. The container null rides on the node's nullable bit
// (g.nullable, independent of the occurrence's pointer-ness), matching the
// nilable-container policy.
func (g *generator) byteSliceNode() *node {
	return &node{
		kind:     kindValue,
		payload:  &Schema{ContentEncoding: content.Base64},
		base:     typename.String,
		nullable: g.nullable,
	}
}

// builtinOverride returns a schema for well-known types, if applicable. A byte
// slice is deliberately not here: it is a nilable container, built through
// [generator.byteSliceNode] on the slice reflection path so both an exact []byte
// and a named byte-slice type share one encoding.
func (g *generator) builtinOverride(t reflect.Type) (*Schema, bool) {
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
	case typeJSONRawMessage:
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

// scalarNode wraps a bare scalar payload in a value node carrying the threaded
// nullable bit, so render applies the anyOf null wrapper if and only if the
// position is nullable.
func (g *generator) scalarNode(payload *Schema, nullable bool) *node {
	return &node{kind: kindValue, payload: payload, nullable: nullable}
}

// schemaForKind handles the kind-based reflection step, producing a node.
func (g *generator) schemaForKind(t reflect.Type, nullable bool) (*node, error) {
	switch t.Kind() {
	case reflect.Bool:
		return g.scalarNode(&Schema{Type: typename.Boolean}, nullable), nil

	case reflect.String:
		return g.scalarNode(&Schema{Type: typename.String}, nullable), nil

	case reflect.Int:
		// Plain int is platform-dependent (32 or 64 bit), so leave it unbounded.
		return g.scalarNode(&Schema{Type: typename.Integer}, nullable), nil

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

		return g.scalarNode(s, nullable), nil

	case reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint8, reflect.Uint16, reflect.Uint32:
		// Fixed-width integers whose full range float64 can name inclusively.
		b := inclusiveIntBounds[t.Kind()]
		return g.scalarNode(boundedInteger(b[0], b[1]), nullable), nil

	case reflect.Uint, reflect.Uintptr:
		// Uint/uintptr are platform-dependent; only a lower bound is certain.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0))}
		return g.scalarNode(s, nullable), nil

	case reflect.Uint64:
		// Float64 cannot represent MaxUint64 (2^64-1) exactly; see the Int64 case.
		// 2^64 is exactly representable, so an exclusive maximum of 2^64 admits
		// exactly v <= 2^64-1 = MaxUint64, including the boundary value.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0)), ExclusiveMaximum: new(exclusiveMaxUint64)}
		return g.scalarNode(s, nullable), nil

	case reflect.Float32, reflect.Float64:
		return g.scalarNode(&Schema{Type: typename.Number}, nullable), nil

	case reflect.Interface:
		return g.scalarNode(&Schema{}, nullable), nil

	case reflect.Slice:
		return g.schemaForSlice(t, nullable)

	case reflect.Array:
		return g.schemaForArray(t, nullable)

	case reflect.Map:
		return g.schemaForMap(t, nullable)

	case reflect.Struct:
		return g.schemaForStruct(t, nullable)

	default:
		// Func, chan, complex, and unsafe.Pointer have no JSON Schema
		// representation; encoding/json cannot marshal them either.
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, t)
	}
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

// schemaForSlice generates a schema for slice types.
//
//nolint:unparam // nullable is accepted for consistency with other schema-producing methods.
func (g *generator) schemaForSlice(t reflect.Type, nullable bool) (*node, error) {
	// Byte slices marshal to base64 strings in encoding/json. The element's kind
	// (uint8) drives this, not the slice's exact type, so named byte-slice types
	// (type Bytes []byte) and slices of named uint8 elements are base64 too,
	// with the marshaler-bearing-element exception the predicate carries.
	if reflectkind.IsBase64ByteSlice(t) {
		return g.byteSliceNode(), nil
	}

	// A slice is nil-able in Go, so it folds g.nullable into the node itself,
	// independent of the threaded flag: a bare non-pointer []int field still
	// produces the ["null","array"] type list.
	items, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("element type: %w", err)
	}

	return &node{
		kind:     kindList,
		payload:  &Schema{Items: items.payload},
		items:    items,
		nullable: g.nullable,
		base:     typename.Array,
	}, nil
}

// schemaForArray generates a schema for fixed-length array types as a tuple.
// Draft 2020-12 uses prefixItems with one entry per element; Draft-07 uses the
// items-as-array form. MinItems/maxItems pin the length. Each element schema is
// generated independently so the result is a tree (no shared sub-schema
// pointers), which the validator requires.
func (g *generator) schemaForArray(t reflect.Type, nullable bool) (*node, error) {
	n := t.Len()

	prefix := make([]*node, n)
	elems := make([]*Schema, n)

	for i := range prefix {
		item, err := g.schemaForType(t.Elem(), false)
		if err != nil {
			return nil, fmt.Errorf("element type: %w", err)
		}

		prefix[i] = item
		elems[i] = item.payload
	}

	s := &Schema{
		Type:     typename.Array,
		MinItems: new(n),
		MaxItems: new(n),
	}
	if !g.profile.prefixItemsTuple {
		s.ItemsArray = elems
	} else {
		s.PrefixItems = elems
	}

	// A fixed array is not nil-able in Go, so its null (a pointer to it) uses the
	// threaded flag and the anyOf encoding, unlike slices and maps.
	return &node{kind: kindTuple, payload: s, prefix: prefix, nullable: nullable}, nil
}

// schemaForMap generates a schema for map types.
//
//nolint:unparam // nullable is accepted for consistency with other schema-producing methods.
func (g *generator) schemaForMap(t reflect.Type, nullable bool) (*node, error) {
	if !reflectkind.IsValidMapKey(t.Key()) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMapKey, t.Key())
	}

	// A map is nil-able in Go, so like a slice it folds g.nullable into the node
	// itself, independent of the threaded flag.
	val, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("map value type: %w", err)
	}

	return &node{
		kind:     kindMap,
		payload:  &Schema{AdditionalProperties: val.payload},
		items:    val,
		nullable: g.nullable,
		base:     typename.Object,
	}, nil
}

// schemaForStruct generates a schema for struct types.
func (g *generator) schemaForStruct(t reflect.Type, nullable bool) (*node, error) {
	// Cycle detection: even when definitions are disabled, cyclic types must
	// emit $defs/$ref to prevent infinite recursion.
	if g.visiting[t] {
		return g.refNode(g.newDefEntry(t), nullable), nil
	}

	// Check for extraction to $defs.
	if g.shouldExtract(t) {
		// Check if already registered.
		if e, exists := g.typeToDef[t]; exists {
			return g.refNode(e, nullable), nil
		}

		g.visiting[t] = true

		obj, stance, err := g.buildStructSchema(t)
		if err != nil {
			return nil, err
		}

		delete(g.visiting, t)

		return g.defineType(t, obj, stance, nullable), nil
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
		if e.body == nil {
			e.body = obj
		}

		e.nullability = stance

		return g.refNode(e, nullable), nil
	}

	obj.nullable = combineNullable(stance, nullable)

	return obj, nil
}

// buildStructSchema builds the object node for a struct type, including
// type-level comment extraction and JSONSchemaExtend. The payload's Properties
// hold the child nodes' bare payloads (shared pointers), so a tag interpreter
// reading FieldContext.Parent sees the sibling shapes; render later overwrites
// each entry with its rendered child.
func (g *generator) buildStructSchema(t reflect.Type) (*node, Nullability, error) {
	s := &Schema{
		Type: typename.Object,
	}

	// Set additionalProperties: false (unless opted out).
	if !g.additionalProperties {
		s.AdditionalProperties = &Schema{Not: &Schema{}}
	}

	obj := &node{kind: kindObject, payload: s}

	// Process fields using encoding/json rules.
	//
	// Two passes: first build every field's schema and populate Properties,
	// then run tag interpreters. This ensures a tag interpreter observing
	// FieldContext.Parent sees the complete sibling property set regardless of
	// field order.
	fields := g.collectStructFields(t)

	var hasAllOf, hasShadowPartial bool

	type pendingField struct {
		node *node
		fi   structFieldInfo
	}

	var pending []pendingField

	for idx := range fields {
		if fields[idx].composeViaAllOf {
			err := g.processAllOfField(fields[idx], obj)
			if err != nil {
				return nil, NullFromReflection, fmt.Errorf("embedded %s: %w", fields[idx].field.Type, err)
			}

			hasAllOf = true
			hasShadowPartial = hasShadowPartial || fields[idx].shadowPartial

			continue
		}

		fieldNode, err := g.buildFieldSchema(t, fields[idx], obj)
		if err != nil {
			return nil, NullFromReflection, fmt.Errorf("field %q: %w", fields[idx].jsonName, err)
		}

		pending = append(pending, pendingField{fi: fields[idx], node: fieldNode})
	}

	for i := range pending {
		pf := &pending[i]

		err := g.applyFieldInterpreters(t, pf.fi, pf.node, obj)
		if err != nil {
			return nil, NullFromReflection, fmt.Errorf("field %q: %w", pf.fi.jsonName, err)
		}
	}

	// Handle allOf + additionalProperties interaction. Decided here on embed
	// presence and carried on the payload; render appends the embed branches.
	if hasAllOf && !g.additionalProperties {
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

		default:
			// Draft-07: omit additionalProperties when allOf is in use.
			s.AdditionalProperties = nil
		}
	}

	// Type-level comment.
	err := g.applyTypeDescription(t, s)
	if err != nil {
		return nil, NullFromReflection, err
	}

	// JSONSchemaExtend, then registered extenders. An extender may declare a
	// nullability stance, returned for the caller to fold into the reference or
	// inline node's null decision.
	stance, err := g.extendTypeSchema(t, s)
	if err != nil {
		return nil, NullFromReflection, err
	}

	return obj, stance, nil
}

// structFieldInfo holds processed information about a struct field.
type structFieldInfo struct {
	jsonName        string
	field           reflect.StructField
	omitempty       bool
	omitzero        bool
	jsonString      bool
	composeViaAllOf bool
	// Tagged records whether the JSON name came from an explicit json tag
	// name, the input to encoding/json's same-depth tie-break. It is carried
	// so a composed embed's promoted fields (ghost sightings in the enclosing
	// struct's resolution) replay the tie-break correctly.
	tagged bool
	// Optional is true for an allOf-composed embed reached through a
	// pointer-typed embedded field (directly or via an enclosing pointer
	// embed). Encoding/json omits the embed's entire contribution when the
	// pointer is nil, so the composed schema must not be unconditionally
	// required. Regular fields fold this into omitempty instead.
	optional bool
	// Shadowed marks an allOf-composed embed at least one of whose promoted
	// JSON names loses encoding/json's field resolution to a real field: the
	// marshaled object carries the winner's value under that name (or drops
	// the name on an ambiguity tie), so the composed schema's claim on it
	// does not hold and the branch must not be unconditional.
	shadowed bool
	// ShadowPartial marks a shadowed composed embed that still promotes at
	// least one unshadowed name. Only the (now conditional) branch evaluates
	// that name, so the parent object must stay open or a failing branch
	// would leave it unevaluated and rejected.
	shadowPartial bool
}

// collectStructFields mimics encoding/json's field collection logic,
// handling promotion, shadowing, and ambiguity.
//
// The walk is breadth-first level by level, matching encoding/json's
// typeFields: all fields at depth d are recorded before any embed at depth
// d+1 is descended into, and a struct type is processed only once, at its
// shallowest occurrence. A depth-first walk would mark a deep occurrence of
// a type as visited and then skip a shallower embed of the same type,
// silently dropping fields that encoding/json promotes.
//
//nolint:nestif // Mirrors encoding/json's field collection logic which is inherently nested.
func (g *generator) collectStructFields(t reflect.Type) []structFieldInfo {
	// Mark t in-progress so the ghost collection below (which re-enters this
	// method for composed embed types) terminates on a self- or mutually
	// composed cycle instead of recursing without bound.
	if owned := !g.shadowScan[t]; owned {
		g.shadowScan[t] = true
		defer delete(g.shadowScan, t)
	}

	type fieldLevel struct {
		// GhostOwner names the composed embed type a ghost sighting belongs
		// to; see the ghost flag below.
		ghostOwner reflect.Type
		field      reflect.StructField
		depth      int
		// Optional is true when the field is promoted through a pointer-typed
		// embedded struct. Such fields are omitted by encoding/json when the
		// embedded pointer is nil, so they are not required.
		optional bool
		// Tagged is true when the field's JSON name comes from an explicit json
		// tag name rather than the Go field name. Encoding/json's tie-break for
		// fields colliding on a JSON name at the same depth keeps the field only
		// if exactly one of them is tagged; this records the input to that rule.
		tagged bool
		// ComposeAllOf marks a synthetic sighting for an embedded type composed
		// via allOf rather than a real promoted field. It is carried explicitly
		// instead of being inferred from the synthetic name's prefix, so a user
		// field whose JSON name happens to start with that prefix is not
		// misclassified as a composition.
		composeAllOf bool
		// Ghost marks a sighting of a composed embed's promoted JSON name.
		// Encoding/json promotes those fields normally, so they compete in
		// name resolution (shadowing deeper real fields, annihilating on
		// ties), but a winning ghost never becomes a property: the embed's
		// allOf branch carries its assertion.
		ghost bool
	}

	// A fieldKey groups sightings; the composeAllOf flag puts synthetic allOf
	// compositions in a namespace disjoint from real JSON names, so a user field
	// whose JSON name equals a composition's synthetic key cannot collide with it
	// and shadow the composition in the same-depth tie-break.
	type fieldKey struct {
		//nolint:unused // Read via struct equality when used as a map key.
		name string
		//nolint:unused // Read via struct equality when used as a map key.
		composeAllOf bool
	}

	// Collect all visible fields grouped by JSON name.
	byName := map[fieldKey][]fieldLevel{}

	var order []fieldKey

	// Record adds a sighting of a JSON name. The dup flag marks fields of a
	// struct type embedded more than once at the same depth: the sighting is
	// recorded twice so the same-depth ambiguity resolution below drops the
	// name, matching encoding/json's annihilation of fields from repeated
	// embeds.
	record := func(name string, fl fieldLevel, dup bool) {
		key := fieldKey{composeAllOf: fl.composeAllOf, name: name}
		if _, seen := byName[key]; !seen {
			order = append(order, key)
		}

		byName[key] = append(byName[key], fl)
		if dup {
			byName[key] = append(byName[key], fl)
		}
	}

	// Embedded types composed via allOf get a synthetic byName key from
	// allOfName. The key is stable per type, so the same type composed at one
	// depth collides into a single name and its two sightings annihilate as
	// ambiguous, matching encoding/json's treatment of a type embedded twice at
	// the same depth; a deeper re-occurrence is shadowed by the shallower one.
	// The per-type index keeps distinct types apart even when their names match
	// across packages.
	// GhostPromoted memoizes each composed embed type's promoted fields,
	// collected once for the ghost sightings and reused by the shadow marking.
	ghostPromoted := map[reflect.Type][]structFieldInfo{}

	allOfNames := map[reflect.Type]string{}
	allOfName := func(ft reflect.Type) string {
		if n, ok := allOfNames[ft]; ok {
			return n
		}

		n := fmt.Sprintf("__allof__%s__%d", ft.Name(), len(allOfNames))
		allOfNames[ft] = n

		return n
	}

	// EmbedEntry is a struct type queued for processing at the next depth.
	type embedEntry struct {
		typ      reflect.Type
		index    []int
		optional bool
	}

	// Visited tracks every struct type processed during the walk. A type is
	// processed only at its shallowest level: a deeper re-occurrence (including
	// a self-embedding type T struct{ *T; X int }) is skipped, because its
	// fields are shadowed by the shallower ones, matching encoding/json.
	visited := map[reflect.Type]bool{}

	next := []embedEntry{{typ: t}}

	var count, nextCount map[reflect.Type]int

	for depth := 0; len(next) > 0; depth++ {
		current := next
		next = nil
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, e := range current {
			if visited[e.typ] {
				continue
			}

			visited[e.typ] = true

			// A type embedded more than once at this depth contributes every
			// field twice so the resolution drops them all as ambiguous.
			dup := count[e.typ] > 1

			for i := range e.typ.NumField() {
				f := e.typ.Field(i)
				fieldIndex := append(slices.Clone(e.index), i)
				f.Index = fieldIndex

				if f.Anonymous {
					// Embedded field.
					ft := f.Type
					embeddedViaPointer := ft.Kind() == reflect.Pointer
					if embeddedViaPointer {
						ft = ft.Elem()
					}

					// Skip unexported embedded non-struct types, matching
					// encoding/json behavior. Unexported embedded structs
					// still have their exported fields promoted.
					if !f.IsExported() && ft.Kind() != reflect.Struct {
						continue
					}

					tagVal, hasTag := f.Tag.Lookup("json")
					explicitName, _, _ := strings.Cut(tagVal, ",")
					if hasTag && jsontag.ValidName(explicitName) {
						// Embedded struct with an explicit json name → treated as a
						// regular named field; encoding/json does not promote it. An
						// options-only tag (json:",omitempty") has no name -- and a
						// name encoding/json rejects as invalid is discarded the same
						// way -- so both fall through to promotion below, matching
						// encoding/json.
						info := jsontag.Parse(f)
						if info.JSONName == "" {
							continue // json:"-"
						}

						record(
							info.JSONName,
							fieldLevel{field: f, depth: depth, optional: e.optional, tagged: true},
							dup,
						)

						continue
					}

					if ft.Kind() == reflect.Struct {
						// Check if this embedded struct needs to be composed via allOf.
						if g.needsAllOfComposition(ft) {
							// Compose via allOf: treat as a single entry. A pointer
							// embed makes the composition optional: a nil pointer
							// contributes nothing to the marshaled object.
							record(
								allOfName(ft),
								fieldLevel{
									field:        f,
									depth:        depth,
									optional:     e.optional || embeddedViaPointer,
									composeAllOf: true,
								},
								false,
							)

							// Ghost sightings: encoding/json still promotes the
							// composed embed's fields normally, so its names
							// compete in resolution against real fields and
							// other embeds. A skipped scan (a self- or mutually
							// composed type) leaves its names out, keeping the
							// pre-ghost conservative behavior for that cycle.
							if !g.shadowScan[ft] {
								promoted := g.collectStructFields(ft)
								ghostPromoted[ft] = promoted

								for pi := range promoted {
									p := &promoted[pi]
									if p.composeViaAllOf {
										// A nested composition's names are
										// opaque to this analysis.
										continue
									}

									record(p.jsonName, fieldLevel{
										depth:      depth + len(p.field.Index),
										tagged:     p.tagged,
										ghost:      true,
										ghostOwner: ft,
									}, dup)
								}
							}

							continue
						}

						// Queue for the next depth. A type queued more than once at
						// the same depth is processed once but counted, so its fields
						// annihilate as ambiguous, matching encoding/json.
						nextCount[ft]++
						if nextCount[ft] == 1 {
							// Fields reached through a pointer embed are optional
							// because a nil embed omits them entirely.
							next = append(
								next,
								embedEntry{typ: ft, index: fieldIndex, optional: e.optional || embeddedViaPointer},
							)
						}

						continue
					}

					// Embedded non-struct type (interfaces included): encoding/json
					// records it as a regular leaf field under the field name, never
					// flattened, so it participates in normal shadowing and
					// ambiguity resolution. The field name is the unqualified type
					// identifier, without the type arguments [reflect.Type.Name]
					// carries for an instantiated generic type.
					record(f.Name, fieldLevel{field: f, depth: depth, optional: e.optional}, dup)

					continue
				}

				if !f.IsExported() {
					continue
				}

				info := jsontag.Parse(f)
				if info.JSONName == "" {
					continue // json:"-"
				}

				record(
					info.JSONName,
					fieldLevel{field: f, depth: depth, optional: e.optional, tagged: info.TaggedName},
					dup,
				)
			}
		}
	}

	// Resolve shadowing and ambiguity. Each real JSON name's outcome is
	// recorded for the shadow marking below: which depth claimed the name,
	// whether a same-depth ambiguity annihilated it, and whether the winner
	// was a composed embed's ghost.
	var result []structFieldInfo

	outcomes := map[string]nameOutcome{}

	for _, key := range order {
		candidates := byName[key]
		if len(candidates) == 0 {
			continue
		}

		// Find minimum depth.
		minDepth := candidates[0].depth
		for ci := 1; ci < len(candidates); ci++ {
			if candidates[ci].depth < minDepth {
				minDepth = candidates[ci].depth
			}
		}

		// Filter to only those at minimum depth.
		var atMin []fieldLevel

		for ci := range candidates {
			if candidates[ci].depth == minDepth {
				atMin = append(atMin, candidates[ci])
			}
		}

		// Multiple fields collide on this JSON name at the shallowest depth.
		// Encoding/json breaks the tie by explicit tag: if exactly one of them
		// has an explicit json tag name, that field wins; if none or more than
		// one is tagged, they are all dropped as ambiguous.
		if len(atMin) > 1 {
			var tagged []fieldLevel

			for ci := range atMin {
				if atMin[ci].tagged {
					tagged = append(tagged, atMin[ci])
				}
			}

			if len(tagged) != 1 {
				if !key.composeAllOf {
					outcomes[key.name] = nameOutcome{depth: minDepth, annihilated: true}
				}

				continue
			}

			atMin = tagged
		}

		if atMin[0].ghost {
			// A composed embed's promoted field won the name: no property is
			// emitted (the embed's allOf branch carries the assertion), and
			// the real fields it defeated stay out of the result, matching
			// the value encoding/json actually marshals under the name.
			outcomes[key.name] = nameOutcome{depth: minDepth, ghostOwner: atMin[0].ghostOwner}

			continue
		}

		f := atMin[0].field
		isAllOf := atMin[0].composeAllOf

		if isAllOf {
			result = append(result, structFieldInfo{
				field:           f,
				composeViaAllOf: true,
				optional:        atMin[0].optional,
			})

			continue
		}

		info := jsontag.Parse(f)
		if info.JSONName == "" {
			continue
		}

		outcomes[info.JSONName] = nameOutcome{depth: minDepth}

		sfi := structFieldInfo{
			field:      f,
			jsonName:   info.JSONName,
			omitempty:  info.Omitempty || atMin[0].optional,
			omitzero:   info.Omitzero,
			jsonString: info.JSONString,
			tagged:     atMin[0].tagged,
		}
		result = append(result, sfi)
	}

	// The breadth-first walk sights names level by level, so `order` lists all
	// depth-0 names before any promoted ones. Sorting the winners by their
	// index path restores source declaration order, matching encoding/json's
	// byIndex ordering. A promoted field sorts at its embed's position.
	slices.SortStableFunc(result, func(a, b structFieldInfo) int {
		return slices.Compare(a.field.Index, b.field.Index)
	})

	g.markShadowedCompositions(result, outcomes, ghostPromoted)

	return result
}

// nameOutcome is the resolution result for one real JSON name: the depth that
// claimed it, whether a same-depth ambiguity annihilated it (the name never
// appears in the marshaled object), and, when a composed embed's ghost won,
// which embed type owns it.
type nameOutcome struct {
	ghostOwner  reflect.Type
	depth       int
	annihilated bool
}

// markShadowedCompositions flags each allOf-composed embed whose promoted
// JSON names encoding/json resolves away from the embed: a shallower (or
// same-depth winning) real field carries the winner's value under the name,
// an annihilated name is dropped from the marshaled object entirely, and
// another embed's winning ghost carries that embed's value. In each case the
// composed branch would assert this embed's constraints against a value it
// does not produce, so the branch must not be unconditional. The outcomes map
// is the enclosing resolution's per-name verdict (ghost sightings included,
// so the tag tie-break is already replayed), and ghostPromoted carries each
// scanned embed type's promoted fields; a type absent from it was skipped as
// a self- or mutually composed cycle and keeps its unconditional branch, as
// before.
func (g *generator) markShadowedCompositions(
	fields []structFieldInfo,
	outcomes map[string]nameOutcome,
	ghostPromoted map[reflect.Type][]structFieldInfo,
) {
	for i := range fields {
		fi := &fields[i]
		if !fi.composeViaAllOf {
			continue
		}

		ft := fi.field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		promoted, scanned := ghostPromoted[ft]
		if !scanned {
			continue
		}

		embedDepth := len(fi.field.Index) - 1

		var shadowedAny, unshadowedAny bool

		for j := range promoted {
			p := &promoted[j]
			if p.composeViaAllOf {
				// A nested composition's names are opaque to this analysis;
				// assume it contributes to the marshaled object.
				unshadowedAny = true

				continue
			}

			// The promoted name sits at the embed's depth plus its own depth
			// within the embed type.
			de := embedDepth + len(p.field.Index)

			out, ok := outcomes[p.jsonName]

			switch {
			case !ok:
				// No outcome recorded: the name resolved to nothing (its
				// ghost was skipped with this scan); keep the branch's claim.
				unshadowedAny = true
			case !out.annihilated && out.ghostOwner == ft && out.depth == de:
				// This embed's own ghost won the name, so the marshaled
				// object carries the embed's value there.
				unshadowedAny = true
			case out.depth <= de:
				// A real field won the tie-break, the name annihilated, or
				// another embed claimed it at or above this depth.
				shadowedAny = true
			default:
				unshadowedAny = true
			}
		}

		fi.shadowed = shadowedAny
		fi.shadowPartial = shadowedAny && unshadowedAny
	}
}

// needsAllOfComposition reports whether an embedded struct type should be
// composed via allOf rather than having its fields promoted.
func (g *generator) needsAllOfComposition(t reflect.Type) bool {
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

	// A direct TextMarshaler or a built-in override is deliberately not
	// composed here. When an embedded type's promoted marshaler drives the
	// whole outer value, schemaForType intercepts the outer before reflection
	// begins, so this probe is only reached for an outer that reflects as an
	// object (one that implements json.Marshaler itself, or one whose embeds'
	// marshalers are suppressed as ambiguous). There the embed's marshaler
	// never serializes the value -- encoding/json emits a plain object of the
	// reflected fields -- so composing the scalar builtin schema as an
	// allOf:[{"type":"string"}] branch would make the object schema
	// unsatisfiable; the embed's fields are promoted instead (every builtin
	// struct override exports none, contributing nothing, exactly matching the
	// marshaled output).

	return false
}

// buildFieldSchema generates a struct field's schema, applies the json:",string"
// override, comment extraction, and jsonschema struct tag, then registers it in
// the parent's Properties/PropertyOrder and required list. Tag interpreters run
// later in applyFieldInterpreters once all sibling properties exist.
func (g *generator) buildFieldSchema(
	parentType reflect.Type,
	fi structFieldInfo,
	parent *node,
) (*node, error) {
	fieldType := fi.field.Type
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
	// On a stringable type the override fully replaces the field schema, so
	// generating the field's own type is skipped: it would be wasted work and,
	// for a type extracted to $defs (a provider or extender), would register an
	// orphan definition and drop the provider's constraints.
	stringOverride := fi.jsonString && reflectkind.IsStringableType(fieldType)
	tagTypeSchema := (*Schema)(nil)

	var (
		fieldNode *node
		err       error
	)

	if stringOverride {
		// A pointer to a stringable type is a nilable container, so it shares the
		// slice/map null-branch policy (recorded on the node, applied by render);
		// a non-pointer is always a bare string.
		payload := &Schema{}
		fieldNode = &node{kind: kindValue, payload: payload}

		if isPointer {
			fieldNode.nullable = g.nullable
			fieldNode.base = typename.String
			tagTypeSchema = &Schema{Types: []string{typename.Null, typename.String}}
		} else {
			payload.Type = typename.String
		}
	} else {
		fieldNode, err = g.schemaForType(fieldType, false)
		if err != nil {
			return nil, err
		}
	}

	fieldNode.isField = true

	// Allocate the authored canvas for the field and every sequence/map element
	// beneath it. Field-level processing (the comment provider, the jsonschema
	// tag, tag interpreters) declares its facts on the canvas rather than mutating
	// the type-derived payload, so which schema a keyword lives in is its
	// provenance and reconcileField composes the two at render.
	allocCanvasTree(fieldNode, g.draft)

	// 2. Field-level comment.
	err = g.applyFieldDescription(parentType, fi, fieldNode, parent.payload)
	if err != nil {
		return nil, err
	}

	// 3. Schema struct tag. Facts land on the authored canvas; a type= override
	// restructures the type-derived payload (it replaces the reflected assertion),
	// so it takes the payload directly.
	if tag, ok := fi.field.Tag.Lookup("jsonschema"); ok {
		res, err := tagparse.Apply(
			tag, fieldType, fieldNode.authored, fieldNode.payload, tagTypeSchema)
		if err != nil {
			// Tagparse carries its own ErrInvalidType sentinel; map it onto the
			// package's exported ErrInvalidType so errors.Is keeps working.
			if errors.Is(err, tagparse.ErrInvalidType) {
				err = fmt.Errorf("%w: %w", ErrInvalidType, err)
			}

			// Tagparse errors already carry the "jsonschema tag:" prefix.
			return nil, err
		}

		// A type= override replaces the field's type wholesale, so the field is
		// now inline: it is not a reference and not nullable.
		if res.TypeOverridden {
			fieldNode = rebuildOverriddenField(fieldNode)
		}
	}

	// Add to parent. The payload (bare) is shared into parent.Properties so a
	// build-time interpreter sees the sibling shape; render overwrites it.
	if parent.payload.Properties == nil {
		parent.payload.Properties = map[string]*Schema{}
	}

	required := !fi.omitempty && !fi.omitzero
	if required {
		parent.payload.Required = append(parent.payload.Required, fi.jsonName)
	}

	parent.payload.Properties[fi.jsonName] = fieldNode.payload
	parent.payload.PropertyOrder = append(parent.payload.PropertyOrder, fi.jsonName)
	parent.props = append(parent.props, nodeProp{name: fi.jsonName, schema: fieldNode})

	return fieldNode, nil
}

// rebuildOverriddenField rebuilds a field node after a type= override replaced
// its type wholesale: a plain value node over the overridden payload, dropping
// the def link, children, and null bits in one place, while carrying the
// authored canvas across. Reachability drops any def the detached ref
// orphaned. A type=array override on a sequence field keeps the payload's
// element structure (applyTypeOverride drops it for every other type), and the
// authored element canvases can carry a redirected element enum; those element
// nodes are kept so reconcile still composes them per child instead of
// silently dropping the author's enum.
func rebuildOverriddenField(fieldNode *node) *node {
	rebuilt := &node{
		kind:     kindValue,
		payload:  fieldNode.payload,
		authored: fieldNode.authored,
		isField:  true,
	}

	switch {
	case fieldNode.kind == kindList && fieldNode.payload.Items != nil:
		rebuilt.kind = kindList
		rebuilt.items = fieldNode.items

	case fieldNode.kind == kindTuple &&
		(len(fieldNode.payload.PrefixItems) > 0 || len(fieldNode.payload.ItemsArray) > 0):
		rebuilt.kind = kindTuple
		rebuilt.prefix = fieldNode.prefix
	}

	return rebuilt
}

// fieldContext builds the FieldContext passed to tag interpreters and the
// description provider for one struct field, computing the declaring type once.
// Canvas is the field's authored canvas (where a hook declares its facts) and
// Base is the type-derived payload (read-only); the accessor exposing element
// canvases reads the field node's element children.
func (g *generator) fieldContext(
	parentType reflect.Type,
	fi structFieldInfo,
	fieldNode *node,
	parent *Schema,
) FieldContext {
	return FieldContext{
		Name:        fi.jsonName,
		Type:        fi.field.Type,
		Owner:       reflectkind.DeclaringType(parentType, fi.field),
		Canvas:      fieldNode.authored,
		Base:        fieldNode.payload,
		Parent:      parent,
		StructField: fi.field,
		Draft:       g.draft,
		node:        fieldNode,
	}
}

// applyFieldInterpreters runs the registered tag interpreters for a field on its
// authored canvas. It runs after all field payloads are in place so interpreters
// see the full parent.Properties. Const/enum placement and the Draft-07 $ref
// wrap are handled by render, from the complete graph.
func (g *generator) applyFieldInterpreters(
	parentType reflect.Type,
	fi structFieldInfo,
	fieldNode, parent *node,
) error {
	for _, reg := range g.tagInterpreters {
		if tag, ok := fi.field.Tag.Lookup(reg.key); ok {
			fc := g.fieldContext(parentType, fi, fieldNode, parent.payload)

			err := reg.interp.Interpret(g.ctx, fc, Tag{Key: reg.key, Value: tag})
			if err != nil {
				return fmt.Errorf("tag interpreter %q: %w", reg.key, err)
			}
		}
	}

	return nil
}

// wrapRefForDraft7 wraps a bare $ref with allOf if sibling keywords were added
// and the draft is Draft-07 (where $ref siblings are ignored). It serves the
// post-render defaults path, where a default landing beside a bare $ref root or
// property needs the wrap; render's renderRef handles the field path itself.
func (g *generator) wrapRefForDraft7(s *Schema) {
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
// An embed reached through a pointer (fi.optional) contributes nothing to the
// marshaled object when the pointer is nil, so its schema cannot be an
// unconditional allOf branch. Such a branch would require the embed's
// properties in every instance and reject the nil-embed serialization. The
// branch is wrapped as anyOf[embedded, {}] instead: a non-nil embed matches
// the schema (and its annotations still flow to unevaluatedProperties), while
// a nil embed satisfies the empty alternative. The empty branch also admits a
// partial embed serialization under Draft-07 (which lacks unevaluated
// semantics); accepting those is the price of not rejecting valid documents.
//
// A shadowed embed (fi.shadowed) takes the same wrap for the same reason: a
// real field wins one of the embed's promoted names, so the marshaled object
// carries the winner's value where the branch asserts the embed's
// constraints, and an unconditional branch would reject the type's own
// marshaled JSON.
func (g *generator) processAllOfField(fi structFieldInfo, parent *node) error {
	ft := fi.field.Type
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	branch, err := g.schemaForType(ft, false)
	if err != nil {
		return err
	}

	// The embed is composition, not nullability: render emits the optional
	// anyOf[branch, {}] from embedNode.optional, and applyNull never touches an
	// embed. Clear any nullable bit the schemaForType path set (a pointer embed's
	// optionality rides on embedNode.optional, not the node's null bit).
	branch.nullable = false

	parent.embeds = append(parent.embeds, embedNode{branch: branch, optional: fi.optional || fi.shadowed})

	return nil
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

	provErr, ok := results[1].Interface().(error)
	if ok && provErr != nil {
		return TypeSchema{}, fmt.Errorf("%s.JSONSchema: %w", t, provErr)
	}

	res, ok := results[0].Interface().(TypeSchema)
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

	extErr, ok := results[0].Interface().(error)
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
func (g *generator) extendType(t reflect.Type, ts *TypeSchema) error {
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

// extendTypeSchema runs [generator.extendType] over the type-derived payload s,
// wrapping it in a [TypeSchema] the extenders mutate. It returns the declared
// [Nullability] stance (NullFromReflection when no extender sets one) for the call
// site to fold into the node's null decision. An extender that replaces ts.Value
// with a new pointer is copied back into s in place, since s is the shared
// payload aliased into the node graph (and, once the field is registered, into
// the parent's Properties); mutating in place keeps those aliases consistent. An
// extender that clears ts.Value to nil leaves the payload unchanged, since there
// is nothing to copy back.
//
// The envelope enters with Verbatim and Ref nil: those declare a replacement
// schema, which only a provider supplies. An extender honors only Value and
// Nullability, so one that sets Verbatim or Ref is reported as a malformed
// TypeSchema rather than having the declaration silently ignored.
func (g *generator) extendTypeSchema(t reflect.Type, s *Schema) (Nullability, error) {
	ts := &TypeSchema{Value: s}

	err := g.extendType(t, ts)
	if err != nil {
		return NullFromReflection, err
	}

	if ts.Verbatim != nil || ts.Ref != nil {
		return NullFromReflection, fmt.Errorf(
			"%w: extender for type %s sets Verbatim or Ref; an extender declares only Value and Nullability",
			ErrConflictingTypeSchema, t)
	}

	if ts.Value != nil && ts.Value != s {
		*s = *ts.Value
	}

	return ts.Nullability, nil
}

// jsonTagInfo holds parsed json tag information.
// ApplyTypeDescription sets the description from the comment provider on a
// type's schema. An empty comment leaves the description unset; a provider
// error aborts generation.
func (g *generator) applyTypeDescription(t reflect.Type, s *Schema) error {
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
func (g *generator) applyFieldDescription(
	parentType reflect.Type, fi structFieldInfo, fieldNode *node, parent *Schema,
) error {
	if g.descriptionProvider == nil {
		return nil
	}

	fc := g.fieldContext(parentType, fi, fieldNode, parent)

	comment, err := g.descriptionProvider.FieldDescription(g.ctx, fc)
	if err != nil {
		return fmt.Errorf("describe field %q of %s: %w", fi.jsonName, parentType, err)
	}

	if comment != "" {
		fieldNode.authored.Description = comment
	}

	return nil
}
