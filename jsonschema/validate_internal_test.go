package jsonschema

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// TestCompileRecordsRefBearing pins the flag that gates the run's cycle set:
// a graph with no reference leaves it unset, a $ref or an active $dynamicRef
// anywhere in the indexed graph sets it, and a $dynamicRef under Draft-07,
// where the keyword never evaluates, does not. The rows' compile steps are
// the record point, so a reference in a compile-fetched document counts too.
func TestCompileRecordsRefBearing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		opts   []ValidateOption
		want   bool
	}{
		"reference-free root": {
			schema: `{"type": "object", "properties": {"a": {"type": "string"}}}`,
		},
		"$ref": {
			schema: `{"$ref": "#/$defs/a", "$defs": {"a": {"type": "string"}}}`,
			want:   true,
		},
		"$dynamicRef under 2020-12": {
			schema: `{"$dynamicRef": "#a", "$defs": {"a": {"$dynamicAnchor": "a", "type": "string"}}}`,
			want:   true,
		},
		"$dynamicRef under Draft-07": {
			schema: `{"$dynamicRef": "#a", "definitions": {"a": {"type": "string"}}}`,
			opts:   []ValidateOption{WithDraft(Draft7)},
		},
		"nested Draft-07 $ref": {
			schema: `{"properties": {"a": {"$ref": "#/definitions/a"}}, "definitions": {"a": {"type": "string"}}}`,
			opts:   []ValidateOption{WithDraft(Draft7)},
			want:   true,
		},
		"reference only to a remote": {
			schema: `{"$ref": "http://x/a"}`,
			opts: []ValidateOption{WithRefResolver(SchemaMap{
				"http://x/a": {Type: "string"},
			})},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := CompileJSON(t.Context(), []byte(tc.schema), tc.opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, v.proto.refBearing)

			run := v.proto.forInstance(t.Context())
			require.Equal(t, tc.want, run.visiting != nil,
				"the run allocates the cycle set exactly on a ref-bearing graph")
		})
	}
}

// FuzzNumericIntegerPathVsRat holds the machine-integer path of the numeric
// keywords to exact arithmetic. For an int64 instance against bounds that
// all hold an int64 form, the keywords validateNumericInt64 reports, in
// order and wording, are the ones a big.Rat comparison of the instance with
// each bound's shortest-decimal rational reports, and the public Validate
// entry, which picks between the integer and rational paths, reports the
// same set. The bound set holds an int64 form exactly when every set
// bound's shortest-decimal rational is an integer inside int64 range and
// the divisor, when set, is positive.
func FuzzNumericIntegerPathVsRat(f *testing.F) {
	f.Add(int64(6), 3.0, -9.0, 9.0, -9.0, 9.0, uint8(0x1f))
	f.Add(int64(-10), 3.0, -9.0, -11.0, -10.0, -11.0, uint8(0x1f))
	f.Add(int64(0), 0.5, 0.0, 0.0, math.Copysign(0, -1), 1e19, uint8(0x1f))
	f.Add(int64(math.MaxInt64), 7.0, 0.0, 0.0, 0.0, 0.0, uint8(0x01))
	f.Add(int64(math.MinInt64), 1.0, -9.223372036854775808e18, 0.0, 0.0, 0.0, uint8(0x03))
	f.Add(int64(999999999999999999), 17.0, 0.0, 1e18, 0.0, 0.0, uint8(0x05))
	f.Add(int64(1), 0.0, math.NaN(), math.Inf(1), 1.5, 0.0, uint8(0x1f))

	f.Fuzz(func(
		t *testing.T, n int64, multipleOf, minimum, maximum, exclusiveMinimum, exclusiveMaximum float64, mask uint8,
	) {
		schema := &Schema{}
		if mask&0x01 != 0 {
			schema.MultipleOf = &multipleOf
		}

		if mask&0x02 != 0 {
			schema.Minimum = &minimum
		}

		if mask&0x04 != 0 {
			schema.Maximum = &maximum
		}

		if mask&0x08 != 0 {
			schema.ExclusiveMinimum = &exclusiveMinimum
		}

		if mask&0x10 != 0 {
			schema.ExclusiveMaximum = &exclusiveMaximum
		}

		bounds := computeBounds(schema)
		if bounds == nil {
			require.Zero(t, mask&0x1f, "a schema with a bound set caches no bounds")

			return
		}

		wantInts := true

		for _, b := range []*float64{schema.Minimum, schema.Maximum, schema.ExclusiveMinimum, schema.ExclusiveMaximum} {
			if b != nil && !decimalIntegerInInt64(*b) {
				wantInts = false
			}
		}

		if schema.MultipleOf != nil && (!decimalIntegerInInt64(*schema.MultipleOf) || *schema.MultipleOf <= 0) {
			wantInts = false
		}

		require.Equal(t, wantInts, bounds.ints != nil, "the int64 forms are held exactly when every bound has one")

		if bounds.ints == nil {
			return
		}

		want := ratVerdicts(schema, n)

		var got []string

		for _, e := range validateNumericInt64(schema, bounds.ints, n, instanceLocation{}, schemaLocation{}) {
			got = append(got, e.Keyword+": "+e.Message)
		}

		require.Equal(t, want, got, "the integer path disagrees with exact arithmetic on %d", n)

		var public []string

		err := Validate(t.Context(), schema, jsonv1.Number(strconv.FormatInt(n, 10)))
		if err != nil {
			var verr *ValidationError

			require.ErrorAs(t, err, &verr)

			if len(verr.Causes) == 0 {
				public = append(public, verr.Keyword+": "+verr.Message)
			}

			for _, cause := range verr.Causes {
				public = append(public, cause.Keyword+": "+cause.Message)
			}
		}

		require.Equal(t, want, public, "Validate disagrees with exact arithmetic on %d", n)
	})
}

