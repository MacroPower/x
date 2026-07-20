package reflectkind_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// namedIntPtr is a named pointer type. Encoding/json never dereferences a
// named pointer for the json:",string" option, so it is not quotable.
type namedIntPtr *int

// namedInt is a named integer type, quotable without any dereference.
type namedInt int

func TestIsStringableType_PointerDeref(t *testing.T) {
	t.Parallel()

	// Encoding/json dereferences exactly one pointer level, and only when the
	// pointer type is unnamed, before checking the quotable kinds.
	tests := map[string]struct {
		typ  reflect.Type
		want bool
	}{
		"single unnamed pointer":       {typ: reflect.TypeFor[*int](), want: true},
		"unnamed pointer to named int": {typ: reflect.TypeFor[*namedInt](), want: true},
		"named int":                    {typ: reflect.TypeFor[namedInt](), want: true},
		"double pointer":               {typ: reflect.TypeFor[**int](), want: false},
		"named pointer":                {typ: reflect.TypeFor[namedIntPtr](), want: false},
		"unnamed pointer to named ptr": {typ: reflect.TypeFor[*namedIntPtr](), want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.IsStringableType(tc.typ))
		})
	}
}
