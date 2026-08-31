package validate_test

import (
	"encoding/json/v2"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// levelText is a numeric type that marshals itself to a string ("L<n>"), so
// its generated schema has type string while its Go kind stays numeric.
type levelText int

// MarshalText implements encoding.TextMarshaler.
func (l levelText) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "L%d", int(l)), nil
}

// tinyText is a string-marshaling type over a narrow integer, so scalar oneof
// values must still range-check against the underlying kind.
type tinyText int8

// MarshalText implements encoding.TextMarshaler.
func (t tinyText) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "T%d", int(t)), nil
}

// TestValidateInterpreter_SequenceOneOfStringCoercedElement pins that oneof on
// a sequence whose element type is string-coerced (a string-marshaling numeric
// type) routes through the coerced dispatch, exactly as the dive path does:
// each value parses at the element's real kind and the enum members are the
// serialized forms the field actually emits. Dispatching on the raw element
// kind would stamp a numeric enum onto the string element schema, which no
// instance could ever satisfy.
func TestValidateInterpreter_SequenceOneOfStringCoercedElement(t *testing.T) {
	t.Parallel()

	type Form struct {
		Seq  []levelText `json:"seq"  validate:"oneof=1 2 3"`
		Dive []levelText `json:"dive" validate:"dive,oneof=1 2 3"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"seq":{"type":"array","items":{"type":"string","enum":["L1","L2","L3"]}},
			"dive":{"type":"array","items":{"type":"string","enum":["L1","L2","L3"]}}
		},
		"required":["seq","dive"],
		"additionalProperties":false
	}`, string(got))

	// The instance Go actually marshals for []levelText{1} must validate.
	instance, err := json.Marshal(Form{Seq: []levelText{1}, Dive: []levelText{2}})
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(), instance),
		"the serialized element forms satisfy the enum")
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"seq":["L4"],"dive":["L1"]}`)),
		"a serialized value outside the oneof set must be rejected")
}

// TestValidateInterpreter_SequenceOneOfStringCoercedRangeChecked pins that the
// coerced sequence path keeps the documented range check: a oneof value the
// element's Go type cannot hold is a generation error, not an unreachable enum
// member.
func TestValidateInterpreter_SequenceOneOfStringCoercedRangeChecked(t *testing.T) {
	t.Parallel()

	type Form struct {
		Seq []tinyText `json:"seq" validate:"oneof=1 200"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
	require.Error(t, err, "a value the element type cannot hold must be rejected")
	assert.Contains(t, err.Error(), "out of range")
	assert.Nil(t, s)
}
