package jsonschema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

var (
	typeJSONRawMessage = reflect.TypeFor[json.RawMessage]()
	typeTime           = reflect.TypeFor[time.Time]()
	typeJSONNumber     = reflect.TypeFor[json.Number]()
	typeBigInt         = reflect.TypeFor[big.Int]()
	typeBigRat         = reflect.TypeFor[big.Rat]()
	typeBigFloat       = reflect.TypeFor[big.Float]()
	typeByteSlice      = reflect.TypeFor[[]byte]()
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

	typeToDefName     map[reflect.Type]string
	typeProviders     []TypeSchemaProvider
	namer             Namer
	defs              map[string]*Schema
	defsNameToTypes   map[string][]reflect.Type
	typeToDefSchema   map[reflect.Type]*Schema
	typeOverrideCache map[reflect.Type]typeOverrideResult
	visiting          map[reflect.Type]bool
	// DefaultsFrom is the WithDefaultsFrom instance; defaultsFromSet
	// distinguishes an explicit nil instance from the option being absent.
	defaultsFrom         any
	refRecords           []refRecord
	descriptionProvider  DescriptionProvider
	tagInterpreters      []tagInterpreterRegistration
	typeExtenders        []TypeSchemaExtender
	draft                Draft
	definitions          bool
	additionalProperties bool
	nullable             bool
	defaultsFromSet      bool
	rootTitle            bool
}

// refRecord tracks a $ref schema and the Go type it references, enabling
// correct ref updates during $defs name disambiguation.
type refRecord struct {
	schema *Schema
	target reflect.Type
}

// typeOverrideResult memoizes one [generator.resolveTypeSchema] consultation so
// the registered type providers run at most once per type per run, even when the
// allOf-composition probe and the real build both ask about the same type.
type typeOverrideResult struct {
	schema   *Schema
	err      error
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

	return g
}

