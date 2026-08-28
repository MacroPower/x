package schemafield //nolint:testpackage // In-package by design: the canonical field table is guarded from inside its own package (see jsonschema/CLAUDE.md); the no-in-package-test policy is main-package only.

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shapeForType reports the [Shape] a field's Go type implies, so the table's
// declared Shape can be checked against upstream's struct. Only the three
// sub-schema container types map to a non-None shape.
func shapeForType(t reflect.Type) Shape {
	switch t {
	case reflect.TypeFor[*jsonschema.Schema]():
		return Single
	case reflect.TypeFor[[]*jsonschema.Schema]():
		return Slice
	case reflect.TypeFor[map[string]*jsonschema.Schema]():
		return Map
	default:
		return None
	}
}

// needsCloneContainer reports whether a field's Go type is one of the mutable
// header containers CloneSchemas leaves aliased ({slice, map, *any,
// json.RawMessage}) and is not itself a sub-schema field (those are cloned by
// upstream CloneSchemas). The numeric pointer fields (*float64, *int) are
// reference-typed but point at immutable scalars, so they are excluded.
func needsCloneContainer(t reflect.Type) bool {
	if shapeForType(t) != None {
		return false
	}

	switch t.Kind() {
	case reflect.Slice, reflect.Map:
		return true
	case reflect.Pointer:
		return t.Elem().Kind() == reflect.Interface
	default:
		return false
	}
}

// needsCloneDeep reports whether a field's Go type keeps a mutable interior
// after CloneContainer reallocates its header, which is what CloneDeep exists to
// copy. A container whose element type is itself a reference kind (an any, a
// slice, a map, or a pointer) qualifies; one whose elements are immutable
// scalars ([]byte, []string, map[string]bool, *float64) does not, and neither
// does a sub-schema field, whose children CloneSubschemas rebuilds.
func needsCloneDeep(t reflect.Type) bool {
	if shapeForType(t) != None {
		return false
	}

	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
	default:
		return false
	}

	switch t.Elem().Kind() {
	case reflect.Interface, reflect.Slice, reflect.Map, reflect.Pointer:
		return true
	default:
		return false
	}
}

// childOfShape returns a one-child container of the shape a sub-schema field
// holds, for driving a field's CloneSubschemas closure through reflection.
func childOfShape(shape Shape, child *jsonschema.Schema) any {
	switch shape {
	case Single:
		return child
	case Slice:
		return []*jsonschema.Schema{child}
	case Map:
		return map[string]*jsonschema.Schema{"k": child}
	case None:
		return nil
	}

	return nil
}

// TestFieldTableMatchesUpstream is the primary staleness alarm: it reflects over
// the upstream Schema and asserts the canonical table classifies every exported
// field exactly once, with a Shape matching the Go type, a valid Class, the
// right sub-schema accessor, and the right CloneContainer presence. When upstream
// adds a field, this test fails until the table lists it; no derived predicate
// needs touching.
func TestFieldTableMatchesUpstream(t *testing.T) {
	t.Parallel()

	schemaType := reflect.TypeFor[jsonschema.Schema]()

	byName := map[string]*Field{}

	for i := range Fields {
		f := &Fields[i]

		_, dup := byName[f.Name]
		require.False(t, dup, "field %q appears more than once in the table", f.Name)

		byName[f.Name] = f
	}

	var exported int

	for sf := range schemaType.Fields() {
		if !sf.IsExported() {
			continue
		}

		exported++

		f, ok := byName[sf.Name]
		require.True(t, ok, "upstream Schema field %q is not in the canonical table", sf.Name)

		t.Run(sf.Name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, shapeForType(sf.Type), f.Shape,
				"field %q Shape does not match its Go type %s", sf.Name, sf.Type)

			assert.GreaterOrEqual(t, f.Class, Constraint, "field %q has an invalid Class", sf.Name)
			assert.LessOrEqual(t, f.Class, Extension, "field %q has an invalid Class", sf.Name)

			assert.NotNil(t, f.IsZero, "field %q must have an IsZero closure", sf.Name)

			// Exactly the accessor selected by Shape is non-nil.
			assert.Equal(
				t,
				f.Shape == Single,
				f.SingleOf != nil,
				"field %q SingleOf presence must match Shape",
				sf.Name,
			)
			assert.Equal(t, f.Shape == Slice, f.SliceOf != nil, "field %q SliceOf presence must match Shape", sf.Name)
			assert.Equal(t, f.Shape == Map, f.MapOf != nil, "field %q MapOf presence must match Shape", sf.Name)

			// A sub-schema field carries its keyword token; a non-sub-schema
			// field leaves it empty.
			assert.Equal(t, f.Shape != None, f.Keyword != "",
				"field %q Keyword presence must match whether it is a sub-schema field", sf.Name)

			assert.Equal(t, needsCloneContainer(sf.Type), f.CloneContainer != nil,
				"field %q CloneContainer presence does not match its Go type %s", sf.Name, sf.Type)

			assert.Equal(t, needsCloneDeep(sf.Type), f.CloneDeep != nil,
				"field %q CloneDeep presence does not match its Go type %s", sf.Name, sf.Type)

			// A field with a mutable interior carries both closures: the deep
			// copy reallocates the header the shallow one would.
			if f.CloneDeep != nil {
				assert.NotNil(t, f.CloneContainer,
					"field %q carries CloneDeep, so it must carry CloneContainer too", sf.Name)
			}

			assert.Equal(t, f.Shape != None, f.CloneSubschemas != nil,
				"field %q CloneSubschemas presence must match Shape", sf.Name)
		})
	}

	assert.Len(t, Fields, exported, "the table must list exactly the exported Schema fields")
}

