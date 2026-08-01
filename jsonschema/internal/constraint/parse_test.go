package constraint_test

import (
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

func TestParseNumericBound(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value   string
		kind    reflect.Kind
		wantVal float64
		wantErr bool
		// The unrepresentable field asserts the 2^53 policy fired.
		unrepresentable bool
	}{
		"int in range": {value: "100", kind: reflect.Int, wantVal: 100},
		"int at 2^53":  {value: "9007199254740992", kind: reflect.Int64, wantVal: 9007199254740992},
		"int beyond 2^53 rejected": {
			value:           "9007199254740993",
			kind:            reflect.Int64,
			wantErr:         true,
			unrepresentable: true,
		},
		"int negative beyond -2^53": {
			value:           "-9007199254740993",
			kind:            reflect.Int64,
			wantErr:         true,
			unrepresentable: true,
		},
		"uint max rejected": {
			value:           "18446744073709551615",
			kind:            reflect.Uint64,
			wantErr:         true,
			unrepresentable: true,
		},
		"int rejects fractional":    {value: "1.5", kind: reflect.Int, wantErr: true},
		"unsigned rejects negative": {value: "-1", kind: reflect.Uint, wantErr: true},

		// Float-kind fields: decision 2 unifies the 2^53 guard here too.
		"float fractional ok":        {value: "1.5", kind: reflect.Float64, wantVal: 1.5},
		"float tenth ok":             {value: "0.1", kind: reflect.Float64, wantVal: 0.1},
		"float exponent integral ok": {value: "1e2", kind: reflect.Float64, wantVal: 100},
		"float power of two above 2^53 rejected": {
			// 2^60 is binary-exact in a float64, but the schema ships the
			// float64's shortest decimal (1152921504606847000), a different
			// number, so accepting it would loosen the bound by 24.
			value:           "1152921504606846976",
			kind:            reflect.Float64,
			wantErr:         true,
			unrepresentable: true,
		},
		"invalid kind power of two above 2^53 rejected": {
			value:           "1152921504606846976",
			kind:            reflect.Invalid,
			wantErr:         true,
			unrepresentable: true,
		},
		"float shortest-decimal integer above 2^53 ok": {
			// 2^54 is its own float64's shortest decimal, so it ships verbatim.
			value: "18014398509481984", kind: reflect.Float64, wantVal: 18014398509481984,
		},
		"float shortest-decimal spelling of 2^60 float64 ok": {
			// The number float64(2^60) actually denotes is accepted: it ships
			// and enforces exactly as authored.
			value: "1152921504606847000", kind: reflect.Float64, wantVal: 1152921504606847000,
		},
		"float power of ten above 2^53 ok": {
			// 1e23 is not binary-exact, but its float64's shortest decimal is
			// 1e23 itself, so the shipped bound denotes the authored value.
			value: "1e23", kind: reflect.Float64, wantVal: 1e23,
		},
		"float integer beyond 2^53": {
			value:           "9007199254740993",
			kind:            reflect.Float64,
			wantErr:         true,
			unrepresentable: true,
		},
		"float integer beyond 2^53 exp": {
			value:           "9.007199254740993e15",
			kind:            reflect.Float64,
			wantErr:         true,
			unrepresentable: true,
		},
		"float nan rejected":            {value: "NaN", kind: reflect.Float64, wantErr: true},
		"float inf rejected":            {value: "Inf", kind: reflect.Float64, wantErr: true},
		"float hex rejected":            {value: "0x1p4", kind: reflect.Float64, wantErr: true},
		"float underscore rejected":     {value: "1_000", kind: reflect.Float64, wantErr: true},
		"invalid kind takes float path": {value: "5.5", kind: reflect.Invalid, wantVal: 5.5},
		"invalid kind 2^53 float reject": {
			value:           "9007199254740993",
			kind:            reflect.Invalid,
			wantErr:         true,
			unrepresentable: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := constraint.ParseNumericBound(tc.value, tc.kind)
			if tc.wantErr {
				require.Error(t, err)

				if tc.unrepresentable {
					require.ErrorIs(t, err, constraint.ErrNotRepresentable)
					assert.Contains(t, err.Error(), tc.value)
				}

				return
			}

			require.NoError(t, err)
			assert.InDelta(t, tc.wantVal, got.Val, 0)
			require.NotNil(t, got.Rat)
		})
	}
}

