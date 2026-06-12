package jsonschema

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/big"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

var (
	typeTextMarshaler  = reflect.TypeFor[encoding.TextMarshaler]()
	typeJSONMarshaler  = reflect.TypeFor[json.Marshaler]()
	typeJSONRawMessage = reflect.TypeFor[json.RawMessage]()
	typeTime           = reflect.TypeFor[time.Time]()
	typeJSONNumber     = reflect.TypeFor[json.Number]()
	typeBigInt         = reflect.TypeFor[big.Int]()
	typeBigRat         = reflect.TypeFor[big.Rat]()
	typeBigFloat       = reflect.TypeFor[big.Float]()
	typeByteSlice      = reflect.TypeFor[[]byte]()
	typeProvider       = reflect.TypeFor[JSONSchemaProvider]()
	typeExtender       = reflect.TypeFor[JSONSchemaExtender]()
)

// generator holds the state for a single schema generation run.
type generator struct {
	// The caller's context for this generation run, passed to the
	// DescriptionProvider with every comment lookup.
	ctx context.Context

	typeToDefName   map[reflect.Type]string
	typeResolvers   []TypeSchemaResolver
	namer           Namer
	defs            map[string]*Schema
	defsNameToTypes map[string][]reflect.Type
	typeToDefSchema map[reflect.Type]*Schema
	visiting        map[reflect.Type]bool
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

func newGenerator(ctx context.Context, opts []GenerateOption) *generator {
	g := &generator{
		ctx:         ctx,
		draft:       Draft2020,
		namer:       NamerFunc(defaultNamer),
		definitions: true,
		nullable:    true,

		defs:            map[string]*Schema{},
		defsNameToTypes: map[string][]reflect.Type{},
		typeToDefName:   map[reflect.Type]string{},
		typeToDefSchema: map[reflect.Type]*Schema{},
		visiting:        map[reflect.Type]bool{},
	}
	for _, opt := range opts {
		opt.applyGenerate(g)
	}

	return g
}

// generate produces the root schema for the given type.
func (g *generator) generate(t reflect.Type) (*Schema, error) {
	// Follow pointers for root type identity.
	rootType := t
	for rootType.Kind() == reflect.Pointer {
		rootType = rootType.Elem()
	}

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

				delete(g.defs, defName)
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
	// supplied one. Unnamed roots produce an empty name and stay untitled.
	if g.rootTitle {
		target := g.rootTitleTarget(schema, rootType)
		if name := g.namer.SchemaName(rootType); name != "" && target.Title == "" {
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
	if inner := nullableInnerSchema(target); inner != nil {
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
	// Follow pointers.
	for t.Kind() == reflect.Pointer {
		nullable = g.nullable
		t = t.Elem()
	}

	// 1. Type resolver override (WithTypeSchemaResolver / WithTypeSchema).
	if s, ok := g.resolveTypeSchema(t); ok {
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
	// by that method — encoding/json resolves marshalers through the method
	// set, so the embedded type's marshaler takes over the whole struct and
	// reflecting its fields would describe a shape that never appears.
	// A promoted json.Marshaler can emit any JSON value, so the schema is
	// unrestricted; a promoted TextMarshaler always emits a string. A type
	// that directly implements json.Marshaler is deliberately NOT handled
	// here: per the documented resolution priority it falls through to
	// kind-based reflection, and WithTypeSchema or JSONSchemaProvider is the
	// escape hatch for its real shape.
	if isPromotedJSONMarshaler(t) {
		return g.handleBuiltinType(t, &Schema{}, nullable)
	}

	if isPromotedTextMarshaler(t) && !implementsJSONMarshaler(t) {
		return g.handleBuiltinType(t, &Schema{Type: typeNameString}, nullable)
	}

	// 5. TextMarshaler (direct implementation). A direct TextMarshaler
	// serializes as a string and shares the built-in path's type-level
	// post-processing (comments, extender, $defs extraction).
	if isDirectTextMarshaler(t) {
		s := &Schema{Type: typeNameString}
		return g.handleBuiltinType(t, s, nullable)
	}

	// 6. Cycle detection for named container types. A named type that contains
	// itself (type T []T, type M map[string]M, type A [N]A) recurses without
	// bound through schemaForKind. Tracking the type on the visiting stack lets a
	// re-entry emit a $ref to its $defs entry, breaking the cycle exactly as
	// schemaForStruct does for self-referential structs. Struct types run their
	// own equivalent guard inside schemaForStruct, so they are excluded here.
	guarded := t.Kind() != reflect.Struct && t.Name() != "" && isRecursiveContainerKind(t.Kind())
	if guarded {
		if g.visiting[t] {
			return g.refForType(t, nullable), nil
		}

		g.visiting[t] = true
	}

	// 7. Kind-based reflection.
	s, err := g.schemaForKind(t, nullable)
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
		g.applyTypeDescription(t, s)

		err := g.extendType(t, s)
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

// isRecursiveContainerKind reports whether a kind can hold a value of its own
// type and thus form a cycle through schemaForKind: slices, arrays, and maps
// recurse on the element (or value) type. Other non-struct kinds cannot embed
// themselves, so they need no cycle guard.
func isRecursiveContainerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

// resolveTypeSchema consults the registered type resolvers for t, newest
// registration first, and returns the first schema offered. The order makes a
// later registration win for the types two resolvers both handle, which for
// the exact-match resolvers WithTypeSchema registers preserves its
// last-registration-wins behavior.
func (g *generator) resolveTypeSchema(t reflect.Type) (*Schema, bool) {
	for _, v := range slices.Backward(g.typeResolvers) {
		if s, ok := v.SchemaForType(t); ok {
			return s, true
		}
	}

	return nil, false
}

// handleOverrideType processes a type resolved by a registered
// TypeSchemaResolver (WithTypeSchemaResolver or WithTypeSchema). A nil override
// marks the type unrestricted, mirroring a JSONSchemaProvider returning nil.
//
// The override is copied with the upstream shallow CloneSchemas, not the JSON
// round-trip cloneSchema used for remote refs: CloneSchemas preserves the
// caller's exact any-typed Enum/Const/Default values, whereas a round-trip
// would rewrite them (a Go int enum value would decode back as float64).
//
// CloneSchemas only deep-copies sub-schema fields, leaving the Enum, Const,
// Default, and Extra headers aliased to the caller's schema. CloneOverrideExtras
// copies those too, so a tag interpreter or JSONSchemaExtender that mutates them
// in place (appending to Enum, reassigning Const, writing into Extra) cannot
// reach back into an override reused across Generate calls.
func (g *generator) handleOverrideType(t reflect.Type, override *Schema, nullable bool) (*Schema, error) {
	if override == nil {
		override = &Schema{} // unrestricted
	}

	s := override.CloneSchemas()
	cloneOverrideExtras(s)

	// Apply type-level comments.
	g.applyTypeDescription(t, s)

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
// generate_override_test.go fails if a future upstream field of one of those
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
// extractToDefs aliases the pointer into g.defs. The same clone the override
// path uses (CloneSchemas to deep-copy sub-schemas, cloneOverrideExtras to copy
// the aliased Enum/Const/Default/Extra containers) isolates the generator's copy
// so the provider's source schema is never corrupted.
func (g *generator) handleProviderType(t reflect.Type, nullable bool) (*Schema, error) {
	provided, err := callProvider(t)
	if err != nil {
		return nil, err
	}

	if provided == nil {
		provided = &Schema{} // unrestricted
	}

	s := provided.CloneSchemas()
	cloneOverrideExtras(s)

	// Apply type-level comments.
	g.applyTypeDescription(t, s)

	if g.shouldExtract(t) {
		return g.extractToDefs(t, s, nullable)
	}

	return g.applyNullable(s, t, nullable), nil
}

// handleBuiltinType processes a type with a built-in override, applying
// type-level post-processing (comments, extender, $defs extraction) per
// the processing order.
func (g *generator) handleBuiltinType(t reflect.Type, s *Schema, nullable bool) (*Schema, error) {
	//nolint:nestif // Sequential post-processing steps; flattening adds no clarity.
	if t.Name() != "" {
		g.applyTypeDescription(t, s)

		err := g.extendType(t, s)
		if err != nil {
			return nil, err
		}

		if g.shouldExtract(t) {
			return g.extractToDefs(t, s, nullable)
		}
	}

	return g.applyNullable(s, t, nullable), nil
}

// builtinOverride returns a schema for well-known types, if applicable.
func (g *generator) builtinOverride(t reflect.Type) (*Schema, bool) {
	switch t {
	case typeByteSlice:
		s := &Schema{ContentEncoding: contentEncodingBase64}
		g.applyContainerType(s, typeNameString)

		return s, true

	case typeTime:
		return &Schema{Type: typeNameString, Format: formatDateTime}, true
	case typeJSONRawMessage:
		return &Schema{}, true
	case typeJSONNumber:
		return &Schema{Type: typeNameNumber}, true
	case typeBigInt:
		// Big.Int.MarshalJSON emits a bare JSON number (arbitrary precision),
		// not a string, so the schema is an unbounded integer. (big.Rat and
		// big.Float marshal via MarshalText and so are strings below.)
		return &Schema{Type: typeNameInteger}, true
	case typeBigRat:
		return &Schema{Type: typeNameString, Pattern: `^-?[0-9]+(/[0-9]+)?$`}, true
	case typeBigFloat:
		return &Schema{Type: typeNameString, Pattern: `^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`}, true
	}

	return nil, false
}

// schemaForKind handles the kind-based reflection step.
func (g *generator) schemaForKind(t reflect.Type, nullable bool) (*Schema, error) {
	switch t.Kind() {
	case reflect.Bool:
		return g.applyNullable(&Schema{Type: typeNameBoolean}, t, nullable), nil

	case reflect.String:
		return g.applyNullable(&Schema{Type: typeNameString}, t, nullable), nil

	case reflect.Int:
		// Plain int is platform-dependent (32 or 64 bit), so leave it unbounded.
		return g.applyNullable(&Schema{Type: typeNameInteger}, t, nullable), nil

	case reflect.Int64:
		// Float64 has a 52-bit mantissa and cannot represent MaxInt64 (2^63-1)
		// exactly, so an inclusive maximum cannot name the true boundary. 2^63 is
		// exactly representable (a power of two), so an exclusive maximum of 2^63
		// admits exactly the values v <= 2^63-1 = MaxInt64, including the boundary,
		// without ever accepting an out-of-range integer. MinInt64 (-2^63) is
		// representable exactly, so the minimum stays inclusive.
		s := &Schema{
			Type:             typeNameInteger,
			Minimum:          Ptr(float64(math.MinInt64)),
			ExclusiveMaximum: Ptr(exclusiveMaxInt64),
		}

		return g.applyNullable(s, t, nullable), nil

	case reflect.Int8:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(math.MinInt8)), Maximum: Ptr(float64(math.MaxInt8))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Int16:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(math.MinInt16)), Maximum: Ptr(float64(math.MaxInt16))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Int32:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(math.MinInt32)), Maximum: Ptr(float64(math.MaxInt32))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint, reflect.Uintptr:
		// Uint/uintptr are platform-dependent; only a lower bound is certain.
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(0))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint64:
		// Float64 cannot represent MaxUint64 (2^64-1) exactly; see the Int64 case.
		// 2^64 is exactly representable, so an exclusive maximum of 2^64 admits
		// exactly v <= 2^64-1 = MaxUint64, including the boundary value.
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(0)), ExclusiveMaximum: Ptr(exclusiveMaxUint64)}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint8:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(0)), Maximum: Ptr(float64(math.MaxUint8))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint16:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(0)), Maximum: Ptr(float64(math.MaxUint16))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Uint32:
		s := &Schema{Type: typeNameInteger, Minimum: Ptr(float64(0)), Maximum: Ptr(float64(math.MaxUint32))}
		return g.applyNullable(s, t, nullable), nil

	case reflect.Float32, reflect.Float64:
		return g.applyNullable(&Schema{Type: typeNameNumber}, t, nullable), nil

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
		if !pt.Implements(typeJSONMarshaler) && !pt.Implements(typeTextMarshaler) {
			s := &Schema{ContentEncoding: contentEncodingBase64}
			g.applyContainerType(s, typeNameString)

			return s, nil
		}
	}

	items, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("element type: %w", err)
	}

	s := &Schema{Items: items}
	g.applyContainerType(s, typeNameArray)

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
		Type:     typeNameArray,
		MinItems: Ptr(n),
		MaxItems: Ptr(n),
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
	if !isValidMapKey(t.Key()) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMapKey, t.Key())
	}

	valSchema, err := g.schemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("map value type: %w", err)
	}

	s := &Schema{AdditionalProperties: valSchema}
	g.applyContainerType(s, typeNameObject)

	return s, nil
}

