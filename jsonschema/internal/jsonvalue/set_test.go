package jsonvalue_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
)

// TestSetContains checks membership on both sides of the index limit, so the
// linear scan and the hash index answer alike: every spelling of a member
// number is found, a value with no numeric value matches only its own text,
// and a non-member is refused whether or not its hash collides.
func TestSetContains(t *testing.T) {
	t.Parallel()

	base := []jsonvalue.Value{
		jsonvalue.NewNumber("1"),
		jsonvalue.NewString("a"),
		jsonvalue.NewArray([]jsonvalue.Value{jsonvalue.NewNumber("2.50"), jsonvalue.NewNull()}),
		jsonvalue.NewObject(map[string]jsonvalue.Value{"k": jsonvalue.NewBool(true)}),
		jsonvalue.NewNumber("1e400"),
		jsonvalue.NewNumber("-0"),
	}

	tests := map[string]struct {
		probe jsonvalue.Value
		want  bool
	}{
		"integer spelled with a fraction":  {probe: jsonvalue.NewNumber("1.0"), want: true},
		"integer spelled with an exponent": {probe: jsonvalue.NewNumber("10e-1"), want: true},
		"go float":                         {probe: jsonvalue.NewFloat(1), want: true},
		"string":                           {probe: jsonvalue.NewString("a"), want: true},
		"array with a respelled number": {
			probe: jsonvalue.NewArray([]jsonvalue.Value{jsonvalue.NewNumber("2.5"), jsonvalue.NewNull()}),
			want:  true,
		},
		"object": {
			probe: jsonvalue.NewObject(map[string]jsonvalue.Value{"k": jsonvalue.NewBool(true)}),
			want:  true,
		},
		"over-cap literal":           {probe: jsonvalue.NewNumber("1E400"), want: true},
		"zero against negative zero": {probe: jsonvalue.NewNumber("0"), want: true},
		"absent string":              {probe: jsonvalue.NewString("b"), want: false},
		"absent number":              {probe: jsonvalue.NewNumber("2"), want: false},
		"array of another length": {
			probe: jsonvalue.NewArray([]jsonvalue.Value{jsonvalue.NewNumber("2.5")}),
			want:  false,
		},
		"non-finite float": {probe: jsonvalue.NewFloat(math.NaN()), want: false},
	}

	// The padded set crosses the index limit with members that share no
	// value with the probes.
	padded := append([]jsonvalue.Value(nil), base...)
	for i := range 16 {
		padded = append(padded, jsonvalue.NewString("pad"+strconv.Itoa(i)))
	}

	sets := map[string]*jsonvalue.Set{
		"linear":  jsonvalue.NewSet(base),
		"indexed": jsonvalue.NewSet(padded),
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for mode, set := range sets {
				assert.Equal(t, tt.want, set.Contains(tt.probe), mode)
			}
		})
	}
}

// TestSetIndexFindsEveryMember checks that a set past the index limit finds
// each of its own members, including duplicates and members whose hashes
// chain, and reports its members in the given order.
func TestSetIndexFindsEveryMember(t *testing.T) {
	t.Parallel()

	var members []jsonvalue.Value

	for i := range 64 {
		members = append(members, jsonvalue.NewNumber(strconv.Itoa(i%40)))
	}

	set := jsonvalue.NewSet(members)
	require.Len(t, set.Members(), 64)

	for i, m := range members {
		assert.True(t, set.Contains(m), "member %d", i)
	}

	assert.False(t, set.Contains(jsonvalue.NewNumber("40")))
}
