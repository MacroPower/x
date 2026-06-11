//nolint:testpackage // white-box: maintenance guard alongside clone_*_whitebox_test.go.
package jsonschema

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubschemasFieldCoverage is a maintenance guard over [Subschemas], the
// single source of truth for which Schema fields hold sub-schemas. It
// enumerates Schema's fields via reflection, populates every field of type
// *Schema, []*Schema, or map[string]*Schema on a probe schema with a distinct
// child, and asserts Subschemas returns each child exactly once. When upstream
// adds a new sub-schema-bearing field, the probe gains a child Subschemas does
// not return and this test fails, forcing the field list to be extended — so
// every traversal built on Subschemas (Walk, eachSubschema, schemaFormsTree,
// schemaContainsRef) picks the new keyword up in one place.
func TestSubschemasFieldCoverage(t *testing.T) {
	t.Parallel()

	var (
		singleType = reflect.TypeFor[*Schema]()
		sliceType  = reflect.TypeFor[[]*Schema]()
		mapType    = reflect.TypeFor[map[string]*Schema]()
	)

	probe := &Schema{}
	probeValue := reflect.ValueOf(probe).Elem()
	schemaType := probeValue.Type()

	// Map each planted child to the field it was planted in, so a missing
	// child names the uncovered field.
	want := map[*Schema]string{}

	for i := range schemaType.NumField() {
		field := schemaType.Field(i)
		if !field.IsExported() {
			continue
		}

		child := &Schema{}

		switch field.Type {
		case singleType:
			probeValue.Field(i).Set(reflect.ValueOf(child))
		case sliceType:
			probeValue.Field(i).Set(reflect.ValueOf([]*Schema{child}))
		case mapType:
			probeValue.Field(i).Set(reflect.ValueOf(map[string]*Schema{"k": child}))
		default:
			continue
		}

		want[child] = field.Name
	}

	// The upstream Schema currently carries 23 sub-schema-bearing fields; the
	// exact count is not pinned, but an implausibly low one means the
	// reflection above stopped matching the field types.
	require.GreaterOrEqual(t, len(want), 23,
		"reflection found fewer sub-schema-bearing fields than the known upstream set")

	got := map[*Schema]int{}
	for _, child := range Subschemas(probe) {
		got[child]++
	}

	for child, fieldName := range want {
		assert.Equal(t, 1, got[child],
			"Schema field %q holds a sub-schema that Subschemas must return exactly once", fieldName)
	}

	assert.Len(t, got, len(want),
		"Subschemas returned schemas that were not planted on the probe")
}
