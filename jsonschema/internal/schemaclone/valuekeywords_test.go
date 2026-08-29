package schemaclone //nolint:testpackage // In-package by design: valueKeywords is this package's own table and has no exported form (see jsonschema/CLAUDE.md); the no-in-package-test policy is main-package only.

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// TestValueKeywordsCoverCopiedFields pins that every field handing its
// container to the value copier has a keyword the walk can push, so a path
// reported through that container names the keyword its contents sit under.
// Extra is the one exception, since its own keys are top-level keywords the map
// walk pushes. A future any-typed field arriving with no entry fails here
// instead of reporting a cycle path missing a segment.
//
// DependencyStrings and DependentRequired carry a CloneDeep that never reaches
// the copier, so they need no entry. The test reads which rows call the copier
// rather than naming those rows, so a row that starts copying values is caught
// too.
func TestValueKeywordsCoverCopiedFields(t *testing.T) {
	t.Parallel()

	copies := map[string]bool{}
	for i := range schemafield.Fields {
		copies[schemafield.Fields[i].Name] = schemafield.Fields[i].CloneDeep != nil
	}

	for name := range valueKeywords {
		assert.True(t, copies[name], "valueKeywords names %q, which no field deep-copies", name)
	}

	for i := range schemafield.Fields {
		f := &schemafield.Fields[i]
		if f.CloneDeep == nil {
			continue
		}

		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()

			s := &jsonschema.Schema{}
			field := reflect.ValueOf(s).Elem().FieldByName(f.Name)
			field.Set(populatedContainer(t, field.Type()))

			var copied bool

			f.CloneDeep(s, func(v any) any {
				copied = true

				return v
			})

			if !copied {
				return
			}

			_, named := valueKeywords[f.Name]
			assert.True(t, named || f.Name == "Extra",
				"field %q hands its contents to the copier, so it needs a keyword to push", f.Name)
		})
	}
}

// populatedContainer returns a non-nil value of a pointer, slice, or map type,
// so a CloneDeep closure guarded on a nil field reaches its copier.
func populatedContainer(t *testing.T, typ reflect.Type) reflect.Value {
	t.Helper()

	switch typ.Kind() {
	case reflect.Pointer:
		return reflect.New(typ.Elem())

	case reflect.Slice:
		return reflect.MakeSlice(typ, 1, 1)

	case reflect.Map:
		m := reflect.MakeMap(typ)
		m.SetMapIndex(reflect.New(typ.Key()).Elem(), reflect.New(typ.Elem()).Elem())

		return m

	default:
		require.Fail(t, "a deep-copied field must hold a pointer, slice, or map",
			"field type %s holds its value directly, so the walk needs a segment rule for it", typ)

		return reflect.Value{}
	}
}