// TestSubschemasMatchesShapeSet cross-checks that the ordered Subschemas list is
// exactly the set of table fields with a sub-schema shape, so the curated
// emission order can never silently drop or gain a field.
func TestSubschemasMatchesShapeSet(t *testing.T) {
	t.Parallel()

	assert.Len(t, Subschemas, 23, "the upstream Schema carries 23 sub-schema-bearing fields")

	inList := map[string]bool{}
	for _, f := range Subschemas {
		require.NotNil(t, f, "Subschemas must not contain a nil field (a name typo in the order list)")
		assert.NotEqual(t, None, f.Shape, "Subschemas field %q must have a sub-schema shape", f.Name)

		inList[f.Name] = true
	}

	for i := range Fields {
		f := &Fields[i]
		if f.Shape != None {
			assert.True(t, inList[f.Name], "sub-schema field %q is missing from Subschemas", f.Name)
		}
	}

	assert.Len(t, inList, 23, "Subschemas must list each sub-schema field exactly once")
}

// TestDerivationsHandleNil pins the nil-schema contract the callers rely on.
func TestDerivationsHandleNil(t *testing.T) {
	t.Parallel()

	assert.False(t, IsTrue(nil))
	assert.False(t, IsEmpty(nil))
	assert.False(t, HasSiblingsBesides(nil, "Ref"))
}

// TestIsZeroReadsNilAndEmpty pins the JSON nil-versus-empty distinction: a
// non-nil empty slice or map counts as set for every container field.
func TestIsZeroReadsNilAndEmpty(t *testing.T) {
	t.Parallel()

	empty := &jsonschema.Schema{}
	assert.True(t, IsTrue(empty), "an all-zero schema is the true schema")

	nonNilEmpty := &jsonschema.Schema{
		Enum:          []any{},
		Examples:      []any{},
		PropertyOrder: []string{},
		Extra:         map[string]any{},
	}
	assert.False(t, IsTrue(nonNilEmpty), "non-nil empty containers count as set")
	assert.True(t, HasSiblingsBesides(nonNilEmpty, "Ref"),
		"a non-nil empty Enum stays a sibling under the strict nil-based semantics")

	outputInvisible := &jsonschema.Schema{
		Examples:      []any{},
		PropertyOrder: []string{},
		Extra:         map[string]any{},
	}
	assert.False(t, IsTrue(outputInvisible), "non-nil empty containers count as set")
	assert.False(t, HasSiblingsBesides(outputInvisible, "Ref"),
		"empty Examples, Extra, and PropertyOrder leave no trace in output, so they are not siblings")
}

// TestCloneContainersUnaliasesHeaders confirms the container clones reallocate
// each top-level header without deep-copying nested values. This is the
// reallocation schemashape.CloneOverrideExtras delegates to CloneContainers.
func TestCloneContainersUnaliasesHeaders(t *testing.T) {
	t.Parallel()

	c := any(42)
	s := &jsonschema.Schema{
		Enum:              []any{"a"},
		Const:             &c,
		Default:           json.RawMessage(`1`),
		Examples:          []any{"e"},
		Required:          []string{"x"},
		Types:             []string{"object"},
		PropertyOrder:     []string{"x"},
		Vocabulary:        map[string]bool{"v": true},
		DependencyStrings: map[string][]string{"a": {"b"}},
		DependentRequired: map[string][]string{"a": {"b"}},
		Extra:             map[string]any{"k": "v"},
	}

	orig := *s
	CloneContainers(s)

	// Header identity changes for every mutable container.
	assert.NotSame(t, &orig.Enum[0], &s.Enum[0])
	assert.NotSame(t, orig.Const, s.Const)

	// Appending to a clone must not reach the original's backing array.
	s.Required = append(s.Required, "y")
	assert.Equal(t, []string{"x"}, orig.Required)

	s.Extra["k2"] = "v2"
	_, leaked := orig.Extra["k2"]
	assert.False(t, leaked, "writes to a cloned map must not reach the source")
}