// isValidMapKey checks if a type is a valid map key for JSON serialization.
func isValidMapKey(t reflect.Type) bool {
	if t.Kind() == reflect.String || isIntegerKind(t.Kind()) {
		return true
	}

	if t.Implements(typeTextMarshaler) || reflect.PointerTo(t).Implements(typeTextMarshaler) {
		return true
	}

	return false
}

// isIntegerKind reports whether k is one of Go's signed or unsigned integer
// kinds (including uintptr), all of which encoding/json renders as JSON
// integers.
func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
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
		Type: typeNameObject,
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
		schema *Schema
		fi     structFieldInfo
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

		fieldSchema, err := g.buildFieldSchema(t, fields[idx], s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fields[idx].jsonName, err)
		}

		pending = append(pending, pendingField{fi: fields[idx], schema: fieldSchema})
	}

	for i := range pending {
		pf := &pending[i]
		err := g.applyFieldInterpreters(pf.fi, pf.schema, s)
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
	g.applyTypeDescription(t, s)

	// JSONSchemaExtend, then registered extenders.
	err := g.extendType(t, s)
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
	}

	// Collect all visible fields grouped by JSON name.
	byName := map[string][]fieldLevel{}

	var order []string

	// Record adds a sighting of a JSON name. The dup flag marks fields of a
	// struct type embedded more than once at the same depth: the sighting is
	// recorded twice so the same-depth ambiguity resolution below drops the
	// name, matching encoding/json's annihilation of fields from repeated
	// embeds.
	record := func(name string, fl fieldLevel, dup bool) {
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}

		byName[name] = append(byName[name], fl)
		if dup {
			byName[name] = append(byName[name], fl)
		}
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
						info := parseJSONTag(f)
						if info.jsonName == "" {
							continue // json:"-"
						}

						record(
							info.jsonName,
							fieldLevel{field: f, depth: depth, optional: e.optional, tagged: true},
							dup,
						)

						continue
					}

					if ft.Kind() == reflect.Interface {
						// Embedded interface types: if they implement
						// JSONSchemaProvider, compose via allOf. Otherwise,
						// skip — an unrestricted schema adds no useful info.
						if g.needsAllOfComposition(ft) {
							name := "__allof__" + ft.Name() + fmt.Sprintf("__%d", len(order))
							record(name, fieldLevel{field: f, depth: depth, optional: e.optional}, false)
						}

						continue
					}

					if ft.Kind() == reflect.Struct {
						// Check if this embedded struct needs to be composed via allOf.
						if g.needsAllOfComposition(ft) {
							// Compose via allOf — treat as a single entry. A pointer
							// embed makes the composition optional: a nil pointer
							// contributes nothing to the marshaled object.
							name := "__allof__" + ft.Name() + fmt.Sprintf("__%d", len(order))
							record(
								name,
								fieldLevel{field: f, depth: depth, optional: e.optional || embeddedViaPointer},
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

				info := parseJSONTag(f)
				if info.jsonName == "" {
					continue // json:"-"
				}

				record(
					info.jsonName,
					fieldLevel{field: f, depth: depth, optional: e.optional, tagged: info.taggedName},
					dup,
				)
			}
		}
	}

	// Resolve shadowing and ambiguity.
	var result []structFieldInfo

	for _, name := range order {
		candidates := byName[name]
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
		isAllOf := strings.HasPrefix(name, "__allof__")

		if isAllOf {
			result = append(result, structFieldInfo{
				field:           f,
				composeViaAllOf: true,
				optional:        atMin[0].optional,
			})

			continue
		}

		info := parseJSONTag(f)
		if info.jsonName == "" {
			continue
		}

		sfi := structFieldInfo{
			field:      f,
			jsonName:   info.jsonName,
			omitempty:  info.omitempty || atMin[0].optional,
			omitzero:   info.omitzero,
			jsonString: info.jsonString,
		}
		result = append(result, sfi)
	}

	// The breadth-first walk sights names level by level, so `order` lists all
	// depth-0 names before any promoted ones. Sorting the winners by their
	// index path restores source declaration order — a promoted field sorts at
	// its embed's position — matching encoding/json's byIndex ordering.
	slices.SortStableFunc(result, func(a, b structFieldInfo) int {
		return slices.Compare(a.field.Index, b.field.Index)
	})

	return result
}