func TestParseNumericBoundRatIsShortestDecimal(t *testing.T) {
	t.Parallel()

	// 0.1 must compare as 1/10, not the binary expansion.
	got, err := constraint.ParseNumericBound("0.1", reflect.Float64)
	require.NoError(t, err)
	assert.Equal(t, "1/10", got.Rat.RatString())
}

func TestParseSizeBound(t *testing.T) {
	t.Parallel()

	type wantBound struct {
		lower bool
		value int
	}

	tests := map[string]struct {
		value string
		rule  constraint.SizeRule
		want  []wantBound
	}{
		"min inclusive floor":        {value: "5", rule: constraint.RuleMin, want: []wantBound{{true, 5}}},
		"min negative clamps to 0":   {value: "-3", rule: constraint.RuleMin, want: []wantBound{{true, 0}}},
		"gt folds to floor plus one": {value: "5", rule: constraint.RuleGt, want: []wantBound{{true, 6}}},
		"gt at MaxInt saturates": {
			value: strconv.Itoa(math.MaxInt),
			rule:  constraint.RuleGt,
			want:  []wantBound{{true, math.MaxInt}},
		},
		"max inclusive ceiling": {value: "5", rule: constraint.RuleMax, want: []wantBound{{false, 5}}},
		"max negative is unsat": {
			value: "-1",
			rule:  constraint.RuleMax,
			want:  []wantBound{{true, 1}, {false, 0}},
		},
		"lt folds to ceiling minus one": {value: "5", rule: constraint.RuleLt, want: []wantBound{{false, 4}}},
		"lt zero is unsat": {
			value: "0",
			rule:  constraint.RuleLt,
			want:  []wantBound{{true, 1}, {false, 0}},
		},
		"lt at MinInt is unsat": {
			value: strconv.Itoa(math.MinInt),
			rule:  constraint.RuleLt,
			want:  []wantBound{{true, 1}, {false, 0}},
		},
		"len pins both bounds": {
			value: "5",
			rule:  constraint.RuleLen,
			want:  []wantBound{{true, 5}, {false, 5}},
		},
		"len negative is unsat": {
			value: "-1",
			rule:  constraint.RuleLen,
			want:  []wantBound{{true, 1}, {false, 0}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := constraint.ParseSizeBound(tc.value, tc.rule, constraint.Intersect, constraint.Authored)
			require.NoError(t, err)
			require.Len(t, got, len(tc.want))

			for i, w := range tc.want {
				assert.Equal(t, w.lower, got[i].Lower, "bound %d side", i)
				assert.True(t, got[i].End.Inclusive, "size bounds are inclusive")
				assert.Equal(t, int64(w.value), got[i].End.Rat.Num().Int64(), "bound %d value", i)
			}
		})
	}
}

func TestParseSizeBoundInvalid(t *testing.T) {
	t.Parallel()

	_, err := constraint.ParseSizeBound("abc", constraint.RuleMin, constraint.Intersect, constraint.Authored)
	require.Error(t, err)
}

// TestParseNumericBoundKindMatrix guards that the integer parse path rejects a
// value the field kind cannot hold while the float path accepts fractional ones.
func TestParseNumericBoundKindMatrix(t *testing.T) {
	t.Parallel()

	_, err := constraint.ParseNumericBound("1.5", reflect.Int)
	require.Error(t, err)

	got, err := constraint.ParseNumericBound("1.5", reflect.Float64)
	require.NoError(t, err)
	assert.InDelta(t, 1.5, got.Val, 0)
}
