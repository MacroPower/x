package jsonschema_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		instance any
		want     any
	}{
		"int":     {instance: 30, want: json.Number("30")},
		"int8":    {instance: int8(-8), want: json.Number("-8")},
		"int16":   {instance: int16(-16), want: json.Number("-16")},
		"int32":   {instance: int32(-32), want: json.Number("-32")},
		"int64":   {instance: int64(-64), want: json.Number("-64")},
		"uint":    {instance: uint(1), want: json.Number("1")},
		"uint8":   {instance: uint8(8), want: json.Number("8")},
		"uint16":  {instance: uint16(16), want: json.Number("16")},
		"uint32":  {instance: uint32(32), want: json.Number("32")},
		"uint64":  {instance: uint64(math.MaxUint64), want: json.Number("18446744073709551615")},
		"uintptr": {instance: uintptr(7), want: json.Number("7")},
		"large int64 exact": {
			instance: int64(9007199254740993), // 2^53+1: float64 would round this
			want:     json.Number("9007199254740993"),
		},
		"float32": {instance: float32(0.5), want: 0.5},
		"float64": {instance: 1.5, want: 1.5},
		"string":  {instance: "x", want: "x"},
		"bool":    {instance: true, want: true},
		"nil":     {instance: nil, want: nil},
		"json.Number": {
			instance: json.Number("1.5"),
			want:     json.Number("1.5"),
		},
		"nested map and slice": {
			instance: map[string]any{
				"age":  30,
				"tags": []any{uint8(1), "a", float32(2)},
				"sub":  map[string]any{"n": int64(5)},
			},
			want: map[string]any{
				"age":  json.Number("30"),
				"tags": []any{json.Number("1"), "a", float64(2)},
				"sub":  map[string]any{"n": json.Number("5")},
			},
		},
		"unaccepted type passes through": {
			instance: struct{ X int }{X: 1},
			want:     struct{ X int }{X: 1},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, jsonschema.Normalize(tt.instance))
		})
	}
}

// TestNormalizeCopyOnWrite pins that Normalize returns an already JSON-shaped
// container unchanged (same backing storage, no allocation) and never mutates
// an input that does need conversion.
func TestNormalizeCopyOnWrite(t *testing.T) {
	t.Parallel()

	t.Run("unchanged input returned as-is", func(t *testing.T) {
		t.Parallel()

		m := map[string]any{"a": "x", "n": 1.5, "l": []any{"y"}}
		got := jsonschema.Normalize(m)

		gotMap, ok := got.(map[string]any)
		require.True(t, ok)
		assert.Equal(t,
			reflect.ValueOf(m).Pointer(), reflect.ValueOf(gotMap).Pointer(),
			"an already-normalized map should be returned without copying")
	})

	t.Run("input not mutated", func(t *testing.T) {
		t.Parallel()

		m := map[string]any{"age": 30, "l": []any{int64(1)}}
		got := jsonschema.Normalize(m)

		assert.Equal(t, map[string]any{"age": 30, "l": []any{int64(1)}}, m,
			"the input must keep its original Go values")
		assert.Equal(t,
			map[string]any{"age": json.Number("30"), "l": []any{json.Number("1")}},
			got)
	})
}

// TestValidateGoNumericKinds pins that Validate accepts Go numeric kinds that
// encoding/json does not produce — values decoded from YAML/TOML or built by
// hand — by normalizing them up front.
func TestValidateGoNumericKinds(t *testing.T) {
	t.Parallel()

	numberObj := &jsonschema.Schema{
		Type:       "object",
		Properties: map[string]*jsonschema.Schema{"age": {Type: "number"}},
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		valid    bool
	}{
		"nested int as number": {
			schema:   numberObj,
			instance: map[string]any{"age": 30},
			valid:    true,
		},
		"top-level int as integer": {
			schema:   &jsonschema.Schema{Type: "integer"},
			instance: 42,
			valid:    true,
		},
		"int64 as number": {
			schema:   &jsonschema.Schema{Type: "number"},
			instance: int64(30),
			valid:    true,
		},
		"uint64 max as integer": {
			schema:   &jsonschema.Schema{Type: "integer"},
			instance: uint64(math.MaxUint64),
			valid:    true,
		},
		"float32 as number": {
			schema:   &jsonschema.Schema{Type: "number"},
			instance: float32(1.5),
			valid:    true,
		},
		"NaN is a number": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"x": {Type: "number"}},
			},
			instance: map[string]any{"x": math.NaN()},
			valid:    true,
		},
		"int fails string assertion": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: 42,
			valid:    false,
		},
		"large int64 compared exactly": {
			// 2^53+1 exceeds a maximum of 2^53; a float64 conversion would
			// round the instance down to the bound and wrongly pass.
			schema: &jsonschema.Schema{
				Type:    "integer",
				Maximum: new(9007199254740992.0),
			},
			instance: int64(9007199254740993),
			valid:    false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tt.schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), tt.instance)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			// The one-shot helper must agree.
			oneShot := jsonschema.Validate(t.Context(), tt.schema, tt.instance)
			assert.Equal(t, err == nil, oneShot == nil)
		})
	}
}

// TestValidateStructStillRejected pins that normalization does not widen the
// accepted instance set beyond numeric kinds: a struct still returns a clear
// non-validation error rather than panicking or marshaling.
func TestValidateStructStillRejected(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{Type: "object"})
	require.NoError(t, err)

	err = v.Validate(t.Context(), struct{ X int }{X: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not accepted")

	var verr *jsonschema.ValidationError

	assert.NotErrorAs(t, err, &verr, "an unaccepted instance is not a validation failure")
}