// forRun derives one generation run from the prototype. The configuration
// (providers, extenders, interpreters, namer, and flags) is shared, since
// generation only reads it. The per-run maps and the context are fresh, so
// concurrent runs from one prototype never share mutable state.
func (g *generator) forRun(ctx context.Context) *generator {
	run := *g
	run.ctx = ctx
	run.defs = map[string]*Schema{}
	run.defsNameToTypes = map[string][]reflect.Type{}
	run.typeToDefName = map[reflect.Type]string{}
	run.typeToDefSchema = map[reflect.Type]*Schema{}
	run.typeOverrideCache = map[reflect.Type]typeOverrideResult{}
	run.visiting = map[reflect.Type]bool{}
	run.refRecords = nil

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

	schema, err := g.schemaForType(t, false)
	if err != nil {
		return nil, err
	}

	// Disambiguate $defs names if there are collisions.
	// All $ref schemas are updated in-place via tracked refRecords.
	g.disambiguateDefs()

	// If the root type was extracted to $defs and nothing references it,
	// inline its schema at the root instead of using $ref. A root reached
	// by another definition (self-reference or mutual recursion) must stay
	// in $defs, or removing it would leave those refs dangling.
	//
	// This runs after disambiguateDefs so the $defs keys are unique: the
	// isReferenced check keys on the root's final $defs name, so a root whose
	// simple name collides with a different referenced type is judged on its own
	// renamed entry rather than the shared pre-disambiguation name.
	if schema.Ref != "" {
		defName := g.typeToDefName[rootType]
		if defName != "" && !g.isReferenced(defName) {
			// Inline: use the $defs entry as the root schema directly.
			inlined := g.defs[defName]
			if inlined != nil {
				schema = inlined

				// Drop the type's registration along with its def so the
				// invariant "every typeToDefName entry has a live def" holds; a
				// later re-resolution of rootType would otherwise produce a $ref
				// to the deleted entry. The defsNameToTypes index needs no
				// cleanup: it is keyed by the pre-disambiguation name and read
				// only by disambiguateDefs, which has already run.
				delete(g.defs, defName)
				delete(g.typeToDefName, rootType)
				delete(g.typeToDefSchema, rootType)
			}
		}
	}

	// Seed property defaults from the WithDefaultsFrom instance. A pointer
	// root under WithNullable generates an anyOf nullable wrapper whose value
	// branch holds the object schema, so the target resolves through the
	// wrapper first. When the resolved target is a $ref to a $defs entry
	// (a pointer root's value branch, or a self-reference or mutual recursion
	// kept the root from being inlined above), the defaults land on that
	// definition's properties, shared by every occurrence of the type.
	if g.defaultsFromSet {
		err := g.applyInstanceDefaults(g.defaultsFrom, rootType, g.rootDefaultsTarget(schema, rootType))
		if err != nil {
			return nil, err
		}
	}

	// Set the root title from the type name when WithRootTitle is enabled and
	// nothing else (WithTypeSchema, JSONSchemaProvider, an extender, or tags)
	// supplied one. Unnamed roots produce an empty name even after the
	// empty-answer deferral to the default namer, and stay untitled.
	if g.rootTitle {
		target := g.rootTitleTarget(schema, rootType)
		if name := g.schemaName(rootType); name != "" && target.Title == "" {
			target.Title = name
		}
	}

	// Set $schema on root.
	schema.Schema = g.draft.schemaURI()

	// Attach $defs if any.
	if len(g.defs) > 0 {
		if g.draft == Draft7 {
			schema.Definitions = g.defs
		} else {
			schema.Defs = g.defs
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
func (g *generator) rootDefaultsTarget(schema *Schema, rootType reflect.Type) *Schema {
	target := schema
	if inner := schemashape.NullableInnerSchema(target); inner != nil {
		target = inner
	}

	if target.Ref == "" {
		return target
	}

	if def := g.defs[g.typeToDefName[rootType]]; def != nil {
		return def
	}

	return target
}

// rootTitleTarget resolves the schema that WithRootTitle titles. Draft-07
// readers ignore keywords beside $ref, so when a self-referential root stays
// a bare $ref into definitions, the title goes on the definitions entry it
// targets instead, shared by every occurrence of the type;
// [generator.rootDefaultsTarget] redirects the same way.
func (g *generator) rootTitleTarget(schema *Schema, rootType reflect.Type) *Schema {
	if g.draft != Draft7 || schema.Ref == "" {
		return schema
	}

	if def := g.defs[g.typeToDefName[rootType]]; def != nil {
		return def
	}

	return schema
}

// isReferenced reports whether any $defs entry contains a $ref to the named
// definition. A root reached this way (self-reference or mutual recursion)
// must not be inlined and deleted, or those refs would dangle.
func (g *generator) isReferenced(defName string) bool {
	target := g.draft.refPrefix() + defName
	for _, s := range g.defs {
		if schemaContainsRef(s, target) {
			return true
		}
	}

	return false
}

// schemaContainsRef recursively checks if a schema contains a $ref to the
// given target. It walks the sub-schema-bearing fields via [SubschemaEntries],
// the single source of truth for which keywords hold sub-schemas, so a
// reference through any keyword is found and the field list stays in one place.
// The freshly generated $defs trees this runs over are acyclic, so the walk
// terminates.
func schemaContainsRef(s *Schema, target string) bool {
	if s == nil {
		return false
	}

	if s.Ref == target {
		return true
	}

	for _, entry := range SubschemaEntries(s) {
		if schemaContainsRef(entry.Schema, target) {
			return true
		}
	}

	return false
}

// schemaForType produces a schema for the given type. If nullable is true,
// the result is made nullable (see applyNullable: pointers wrap in an anyOf
// with a null branch, while slices and maps use a "null" entry in the type
// list).
//
//nolint:unparam // nullable is part of the API contract; callers pass false but the parameter is used internally after pointer unwrapping.
func (g *generator) schemaForType(t reflect.Type, nullable bool) (*Schema, error) {
	// Follow pointers. A pointer at any level makes the schema nullable.
	if t.Kind() == reflect.Pointer {
		nullable = g.nullable
	}

	t = numkind.DerefType(t)

	// A named non-struct type already extracted to $defs is referenced again,
	// not rebuilt: re-running its provider, extender, and description hooks
	// would invoke them once per reference and discard every result after the
	// first. Struct types run the equivalent guard inside schemaForStruct.
	if t.Kind() != reflect.Struct && t.Name() != "" {
		if _, exists := g.typeToDefName[t]; exists {
			return g.refForType(t, nullable), nil
		}
	}

	// 1. Type provider override (WithTypeSchemaProvider / WithTypeSchema).
	s, ok, err := g.resolveTypeSchema(t)
	if err != nil {
		return nil, err
	}

	if ok {
		return g.handleOverrideType(t, s, nullable)
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
	// post-processing (comments, extender, $defs extraction).
	if reflectkind.IsDirectTextMarshaler(t) {
		s := &Schema{Type: typename.String}
		return g.handleBuiltinType(t, s, nullable)
	}

	// 6. Cycle detection for named container types. A named type that contains
	// itself (type T []T, type M map[string]M, type A [N]A) recurses without
	// bound through schemaForKind. Tracking the type on the visiting stack lets a
	// re-entry emit a $ref to its $defs entry, breaking the cycle exactly as
	// schemaForStruct does for self-referential structs. Struct types run their
	// own equivalent guard inside schemaForStruct, so they are excluded here.
	guarded := t.Kind() != reflect.Struct && t.Name() != "" && reflectkind.IsRecursiveContainerKind(t.Kind())
	if guarded {
		if g.visiting[t] {
			return g.refForType(t, nullable), nil
		}

		g.visiting[t] = true
	}

	// 7. Kind-based reflection. A named non-struct type bound for $defs builds its
	// schema bare: the definition is shared by every reference, so nullability
	// belongs on each reference (applied by refForType), not baked into the single
	// shared entry. Baking it in would let whichever reference is processed first
	// decide the definition's nullability for all of them, making the output
	// depend on field declaration order. Inlined types keep nullability on the
	// schema itself, exactly as the built-in and provider paths do.
	extractBare := t.Kind() != reflect.Struct && t.Name() != "" && g.shouldExtract(t)

	kindNullable := nullable
	if extractBare {
		kindNullable = false
	}

	s, err = g.schemaForKind(t, kindNullable)
	if guarded {
		delete(g.visiting, t)
	}

	if err != nil {
		return nil, err
	}

	// Type-level post-processing for non-struct named types.
	// Struct types handle comments, extend, and extraction internally
	// in buildStructSchema/schemaForStruct.
	//nolint:nestif // Sequential post-processing steps; flattening adds no clarity.
	if t.Kind() != reflect.Struct && t.Name() != "" {
		err := g.applyTypeDescription(t, s)
		if err != nil {
			return nil, err
		}

		// A nullable inline scalar or array is anyOf[value, {"type":"null"}].
		// The type's extender refines the type's value schema, so direct it at
		// the value branch; left on the wrapper, a type-level keyword it sets
		// (type, format, a numeric bound) would constrain the wrapper and reject
		// the very value branch it describes, and the permitted null with it. A
		// non-pointer field of the same type presents the bare value schema, so
		// this keeps a pointer and a value field consistent.
		extendTarget := s
		if inner := schemashape.NullableInnerSchema(s); inner != nil {
			extendTarget = inner
		}

		err = g.extendType(t, extendTarget)
		if err != nil {
			return nil, err
		}

		// A cycle detected while building this type's element/value schema left a
		// placeholder $defs entry (created by refForType). Fill it with the now
		// complete schema and return a $ref, mirroring the inline-struct path.
		if _, cyclic := g.typeToDefName[t]; cyclic {
			return g.extractToDefs(t, s, nullable)
		}

		if g.shouldExtract(t) {
			return g.extractToDefs(t, s, nullable)
		}
	}

	return s, nil
}

// resolveTypeSchema consults the registered type providers for t, newest
// registration first, and returns the first schema offered, with ok
// reporting whether any provider handled the type. The order makes a later
// registration win for the types two providers both handle, which for the
// exact-match providers WithTypeSchema registers preserves its
// last-registration-wins behavior. An ErrTypeNotHandled answer passes the
// type to the next provider; any other provider error stops the
// consultation and aborts generation.
func (g *generator) resolveTypeSchema(t reflect.Type) (*Schema, bool, error) {
	// Memoize per run so a stateful or expensive provider is consulted once per
	// type: the allOf-composition probe in needsAllOfComposition and the real
	// build both resolve the same type, and a non-deterministic provider would
	// otherwise disagree between them. The result is cloned by finishTypeOverride
	// before mutation, so handing the same schema to both callers is safe.
	if cached, ok := g.typeOverrideCache[t]; ok {
		return cached.schema, cached.resolved, cached.err
	}

	s, resolved, err := g.resolveTypeSchemaUncached(t)
	g.typeOverrideCache[t] = typeOverrideResult{schema: s, resolved: resolved, err: err}

	return s, resolved, err
}

// resolveTypeSchemaUncached performs the provider consultation that
// resolveTypeSchema memoizes.
func (g *generator) resolveTypeSchemaUncached(t reflect.Type) (*Schema, bool, error) {
	tc := TypeContext{Type: t, Draft: g.draft}
	for _, v := range slices.Backward(g.typeProviders) {
		s, err := v.SchemaForType(g.ctx, tc)
		if errors.Is(err, ErrTypeNotHandled) {
			continue
		}

		if err != nil {
			return nil, false, fmt.Errorf("resolve type %s: %w", t, err)
		}

		return s, true, nil
	}

	return nil, false, nil
}

// handleOverrideType processes a type resolved by a registered
// TypeSchemaProvider (WithTypeSchemaProvider or WithTypeSchema). A nil override
// marks the type unrestricted, mirroring a JSONSchemaProvider returning nil.
func (g *generator) handleOverrideType(t reflect.Type, override *Schema, nullable bool) (*Schema, error) {
	return g.finishTypeOverride(t, override, nullable)
}

// finishTypeOverride applies the post-processing shared by the WithTypeSchema
// override path and the JSONSchemaProvider path: clone the caller-shared schema,
// apply type-level comments, then either extract to $defs (returning a $ref) or
// make the result nullable inline. A nil src marks the type unrestricted.
//
// The source is copied with the upstream shallow CloneSchemas, not the JSON
// round-trip cloneSchema used for remote refs: CloneSchemas preserves the
// caller's exact any-typed Enum/Const/Default values, whereas a round-trip
// would rewrite them (a Go int enum value would decode back as float64).
//
// CloneSchemas only deep-copies sub-schema fields, leaving the Enum, Const,
// Default, and Extra headers aliased to the caller's schema. CloneOverrideExtras
// copies those too, so a tag interpreter or JSONSchemaExtender that mutates them
// in place (appending to Enum, reassigning Const, writing into Extra) cannot
// reach back into an override or provider schema reused across Generate calls.
func (g *generator) finishTypeOverride(t reflect.Type, src *Schema, nullable bool) (*Schema, error) {
	if src == nil {
		src = &Schema{} // unrestricted
	}

	s := src.CloneSchemas()
	cloneOverrideExtras(s)

	// Apply type-level comments.
	err := g.applyTypeDescription(t, s)
	if err != nil {
		return nil, err
	}

	if g.shouldExtract(t) {
		return g.extractToDefs(t, s, nullable)
	}

	return g.applyNullable(s, t, nullable), nil
}

// cloneOverrideExtras clones the non-sub-schema container fields that the
// upstream CloneSchemas leaves aliased to the source schema. CloneSchemas
// deep-copies only the sub-schema fields (*Schema, []*Schema, map[string]*Schema)
// and shallow-shares every other reference field, so an extender or interpreter
// that appends or assigns into one of those in place would corrupt the caller's
// schema across Generate calls.
//
// The policy is top-level headers only: each slice, map, and pointer container
// is reallocated so writes to it cannot reach the source, but the nested any
// values and the bytes they reference keep their identity, preserving the
// caller's exact typed values. Every Schema field whose type is []any, []string,
// map[string]bool, map[string][]string, [json.RawMessage], *any, or
// map[string]any is covered here; TestTypeSchemaOverrideContainersUnaliased in
// generate_test.go fails if a future upstream field of one of those
// types is added without being cloned.
func cloneOverrideExtras(s *Schema) {
	if s.Enum != nil {
		s.Enum = slices.Clone(s.Enum)
	}

	if s.Const != nil {
		c := *s.Const
		s.Const = &c
	}

	if s.Default != nil {
		s.Default = slices.Clone(s.Default)
	}

	if s.Extra != nil {
		s.Extra = maps.Clone(s.Extra)
	}

	if s.Examples != nil {
		s.Examples = slices.Clone(s.Examples)
	}

	if s.Required != nil {
		s.Required = slices.Clone(s.Required)
	}

	if s.Types != nil {
		s.Types = slices.Clone(s.Types)
	}

	if s.PropertyOrder != nil {
		s.PropertyOrder = slices.Clone(s.PropertyOrder)
	}

	if s.Vocabulary != nil {
		s.Vocabulary = maps.Clone(s.Vocabulary)
	}

	if s.DependencyStrings != nil {
		s.DependencyStrings = maps.Clone(s.DependencyStrings)
	}

	if s.DependentRequired != nil {
		s.DependentRequired = maps.Clone(s.DependentRequired)
	}
}

// handleProviderType processes a type implementing JSONSchemaProvider.
//
// The provider's JSONSchema method returns its exact *Schema, which it may share
// across fields and Generate calls (for example a package-level singleton).
// Downstream steps mutate it: applyTypeDescription writes Description in place and
// extractToDefs aliases the pointer into g.defs. The shared finishTypeOverride
// clones it first, so the provider's source schema is never corrupted.
func (g *generator) handleProviderType(t reflect.Type, nullable bool) (*Schema, error) {
	provided, err := callProvider(g.ctx, TypeContext{Type: t, Draft: g.draft})
	if err != nil {
		return nil, err
	}

	return g.finishTypeOverride(t, provided, nullable)
}

// handleBuiltinType processes a type with a built-in override, applying
// type-level post-processing (comments, extender, $defs extraction) per
// the processing order.
func (g *generator) handleBuiltinType(t reflect.Type, s *Schema, nullable bool) (*Schema, error) {
	//nolint:nestif // Sequential post-processing steps; flattening adds no clarity.
	if t.Name() != "" {
		err := g.applyTypeDescription(t, s)
		if err != nil {
			return nil, err
		}

		err = g.extendType(t, s)
		if err != nil {
			return nil, err
		}

		if g.shouldExtract(t) {
			return g.extractToDefs(t, s, nullable)
		}
	}

	// A null-bearing schema (the []byte override folds null into its type list)
	// is returned by applyNullable unchanged, so the bare form is preserved
	// without a redundant second null branch.
	return g.applyNullable(s, t, nullable), nil
}

// byteSliceSchema returns the schema for a byte slice, which encoding/json
// renders as a base64-encoded string.
func (g *generator) byteSliceSchema() *Schema {
	s := &Schema{ContentEncoding: contentEncodingBase64}
	g.applyContainerType(s, typename.String)

	return s
}

// builtinOverride returns a schema for well-known types, if applicable.
func (g *generator) builtinOverride(t reflect.Type) (*Schema, bool) {
	switch t {
	case typeByteSlice:
		return g.byteSliceSchema(), true

	case typeTime:
		return &Schema{Type: typename.String, Format: formatDateTime}, true
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
		return &Schema{Type: typename.String, Pattern: `^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`}, true
	}

	return nil, false
}

// boundedInteger builds an integer schema with inclusive [minimum, maximum]
// bounds.
func boundedInteger(minimum, maximum float64) *Schema {
	return &Schema{Type: typename.Integer, Minimum: new(minimum), Maximum: new(maximum)}
}

// schemaForKind handles the kind-based reflection step.
func (g *generator) schemaForKind(t reflect.Type, nullable bool) (*Schema, error) {
	switch t.Kind() {
	case reflect.Bool:
		return g.applyNullable(&Schema{Type: typename.Boolean}, t, nullable), nil

	case reflect.String:
		return g.applyNullable(&Schema{Type: typename.String}, t, nullable), nil

	case reflect.Int:
		// Plain int is platform-dependent (32 or 64 bit), so leave it unbounded.
		return g.applyNullable(&Schema{Type: typename.Integer}, t, nullable), nil

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

		return g.applyNullable(s, t, nullable), nil

	case reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint8, reflect.Uint16, reflect.Uint32:
		// Fixed-width integers whose full range float64 can name inclusively.
		b := inclusiveIntBounds[t.Kind()]
		return g.applyNullable(boundedInteger(b[0], b[1]), t, nullable), nil

	case reflect.Uint, reflect.Uintptr:
		// Uint/uintptr are platform-dependent; only a lower bound is certain.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint64:
		// Float64 cannot represent MaxUint64 (2^64-1) exactly; see the Int64 case.
		// 2^64 is exactly representable, so an exclusive maximum of 2^64 admits
		// exactly v <= 2^64-1 = MaxUint64, including the boundary value.
		s := &Schema{Type: typename.Integer, Minimum: new(float64(0)), ExclusiveMaximum: new(exclusiveMaxUint64)}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Float32, reflect.Float64:
		return g.applyNullable(&Schema{Type: typename.Number}, t, nullable), nil

	case reflect.Interface:
		return &Schema{}, nil

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
func (g *generator) schemaForSlice(t reflect.Type, nullable bool) (*Schema, error) {
	// Byte slices marshal to base64 strings in encoding/json. The element's kind
	// (uint8) drives this, not the slice's exact type, so named byte-slice types
	// (type Bytes []byte) and slices of named uint8 elements are base64 too.
	// Mirror encoding/json's exception: an element implementing json.Marshaler or
	// encoding.TextMarshaler is encoded through that method, not as base64.
	if el := t.Elem(); el.Kind() == reflect.Uint8 {
		pt := reflect.PointerTo(el)
		if !pt.Implements(reflectkind.TypeJSONMarshaler) && !pt.Implements(reflectkind.TypeTextMarshaler) {
			return g.byteSliceSchema(), nil
		}
	}

	items, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("element type: %w", err)
	}

	s := &Schema{Items: items}
	g.applyContainerType(s, typename.Array)

	return s, nil
}

// schemaForArray generates a schema for fixed-length array types as a tuple.
// Draft 2020-12 uses prefixItems with one entry per element; Draft-07 uses the
// items-as-array form. MinItems/maxItems pin the length. Each element schema is
// generated independently so the result is a tree (no shared sub-schema
// pointers), which the validator requires.
func (g *generator) schemaForArray(t reflect.Type, nullable bool) (*Schema, error) {
	n := t.Len()

	elems := make([]*Schema, n)
	for i := range elems {
		item, err := g.schemaForType(t.Elem(), false)
		if err != nil {
			return nil, fmt.Errorf("element type: %w", err)
		}

		elems[i] = item
	}

	s := &Schema{
		Type:     typename.Array,
		MinItems: new(n),
		MaxItems: new(n),
	}
	if g.draft == Draft7 {
		s.ItemsArray = elems
	} else {
		s.PrefixItems = elems
	}

	return g.applyNullable(s, t, nullable), nil
}

// schemaForMap generates a schema for map types.
//
//nolint:unparam // nullable is accepted for consistency with other schema-producing methods.
func (g *generator) schemaForMap(t reflect.Type, nullable bool) (*Schema, error) {
	if !reflectkind.IsValidMapKey(t.Key()) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMapKey, t.Key())
	}

	valSchema, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("map value type: %w", err)
	}

	s := &Schema{AdditionalProperties: valSchema}
	g.applyContainerType(s, typename.Object)

	return s, nil
}

// schemaForStruct generates a schema for struct types.
func (g *generator) schemaForStruct(t reflect.Type, nullable bool) (*Schema, error) {
	// Cycle detection: even when definitions are disabled, cyclic types must
	// emit $defs/$ref to prevent infinite recursion.
	if g.visiting[t] {
		return g.refForType(t, nullable), nil
	}

	// Check for extraction to $defs.
	if g.shouldExtract(t) {
		// Check if already defined.
		if _, exists := g.typeToDefName[t]; exists {
			return g.refForType(t, nullable), nil
		}

		g.visiting[t] = true
		s, err := g.buildStructSchema(t)
		if err != nil {
			return nil, err
		}

		delete(g.visiting, t)

		return g.extractToDefs(t, s, nullable)
	}

	// Inline, but track visiting to detect cycles.
	g.visiting[t] = true
	s, err := g.buildStructSchema(t)
	if err != nil {
		return nil, err
	}

	delete(g.visiting, t)

	// If a cycle was detected during buildStructSchema (a placeholder was
	// created in $defs via refForType), extract this type's schema to fill it.
	if _, exists := g.typeToDefName[t]; exists {
		return g.extractToDefs(t, s, nullable)
	}

	return g.applyNullable(s, t, nullable), nil
}

// buildStructSchema builds the object schema for a struct type, including
// type-level comment extraction and JSONSchemaExtend.
func (g *generator) buildStructSchema(t reflect.Type) (*Schema, error) {
	s := &Schema{
		Type: typename.Object,
	}

	// Set additionalProperties: false (unless opted out).
	if !g.additionalProperties {
		s.AdditionalProperties = &Schema{Not: &Schema{}}
	}

	// Process fields using encoding/json rules.
	//
	// Two passes: first build every field's schema and populate Properties,
	// then run tag interpreters. This ensures a tag interpreter observing
	// FieldContext.Parent sees the complete sibling property set regardless of
	// field order.
	fields := g.collectStructFields(t)

	var hasAllOf bool

	type pendingField struct {
		schema        *Schema
		fi            structFieldInfo
		boundAuthored bool
	}

	var pending []pendingField

	for idx := range fields {
		if fields[idx].composeViaAllOf {
			err := g.processAllOfField(fields[idx], s)
			if err != nil {
				return nil, fmt.Errorf("embedded %s: %w", fields[idx].field.Type, err)
			}

			hasAllOf = true

			continue
		}

		fieldSchema, boundAuthored, err := g.buildFieldSchema(t, fields[idx], s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fields[idx].jsonName, err)
		}

		pending = append(pending, pendingField{
			fi:            fields[idx],
			schema:        fieldSchema,
			boundAuthored: boundAuthored,
		})
	}

	for i := range pending {
		pf := &pending[i]
		err := g.applyFieldInterpreters(t, pf.fi, pf.schema, s, pf.boundAuthored)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", pf.fi.jsonName, err)
		}
	}

	// Handle allOf + additionalProperties interaction.
	if hasAllOf && !g.additionalProperties {
		if g.draft == Draft2020 {
			s.AdditionalProperties = nil
			s.UnevaluatedProperties = &Schema{Not: &Schema{}}
		} else {
			// Draft-07: omit additionalProperties when allOf is in use.
			s.AdditionalProperties = nil
		}
	}

	// Type-level comment.
	err := g.applyTypeDescription(t, s)
	if err != nil {
		return nil, err
	}

	// JSONSchemaExtend, then registered extenders.
	err = g.extendType(t, s)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// structFieldInfo holds processed information about a struct field.
type structFieldInfo struct {
	jsonName        string
	field           reflect.StructField
	omitempty       bool
	omitzero        bool
	jsonString      bool
	composeViaAllOf bool
	// Optional is true for an allOf-composed embed reached through a
	// pointer-typed embedded field (directly or via an enclosing pointer
	// embed). Encoding/json omits the embed's entire contribution when the
	// pointer is nil, so the composed schema must not be unconditionally
	// required. Regular fields fold this into omitempty instead.
	optional bool
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
	type fieldLevel struct {
		field reflect.StructField
		depth int
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
					if hasTag && explicitName != "" {
						// Embedded struct with an explicit json name → treated as a
						// regular named field; encoding/json does not promote it. An
						// options-only tag (json:",omitempty") has no name and falls
						// through to promotion below, matching encoding/json.
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

					if ft.Kind() == reflect.Interface {
						// Embedded interface types: if they implement
						// JSONSchemaProvider, compose via allOf. Otherwise,
						// skip, since an unrestricted schema adds no useful info.
						if g.needsAllOfComposition(ft) {
							record(
								allOfName(ft),
								fieldLevel{field: f, depth: depth, optional: e.optional, composeAllOf: true},
								false,
							)
						}

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

					// Embedded non-struct type: treated as regular field with type name as key.
					jsonName := ft.Name()
					if jsonName == "" {
						continue
					}

					record(jsonName, fieldLevel{field: f, depth: depth, optional: e.optional}, dup)

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

	// Resolve shadowing and ambiguity.
	var result []structFieldInfo

	for _, key := range order {
		candidates := byName[key]
		if len(candidates) == 0 {
			continue
		}

		// Find minimum depth.
		minDepth := candidates[0].depth
		for _, c := range candidates[1:] {
			if c.depth < minDepth {
				minDepth = c.depth
			}
		}

		// Filter to only those at minimum depth.
		var atMin []fieldLevel

		for _, c := range candidates {
			if c.depth == minDepth {
				atMin = append(atMin, c)
			}
		}

		// Multiple fields collide on this JSON name at the shallowest depth.
		// Encoding/json breaks the tie by explicit tag: if exactly one of them
		// has an explicit json tag name, that field wins; if none or more than
		// one is tagged, they are all dropped as ambiguous.
		if len(atMin) > 1 {
			var tagged []fieldLevel

			for _, c := range atMin {
				if c.tagged {
					tagged = append(tagged, c)
				}
			}

			if len(tagged) != 1 {
				continue
			}

			atMin = tagged
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

		sfi := structFieldInfo{
			field:      f,
			jsonName:   info.JSONName,
			omitempty:  info.Omitempty || atMin[0].optional,
			omitzero:   info.Omitzero,
			jsonString: info.JSONString,
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

	return result
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

	// Check JSONSchemaProvider. An interface type's method set can include
	// JSONSchema, but callProvider cannot instantiate an interface to call it and
	// returns nil, which would compose a vacuous empty allOf branch. A genuine
	// override for the interface is handled by resolveTypeSchema above.
	if t.Kind() != reflect.Interface && implementsProvider(t) {
		return true
	}

	// Check built-in overrides.
	if _, ok := g.builtinOverride(t); ok {
		return true
	}

	// Check TextMarshaler (direct only, not promoted). An interface type whose
	// method set includes MarshalText is reported as a direct implementer by
	// reflectkind.IsDirectTextMarshaler (HasDirectMethod short-circuits to true
	// for any non-struct kind), but an embedded interface cannot be marshaled as
	// a string the way a concrete TextMarshaler is, so composing one into an
	// allOf:[{"type":"string"}] branch makes the schema unsatisfiable. Skip it,
	// mirroring the JSONSchemaProvider guard above.
	if t.Kind() != reflect.Interface && reflectkind.IsDirectTextMarshaler(t) {
		return true
	}

	return false
}

// buildFieldSchema generates a struct field's schema, applies the json:",string"
// override, comment extraction, and jsonschema struct tag, then registers it in
// the parent's Properties/PropertyOrder and required list. Tag interpreters run
// later in applyFieldInterpreters once all sibling properties exist. The returned
// bool reports whether the jsonschema tag authored a numeric bound kept alongside
// an enum, so applyFieldInterpreters can preserve it when it re-drops bounds.
func (g *generator) buildFieldSchema(
	parentType reflect.Type,
	fi structFieldInfo,
	parent *Schema,
) (*Schema, bool, error) {
	fieldType := fi.field.Type
	isPointer := fieldType.Kind() == reflect.Pointer

	// 1. JSON ",string" override. The tag-scalar type (used to parse the
	// jsonschema tag's const/enum/default values) defaults to the field's Go
	// type. When the override coerces the field schema to a string, encoding/json
	// also serializes the value as a quoted string, so the tag scalars must parse
	// as strings too; otherwise a numeric const on an int field would be
	// {"type":"string","const":5}, which the string-encoded "5" can never satisfy.
	//
	// On a stringable type the override fully replaces the field schema, so
	// generating the field's own type is skipped: it would be wasted work and,
	// for a type extracted to $defs (a provider or extender), would register an
	// orphan definition and drop the provider's constraints.
	tagType := fieldType
	stringOverride := fi.jsonString && reflectkind.IsStringableType(fieldType)

	var (
		fieldSchema   *Schema
		boundAuthored bool
		err           error
	)

	if stringOverride {
		// A pointer to a stringable type is a nilable container, so it shares the
		// slice/map null-branch policy; a non-pointer is always a bare string.
		fieldSchema = &Schema{}
		if isPointer {
			g.applyContainerType(fieldSchema, typename.String)
		} else {
			fieldSchema.Type = typename.String
		}

		tagType = reflect.TypeFor[string]()
	} else {
		fieldSchema, err = g.schemaForType(fieldType, false)
		if err != nil {
			return nil, false, err
		}
	}

	// 2. Field-level comment.
	err = g.applyFieldDescription(parentType, fi, fieldSchema, parent)
	if err != nil {
		return nil, false, err
	}

	// 3. Schema struct tag.
	if tag, ok := fi.field.Tag.Lookup("jsonschema"); ok {
		boundAuthored, err = applyJSONSchemaTag(tag, tagType, fieldSchema)
		if err != nil {
			return nil, false, fmt.Errorf("jsonschema tag: %w", err)
		}

		// A nullable pointer field is generated as anyOf[value, null] with
		// annotations kept as siblings of anyOf. Const and enum test the instance
		// value regardless of its type, so on the wrapper they also reject the
		// permitted null; relocate them onto the value branch and drop the now-
		// redundant numeric bounds. An author-set bound combined with enum is
		// kept (it narrows the enum). Type-gated keywords such as pattern do not
		// apply to null and stay put.
		schemashape.DropTypeBoundsForConstEnum(fieldSchema, boundAuthored)
	}

	// Add to parent.
	if parent.Properties == nil {
		parent.Properties = map[string]*Schema{}
	}

	// Required unless omitempty/omitzero.
	if !fi.omitempty && !fi.omitzero {
		parent.Required = append(parent.Required, fi.jsonName)
	}

	parent.Properties[fi.jsonName] = fieldSchema
	parent.PropertyOrder = append(parent.PropertyOrder, fi.jsonName)

	return fieldSchema, boundAuthored, nil
}

// fieldContext builds the FieldContext passed to tag interpreters and the
// description provider for one struct field, computing the declaring type once.
func (g *generator) fieldContext(
	parentType reflect.Type,
	fi structFieldInfo,
	fieldSchema, parent *Schema,
) FieldContext {
	return FieldContext{
		Name:        fi.jsonName,
		Type:        fi.field.Type,
		Owner:       reflectkind.DeclaringType(parentType, fi.field),
		Schema:      fieldSchema,
		Parent:      parent,
		StructField: fi.field,
		Draft:       g.draft,
	}
}

// applyFieldInterpreters runs the registered tag interpreters for a field and
// then wraps a bare $ref with allOf for Draft-07 when siblings were added. It
// runs after all field schemas are in place so interpreters see the full
// parent.Properties.
func (g *generator) applyFieldInterpreters(
	parentType reflect.Type,
	fi structFieldInfo,
	fieldSchema, parent *Schema,
	boundAuthored bool,
) error {
	ranInterpreter := false

	for _, reg := range g.tagInterpreters {
		if tag, ok := fi.field.Tag.Lookup(reg.key); ok {
			ranInterpreter = true
			fc := g.fieldContext(parentType, fi, fieldSchema, parent)

			err := reg.interp.Interpret(g.ctx, fc, Tag{Key: reg.key, Value: tag})
			if err != nil {
				return fmt.Errorf("tag interpreter %q: %w", reg.key, err)
			}
		}
	}

	// An interpreter may set Const/Enum on the field schema, which for a nullable
	// pointer field is the anyOf wrapper. Const and enum test the instance value
	// regardless of its type, so on the wrapper they reject the permitted null;
	// relocate them onto the value branch and drop the now-redundant type-derived
	// numeric bounds, matching the jsonschema-tag path in buildFieldSchema. This
	// reuses the jsonschema tag's boundAuthored provenance: a bound that tag kept
	// alongside an enum survives this second pass, while a purely kind-derived
	// bound (boundAuthored false, the common case with no value-narrowing
	// jsonschema tag) is dropped. The interpreter API exposes no per-keyword
	// provenance of its own, so an interpreter-set bound rides on whatever
	// boundAuthored the jsonschema tag established. Re-dropping runs only when an
	// interpreter touched the field; otherwise buildFieldSchema already dropped.
	if ranInterpreter {
		schemashape.DropTypeBoundsForConstEnum(fieldSchema, boundAuthored)
	}

	// Wrap bare $ref with allOf for Draft-07 if annotations were added. This
	// mutates the schema in place, so the entry already in parent.Properties
	// reflects the change.
	g.wrapRefForDraft7(fieldSchema)

	// For a nullable pointer field, a relocated const/enum (or any sibling
	// keyword) lands on the inner value branch of the anyOf wrapper, which is
	// itself a bare $ref. Under Draft-07 a $ref ignores its siblings, so the
	// inner branch needs the same allOf wrap; otherwise the relocated keyword is
	// silently dropped at validation time.
	if inner := schemashape.NullableInnerSchema(fieldSchema); inner != nil {
		g.wrapRefForDraft7(inner)
	}

	return nil
}

// wrapRefForDraft7 wraps a bare $ref with allOf if sibling keywords were
// added and the draft is Draft-07 (where $ref siblings are ignored).
// This should be called after all field-level processing has been applied.
// It moves the $ref into allOf in-place, preserving all sibling keywords.
func (g *generator) wrapRefForDraft7(s *Schema) {
	if g.draft != Draft7 || s.Ref == "" {
		return
	}

	// Check if there are any sibling keywords on the $ref.
	if !hasRefSiblings(s) {
		return
	}

	// Move $ref into allOf, preserving all sibling keywords in place.
	inner := &Schema{Ref: s.Ref}
	// Repoint any tracked ref record from the outer schema to the inner
	// $ref so $defs name disambiguation updates the live reference rather
	// than the now-emptied outer Ref.
	for i := range g.refRecords {
		if g.refRecords[i].schema == s {
			g.refRecords[i].schema = inner
		}
	}

	s.AllOf = append(s.AllOf, inner)
	s.Ref = ""
}

// hasRefSiblings reports whether a schema has any keyword set beyond just $ref.
// Any such keyword is a sibling Draft-07 validators ignore alongside $ref, so a
// constraint added by field-level processing (jsonschema struct tag or tag
// interpreter) would be silently dropped unless the $ref is wrapped in allOf.
//
// Validation, applicator, and content keywords are detected by clearing $ref on
// a copy and asking [schemashape.IsEmpty], the maintained single source of truth
// for which keywords constrain a value; this catches every constraining keyword,
// including Not/AllOf/AnyOf/OneOf/Required/Types/If/Then/Else/DependentRequired/
// DependentSchemas and any future addition, without re-enumerating the list.
// Annotation, metadata, and identifier keywords (description, title, default,
// deprecated, readOnly, writeOnly, examples, $comment, $id, $schema, $anchor,
// $dynamicAnchor, $vocabulary) and the Extra escape hatch do not constrain a
// value, so schemashape.IsEmpty deliberately ignores them; they are checked
// explicitly here because they too must be preserved across the allOf wrap. The
// set mirrors the non-constraint fields IsTrueSchema enumerates beyond what
// schemashape.IsEmpty covers.
func hasRefSiblings(s *Schema) bool {
	// Annotation, metadata, and identifier keywords, plus Extra: not
	// constraints, so schemashape.IsEmpty ignores them, but field-level
	// processing (a tag interpreter or extender) can set them and they must
	// survive the allOf wrap.
	if s.Description != "" || s.Title != "" || s.Default != nil ||
		s.Deprecated || s.ReadOnly || s.WriteOnly ||
		len(s.Examples) > 0 || len(s.Extra) > 0 ||
		s.Comment != "" || s.ID != "" || s.Schema != "" ||
		s.Anchor != "" || s.DynamicAnchor != "" || s.Vocabulary != nil {
		return true
	}

	// Every constraining keyword: copy, clear $ref, and ask schemashape.IsEmpty.
	withoutRef := *s
	withoutRef.Ref = ""

	return !schemashape.IsEmpty(&withoutRef)
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
func (g *generator) processAllOfField(fi structFieldInfo, parent *Schema) error {
	ft := fi.field.Type
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	embeddedSchema, err := g.schemaForType(ft, false)
	if err != nil {
		return err
	}

	// The schemaForType call already returned a fresh, distinct schema for ft: a
	// bare $ref for an extracted type (tracked by refForType against ft) or an
	// inline schema otherwise. Use it directly; re-wrapping a $ref in another
	// tracked node would leave the first refRecord orphaned, pointing at a schema
	// that is not in the output tree.
	branch := embeddedSchema

	if fi.optional {
		branch = &Schema{AnyOf: []*Schema{branch, {}}}
	}

	parent.AllOf = append(parent.AllOf, branch)

	return nil
}

// extractToDefs places a type's schema in $defs and returns a $ref.
func (g *generator) extractToDefs(t reflect.Type, s *Schema, nullable bool) (*Schema, error) {
	name := g.schemaName(t)

	// Check if already defined (e.g., from a cycle placeholder).
	if existingName, exists := g.typeToDefName[t]; exists {
		// Fill this type's placeholder if it has no schema yet. Key on the
		// per-type schema, not g.defs[name]: under a name collision g.defs[name]
		// holds another colliding type's schema, so testing it would wrongly
		// leave this type's placeholder unfilled (and later assign it the wrong
		// schema during disambiguation).
		if g.typeToDefSchema[t] == nil {
			g.defs[existingName] = s
			g.typeToDefSchema[t] = s
		}

		return g.refForType(t, nullable), nil
	}

	// Register.
	g.typeToDefName[t] = name
	g.defsNameToTypes[name] = append(g.defsNameToTypes[name], t)
	g.defs[name] = s
	g.typeToDefSchema[t] = s

	return g.refForType(t, nullable), nil
}

// refForType creates a $ref schema pointing to the type's $defs entry.
// If nullable, wraps in anyOf. All created $ref schemas are tracked for
// correct updates during $defs name disambiguation.
func (g *generator) refForType(t reflect.Type, nullable bool) *Schema {
	name := g.typeToDefName[t]
	if name == "" {
		// No entry yet: register a placeholder to break the cycle.
		name = g.schemaName(t)
		g.typeToDefName[t] = name
		g.defsNameToTypes[name] = append(g.defsNameToTypes[name], t)
		// Placeholder: nil schema, to be filled later.
		g.defs[name] = nil
	}

	ref := g.draft.refPrefix() + name

	refSchema := &Schema{Ref: ref}
	g.refRecords = append(g.refRecords, refRecord{schema: refSchema, target: t})

	if nullable {
		// If the definition itself already admits null (an override or provider
		// that encodes null in its own type), the anyOf wrapper would add a
		// redundant second null branch; mirror applyNullable's dedup. The def
		// schema is available here for an extracted type (extractToDefs sets it
		// before this runs); a cycle placeholder leaves it nil, so the wrapper is
		// kept until the def is known.
		if def := g.typeToDefSchema[t]; def != nil &&
			(def.Type == typename.Null || slices.Contains(def.Types, typename.Null)) {
			return refSchema
		}

		return &Schema{
			AnyOf: []*Schema{
				refSchema,
				{Type: typename.Null},
			},
		}
	}

	return refSchema
}

// applyContainerType sets the type keyword on s for a nilable container whose
// non-null JSON type is base: ["null", base] when nullability is enabled,
// otherwise the bare base type. Such containers are nil-able in Go and so
// accept null unless WithNullable(false) opts out.
func (g *generator) applyContainerType(s *Schema, base string) {
	if g.nullable {
		s.Types = []string{typename.Null, base}
		return
	}

	s.Type = base
}

// applyNullable makes a schema nullable. Nullability is expressed by wrapping
// the schema in an anyOf with a "null"-typed alternative, matching the pattern
// used for nullable $ref schemas so all nullable pointers are represented
// consistently. A truly empty schema (no keywords at all) already accepts every
// value, including null, so it is returned as-is. A schema that lacks a type
// keyword but is still constrained by $ref/anyOf/allOf/oneOf/enum/const must be
// wrapped, since those constraints can reject null.
//
//nolint:unparam // t is kept for future use.
func (g *generator) applyNullable(s *Schema, t reflect.Type, nullable bool) *Schema {
	if !nullable {
		return s
	}

	// Already accepts null, so the anyOf wrapper adds nothing: an unconstrained
	// (true) schema accepts every value, and a schema whose type declaration
	// includes null already admits it. The latter covers a builtin that folds
	// null into its type list (the []byte override) and an override or provider
	// that returns a null-bearing type, keeping the output the bare form instead
	// of a redundant second null branch.
	if schemashape.IsEmpty(s) || s.Type == typename.Null || slices.Contains(s.Types, typename.Null) {
		return s
	}

	return &Schema{
		AnyOf: []*Schema{
			s,
			{Type: typename.Null},
		},
	}
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
// types it returns nil, since a nil interface cannot be called. An error the
// method itself returns is wrapped with the type and method so it locates
// the failing provider, matching [callExtender]. The user method runs
// against a zero value whose pointer fields are nil, so a method that
// dereferences such a field panics; the panic is recovered and returned as an
// error wrapping [ErrProviderPanic] so it surfaces from Generate rather than
// crashing the caller.
//
//nolint:nonamedreturns,nilnil // Recover needs named returns; a nil schema with a nil error means the type supplies no provider schema, which callers handle.
func callProvider(ctx context.Context, tc TypeContext) (s *jsonschema.Schema, err error) {
	t := tc.Type
	if t.Kind() == reflect.Interface {
		return nil, nil
	}

	defer func() {
		if r := recover(); r != nil {
			s = nil
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
		return nil, fmt.Errorf("%s.JSONSchema: %w", t, provErr)
	}

	if results[0].IsNil() {
		return nil, nil
	}

	sc, ok := results[0].Interface().(*jsonschema.Schema)
	if !ok {
		return nil, nil
	}

	return sc, nil
}

// callExtender calls JSONSchemaExtend on a zero value of the type. As with
// [callProvider], the method runs against a zero value, so a panic (for example
// dereferencing a nil pointer field) is recovered and returned as an error
// wrapping [ErrProviderPanic]. An error the method itself returns is wrapped
// with the type and method so it locates the failing extender.
func callExtender(ctx context.Context, tc TypeContext, s *jsonschema.Schema) (err error) {
	t := tc.Type

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
		Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(tc), reflect.ValueOf(s)})

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
// (structs, built-in overrides, named non-struct kinds) and never from the
// provider paths (registered or on-type), which replace reflection entirely.
func (g *generator) extendType(t reflect.Type, s *Schema) error {
	tc := TypeContext{Type: t, Draft: g.draft}

	if implementsExtender(t) {
		err := callExtender(g.ctx, tc, s)
		if err != nil {
			return err
		}
	}

	for _, e := range g.typeExtenders {
		err := e.ExtendSchemaForType(g.ctx, tc, s)
		if err != nil {
			return fmt.Errorf("extend type %s: %w", t, err)
		}
	}

	return nil
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
// field's schema. The provider receives the [FieldContext] tag interpreters
// get, with the tag pair empty and Owner the type declaring the field (see
// [reflectkind.DeclaringType]); an empty comment leaves the description unset, and a
// provider error aborts generation.
func (g *generator) applyFieldDescription(
	parentType reflect.Type, fi structFieldInfo, fieldSchema, parent *Schema,
) error {
	if g.descriptionProvider == nil {
		return nil
	}

	fc := g.fieldContext(parentType, fi, fieldSchema, parent)

	comment, err := g.descriptionProvider.FieldDescription(g.ctx, fc)
	if err != nil {
		return fmt.Errorf("describe field %q of %s: %w", fi.jsonName, parentType, err)
	}

	if comment != "" {
		fieldSchema.Description = comment
	}

	return nil
}
