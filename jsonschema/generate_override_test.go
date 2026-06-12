package jsonschema_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// aliasingContainerTypes lists the non-sub-schema reference types a registered
// [jsonschema.WithTypeSchema] override carries. Generation must hand out
// schemas whose containers do not alias the registered override, or an
// in-place append/assign by an extender, interpreter, or caller would corrupt
// the override across Generate calls.
var aliasingContainerTypes = []reflect.Type{
	reflect.TypeFor[[]any](),
	reflect.TypeFor[[]string](),
	reflect.TypeFor[map[string]bool](),
	reflect.TypeFor[map[string][]string](),
	reflect.TypeFor[json.RawMessage](),
	reflect.TypeFor[*any](),
	reflect.TypeFor[map[string]any](),
}

// isAliasingContainerType reports whether t is one of the shared-container
// types the guard tracks.
func isAliasingContainerType(t reflect.Type) bool {
	return slices.Contains(aliasingContainerTypes, t)
}

// TestTypeSchemaOverrideContainersUnaliased is a maintenance guard ensuring
// every exported Schema field of a container type is unaliased by the
// [jsonschema.WithTypeSchema] generation path. For each such field the test
// registers an override with fresh backing storage, generates a schema from
// it, and asserts the field's backing pointer changed -- proving a write
// through the generated schema cannot reach the registered override. When
// upstream adds a new field of one of these types and the override copy does
// not cover it, the test fails rather than silently aliasing.
func TestTypeSchemaOverrideContainersUnaliased(t *testing.T) {
	t.Parallel()

	type overrideProbe struct{}

	probeType := reflect.TypeFor[overrideProbe]()
	schemaType := reflect.TypeFor[jsonschema.Schema]()

	var (
		covered   []string
		uncovered []string
	)

	for i := range schemaType.NumField() {
		field := schemaType.Field(i)
		if !field.IsExported() || !isAliasingContainerType(field.Type) {
			continue
		}

		override := populatedSchema(t, field)

		// Definitions are disabled so the generated root is the override copy
		// itself rather than a $ref into $defs.
		got, err := jsonschema.Generate(t.Context(), probeType,
			jsonschema.WithTypeSchema(probeType, override),
			jsonschema.WithDefinitions(false),
		)
		require.NoError(t, err, "generate with override for field %s", field.Name)

		if containerPointer(t, fieldValue(override, field)) != containerPointer(t, fieldValue(got, field)) {
			covered = append(covered, field.Name)

			continue
		}

		uncovered = append(uncovered, field.Name)
	}

	sort.Strings(uncovered)
	require.Empty(t, uncovered,
		"exported Schema field(s) %v of a container type stay aliased between a WithTypeSchema "+
			"override and the generated schema; a write through the generated schema would corrupt "+
			"the override across Generate calls",
		uncovered)

	// Sanity: the guard actually inspected the fields it is meant to protect.
	require.NotEmpty(t, covered, "guard found no container-typed Schema fields to check; the type set is stale")
	assert.Contains(t, covered, "Examples", "Examples is the empirically reproduced aliasing case and must be covered")
}

// populatedSchema returns a *Schema with the given field set to a fresh,
// non-empty value of the field's type, so containerPointer can read a stable
// backing pointer. Slices and maps gain one element; the *any pointer
// addresses a fresh value.
func populatedSchema(t *testing.T, field reflect.StructField) *jsonschema.Schema {
	t.Helper()

	s := &jsonschema.Schema{}
	fv := reflect.ValueOf(s).Elem().FieldByIndex(field.Index)

	switch field.Type.Kind() {
	case reflect.Slice:
		elem := field.Type.Elem()
		slice := reflect.MakeSlice(field.Type, 1, 1)
		slice.Index(0).Set(sampleElem(t, elem))
		fv.Set(slice)

	case reflect.Map:
		m := reflect.MakeMapWithSize(field.Type, 1)
		m.SetMapIndex(sampleElem(t, field.Type.Key()), sampleElem(t, field.Type.Elem()))
		fv.Set(m)

	case reflect.Pointer:
		p := reflect.New(field.Type.Elem())
		fv.Set(p)

	default:
		t.Fatalf("unexpected container kind %s for field %s", field.Type.Kind(), field.Name)
	}

	return s
}

// sampleElem returns a non-zero value for a slice/map element or key type used
// to populate a guard schema.
func sampleElem(t *testing.T, et reflect.Type) reflect.Value {
	t.Helper()

	switch et.Kind() {
	case reflect.String:
		return reflect.ValueOf("x").Convert(et)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(et)
	case reflect.Uint8: // json.RawMessage element
		return reflect.ValueOf(byte('x')).Convert(et)
	case reflect.Interface: // []any element
		return reflect.ValueOf(any("x"))
	case reflect.Slice: // map[string][]string value
		s := reflect.MakeSlice(et, 1, 1)
		s.Index(0).Set(sampleElem(t, et.Elem()))

		return s

	default:
		t.Fatalf("unexpected element kind %s", et.Kind())

		return reflect.Value{}
	}
}

// fieldValue reads the named field off a *Schema.
func fieldValue(s *jsonschema.Schema, field reflect.StructField) reflect.Value {
	return reflect.ValueOf(s).Elem().FieldByIndex(field.Index)
}

// containerPointer returns the backing pointer that identifies a slice, map,
// or pointer value, so two values can be compared for shared storage.
func containerPointer(t *testing.T, v reflect.Value) uintptr {
	t.Helper()

	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
		return v.Pointer()
	default:
		t.Fatalf("containerPointer: unexpected kind %s", v.Kind())

		return 0
	}
}
