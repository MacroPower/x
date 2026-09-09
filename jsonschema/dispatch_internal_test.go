package jsonschema

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// nonZeroValue returns a set value of a Schema field's type, the way the
// field table's IsZero reads set: a non-nil pointer, a non-nil (empty)
// container, a non-empty string, or true.
func nonZeroValue(t *testing.T, typ reflect.Type) reflect.Value {
	t.Helper()

	switch typ.Kind() {
	case reflect.Pointer:
		return reflect.New(typ.Elem())
	case reflect.Slice:
		return reflect.MakeSlice(typ, 0, 0)
	case reflect.Map:
		return reflect.MakeMap(typ)
	case reflect.String:
		return reflect.ValueOf("x").Convert(typ)
	case reflect.Bool:
		return reflect.ValueOf(true)
	default:
		t.Fatalf("no set value for a Schema field of type %s", typ)

		return reflect.Value{}
	}
}

// rowClaims reports whether one of the row's keywords claims the named
// Schema field in the keyword metadata.
func rowClaims(e *keywordEntry, field string) bool {
	for _, kw := range e.keywords {
		if slices.Contains(keywordmeta.ByName[kw].Fields, field) {
			return true
		}
	}

	return false
}

// TestDispatchRowPresenceMatchesFields pins the per-node narrowing's presence
// predicate: no row is set on the empty schema, and for every exported Schema
// field set alone, a row is set exactly when one of its keywords claims that
// field in keywordmeta. The keyword metadata is the one declaration of which
// field a keyword reads, and the dispatch loop must not skip a row whose
// keyword is present.
func TestDispatchRowPresenceMatchesFields(t *testing.T) {
	t.Parallel()

	empty := &Schema{}
	for i := range keywordTable {
		assert.False(t, keywordTable[i].sets(empty), "row %q is set on the empty schema", keywordTable[i].name)
	}

	schemaType := reflect.TypeFor[Schema]()

	for sf := range schemaType.Fields() {
		if !sf.IsExported() {
			continue
		}

		t.Run(sf.Name, func(t *testing.T) {
			t.Parallel()

			probe := &Schema{}
			reflect.ValueOf(probe).Elem().FieldByName(sf.Name).Set(nonZeroValue(t, sf.Type))

			for i := range keywordTable {
				e := &keywordTable[i]
				assert.Equal(t, rowClaims(e, sf.Name), e.sets(probe),
					"row %q presence on a schema setting only %s", e.name, sf.Name)
			}
		})
	}
}

// TestRowsForKeepsTableOrder pins that the narrowed row list is a subsequence
// of the active rows in table order, so the phase partition survives, and
// that a node setting every field runs every active row through the shared
// slice.
func TestRowsForKeepsTableOrder(t *testing.T) {
	t.Parallel()

	v, err := newValidator(t.Context(), &Schema{}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, v.activeRows)

	position := map[*keywordEntry]int{}
	for i := range keywordTable {
		position[&keywordTable[i]] = i
	}

	probes := map[string]*Schema{
		"type and required":   {Type: "object", Required: []string{"a"}},
		"$ref beside allOf":   {Ref: "#", AllOf: []*Schema{{}}},
		"unevaluated last":    {UnevaluatedProperties: &Schema{}, Properties: map[string]*Schema{"a": {}}},
		"annotations only":    {Title: "t", Description: "d"},
		"format and pattern":  {Format: "email", Pattern: "^x"},
		"numeric and content": {Minimum: new(1.0), ContentEncoding: "base64"},
	}

	for name, probe := range probes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rows := v.rowsFor(probe)
			for i := 1; i < len(rows); i++ {
				assert.Less(t, position[rows[i-1]], position[rows[i]],
					"row %q must precede row %q, as in the table", rows[i-1].name, rows[i].name)
			}

			for _, e := range rows {
				assert.True(t, e.sets(probe), "row %q is listed but not set", e.name)
			}

			for _, e := range v.activeRows {
				if e.sets(probe) {
					assert.Contains(t, rows, e, "row %q is set but not listed", e.name)
				}
			}
		})
	}

	every := &Schema{}
	value := reflect.ValueOf(every).Elem()

	for sf := range reflect.TypeFor[Schema]().Fields() {
		if sf.IsExported() {
			value.FieldByName(sf.Name).Set(nonZeroValue(t, sf.Type))
		}
	}

	rows := v.rowsFor(every)
	assert.Equal(t, v.activeRows, rows, "a node setting every field runs every active row")
	assert.Same(t, &v.activeRows[0], &rows[0], "the every-field node shares the activeRows slice")
}
