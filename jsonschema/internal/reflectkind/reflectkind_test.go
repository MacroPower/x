package reflectkind_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// directText declares MarshalText directly.
type directText struct{ V int }

func (directText) MarshalText() ([]byte, error) { return []byte("x"), nil }

// promotedText gets MarshalText solely by embedding directText.
type promotedText struct{ directText }

// shadowText embeds directText but redeclares MarshalText, shadowing the
// promoted method with a directly declared one. The embedded field's
// contribution is exercised only through reflection in the tests below.
type shadowText struct {
	directText //nolint:unused // promotion/shadowing is exercised via reflection
}

func (shadowText) MarshalText() ([]byte, error) { return []byte("y"), nil }

// directJSON declares MarshalJSON directly.
type directJSON struct{ V int }

func (directJSON) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

// promotedJSON gets MarshalJSON solely by embedding directJSON.
type promotedJSON struct{ directJSON }

// plain implements no marshaler.
type plain struct{ V int }

func TestHasDirectMethod(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ    reflect.Type
		method string
		want   bool
	}{
		"directly declared":            {typ: reflect.TypeFor[directText](), method: "MarshalText", want: true},
		"promoted from embedded field": {typ: reflect.TypeFor[promotedText](), method: "MarshalText", want: false},
		"shadowing override":           {typ: reflect.TypeFor[shadowText](), method: "MarshalText", want: true},
		"missing method on struct":     {typ: reflect.TypeFor[plain](), method: "MarshalText", want: false},
		"non-struct short-circuits":    {typ: reflect.TypeFor[int](), method: "MarshalText", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.HasDirectMethod(tc.typ, tc.method))
		})
	}
}

func TestDirectAndPromotedTextMarshaler(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ          reflect.Type
		wantDirect   bool
		wantPromoted bool
	}{
		"direct":   {typ: reflect.TypeFor[directText](), wantDirect: true, wantPromoted: false},
		"promoted": {typ: reflect.TypeFor[promotedText](), wantDirect: false, wantPromoted: true},
		"shadow":   {typ: reflect.TypeFor[shadowText](), wantDirect: true, wantPromoted: false},
		"none":     {typ: reflect.TypeFor[plain](), wantDirect: false, wantPromoted: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantDirect, reflectkind.IsDirectTextMarshaler(tc.typ))
			assert.Equal(t, tc.wantPromoted, reflectkind.IsPromotedTextMarshaler(tc.typ))
		})
	}
}

func TestJSONMarshaler(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ           reflect.Type
		wantImplement bool
		wantPromoted  bool
	}{
		"direct":   {typ: reflect.TypeFor[directJSON](), wantImplement: true, wantPromoted: false},
		"promoted": {typ: reflect.TypeFor[promotedJSON](), wantImplement: true, wantPromoted: true},
		"none":     {typ: reflect.TypeFor[plain](), wantImplement: false, wantPromoted: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantImplement, reflectkind.ImplementsJSONMarshaler(tc.typ))
			assert.Equal(t, tc.wantPromoted, reflectkind.IsPromotedJSONMarshaler(tc.typ))
		})
	}
}

func TestIsStringableType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ  reflect.Type
		want bool
	}{
		"int":         {typ: reflect.TypeFor[int](), want: true},
		"uint8":       {typ: reflect.TypeFor[uint8](), want: true},
		"string":      {typ: reflect.TypeFor[string](), want: true},
		"bool":        {typ: reflect.TypeFor[bool](), want: true},
		"float64":     {typ: reflect.TypeFor[float64](), want: true},
		"pointer int": {typ: reflect.TypeFor[*int](), want: true},
		"struct":      {typ: reflect.TypeFor[plain](), want: false},
		"byte slice":  {typ: reflect.TypeFor[[]byte](), want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.IsStringableType(tc.typ))
		})
	}
}

func TestIsValidMapKey(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ  reflect.Type
		want bool
	}{
		"string":              {typ: reflect.TypeFor[string](), want: true},
		"int":                 {typ: reflect.TypeFor[int](), want: true},
		"value textmarshaler": {typ: reflect.TypeFor[directText](), want: true},
		"float":               {typ: reflect.TypeFor[float64](), want: false},
		"plain struct":        {typ: reflect.TypeFor[plain](), want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.IsValidMapKey(tc.typ))
		})
	}
}

func TestIsRecursiveContainerKind(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		kind reflect.Kind
		want bool
	}{
		"slice":  {kind: reflect.Slice, want: true},
		"array":  {kind: reflect.Array, want: true},
		"map":    {kind: reflect.Map, want: true},
		"struct": {kind: reflect.Struct, want: false},
		"int":    {kind: reflect.Int, want: false},
		"ptr":    {kind: reflect.Pointer, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reflectkind.IsRecursiveContainerKind(tc.kind))
		})
	}
}

func TestDeclaringType(t *testing.T) {
	t.Parallel()

	// Field V of promotedText is promoted from the embedded directText, which is
	// its declaring type rather than the outer struct.
	outer := reflect.TypeFor[promotedText]()
	promoted, ok := outer.FieldByName("V")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeFor[directText](), reflectkind.DeclaringType(outer, promoted))

	// Field V of plain is declared by plain itself.
	own := reflect.TypeFor[plain]()
	field, ok := own.FieldByName("V")
	require.True(t, ok)
	assert.Equal(t, own, reflectkind.DeclaringType(own, field))
}
