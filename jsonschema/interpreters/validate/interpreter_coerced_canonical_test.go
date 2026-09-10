package validate_test

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_StringCoercedCanonicalSpelling pins that scalar
// values on a json:",string" field are parsed against the real Go kind and
// re-serialized, so a float spelling go-playground also reads ("5.0", "1e2")
// yields the canonical text encoding/json emits for the value. Stamping the
// raw tag text would contradict the tag: eq=5.0 would reject the value 5.0
// (serialized "5") and ne=5.0 would forbid a string that never occurs.
func TestValidateInterpreter_StringCoercedCanonicalSpelling(t *testing.T) {
	t.Parallel()

	type Form struct {
		NeFloat  float64 `json:"ne_float,string"  validate:"ne=5.0"`
		EqFloat  float64 `json:"eq_float,string"  validate:"eq=5.0"`
		EqExp    float64 `json:"eq_exp,string"    validate:"eq=1e2"`
		LenFloat float64 `json:"len_float,string" validate:"len=7.0"`
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
			"len_float":{"type":"string","const":"7"}
		},
		"required":["ne_float","eq_float","eq_exp","len_float"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(),
		[]byte(`{"ne_float":"6","eq_float":"5","eq_exp":"100","len_float":"7"}`)),
		"the canonical serialized forms satisfy the constraints")
	require.Error(t, v.ValidateJSON(t.Context(),
		[]byte(`{"ne_float":"5","eq_float":"5","eq_exp":"100","len_float":"7"}`)),
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

// TestValidateInterpreter_StringCoercedOneOfCanonicalSpelling pins that the
// oneof spelling check reads the Go kind rather than the JSON form. Go-playground's
// isOneOf formats an integer with strconv and compares that text against the
// raw tokens whatever the json tag says, so a token spelled -0 matches no
// value there; canonicalizing it to "0" on a json:",string" or
// text-marshaling integer would enumerate a value the schema accepts and
// go-playground rejects. A quoted json.Number takes the JSON grammar check a
// bare one takes, and a canonical token still enumerates the serialized
// text.
func TestValidateInterpreter_StringCoercedOneOfCanonicalSpelling(t *testing.T) {
	t.Parallel()

	const (
		canonical = "go-playground compares against"
		grammar   = "is not a JSON number"
	)

	refused := map[string]struct {
		gen  func(context.Context) (*jsonschema.Schema, error)
		want string
	}{
		"negative zero on a coerced int": {
			gen: func(ctx context.Context) (*jsonschema.Schema, error) {
				type F struct {
					V int `json:"v,string" validate:"oneof=-0 1"`
				}

				return jsonschema.GenerateFor[F](ctx, validateInterp())
			},
			want: canonical,
		},
		"leading plus on a coerced int": {
			gen: func(ctx context.Context) (*jsonschema.Schema, error) {
				type F struct {
					V int `json:"v,string" validate:"oneof=+1"`
				}

				return jsonschema.GenerateFor[F](ctx, validateInterp())
			},
			want: canonical,
		},
		"negative zero on a text-marshaling int": {
			gen: func(ctx context.Context) (*jsonschema.Schema, error) {
				type F struct {
					V levelText `json:"v" validate:"oneof=-0"`
				}

				return jsonschema.GenerateFor[F](ctx, validateInterp())
			},
			want: canonical,
		},
		"non-JSON token on a quoted json.Number": {
			gen: func(ctx context.Context) (*jsonschema.Schema, error) {
				type F struct {
					V jsonv1.Number `json:"v,string" validate:"oneof=+1"`
				}

				return jsonschema.GenerateFor[F](ctx, validateInterp())
			},
			want: grammar,
		},
	}

	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.gen(t.Context())
			require.ErrorContains(t, err, "oneof:")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("canonical tokens enumerate the serialized text", func(t *testing.T) {
		t.Parallel()

		type F struct {
			V int `json:"v,string" validate:"oneof=0 1"`
		}

		s, err := jsonschema.GenerateFor[F](t.Context(), validateInterp())
		require.NoError(t, err)
		assert.Equal(t, []any{"0", "1"}, s.Properties["v"].Enum)
	})
}