// decimalIntegerInInt64 reports whether the shortest decimal of f, the value
// a bound compares as, is an integer inside int64 range, the oracle for the
// int64 bound forms. It reads the decimal text rather than the float, so a
// float past 2^53 whose shortest decimal is not its binary value is judged
// on the decimal.
func decimalIntegerInInt64(f float64) bool {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}

	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'f', -1, 64))

	return ok && r.IsInt() && r.Num().IsInt64()
}

// ratVerdicts returns the numeric keyword errors exact arithmetic reports
// for the integer n against schema's bounds, in keyword order and in the
// wording of the rational path, as "keyword: message" lines.
func ratVerdicts(schema *Schema, n int64) []string {
	val := new(big.Rat).SetInt64(n)
	text := numrat.RatString(val)

	var want []string

	if schema.MultipleOf != nil {
		if !new(big.Rat).Quo(val, numrat.Float64ToRat(*schema.MultipleOf)).IsInt() {
			want = append(want, fmt.Sprintf("multipleOf: %s is not a multiple of %v", text, *schema.MultipleOf))
		}
	}

	if schema.Minimum != nil && val.Cmp(numrat.Float64ToRat(*schema.Minimum)) < 0 {
		want = append(want, fmt.Sprintf("minimum: %s is less than %v", text, *schema.Minimum))
	}

	if schema.Maximum != nil && val.Cmp(numrat.Float64ToRat(*schema.Maximum)) > 0 {
		want = append(want, fmt.Sprintf("maximum: %s is greater than %v", text, *schema.Maximum))
	}

	if schema.ExclusiveMinimum != nil && val.Cmp(numrat.Float64ToRat(*schema.ExclusiveMinimum)) <= 0 {
		want = append(want,
			fmt.Sprintf("exclusiveMinimum: %s is less than or equal to %v", text, *schema.ExclusiveMinimum))
	}

	if schema.ExclusiveMaximum != nil && val.Cmp(numrat.Float64ToRat(*schema.ExclusiveMaximum)) >= 0 {
		want = append(want,
			fmt.Sprintf("exclusiveMaximum: %s is greater than or equal to %v", text, *schema.ExclusiveMaximum))
	}

	return want
}
