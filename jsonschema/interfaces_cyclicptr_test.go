package jsonschema_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// cyclicPtr is a pointer type whose element chain never leaves the Pointer
// kind, the shape that requires a cycle guard in every deref loop.
type cyclicPtr *cyclicPtr

// TestFieldContextElementContextsCyclicPointer pins that ElementContexts
// terminates on a field whose type is a cyclic pointer. A WithTypeSchema
// override lets generation build a node for the type (bypassing the
// kind-dispatch rejection), so an interpreter asking for element contexts
// reaches elementType's pointer deref with a chain that never bottoms out;
// the guarded deref treats it as a non-container and yields no elements.
func TestFieldContextElementContextsCyclicPointer(t *testing.T) {
	t.Parallel()

	type host struct {
		F cyclicPtr `mytag:"x"`
	}

	var elems []jsonschema.FieldContext

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			elems = field.ElementContexts()

			return nil
		},
	)

	_, err := jsonschema.GenerateFor[host](
		t.Context(),
		jsonschema.WithTypeSchema(
			reflect.TypeFor[cyclicPtr](),
			jsonschema.TypeSchema{Value: &jsonschema.Schema{}},
		),
		jsonschema.WithTagInterpreter("mytag", interp),
	)
	require.NoError(t, err)
	require.Empty(t, elems)
}
