package reflectkind_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// namedIntPtr is a named pointer type. Encoding/json/v2's json:",string" flag
// survives every pointer level, named or not, so the payload kind decides.
type namedIntPtr *int

// namedInt is a named integer type, stringifiable without any dereference.
type namedInt int

func TestIsStringifiableNumber_PointerDeref(t *testing.T) {
	t.Parallel()

	// The flag follows the whole pointer chain: a **int under json:",string"
	// stringifies its payload, unlike v1's single-level dereference.
	tests := map[string]struct {
		typ  reflect.Type
		want bool
	}{
		"single unnamed pointer":       {typ: reflect.TypeFor[*int](), want: true},
		"unnamed pointer to named int": {typ: reflect.TypeFor[*namedInt](), want: true},
		"named int":                    {typ: reflect.TypeFor[namedInt](), want: true},
		"named pointer":                {typ: reflect.TypeFor[namedIntPtr](), want: true},
		"double pointer":               {typ: reflect.TypeFor[**int](), want: true},
		"unnamed pointer to named ptr": {typ: reflect.TypeFor[*namedIntPtr](), want: true},
		"pointer to json.Number":       {typ: reflect.TypeFor[*json.Number](), want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.IsStringifiableNumber(tc.typ))
		})
	}
}
