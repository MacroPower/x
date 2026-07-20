package validate_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_StringCoercedCanonicalSpelling pins that scalar
// values on a json:",string" field are parsed against the real Go kind and
// re-serialized, so any legal go-playground spelling ("5.0", "+5", "1e2")
// yields the canonical text encoding/json emits for the value. Stamping the
// raw tag text would contradict the tag: eq=5.0 would reject the value 5.0
// (serialized "5") and ne=5.0 would forbid a string that never occurs.
func TestValidateInterpreter_StringCoercedCanonicalSpelling(t *testing.T) {
	t.Parallel()

	type Form struct {
		NeFloat  float64 `json:"ne_float,string"   validate:"ne=5.0"`
		EqFloat  float64 `json:"eq_float,string"   validate:"eq=5.0"`
		EqExp    float64 `json:"eq_exp,string"     validate:"eq=1e2"`
		EqPlus   int     `json:"eq_plus,string"    validate:"eq=+5"`
		OneOfPad int     `json:"one_of_pad,string" validate:"oneof=01 2"`
		LenFloat float64 `json:"len_float,string"  validate:"len=7.0"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"ne_float":{"type":"string","not":{"const":"5"}},
			"eq_float":{"type":"string","const":"5"},
			"eq_exp":{"type":"string","const":"100"},
			"eq_plus":{"type":"string","const":"5"},
			"one_of_pad":{"type":"string","enum":["1","2"]},
			"len_float":{"type":"string","const":"7"}
		},
		"required":["ne_float","eq_float","eq_exp","eq_plus","one_of_pad","len_float"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(),
		[]byte(`{"ne_float":"6","eq_float":"5","eq_exp":"100","eq_plus":"5","one_of_pad":"2","len_float":"7"}`)),
		"the canonical serialized forms satisfy the constraints")
	require.Error(t, v.ValidateJSON(t.Context(),
		[]byte(`{"ne_float":"5","eq_float":"5","eq_exp":"100","eq_plus":"5","one_of_pad":"2","len_float":"7"}`)),
		"ne=5.0 must reject the value 5.0, which serializes as \"5\"")
}

// TestValidateInterpreter_StringCoercedRangeChecked pins that scalar values on
// a json:",string" field keep the documented range check against the field's
// Go type instead of silently pinning an unsatisfiable const.
func TestValidateInterpreter_StringCoercedRangeChecked(t *testing.T) {
	t.Parallel()

	cases := map[string]func(context.Context) (*jsonschema.Schema, error){
		"eq out of range": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				V int8 `json:"v,string" validate:"eq=200"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
		"ne out of range": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				V int8 `json:"v,string" validate:"ne=200"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
		"oneof out of range": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				V int8 `json:"v,string" validate:"oneof=1 200"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
		"len out of range": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				V int8 `json:"v,string" validate:"len=200"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
	}

	for name, gen := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := gen(t.Context())
			require.Error(t, err, "a value the Go type cannot hold must be rejected")
			assert.Contains(t, err.Error(), "out of range")
			assert.Nil(t, s)
		})
	}
}