// needsAllOfComposition reports whether an embedded struct type should be
// composed via allOf rather than having its fields promoted.
func (g *generator) needsAllOfComposition(t reflect.Type) bool {
	// Check type resolver overrides (WithTypeSchemaResolver / WithTypeSchema).
	if _, ok := g.resolveTypeSchema(t); ok {
		return true
	}

	// Check JSONSchemaProvider.
	if implementsProvider(t) {
		return true
	}

	// Check built-in overrides.
	if _, ok := g.builtinOverride(t); ok {
		return true
	}

	// Check TextMarshaler (direct only, not promoted).
	if isDirectTextMarshaler(t) {
		return true
	}

	return false
}

// buildFieldSchema generates a struct field's schema, applies the json:",string"
// override, comment extraction, and jsonschema struct tag, then registers it in
// the parent's Properties/PropertyOrder and required list. Tag interpreters run
// later in applyFieldInterpreters once all sibling properties exist.
func (g *generator) buildFieldSchema(parentType reflect.Type, fi structFieldInfo, parent *Schema) (*Schema, error) {
	fieldType := fi.field.Type

	// Generate schema for the field's type.
	isPointer := fieldType.Kind() == reflect.Pointer
	fieldSchema, err := g.schemaForType(fieldType, false)
	if err != nil {
		return nil, err
	}

	// 1. JSON ",string" override. The tag-scalar type (used to parse the
	// jsonschema tag's const/enum/default values) defaults to the field's Go
	// type. When the override coerces the field schema to a string, encoding/json
	// also serializes the value as a quoted string, so the tag scalars must parse
	// as strings too — otherwise a numeric const on an int field would be
	// {"type":"string","const":5}, which the string-encoded "5" can never satisfy.
	tagType := fieldType
	if fi.jsonString {
		if isStringableType(fieldType) {
			// A pointer to a stringable type is a nilable container, so it shares
			// the slice/map null-branch policy; a non-pointer is always a bare
			// string.
			fieldSchema = &Schema{}
			if isPointer {
				g.applyContainerType(fieldSchema, typeNameString)
			} else {
				fieldSchema.Type = typeNameString
			}

			tagType = reflect.TypeFor[string]()
		}
	}

	// 2. Field-level comment.
	g.applyFieldDescription(parentType, fi.field, fieldSchema)

	// 3. Schema struct tag.
	if tag, ok := fi.field.Tag.Lookup("jsonschema"); ok {
		err := applyJSONSchemaTag(tag, tagType, fieldSchema)
		if err != nil {
			return nil, fmt.Errorf("jsonschema tag: %w", err)
		}

		// A nullable pointer field is generated as anyOf[value, null] with
		// annotations kept as siblings of anyOf. Const and enum test the instance
		// value regardless of its type, so on the wrapper they also reject the
		// permitted null; relocate them onto the value branch. Type-gated keywords
		// such as minimum and pattern do not apply to null and stay put.
		target := relocateConstEnumToValueBranch(fieldSchema)

		// An explicit const/enum fully constrains the value, so the type-derived
		// numeric bounds are redundant and are dropped. Keeping them would risk
		// rejecting a const/enum set to the type's own boundary value, and they add
		// nothing once the value is pinned to a fixed set.
		if target.Const != nil || target.Enum != nil {
			target.Minimum = nil
			target.Maximum = nil
			target.ExclusiveMinimum = nil
			target.ExclusiveMaximum = nil
		}
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

	return fieldSchema, nil
}

// applyFieldInterpreters runs the registered tag interpreters for a field and
// then wraps a bare $ref with allOf for Draft-07 when siblings were added. It
// runs after all field schemas are in place so interpreters see the full
// parent.Properties.
func (g *generator) applyFieldInterpreters(fi structFieldInfo, fieldSchema, parent *Schema) error {
	fieldType := fi.field.Type

	for _, reg := range g.tagInterpreters {
		if tag, ok := fi.field.Tag.Lookup(reg.key); ok {
			ctx := FieldContext{
				Name:        fi.jsonName,
				Type:        fieldType,
				Schema:      fieldSchema,
				Parent:      parent,
				StructField: fi.field,
				Draft:       g.draft,
			}
			err := reg.interp.Interpret(tag, ctx)
			if err != nil {
				return fmt.Errorf("tag interpreter %q: %w", reg.key, err)
			}
		}
	}

	// Interpreters set Const/Enum on the field schema, which for a nullable
	// pointer field is the anyOf wrapper. Const and enum test the instance value
	// regardless of its type, so on the wrapper they reject the permitted null;
	// relocate them onto the value branch, matching the jsonschema-tag path.
	relocateConstEnumToValueBranch(fieldSchema)

	// Wrap bare $ref with allOf for Draft-07 if annotations were added. This
	// mutates the schema in place, so the entry already in parent.Properties
	// reflects the change.
	g.wrapRefForDraft7(fieldSchema)

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
// a copy and asking [isEmptySchema], the maintained single source of truth for
// which keywords constrain a value; this catches every constraining keyword,
// including Not/AllOf/AnyOf/OneOf/Required/Types/If/Then/Else/DependentRequired/
// DependentSchemas and any future addition, without re-enumerating the list.
// Annotation and metadata keywords (description, title, default, deprecated,
// readOnly, writeOnly, examples) and the Extra escape hatch do not constrain a
// value, so isEmptySchema deliberately ignores them; they are checked
// explicitly here because they too must be preserved across the allOf wrap.
func hasRefSiblings(s *Schema) bool {
	// Annotation and metadata keywords, plus Extra: not constraints, so
	// isEmptySchema ignores them, but field-level processing can set them and
	// they must survive the allOf wrap.
	if s.Description != "" || s.Title != "" || s.Default != nil ||
		s.Deprecated || s.ReadOnly || s.WriteOnly ||
		len(s.Examples) > 0 || len(s.Extra) > 0 {
		return true
	}

	// Every constraining keyword: copy, clear $ref, and ask isEmptySchema.
	withoutRef := *s
	withoutRef.Ref = ""

	return !isEmptySchema(&withoutRef)
}

// processAllOfField handles embedded structs that need allOf composition.
//
// An embed reached through a pointer (fi.optional) contributes nothing to the
// marshaled object when the pointer is nil, so its schema cannot be an
// unconditional allOf branch — that would require the embed's properties in
// every instance and reject the nil-embed serialization. The branch is
// wrapped as anyOf[embedded, {}] instead: a non-nil embed matches the schema
// (and its annotations still flow to unevaluatedProperties), while a nil
// embed satisfies the empty alternative. The empty branch also admits a
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

	branch := embeddedSchema
	if embeddedSchema.Ref != "" {
		ref := &Schema{Ref: embeddedSchema.Ref}
		g.refRecords = append(g.refRecords, refRecord{schema: ref, target: ft})
		branch = ref
	}

	if fi.optional {
		branch = &Schema{AnyOf: []*Schema{branch, {}}}
	}

	parent.AllOf = append(parent.AllOf, branch)

	return nil
}

// extractToDefs places a type's schema in $defs and returns a $ref.
func (g *generator) extractToDefs(t reflect.Type, s *Schema, nullable bool) (*Schema, error) {
	name := g.namer.SchemaName(t)

	// Check if already defined (e.g., from a cycle placeholder).
	if existingName, exists := g.typeToDefName[t]; exists {
		// Already in defs; update if it was a placeholder.
		if g.defs[existingName] == nil {
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
		// Placeholder for cycle — register now.
		name = g.namer.SchemaName(t)
		g.typeToDefName[t] = name
		g.defsNameToTypes[name] = append(g.defsNameToTypes[name], t)
		// Placeholder: nil schema, to be filled later.
		g.defs[name] = nil
	}

	ref := g.draft.refPrefix() + name

	if nullable {
		refSchema := &Schema{Ref: ref}
		g.refRecords = append(g.refRecords, refRecord{schema: refSchema, target: t})

		return &Schema{
			AnyOf: []*Schema{
				refSchema,
				{Type: typeNameNull},
			},
		}
	}

	refSchema := &Schema{Ref: ref}
	g.refRecords = append(g.refRecords, refRecord{schema: refSchema, target: t})

	return refSchema
}

// applyContainerType sets the type keyword on s for a nilable container whose
// non-null JSON type is base: ["null", base] when nullability is enabled,
// otherwise the bare base type. Such containers are nil-able in Go and so
// accept null unless WithNullable(false) opts out.
func (g *generator) applyContainerType(s *Schema, base string) {
	if g.nullable {
		s.Types = []string{typeNameNull, base}
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

	// Unconstrained schema — already accepts null, so wrapping adds nothing.
	if isEmptySchema(s) {
		return s
	}

	return &Schema{
		AnyOf: []*Schema{
			s,
			{Type: "null"},
		},
	}
}

// relocateConstEnumToValueBranch moves any Const and Enum keywords set on a
// nullable pointer field's anyOf wrapper onto its value (non-null) branch and
// returns the schema that holds them afterward. A pointer field is generated as
// anyOf[value, {"type":"null"}] with field-level keywords kept as siblings of
// anyOf. Const and enum test the instance value regardless of its type, so on
// the wrapper they reject the permitted null; relocating them onto the value
// branch keeps null valid. Type-gated keywords such as minimum and pattern do
// not apply to null and stay on the wrapper.
//
// When s is not a nullable wrapper, or carries neither Const nor Enum, s is
// returned unchanged. Each keyword moves only when set on the wrapper, so a nil
// wrapper keyword never clobbers a value-branch keyword.
func relocateConstEnumToValueBranch(s *Schema) *Schema {
	inner := nullableInnerSchema(s)
	if inner == nil || (s.Const == nil && s.Enum == nil) {
		return s
	}

	if s.Const != nil {
		inner.Const, s.Const = s.Const, nil
	}

	if s.Enum != nil {
		inner.Enum, s.Enum = s.Enum, nil
	}

	return inner
}

// nullableInnerSchema returns the value (non-null) branch of a schema produced
// by [generator.applyNullable] — an anyOf of a value schema and
// {"type":"null"} — or nil if s does not have that exact shape.
func nullableInnerSchema(s *Schema) *Schema {
	if len(s.AnyOf) != 2 || s.AnyOf[0] == nil || s.AnyOf[1] == nil {
		return nil
	}

	if s.AnyOf[1].Type == typeNameNull {
		return s.AnyOf[0]
	}

	return nil
}

// isStringableType reports whether json:",string" applies to the given type.
func isStringableType(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if isIntegerKind(t.Kind()) {
		return true
	}

	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// isDirectTextMarshaler reports whether a type directly implements
// [encoding.TextMarshaler] (not via a promoted embedded field method).
func isDirectTextMarshaler(t reflect.Type) bool {
	if !implementsTextMarshaler(t) {
		return false
	}

	return hasDirectMethod(t, "MarshalText")
}

// implementsJSONMarshaler reports whether the type or its pointer type
// implements [encoding/json.Marshaler], directly or via promotion.
func implementsJSONMarshaler(t reflect.Type) bool {
	return t.Implements(typeJSONMarshaler) || reflect.PointerTo(t).Implements(typeJSONMarshaler)
}

// implementsTextMarshaler reports whether the type or its pointer type
// implements [encoding.TextMarshaler], directly or via promotion.
func implementsTextMarshaler(t reflect.Type) bool {
	return t.Implements(typeTextMarshaler) || reflect.PointerTo(t).Implements(typeTextMarshaler)
}

// isPromotedJSONMarshaler reports whether a type's method set includes
// MarshalJSON solely via promotion from an embedded field. Encoding/json
// resolves marshalers through the method set, so a promoted MarshalJSON
// serializes the whole outer value. Non-struct types cannot have promoted
// methods, so this is always false for them.
func isPromotedJSONMarshaler(t reflect.Type) bool {
	if !implementsJSONMarshaler(t) {
		return false
	}

	return !hasDirectMethod(t, "MarshalJSON")
}

// isPromotedTextMarshaler reports whether a type's method set includes
// MarshalText solely via promotion from an embedded field. See
// [isPromotedJSONMarshaler].
func isPromotedTextMarshaler(t reflect.Type) bool {
	if !implementsTextMarshaler(t) {
		return false
	}

	return !hasDirectMethod(t, "MarshalText")
}

// implementsProvider checks if a type (or pointer to type) implements
// JSONSchemaProvider directly (not just via an embedded field).
func implementsProvider(t reflect.Type) bool {
	if !t.Implements(typeProvider) && !reflect.PointerTo(t).Implements(typeProvider) {
		return false
	}

	return hasDirectMethod(t, "JSONSchema")
}

// implementsExtender checks if a type (or pointer to type) implements
// JSONSchemaExtender directly (not just via an embedded field).
func implementsExtender(t reflect.Type) bool {
	if !t.Implements(typeExtender) && !reflect.PointerTo(t).Implements(typeExtender) {
		return false
	}

	return hasDirectMethod(t, "JSONSchemaExtend")
}

// hasDirectMethod reports whether a method is defined directly on the type
// (not solely promoted from an embedded field). A method declared directly on
// the outer type shadows an embedded one at runtime, so detection must honor
// that: it cannot simply assume the method is promoted whenever an embedded
// field also provides it.
//
// Go offers no reflect API distinguishing a shadowing direct method from a
// promoted one, so this inspects the method's implementation: the compiler
// emits promotion wrappers with a synthetic "<autogenerated>" source location,
// whereas a directly declared method points to its real source file. It checks
// the value receiver first, then the pointer receiver, mirroring how Go
// resolves the method set; a pointer-receiver method that shadows a promoted
// value method suppresses that promotion, so the value method set reports no
// method and the pointer set yields the direct one.
func hasDirectMethod(t reflect.Type, name string) bool {
	if t.Kind() != reflect.Struct {
		// Non-struct types can't have promoted methods.
		return true
	}

	if m, ok := t.MethodByName(name); ok {
		return !isPromotedMethod(m)
	}

	if m, ok := reflect.PointerTo(t).MethodByName(name); ok {
		return !isPromotedMethod(m)
	}

	// The method is not in the type's method set at all; treat as not direct.
	return false
}

// isPromotedMethod reports whether a method is a compiler-generated promotion
// wrapper rather than a directly declared method. Promotion wrappers report a
// synthetic "<autogenerated>" source location.
func isPromotedMethod(m reflect.Method) bool {
	fn := runtime.FuncForPC(m.Func.Pointer())
	if fn == nil {
		return false
	}

	file, _ := fn.FileLine(m.Func.Pointer())

	return strings.Contains(file, "<autogenerated>")
}

// callProvider calls JSONSchema() on a zero value of the type. For interface
// types it returns nil, since a nil interface cannot be called. The user method
// runs against a zero value whose pointer fields are nil, so a method that
// dereferences such a field panics; the panic is recovered and returned as an
// error wrapping [ErrProviderPanic] so it surfaces from Generate rather than
// crashing the caller.
//
//nolint:nonamedreturns,nilnil // Recover needs named returns; a nil schema with a nil error means the type supplies no provider schema, which callers handle.
func callProvider(t reflect.Type) (s *jsonschema.Schema, err error) {
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

	result := v.MethodByName("JSONSchema").Call(nil)
	if result[0].IsNil() {
		return nil, nil
	}

	sc, ok := result[0].Interface().(*jsonschema.Schema)
	if !ok {
		return nil, nil
	}

	return sc, nil
}

// callExtender calls JSONSchemaExtend() on a zero value of the type. As with
// [callProvider], the method runs against a zero value, so a panic (for example
// dereferencing a nil pointer field) is recovered and returned as an error
// wrapping [ErrProviderPanic].
func callExtender(t reflect.Type, s *jsonschema.Schema) (err error) {
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

	v.MethodByName("JSONSchemaExtend").Call([]reflect.Value{reflect.ValueOf(s)})

	return nil
}

// extendType runs type-level schema extension for a reflection-produced
// schema: the type's own JSONSchemaExtend when implemented, then the
// registered [TypeSchemaExtender] values ([WithTypeSchemaExtender]) in
// registration order, so a registered extender sees — and can adjust — what
// the type's author produced. It is called from each reflection path
// (structs, built-in overrides, named non-struct kinds) and never from the
// resolver or [JSONSchemaProvider] paths, which replace reflection entirely.
func (g *generator) extendType(t reflect.Type, s *Schema) error {
	if implementsExtender(t) {
		err := callExtender(t, s)
		if err != nil {
			return err
		}
	}

	for _, e := range g.typeExtenders {
		err := e.ExtendSchemaForType(t, s)
		if err != nil {
			return fmt.Errorf("extend type %s: %w", t, err)
		}
	}

	return nil
}

// jsonTagInfo holds parsed json tag information.
type jsonTagInfo struct {
	jsonName   string // empty means excluded (json:"-")
	omitempty  bool
	omitzero   bool
	jsonString bool
	// TaggedName is true when jsonName comes from an explicit json tag name
	// rather than the Go field name. Encoding/json's same-depth collision
	// tie-break keeps a field only when exactly one colliding field is tagged.
	taggedName bool
}

// parseJSONTag parses the json struct tag.
func parseJSONTag(f reflect.StructField) jsonTagInfo {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		// Use field name if no tag.
		if !f.IsExported() && !f.Anonymous {
			return jsonTagInfo{} // excluded
		}

		name := f.Name
		// For embedded non-struct types, use the type name.
		if f.Anonymous {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			name = ft.Name()
		}

		return jsonTagInfo{jsonName: name}
	}

	name, rest, found := strings.Cut(tag, ",")
	if name == "-" && !found {
		return jsonTagInfo{} // excluded
	}

	// A non-empty name segment is an explicit json tag name; an options-only tag
	// (json:",omitempty") leaves the name empty and falls back to the field name,
	// which is not "tagged" for the same-depth collision tie-break.
	taggedName := name != ""

	if name == "" {
		// Use field name.
		name = f.Name
		if f.Anonymous {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			name = ft.Name()
		}
	}

	info := jsonTagInfo{jsonName: name, taggedName: taggedName}
	if found {
		for s := range strings.SplitSeq(rest, ",") {
			switch s {
			case "omitempty":
				info.omitempty = true
			case "omitzero":
				info.omitzero = true
			case "string": //nolint:goconst // The encoding/json ",string" tag option, not the JSON Schema type name.
				info.jsonString = true
			}
		}
	}

	return info
}

// applyTypeDescription sets the description from the comment provider on a
// type's schema. An empty comment leaves the description unset.
func (g *generator) applyTypeDescription(t reflect.Type, s *Schema) {
	if g.descriptionProvider == nil {
		return
	}

	if comment := g.descriptionProvider.TypeDescription(g.ctx, t); comment != "" {
		s.Description = comment
	}
}

// applyFieldDescription sets the description from the comment provider on a
// field's schema. The provider receives the type declaring the field (see
// [declaringType]); an empty comment leaves the description unset.
func (g *generator) applyFieldDescription(structType reflect.Type, f reflect.StructField, s *Schema) {
	if g.descriptionProvider == nil {
		return
	}

	if comment := g.descriptionProvider.FieldDescription(g.ctx, declaringType(structType, f), f.Name); comment != "" {
		s.Description = comment
	}
}

// declaringType returns the struct type that actually declares field f. For a
// field promoted from an embedded struct this is the embedded type, not the
// outer struct, and the field's doc comment lives in that type's source. The
// field's index path is absolute from outer, so walking all but its last element
// (dereferencing embedded pointers) reaches the declaring type.
func declaringType(outer reflect.Type, f reflect.StructField) reflect.Type {
	t := outer

	for _, i := range f.Index[:max(len(f.Index)-1, 0)] {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}

		t = t.Field(i).Type
	}

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}
