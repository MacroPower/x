package jsonvalue_test

import (
	"math"
	"math/big"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
)

// TestValueSize pins the by-value contract: the validator copies a Value into
// every per-node context, so the struct must stay within one cache line.
func TestValueSize(t *testing.T) {
	t.Parallel()

	assert.LessOrEqual(t, unsafe.Sizeof(jsonvalue.Value{}), uintptr(64))
}

func TestTypeName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   jsonvalue.Value
		want string
	}{
		"null":            {in: jsonvalue.NewNull(), want: "null"},
		"boolean":         {in: jsonvalue.NewBool(true), want: "boolean"},
		"string":          {in: jsonvalue.NewString("x"), want: "string"},
		"integer literal": {in: jsonvalue.NewNumber("5"), want: "integer"},
		"number literal":  {in: jsonvalue.NewNumber("5.5"), want: "number"},
		"point-zero":      {in: jsonvalue.NewNumber("7.0"), want: "integer"},
		"exponent form":   {in: jsonvalue.NewNumber("1e3"), want: "integer"},
		"beyond int64":    {in: jsonvalue.NewNumber("1e30"), want: "integer"},
		"unparseable":     {in: jsonvalue.NewNumber("abc"), want: "number"},
		"empty literal":   {in: jsonvalue.NewNumber(""), want: "number"},
		"integer float":   {in: jsonvalue.NewFloat(5.0), want: "integer"},
		"large float":     {in: jsonvalue.NewFloat(1e30), want: "integer"},
		"number float":    {in: jsonvalue.NewFloat(5.5), want: "number"},
		"nan":             {in: jsonvalue.NewFloat(math.NaN()), want: "number"},
		"infinity":        {in: jsonvalue.NewFloat(math.Inf(1)), want: "number"},
		"object":          {in: jsonvalue.NewObject(map[string]jsonvalue.Value{}), want: "object"},
		"array":           {in: jsonvalue.NewArray([]jsonvalue.Value{}), want: "array"},
		"invalid":         {in: jsonvalue.Value{}, want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.in.TypeName())
		})
	}
}

func TestMatchesType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   jsonvalue.Value
		typ  string
		want bool
	}{
		"null match":            {in: jsonvalue.NewNull(), typ: "null", want: true},
		"boolean match":         {in: jsonvalue.NewBool(true), typ: "boolean", want: true},
		"string match":          {in: jsonvalue.NewString("x"), typ: "string", want: true},
		"integer match":         {in: jsonvalue.NewNumber("5"), typ: "integer", want: true},
		"number match":          {in: jsonvalue.NewFloat(5.5), typ: "number", want: true},
		"number via literal":    {in: jsonvalue.NewNumber("5"), typ: "number", want: true},
		"object match":          {in: jsonvalue.NewObject(nil), typ: "object", want: true},
		"array match":           {in: jsonvalue.NewArray(nil), typ: "array", want: true},
		"string not integer":    {in: jsonvalue.NewString("5"), typ: "integer", want: false},
		"float not integer":     {in: jsonvalue.NewFloat(5.5), typ: "integer", want: false},
		"nan is a number":       {in: jsonvalue.NewFloat(math.NaN()), typ: "number", want: true},
		"nan is not an integer": {in: jsonvalue.NewFloat(math.NaN()), typ: "integer", want: false},
		"unknown type":          {in: jsonvalue.NewString("x"), typ: "bogus", want: false},
		"invalid matches none":  {in: jsonvalue.Value{}, typ: "null", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.in.MatchesType(tc.typ))
		})
	}
}

// TestNumberStates pins the three number states: an in-cap literal yields a
// rational, an over-cap one keeps its decomposition and no rational, and a
// non-finite float or unparseable literal carries no value at all.
func TestNumberStates(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in         jsonvalue.Value
		comparable bool
		exact      bool
		rat        *big.Rat
	}{
		"in-cap literal":   {in: jsonvalue.NewNumber("1.5"), comparable: true, exact: true, rat: big.NewRat(3, 2)},
		"float":            {in: jsonvalue.NewFloat(0.1), comparable: true, exact: true, rat: big.NewRat(1, 10)},
		"negative zero":    {in: jsonvalue.NewNumber("-0"), comparable: true, exact: true, rat: new(big.Rat)},
		"over-cap literal": {in: jsonvalue.NewNumber("1e1000000000"), comparable: true, exact: false},
		"unparseable":      {in: jsonvalue.NewNumber("abc"), comparable: false, exact: false},
		"nan":              {in: jsonvalue.NewFloat(math.NaN()), comparable: false, exact: false},
		"not a number":     {in: jsonvalue.NewString("1"), comparable: false, exact: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.comparable, tc.in.Comparable())
			assert.Equal(t, tc.exact, tc.in.Exact())

			r, ok := tc.in.Rat()
			require.Equal(t, tc.rat != nil, ok)

			if ok {
				assert.Zero(t, tc.rat.Cmp(r))
			}
		})
	}
}

// TestNewFloatText pins the literal a Go float carries, which the numeric
// bound messages print for a non-finite value.
func TestNewFloatText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "NaN", jsonvalue.NewFloat(math.NaN()).Literal())
	assert.Equal(t, "+Inf", jsonvalue.NewFloat(math.Inf(1)).Literal())
	assert.Equal(t, "-Inf", jsonvalue.NewFloat(math.Inf(-1)).Literal())
	assert.True(t, jsonvalue.NewFloat(1).FromFloat())
	assert.False(t, jsonvalue.NewNumber("1").FromFloat())
}

func TestInterface(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   jsonvalue.Value
		want any
	}{
		"null":     {in: jsonvalue.NewNull(), want: nil},
		"bool":     {in: jsonvalue.NewBool(true), want: true},
		"string":   {in: jsonvalue.NewString("x"), want: "x"},
		"literal":  {in: jsonvalue.NewNumber("1e400"), want: jsonv1.Number("1e400")},
		"float":    {in: jsonvalue.NewFloat(1.5), want: 1.5},
		"neg zero": {in: jsonvalue.NewFloat(math.Copysign(0, -1)), want: math.Copysign(0, -1)},
		"infinity": {in: jsonvalue.NewFloat(math.Inf(-1)), want: math.Inf(-1)},
		"array": {
			in:   jsonvalue.NewArray([]jsonvalue.Value{jsonvalue.NewNumber("1")}),
			want: []any{jsonv1.Number("1")},
		},
		"nil array": {in: jsonvalue.NewArray(nil), want: []any{}},
		"object": {
			in:   jsonvalue.NewObject(map[string]jsonvalue.Value{"k": jsonvalue.NewNull()}),
			want: map[string]any{"k": nil},
		},
		"invalid": {in: jsonvalue.Value{}, want: nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.in.Interface()
			if f, ok := tc.want.(float64); ok {
				gotFloat, isFloat := got.(float64)
				require.True(t, isFloat)
				assert.Equal(t, math.Float64bits(f), math.Float64bits(gotFloat))

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}

	nan := jsonvalue.NewFloat(math.NaN()).Interface()
	f, ok := nan.(float64)
	require.True(t, ok)
	assert.True(t, math.IsNaN(f))
}