// TestCloneSubschemasWritesItsOwnField pins that each sub-schema field's setter
// writes the field its getter reads. A presence check cannot catch a copied row
// whose setter still names the field it was copied from: the closure would read
// one field and write another, and the clone would silently drop children.
func TestCloneSubschemasWritesItsOwnField(t *testing.T) {
	t.Parallel()

	for i := range Fields {
		f := &Fields[i]
		if f.Shape == None {
			continue
		}

		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()

			child := &jsonschema.Schema{Title: "child"}
			replacement := &jsonschema.Schema{Title: "replacement"}

			s := &jsonschema.Schema{}
			field := reflect.ValueOf(s).Elem().FieldByName(f.Name)
			field.Set(reflect.ValueOf(childOfShape(f.Shape, child)))

			var seen []*jsonschema.Schema

			f.CloneSubschemas(s, func(sub *jsonschema.Schema) *jsonschema.Schema {
				seen = append(seen, sub)

				return replacement
			})

			assert.Equal(t, []*jsonschema.Schema{child}, seen,
				"the getter must read the field the test populated")

			after := reflect.ValueOf(s).Elem().FieldByName(f.Name)
			assert.Equal(t, []*jsonschema.Schema{replacement}, childrenOf(after),
				"the setter must write the field the getter reads")
		})
	}
}

// childrenOf reads the sub-schemas out of a populated container of any shape.
func childrenOf(field reflect.Value) []*jsonschema.Schema {
	switch value := field.Interface().(type) {
	case *jsonschema.Schema:
		return []*jsonschema.Schema{value}

	case []*jsonschema.Schema:
		return value

	case map[string]*jsonschema.Schema:
		return slices.Collect(maps.Values(value))

	default:
		return nil
	}
}

// TestCloneSubschemasKeepsNilAndEmpty pins the nil-versus-empty distinction
// through the clone: an absent container stays absent, and a present empty one
// stays present, which is the difference between an ignored keyword and one that
// vacuously rejects.
func TestCloneSubschemasKeepsNilAndEmpty(t *testing.T) {
	t.Parallel()

	identity := func(sub *jsonschema.Schema) *jsonschema.Schema { return sub }

	for i := range Fields {
		f := &Fields[i]
		if f.Shape == None || f.Shape == Single {
			continue
		}

		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()

			absent := &jsonschema.Schema{}
			f.CloneSubschemas(absent, identity)
			assert.True(t, f.IsZero(absent), "a nil container must stay nil")

			empty := &jsonschema.Schema{}
			field := reflect.ValueOf(empty).Elem().FieldByName(f.Name)

			if f.Shape == Map {
				field.Set(reflect.MakeMap(field.Type()))
			} else {
				field.Set(reflect.MakeSlice(field.Type(), 0, 0))
			}

			f.CloneSubschemas(empty, identity)
			assert.False(t, f.IsZero(empty), "a non-nil empty container must stay non-nil")
		})
	}
}

// TestCloneDeepUnaliasesInteriors confirms the deep clones reach one level past
// the header CloneContainers reallocates, for the two map-of-list fields whose
// values a maps.Clone leaves shared and for the any-typed fields the value copier
// walks.
func TestCloneDeepUnaliasesInteriors(t *testing.T) {
	t.Parallel()

	c := any(map[string]any{"k": "v"})
	s := &jsonschema.Schema{
		Const:             &c,
		Enum:              []any{map[string]any{"k": "v"}},
		Examples:          []any{map[string]any{"k": "v"}},
		Extra:             map[string]any{"x": map[string]any{"k": "v"}},
		DependencyStrings: map[string][]string{"a": {"b"}},
		DependentRequired: map[string][]string{"a": {"b"}},
	}

	orig := *s

	// A copier that rebuilds every map it is handed, standing in for the
	// structural clone's own value walk.
	copyValue := func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}

		return maps.Clone(m)
	}

	for i := range Fields {
		if clone := Fields[i].CloneDeep; clone != nil {
			clone(s, copyValue)
		}
	}

	s.DependencyStrings["a"][0] = "mutated"
	s.DependentRequired["a"][0] = "mutated"

	if nested, ok := s.Extra["x"].(map[string]any); ok {
		nested["k"] = "mutated"
	}

	assert.Equal(t, []string{"b"}, orig.DependencyStrings["a"])
	assert.Equal(t, []string{"b"}, orig.DependentRequired["a"])
	assert.Equal(t, map[string]any{"k": "v"}, orig.Extra["x"])
}
