package jsonschema_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// Tests for jsonschema struct-tag parsing: key-value vs bare-description
// detection, value typing, and error paths.

// extenderWithDefs implements JSONSchemaExtender and sets $defs on the schema.
// Used to test that extender-set fields survive extractToDefs wrapping.
type extenderWithDefs struct {
	Value string `json:"value"`
}

func (extenderWithDefs) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	if ts.Value.Defs == nil {
		ts.Value.Defs = map[string]*jsonschema.Schema{}
	}

	ts.Value.Defs["customDef"] = &jsonschema.Schema{Type: "string"}

	return nil
}

// parentSnapshot records what a tag interpreter saw in Parent.Properties.
type parentSnapshot struct {
	fieldName string
	propCount int
}

// parentInspector is a TagInterpreter that records the Parent.Properties
// count at the time each field is processed.
type parentInspector struct {
	snapshots []parentSnapshot
}

func (p *parentInspector) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	count := 0
	if field.Parent != nil {
		count = len(field.Parent.Properties)
	}

	p.snapshots = append(p.snapshots, parentSnapshot{
		fieldName: field.Name,
		propCount: count,
	})

	return nil
}

func TestSplitTagPairsCommasInValues(t *testing.T) {
	t.Parallel()

	// A comma separates tag segments: description=Hello World,minimum=1 yields
	// the description "Hello World" and a separate minimum. The field is numeric
	// so the second segment is a keyword the shape can actually carry; a
	// minimum on a string would be rejected rather than parsed and ignored.
	type MyType struct {
		Count int `json:"count" jsonschema:"description=Hello World,minimum=1"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context())
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)

	assert.Equal(t, "Hello World", prop.Description)
	require.NotNil(t, prop.Minimum)
	assert.InDelta(t, 1.0, *prop.Minimum, 0)
}

func TestBareDescriptionWithEqualsSign(t *testing.T) {
	t.Parallel()

	// A bare description whose first token looks like word= but carries a spaced
	// value (e.g. "a=b is the formula") is treated as a description, not as a
	// key-value pair, so it does not produce an unrecognized-key error.
	type MyType struct {
		Name string `json:"name" jsonschema:"a=b is the formula"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context())
	require.NoError(t, err,
		"bare description starting with word= should not produce an error")

	prop := s.Properties["name"]
	require.NotNil(t, prop)
	assert.Equal(t, "a=b is the formula", prop.Description,
		"bare description starting with word= should be treated as description")
}

func TestLeadingCommaTagRejected(t *testing.T) {
	t.Parallel()

	// A jsonschema tag with a leading comma (e.g. ",minimum=1") is a malformed
	// key-value tag, not a bare description: the empty first segment must surface
	// as a parse error rather than swallowing the real constraint into the
	// description. Every other malformed-comma position (trailing, doubled) already
	// errors, so the leading position must too.
	cases := map[string]func(context.Context) (*jsonschema.Schema, error){
		"leading comma before keyword": func(ctx context.Context) (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:",minimum=1"`
			}

			return jsonschema.GenerateFor[T](ctx)
		},
		"leading comma before type": func(ctx context.Context) (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:",type=integer"`
			}

			return jsonschema.GenerateFor[T](ctx)
		},
		"doubled leading comma": func(ctx context.Context) (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:",,minimum=1"`
			}

			return jsonschema.GenerateFor[T](ctx)
		},
	}

	for name, gen := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := gen(t.Context())
			require.Error(t, err,
				"a leading comma must be rejected, not swallowed into the description")
			assert.Contains(t, err.Error(), "missing '='")
		})
	}
}

func TestProseDescriptionResolvesEscapes(t *testing.T) {
	t.Parallel()

	// A bare description and a WORD=-gated prose description both resolve the
	// comma/backslash escapes the key=value form does, rather than storing the
	// raw tag with a stray backslash.
	type Bare struct {
		Name string `json:"name" jsonschema:"Hello\\, World"`
	}

	s, err := jsonschema.GenerateFor[Bare](t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Hello, World", s.Properties["name"].Description)

	type Prose struct {
		Name string `json:"name" jsonschema:"a=b is the formula\\, more text"`
	}

	s, err = jsonschema.GenerateFor[Prose](t.Context())
	require.NoError(t, err)
	assert.Equal(t, "a=b is the formula, more text", s.Properties["name"].Description)
}

func TestParseIntRejectsNegativeValues(t *testing.T) {
	t.Parallel()

	// MinLength, maxLength, minItems, maxItems, minProperties, and maxProperties
	// must be non-negative integers per JSON Schema; negatives are rejected.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"negative minLength": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"minLength=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"negative maxLength": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"maxLength=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"negative minItems": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"minItems=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"negative maxItems": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"maxItems=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"negative minProperties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]string `json:"v" jsonschema:"minProperties=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"negative maxProperties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]string `json:"v" jsonschema:"maxProperties=-1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"negative value should be rejected for non-negative-only schema keyword")
		})
	}
}

func TestParseFloatRejectsNaNInf(t *testing.T) {
	t.Parallel()

	// NaN and Inf are not finite numbers and are rejected as numeric keyword
	// values.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"minimum=NaN": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"minimum=NaN"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"maximum=+Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"maximum=+Inf"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"minimum=-Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"minimum=-Inf"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"NaN/Inf should be rejected as schema keyword values")
		})
	}
}

func TestParseFloatRejectsImpreciseIntegerBound(t *testing.T) {
	t.Parallel()

	// An integer bound the float64 cannot represent exactly would silently round
	// and loosen the constraint, so it is rejected rather than shipped.
	type TooBig struct {
		V int64 `json:"v" jsonschema:"maximum=9223372036854775807"`
	}

	_, err := jsonschema.GenerateFor[TooBig](t.Context())
	require.ErrorContains(t, err, "exceeds exact float64 precision")

	// An integer above 2^53 whose float64 is binary-exact but whose shortest
	// decimal differs is still rejected: 2^60 would ship (and enforce) as the
	// float64's shortest decimal 1152921504606847000, loosening the bound by 24.
	type BinaryExact struct {
		V int64 `json:"v" jsonschema:"maximum=1152921504606846976"` // 2^60
	}

	_, err = jsonschema.GenerateFor[BinaryExact](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrBoundNotRepresentable)
	require.ErrorContains(t, err, "exceeds exact float64 precision")

	// An integer above 2^53 that IS the float64's own shortest decimal is
	// accepted unchanged: it ships and enforces as exactly the authored value.
	type Representable struct {
		V int64 `json:"v" jsonschema:"maximum=18014398509481984"` // 2^54
	}

	s, err := jsonschema.GenerateFor[Representable](t.Context())
	require.NoError(t, err)
	require.NotNil(t, s.Properties["v"].Maximum)
	assert.InDelta(t, 18014398509481984.0, *s.Properties["v"].Maximum, 0)

	// The same out-of-precision integer written in exponent form must also be
	// rejected: the check keys on the exact value, not the spelling.
	type ExpForm struct {
		V int64 `json:"v" jsonschema:"maximum=9.007199254740993e15"` // 2^53 + 1
	}

	_, err = jsonschema.GenerateFor[ExpForm](t.Context())
	require.ErrorContains(t, err, "exceeds exact float64 precision")

	// An exponent-form integer that IS representable is accepted.
	type ExpRepresentable struct {
		V int64 `json:"v" jsonschema:"maximum=1e15"`
	}

	s, err = jsonschema.GenerateFor[ExpRepresentable](t.Context())
	require.NoError(t, err)
	require.NotNil(t, s.Properties["v"].Maximum)
	assert.InDelta(t, 1e15, *s.Properties["v"].Maximum, 0)
}

func TestParseTypedScalarRejectsNaNInf(t *testing.T) {
	t.Parallel()

	// The const, enum, and examples keywords flow through parseTypedScalar.
	// NaN/Inf parse without error but cannot be marshaled into a schema and a NaN
	// const matches nothing, so they are rejected like the bound keywords.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"const=NaN": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"const=NaN"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"const=+Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"const=+Inf"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"enum=NaN": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"enum=NaN"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"examples=-Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"examples=-Inf"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"NaN/Inf should be rejected as const/enum/examples values")
		})
	}
}

// TestParseFloatRejectsNonDecimalForms verifies the float keys reject the
// underscore digit separator and hexadecimal float forms that
// strconv.ParseFloat accepts, matching the base-10 integer keys. It covers both
// float-parsing paths: parseFloat for the bound keys and parseTypedScalar for
// const.
func TestParseFloatRejectsNonDecimalForms(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"minimum underscore": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"minimum=1_000.5"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"maximum hex float": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"maximum=0x1p-2"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"multipleOf underscore": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"multipleOf=1_0"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"const underscore": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"const=1_000.5"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"const hex float": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"const=0x1p4"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"underscore and hex float forms should be rejected like base-10 integers")
		})
	}
}

func TestMultipleOfZero(t *testing.T) {
	t.Parallel()

	type MyType struct {
		Value float64 `json:"value" jsonschema:"multipleOf=0"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context())
	// The multipleOf value MUST be strictly > 0 per JSON Schema spec.
	require.Error(t, err,
		"multipleOf=0 should be rejected")
}

func TestMultipleOfNegative(t *testing.T) {
	t.Parallel()

	type MyType struct {
		Value float64 `json:"value" jsonschema:"multipleOf=-1"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context())
	// The multipleOf value MUST be strictly > 0 per JSON Schema spec.
	require.Error(t, err,
		"negative multipleOf should be rejected")
}

func TestParseTypedScalarPrecisionLoss(t *testing.T) {
	t.Parallel()

	// 2^53 + 1 loses precision when cast to float64.
	type MyType struct {
		Value int64 `json:"value" jsonschema:"const=9007199254740993"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context())
	require.NoError(t, err)

	prop := s.Properties["value"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.Const)

	// The const value should be exactly 9007199254740993, not 9007199254740992.
	b, err := json.Marshal(prop.Const)
	require.NoError(t, err)
	assert.Equal(t, "9007199254740993", string(b),
		"large int64 const should not lose precision in float64 cast")
}

func TestEmptyStringConstAndDefault(t *testing.T) {
	t.Parallel()

	// An empty const/default value expresses the valid JSON Schema empty string
	// on a string field; it stays rejected on a non-string field where "" is
	// meaningless.
	type StringField struct {
		C string `json:"c" jsonschema:"const="`
		D string `json:"d" jsonschema:"default="`
	}

	s, err := jsonschema.GenerateFor[StringField](t.Context())
	require.NoError(t, err)

	require.NotNil(t, s.Properties["c"].Const)
	assert.Empty(t, *s.Properties["c"].Const)
	assert.JSONEq(t, `""`, string(s.Properties["d"].Default))

	type IntField struct {
		C int `json:"c" jsonschema:"const="`
	}

	_, err = jsonschema.GenerateFor[IntField](t.Context())
	require.Error(t, err, "an empty const on a non-string field stays rejected")
}

func TestParseTypedScalarRejectsUnknownKinds(t *testing.T) {
	t.Parallel()

	type Inner struct {
		X string `json:"x"`
	}

	type MyType struct {
		Data Inner `json:"data" jsonschema:"default=foo"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context())
	// A scalar tag value on a non-primitive (struct) field is rejected rather
	// than coerced to a string.
	require.Error(t, err,
		"default= on a struct type is rejected, not coerced to a string")
}

func TestValidationErrorErrorCycleProtection(t *testing.T) {
	t.Parallel()

	// Construct a cyclic error tree.
	a := &jsonschema.ValidationError{Message: "a"}
	b := &jsonschema.ValidationError{Message: "b"}
	a.Causes = []*jsonschema.ValidationError{b}
	b.Causes = []*jsonschema.ValidationError{a}

	// This should not stack overflow.
	assert.NotPanics(t, func() {
		_ = a.Error()
	}, "cyclic ValidationError tree should not cause stack overflow")
}

func TestDraftOrdering(t *testing.T) {
	t.Parallel()

	assert.Less(t, int(jsonschema.Draft7), int(jsonschema.Draft2020),
		"Draft7 < Draft2020 for comparison operators to work")

	// The values are spaced so a future draft (2019-09) can slot between
	// the existing ones without renumbering.
	assert.Less(t, 1, int(jsonschema.Draft2020)-int(jsonschema.Draft7),
		"adjacent integer values leave no room for intermediate drafts")
}

func TestUnknownDraftDoesNotEmit2020URI(t *testing.T) {
	t.Parallel()

	// An unknown Draft value does not emit the 2020-12 schema URI.
	type MyType struct {
		Name string `json:"name"`
	}

	unknownDraft := jsonschema.Draft(99)
	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithDraft(unknownDraft),
	)
	require.NoError(t, err)

	assert.NotEqual(t, "https://json-schema.org/draft/2020-12/schema", s.Schema,
		"unknown Draft value should not emit the Draft2020 URI")
}

func TestVocabSetOmitsMetaData(t *testing.T) {
	t.Parallel()

	// Disabling the metaData vocabulary leaves a string instance valid:
	// annotation keywords (title, description) are not validated.
	schema := &jsonschema.Schema{
		Schema:      "https://json-schema.org/draft/2020-12/schema",
		Type:        "string",
		Title:       "My Title",
		Description: "My Description",
	}

	err := jsonschema.Validate(t.Context(), schema, "hello",
		jsonschema.WithVocabularies(
			jsonschema.VocabCore2020,
			jsonschema.VocabValidation2020,
			// The metaData vocabulary is NOT active.
		),
	)
	require.NoError(t, err)
}

func TestJSONSchemaExtenderReceivesMutableSchema(t *testing.T) {
	t.Parallel()

	// The extender receives the schema before extractToDefs may wrap it.
	// If the extender sets $defs directly, the subsequent extraction
	// creates a $ref wrapper that loses those fields.
	type MyType struct {
		Item extenderWithDefs `json:"item"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context())
	require.NoError(t, err)

	// The extender sets a $defs entry; verify it survives extraction.
	prop := s.Properties["item"]
	require.NotNil(t, prop)

	// After extractToDefs wraps this as a $ref, the inline $defs set by
	// the extender should still be present in the definition schema.
	defSchema := s.Defs["extenderWithDefs"]
	require.NotNil(t, defSchema, "definition for extenderWithDefs should exist")
	require.NotNil(t, defSchema.Defs, "extender-set $defs should survive extraction")
	assert.Contains(t, defSchema.Defs, "customDef",
		"extender-set $defs entry should be preserved")
}

func TestFieldContextParentPartiallyBuilt(t *testing.T) {
	t.Parallel()

	// Every field's interpreter sees the fully populated Parent.Properties, so
	// the count is independent of field processing order.
	interp := &parentInspector{}

	type MyType struct {
		Alpha string `inspect:"true" json:"alpha"`
		Beta  string `inspect:"true" json:"beta"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("inspect", interp),
	)
	require.NoError(t, err)

	// Both fields see the complete parent Properties map (all siblings present).
	for _, snap := range interp.snapshots {
		assert.Equal(t, 2, snap.propCount,
			"field %q should see all sibling properties in Parent, got %d",
			snap.fieldName, snap.propCount)
	}
}

// structFieldInspector is a TagInterpreter that records the FieldContext it
// receives, so the test can assert what generation populates.
type structFieldInspector struct {
	contexts []jsonschema.FieldContext
}

func (i *structFieldInspector) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	i.contexts = append(i.contexts, field)

	return nil
}

func TestFieldContextStructField(t *testing.T) {
	t.Parallel()

	// The full reflect.StructField reaches interpreters, so they can read
	// sibling struct tags (here the json tag's omitempty) and the Go name.
	interp := &structFieldInspector{}

	type MyType struct {
		Alpha string `inspect:"true" json:"alpha,omitempty"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("inspect", interp),
	)
	require.NoError(t, err)

	require.Len(t, interp.contexts, 1)

	field := interp.contexts[0]

	assert.Equal(t, "alpha", field.Name)
	assert.Equal(t, "Alpha", field.StructField.Name)
	assert.Equal(t, "alpha,omitempty", field.StructField.Tag.Get("json"))
	assert.Equal(t, field.Type, field.StructField.Type, "Type mirrors StructField.Type")
	assert.Equal(t, jsonschema.Draft2020, field.Draft, "Draft carries the default target draft")
}

func TestFieldContextDraft7(t *testing.T) {
	t.Parallel()

	// The generation run's target draft reaches interpreters, so one emitting
	// draft-dependent keywords (dependentRequired versus dependencies) can
	// pick correctly.
	interp := &structFieldInspector{}

	type MyType struct {
		Alpha string `inspect:"true" json:"alpha"`
	}

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("inspect", interp),
	)
	require.NoError(t, err)

	require.Len(t, interp.contexts, 1)
	assert.Equal(t, jsonschema.Draft7, interp.contexts[0].Draft)
}

func TestSchemaTypeAliasBlocksExtension(t *testing.T) {
	t.Parallel()

	// Keywords absent from the upstream Schema struct (e.g. $recursiveAnchor from
	// 2019-09) live only in Extra and are ignored by the validator. This is a
	// limitation of the type alias: the validator inspects struct fields, not
	// Extra.
	schema := &jsonschema.Schema{
		Type: "object",
		Extra: map[string]any{
			"$recursiveAnchor": true,
		},
	}

	// $recursiveAnchor is ignored, so any object instance validates.
	err := jsonschema.Validate(t.Context(), schema, map[string]any{})
	require.NoError(t, err)
}

func TestParseFloatMultipleOfNegative(t *testing.T) {
	t.Parallel()

	// Negative multipleOf should be rejected.
	schema := &jsonschema.Schema{
		Type:       "number",
		MultipleOf: new(-1.0),
	}

	// Validating with a negative multipleOf should produce an error.
	// Per JSON Schema (Section 6.2.1), multipleOf MUST be > 0.
	err := jsonschema.Validate(t.Context(), schema, 5.0)
	require.Error(t, err,
		"negative multipleOf should be rejected during validation")
}

func TestNaNInfInSchema(t *testing.T) {
	t.Parallel()

	// NaN and Inf in schema fields corrupt JSON serialization.
	schema := &jsonschema.Schema{
		Type:    "number",
		Minimum: new(math.NaN()),
	}

	// JSON marshaling should fail or produce invalid JSON for NaN.
	_, err := json.Marshal(schema)
	// Go's json.Marshal returns an error for NaN/Inf.
	require.Error(t, err,
		"NaN in schema should cause JSON serialization error")
}

func TestTagProcessingErrorPaths(t *testing.T) {
	t.Parallel()

	// Non-numeric values for numeric and integer keywords are rejected.
	tests := map[string]struct {
		typeDef func() (*jsonschema.Schema, error)
	}{
		"minimum=notanumber": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"minimum=notanumber"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"maxLength=notanumber": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"maxLength=notanumber"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"minItems=notanumber": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"minItems=notanumber"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.typeDef()
			require.Error(t, err,
				"invalid tag value should produce an error")
		})
	}
}

func TestFloat32ScalarKeepsDecimal(t *testing.T) {
	t.Parallel()

	// A float32 field's const/default/enum/examples value is parsed at 64 bits so
	// the stored schema value is the float64 closest to the decimal the author
	// wrote, not the float32-rounded approximation. Parsing 0.1 at 32 bits and
	// widening yields 0.10000000149011612, which would make a {"v":0.1} instance
	// fail validation against its own const. A value exactly representable in
	// float32 (e.g. 0.5, 1.5) is unchanged either way.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		key      string // schema field carrying the value: const/default/enum/examples
		want     string // marshaled JSON of that schema field
	}{
		"const decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=0.1"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "const",
			want: `0.1`,
		},
		"default decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"default=0.1"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "default",
			want: `0.1`,
		},
		"enum decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"enum=0.1|0.2"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "enum",
			want: `[0.1,0.2]`,
		},
		"examples decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"examples=0.1|0.2"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "examples",
			want: `[0.1,0.2]`,
		},
		"const exact float32 value unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=0.5"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "const",
			want: `0.5`,
		},
		"const exact float32 value 1.5 unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=1.5"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "const",
			want: `1.5`,
		},
		"float64 const decimal unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float64 `json:"v" jsonschema:"const=0.1"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
			key:  "const",
			want: `0.1`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			prop := s.Properties["v"]
			require.NotNil(t, prop)

			var got []byte

			switch tc.key {
			case "const":
				require.NotNil(t, prop.Const)

				got, err = json.Marshal(prop.Const)

			case "default":
				got = prop.Default
			case "enum":
				got, err = json.Marshal(prop.Enum)
			case "examples":
				got, err = json.Marshal(prop.Examples)
			}

			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got),
				"float32 %s value should keep the author's decimal, not the float32-rounded approximation", tc.key)
		})
	}
}

func TestFloat32ScalarOverflow(t *testing.T) {
	t.Parallel()

	// A float32 field still rejects a value outside its range: the 32-bit parse is
	// retained as an overflow check, so const/default/enum/examples values that the
	// float32 type can never hold surface as generation errors.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"const overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=1e300"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"default overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"default=1e300"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"enum overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"enum=1.0|1e300"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"examples overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"examples=1e300"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"out-of-range float32 tag scalar should be rejected at generation")
		})
	}
}

// TestTagTypeOverride pins the type= tag key: it replaces the reflected type
// assertion, removes the nullable anyOf wrapper a pointer field generates,
// and drops kind-derived numeric bounds when the new type is not numeric, so
// a Go type whose JSON representation differs from its reflection (such as a
// duration encoded as a string) needs no JSONSchemaExtend.
func TestTagTypeOverride(t *testing.T) {
	t.Parallel()

	t.Run("pointer duration as string", func(t *testing.T) {
		t.Parallel()

		type T struct {
			SLA *time.Duration `json:"sla" jsonschema:"type=string,pattern=^[0-9]+(ms|s|m|h)$"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		got, err := json.Marshal(s.Properties["sla"])
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"string","pattern":"^[0-9]+(ms|s|m|h)$"}`, string(got),
			"no anyOf/null wrapper and no leftover integer bounds")
	})

	t.Run("non-pointer duration as string", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Dur time.Duration `json:"dur" jsonschema:"type=string"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		got, err := json.Marshal(s.Properties["dur"])
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"string"}`, string(got),
			"the int64-derived range bounds are dropped with the type")
	})

	t.Run("string keywords dropped for non-string type", func(t *testing.T) {
		t.Parallel()

		// A time.Time reflects to {"type":"string","format":"date-time"}; an
		// integer override must not leave the stale string format behind.
		// WithDefinitions(false) keeps the schema inline so the keyword is
		// visible (in $defs mode the field would be a bare $ref).
		type T struct {
			D time.Time `json:"d" jsonschema:"type=integer"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(), jsonschema.WithDefinitions(false))
		require.NoError(t, err)

		got, err := json.Marshal(s.Properties["d"])
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"integer"}`, string(got),
			"the string-derived format is dropped with the type")
	})

	t.Run("numeric override keeps bounds", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int64 `json:"n" jsonschema:"type=number"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		field := s.Properties["n"]
		assert.Equal(t, "number", field.Type)
		assert.NotNil(t, field.Minimum, "kind-derived bounds stay for a numeric type")
	})

	t.Run("container types array replaced", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Tags []string `json:"tags" jsonschema:"type=array"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		field := s.Properties["tags"]
		assert.Equal(t, "array", field.Type, "the explicit type suppresses null in the type")
		assert.Nil(t, field.Types)
		require.NotNil(t, field.Items, "the element schema is preserved")
		assert.Equal(t, "string", field.Items.Type)
	})

	t.Run("definition ref replaced", func(t *testing.T) {
		t.Parallel()

		type Inner struct {
			A string `json:"a"`
		}

		type T struct {
			Field Inner `json:"field" jsonschema:"type=string"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		field := s.Properties["field"]
		require.NotNil(t, field)

		got, err := json.Marshal(field)
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"string"}`, string(got),
			"the bare $ref to the definition is dropped, not left as an unsatisfiable {$ref,type}")
	})

	t.Run("unknown type name rejected", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V string `json:"v" jsonschema:"type=interger"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidType)
	})
}

// TestTagEnumOnSequenceFields pins that an enum tag on a slice or array field
// constrains each element ("array of enum values"): the values parse against
// the element type and land on the item schemas rather than erroring or
// constraining the array value itself.
func TestTagEnumOnSequenceFields(t *testing.T) {
	t.Parallel()

	itemsOf := func(s *jsonschema.Schema, prop string) []*jsonschema.Schema {
		t.Helper()

		field := s.Properties[prop]
		require.NotNil(t, field)

		// Follow the nullable pointer wrapper if present.
		if len(field.AnyOf) == 2 && field.AnyOf[1] != nil && field.AnyOf[1].Type == "null" {
			field = field.AnyOf[0]
		}

		switch {
		case field.Items != nil:
			return []*jsonschema.Schema{field.Items}
		case len(field.PrefixItems) > 0:
			return field.PrefixItems
		case len(field.ItemsArray) > 0:
			return field.ItemsArray
		default:
			return nil
		}
	}

	t.Run("slice of strings", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days []string `json:"days" jsonschema:"enum=monday|tuesday|wednesday"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)
		assert.Equal(t, []any{"monday", "tuesday", "wednesday"}, items[0].Enum)
		assert.Equal(t, "string", items[0].Type)
		assert.Nil(t, s.Properties["days"].Enum, "the array schema itself carries no enum")
	})

	t.Run("slice of ints", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Codes []int `json:"codes" jsonschema:"enum=1|2|3"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "codes")
		require.Len(t, items, 1)
		assert.Equal(t, []any{int64(1), int64(2), int64(3)}, items[0].Enum)
	})

	t.Run("slice of sized ints drops the kind-derived element bounds", func(t *testing.T) {
		t.Parallel()

		// An enum on the elements restricts each element to a set the int8 range
		// already admits, so the kind-derived -128/127 bounds are redundant and
		// drop. The rule reads the keyword, not the dialect that wrote it: a
		// validate dive/oneof pin on the same elements resolves identically.
		type T struct {
			Codes []int8 `json:"codes" jsonschema:"enum=1|2|3"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "codes")
		require.Len(t, items, 1)
		require.Len(t, items[0].Enum, 3)
		assert.Nil(t, items[0].Minimum, "the enum subsumes the kind-derived int8 minimum")
		assert.Nil(t, items[0].Maximum, "the enum subsumes the kind-derived int8 maximum")
	})

	t.Run("pointer to slice", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days *[]string `json:"days" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)
		assert.Equal(t, []any{"monday", "tuesday"}, items[0].Enum)
	})

	t.Run("slice of override wrapper elements", func(t *testing.T) {
		t.Parallel()

		// The element type declares a NullAllowed stance; the enum must relocate onto
		// the generated wrapper's value branch so the null the stance admits stays
		// valid.
		type Day string

		type T struct {
			Days []Day `json:"days" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTypeSchemaFor[Day](jsonschema.TypeSchema{
				Value:       &jsonschema.Schema{Type: "string"},
				Nullability: jsonschema.NullAllowed,
			}),
		)
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)
		assert.Nil(t, items[0].Enum, "the enum must not sit beside the wrapper")
		require.Len(t, items[0].AnyOf, 2)
		assert.Equal(t, []any{"monday", "tuesday"}, items[0].AnyOf[0].Enum,
			"the enum lands on the wrapper's value branch")
	})

	t.Run("slice of nullable pointers puts enum on the value branch", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days []*string `json:"days" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)

		// The element is a nullable pointer, so its schema is anyOf[value,
		// null]. The enum must land on the value branch, not as a sibling of
		// anyOf where it would reject a valid null element.
		item := items[0]
		assert.Nil(t, item.Enum, "the anyOf wrapper carries no enum")
		require.Len(t, item.AnyOf, 2)
		assert.Equal(t, []any{"monday", "tuesday"}, item.AnyOf[0].Enum)
		assert.Equal(t, "null", item.AnyOf[1].Type)
	})

	t.Run("slice of nullable pointers accepts a null enum member", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days []*string `json:"days" jsonschema:"enum=monday|tuesday|null"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)

		// The nullable-pointer element legitimately permits a null member, so
		// generation succeeds (rather than rejecting null against the
		// dereferenced string) and the null lands on the value branch's enum.
		item := items[0]
		require.Len(t, item.AnyOf, 2)
		assert.Equal(t, []any{"monday", "tuesday", nil}, item.AnyOf[0].Enum)
		assert.Equal(t, "null", item.AnyOf[1].Type)
	})

	t.Run("fixed array uses prefixItems", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Pair [2]string `json:"pair" jsonschema:"enum=a|b"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "pair")
		require.Len(t, items, 2)

		for _, item := range items {
			assert.Equal(t, []any{"a", "b"}, item.Enum)
		}
	})

	t.Run("fixed array draft7 uses items array", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Pair [2]string `json:"pair" jsonschema:"enum=a|b"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
		require.NoError(t, err)

		field := s.Properties["pair"]
		require.NotNil(t, field)
		require.Len(t, field.ItemsArray, 2)

		for _, item := range field.ItemsArray {
			assert.Equal(t, []any{"a", "b"}, item.Enum)
		}
	})

	t.Run("nested slice constrains innermost items", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Groups [][]string `json:"groups" jsonschema:"enum=x|y"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		items := itemsOf(s, "groups")
		require.Len(t, items, 1)
		assert.Nil(t, items[0].Enum, "the inner array schema carries no enum")
		require.NotNil(t, items[0].Items)
		assert.Equal(t, []any{"x", "y"}, items[0].Items.Enum)
	})

	t.Run("byte slice has no item schema", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Data []byte `json:"data" jsonschema:"enum=a|b"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.Error(t, err, "a []byte field encodes as a base64 string with no items")
		assert.Contains(t, err.Error(), "no item schema")
	})

	t.Run("element type still checked", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Codes []int `json:"codes" jsonschema:"enum=1|oops"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.Error(t, err)
	})

	t.Run("const on slice remains an error", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days []string `json:"days" jsonschema:"const=monday"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.Error(t, err, "const is a whole-value constraint and is not redirected to items")
	})

	t.Run("scalar enum unchanged", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Day string `json:"day" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)
		assert.Equal(t, []any{"monday", "tuesday"}, s.Properties["day"].Enum)
	})
}

func TestTagEnumExamplesEmptySegment(t *testing.T) {
	t.Parallel()

	// A trailing or doubled '|' in enum/examples would otherwise inject a
	// spurious empty-string member for string fields (numeric/bool fields
	// already reject it). Empty segments are a parse error, consistent with the
	// rest of tag parsing.
	tests := map[string]struct {
		typeDef func() (*jsonschema.Schema, error)
		wantErr bool
	}{
		"enum trailing separator": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"enum=red|green|"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			wantErr: true,
		},
		"enum doubled separator": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"enum=red||green"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			wantErr: true,
		},
		"examples trailing separator": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"examples=a|"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			wantErr: true,
		},
		"valid enum still parses": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"enum=red|green"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			wantErr: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.typeDef()
			if tc.wantErr {
				require.Error(t, err, "empty enum/examples segment should be rejected")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestTagScalarAfterTypeOverride pins the effective scalar-parse type for the
// default/const/enum/examples keys: pairs apply in order, so a scalar key
// before type= parses against the field's Go type, while one after it parses
// against the overridden JSON type via a stand-in. Overrides to the non-scalar
// types (array, object, null) leave no type to parse against, so scalar keys
// following them are errors. The ordering rule does not govern the literal
// null. An override replaces the occurrence the literal was parsed against, so
// the tag rejects the literal on either side of the pair, and when a key on
// either side is an error, the message names the JSON type rather than the
// stand-in's Go kind. A literal before type=null is the exception, since that
// override names the null instance outright.
func TestTagScalarAfterTypeOverride(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		prop     string
		want     string // marshaled property schema
		err      string // substring required in the generation error
	}{
		"duration default after string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					SLA *time.Duration `json:"sla" jsonschema:"title=SLA,type=string,default=15m"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "sla",
			want: `{"title":"SLA","type":"string","default":"15m"}`,
		},
		"int const after string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"type=string,const=42"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string","const":"42"}`,
		},
		"default before override keeps Go-kind parsing": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"default=5,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string","default":5}`,
		},
		"examples after integer override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"type=integer,examples=1|2"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"integer","examples":[1,2]}`,
		},
		"enum after override applies to the value schema": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					Days []string `json:"days" jsonschema:"type=string,enum=a|b"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "days",
			// The enum lands on the field schema itself: the stand-in is
			// never a sequence, so the enum-to-items redirection a slice
			// field normally gets is off after the override. The override
			// also drops the reflected items keyword, which no longer
			// applies to a string.
			want: `{"type":"string","enum":["a","b"]}`,
		},
		"map override to scalar drops additionalProperties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]int `json:"v" jsonschema:"type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string"}`,
		},
		"inline struct override to scalar drops properties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V struct {
						X string `json:"x"`
					} `json:"v" jsonschema:"type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string"}`,
		},
		"default after object override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"type=object,default=x"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: `key "default" cannot follow type=object`,
		},
		"enum after array override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"type=array,enum=a|b"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: `key "enum" cannot follow type=array`,
		},
		"null default after string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *int `json:"v" jsonschema:"type=string,default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type string",
		},
		"null default after integer override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *int `json:"v" jsonschema:"type=integer,default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type integer",
		},
		// The same rejection from the other side of the pair. The fold takes
		// the literal against the slice's own decision, and the override then
		// withdraws the occurrence that admitted it.
		"null examples before a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"examples=null,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type string",
		},
		"null const before an integer override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *int `json:"v" jsonschema:"const=null,type=integer"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type integer",
		},
		"null enum member before a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"enum=a|null,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type string",
		},
		// A null literal keeps its meaning under the one override that names
		// the null instance, so type=null is the exception to the rule the
		// rows above state. Only the literal preceding it survives. A type=null
		// override carries no scalar type, so a key after it is an error like
		// every other scalar key there.
		"null default before a null override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"default=null,type=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"null","default":null}`,
		},
		"null default after a null override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"type=null,default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: `key "default" cannot follow type=null`,
		},
		// A list-valued key after the override reaches the rejection through the
		// per-member null test, so the report names the JSON type here too
		// rather than the stand-in's int64.
		"null enum member after an integer override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"type=integer,enum=1|null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type integer",
		},
		// An override to array carries no scalar type, so a null key following
		// it reports the missing scalar type instead. One preceding it still
		// reaches the null rejection, since the array the override names admits
		// no null either.
		"null default before an array override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"default=null,type=array"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type array",
		},
		// An enum on a sequence retargets to the item schemas, so a non-array
		// override reports the group conflict before the null literal is ever
		// weighed. That conflict is what keeps the element retarget outside the
		// null bookkeeping.
		"null enum member on a sequence before a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []*string `json:"v" jsonschema:"enum=a|null,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "array constraint conflicts with type=string",
		},
		// Tag order picks the key the report names.
		"the first null key names the rejection": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"default=null,examples=null,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: `key "default": cannot assign null`,
		},
		// Only the null literal is rejected. A non-null scalar before the
		// override still parses against the field's own shape.
		"a non-null default before a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"default=abc,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string","default":"abc"}`,
		},
		// The key after the override is the side read from the raw tag text, so
		// this is where a value that merely begins with null has to stay an
		// ordinary string.
		"a default beginning with null after a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *string `json:"v" jsonschema:"type=string,default=nullish"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"string","default":"nullish"}`,
		},
		"overflow still checked against stand-in": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"type=integer,const=99999999999999999999"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "invalid integer",
		},
		"explicit uniqueItems conflicts with a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"uniqueItems=true,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "array constraint conflicts with type=string",
		},
		"explicit pattern conflicts with an integer override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"pattern=^x$,type=integer"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "string constraint conflicts with type=integer",
		},
		"explicit minProperties conflicts with a string override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]int `json:"v" jsonschema:"minProperties=1,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "object constraint conflicts with type=string",
		},
		"numeric bound survives a number override": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"minimum=1,type=number"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":"number","minimum":1}`,
		},
		"numeric bound after a string override conflicts": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"type=string,minimum=5"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "numeric constraint conflicts with type=string",
		},
		"pattern after an integer override conflicts": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"type=integer,pattern=^x$"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "string constraint conflicts with type=integer",
		},
		"uniqueItems after a string override conflicts": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"type=string,uniqueItems=true"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "array constraint conflicts with type=string",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			got, err := json.Marshal(s.Properties[tc.prop])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// nullStanced is the type the stance and verbatim rows below configure. A
// type-level hook keys on a named type, so the rows cannot declare it locally
// the way the rest of the table declares its field structs.
type nullStanced struct {
	X string `json:"x"`
}

// TestTagNullLiteralFollowsTheNullDecision pins where the tag's null literal is
// a value the field can hold. A scalar key spells null wherever the occurrence
// admits one, and which occurrences those are is the generator's decision
// rather than the Go type's. A bare slice, map, byte slice, or interface is
// nilable without being a pointer, and WithNullable(false), a NullForbidden
// stance, and a verbatim payload each turn the decision off. Each switch gets a
// pair of rows running the same tag against the same field shape.
//
// Default and examples both reach the literal through Shape.ParseScalar, so
// one examples row per direction stands in for the type roster the default
// rows walk. A const on a container never reaches it. The shape the container
// names rejects the key before the parser reads the value. An enum on a
// sequence reaches the elements instead, and an element occurrence answers
// from its own Go type. Rows for both pin that neither takes the literal from
// the container's decision.
func TestTagNullLiteralFollowsTheNullDecision(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		prop     string
		want     string // marshaled property schema
		err      string // substring required in the generation error
	}{
		"null default on a bare slice": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":["null","array"],"items":{"type":"string"},"default":null}`,
		},
		"null default on a bare map": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]int `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":["null","object"],"additionalProperties":{"type":"integer"},"default":null}`,
		},
		"null default on a byte slice": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []byte `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":["null","string"],"contentEncoding":"base64","default":null}`,
		},
		// A plain interface reflects as the unrestricted schema, and render
		// drops the null branch as a duplicate of it, so the row pins the
		// accepted default rather than a type list.
		"null default on an interface": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V any `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"default":null}`,
		},
		// A json.RawMessage renders as the same unrestricted schema an interface
		// does, and answers the opposite way. Its schema comes from the built-in
		// leaf table rather than from reflection over a container, so no node
		// records a null decision for the tag to read.
		"null default on a raw message": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V json.RawMessage `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null",
		},
		"null examples member on a bare slice": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"examples=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":["null","array"],"items":{"type":"string"},"examples":[null]}`,
		},
		"null examples member with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"examples=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		"null default on a slice with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		"null default on a map with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]int `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		"null default on a byte slice with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []byte `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		"null default on an interface with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V any `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		// The pointer occurrence answers the same decision. With the null branch
		// dropped its schema is a bare integer, which a null default could
		// never be an instance of.
		"null default on a pointer with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *int `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			err: "cannot assign null",
		},
		// WithNullable is not the only switch. A type declaring NullForbidden
		// gives every occurrence of itself a schema with no null branch, and the
		// generator emits a verbatim payload as authored, with no null encoding
		// at all, so a pointer to either holds no null for the literal to name.
		// A stance moves the decision the other way too, which the NullAllowed
		// row below takes on a field that is not a pointer at all.
		"null default on a pointer to a null-forbidding type": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *nullStanced `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(),
					jsonschema.WithTypeSchemaFor[nullStanced](jsonschema.TypeSchema{
						Value:       &jsonschema.Schema{Type: "object"},
						Nullability: jsonschema.NullForbidden,
					}))
			},
			err: "cannot assign null",
		},
		"null default on a pointer to a verbatim type": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V *nullStanced `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(),
					jsonschema.WithTypeSchemaFor[nullStanced](jsonschema.TypeSchema{
						Verbatim: &jsonschema.Schema{Type: "object"},
					}))
			},
			err: "cannot assign null",
		},
		"null default on a value field of a null-admitting type": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V nullStanced `json:"v" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[T](t.Context(),
					jsonschema.WithTypeSchemaFor[nullStanced](jsonschema.TypeSchema{
						Value:       &jsonschema.Schema{Type: "object"},
						Nullability: jsonschema.NullAllowed,
					}))
			},
			prop: "v",
			want: `{"default":null,"anyOf":[{"$ref":"#/$defs/nullStanced"},{"type":"null"}]}`,
		},
		// Pairs apply in order, so a scalar key before a type= pair parses
		// against the field's own occurrence. The ordering rule does not govern
		// the literal null. The override withdraws the occurrence that admitted
		// the null, so the fold rejects the literal it already took.
		// TestTagScalarAfterTypeOverride covers the remaining orderings.
		"null default before a type override on a bare slice": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"default=null,type=string"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type string",
		},
		// The carve-out is the null literal alone. A slice has no scalar value
		// the tag can spell.
		"a non-null default on a bare slice": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"default=x"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign scalar value",
		},
		// The shape a map names refuses a const before the parser reads the
		// value, so the null decision never reaches it.
		"null const on a bare map": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]int `json:"v" jsonschema:"const=null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "pinned value is not supported on a object",
		},
		// An enum on a sequence constrains the elements, and an element
		// occurrence is not the container. These rows read the element's own
		// pointer-ness, which the container's decision must not displace. The
		// last of them is a mismatch the package accepts. The member has no
		// null branch on the element schema to match it.
		"null enum member on a slice of pointers": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []*string `json:"v" jsonschema:"enum=a|null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			prop: "v",
			want: `{"type":["null","array"],"items":{"anyOf":[{"type":"string","enum":["a",null]},{"type":"null"}]}}`,
		},
		"null enum member on a slice of values": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"enum=a|null"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
			err: "cannot assign null to non-nullable type string",
		},
		"null enum member on a slice of pointers with the null branch dropped": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []*string `json:"v" jsonschema:"enum=a|null"`
				}

				return jsonschema.GenerateFor[T](t.Context(), jsonschema.WithNullable(false))
			},
			prop: "v",
			want: `{"type":"array","items":{"type":"string","enum":["a",null]}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			got, err := json.Marshal(s.Properties[tc.prop])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// TestTagNullDefaultOnASelfReferentialField pins the null decision on a node
// that carries it in two halves. A field referencing a type links to a $defs
// entry rather than to a body, and such a node's own nullable field is never
// set. Its decision is the entry's recorded stance combined with the
// occurrence's pointer-ness, so reading that nullable field instead of the
// combination would reject this tag.
func TestTagNullDefaultOnASelfReferentialField(t *testing.T) {
	t.Parallel()

	type selfRef struct {
		Next *selfRef `json:"next" jsonschema:"default=null"`
	}

	s, err := jsonschema.GenerateFor[selfRef](t.Context())
	require.NoError(t, err)

	def := s.Defs["selfRef"]
	require.NotNil(t, def)

	got, err := json.Marshal(def.Properties["next"])
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"default":null,"anyOf":[{"$ref":"#/$defs/selfRef"},{"type":"null"}]}`, string(got))
}

// nullStancedRecursive is the self-referential type
// TestTagNullLiteralOnARecursiveStancedType configures. The untagged sibling
// references the same type, so the report has to name the field whose tag took
// the literal rather than every reference the stance reaches.
type nullStancedRecursive struct {
	Other *nullStancedRecursive `json:"other"`
	Next  *nullStancedRecursive `json:"next"  jsonschema:"default=null"`
}

// nullStancedRecursiveExamples is the examples half of the same shape. Default
// and examples are the two keys that reach a null literal on a reference,
// since the matrix rejects const and enum on a referenced definition before
// the null decision runs. TestTagValueKeyOnAStructShapedField pins that
// rejection.
type nullStancedRecursiveExamples struct {
	Next *nullStancedRecursiveExamples `json:"next" jsonschema:"examples=null"`
}

// nullStancedMutualOuter and nullStancedMutualInner are the mutually recursive
// pair TestTagNullLiteralOnAMutuallyRecursiveStancedType configures. The tagged
// field sits on the inner type, so the re-check reaches it through the outer
// type's $defs body rather than through the root's own properties.
type nullStancedMutualOuter struct {
	Inner *nullStancedMutualInner `json:"inner"`
}

// nullStancedMutualInner is nullStancedMutualOuter's partner.
type nullStancedMutualInner struct {
	Outer *nullStancedMutualOuter `json:"outer" jsonschema:"default=null"`
}

// nullStancedPlain is the type
// TestTagNullLiteralOnANonRecursiveStancedType records a stance for. Nothing
// references it from inside its own definition, so its stance is recorded
// before any tag reads it.
type nullStancedPlain struct {
	X string `json:"x"`
}

// forbidNullStance returns the extender that records a NullForbidden stance for
// T, the one hook the null re-check answers to.
func forbidNullStance[T any]() jsonschema.GenerateOption {
	return jsonschema.WithTypeSchemaExtenderFor[T](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			ts.Nullability = jsonschema.NullForbidden

			return nil
		})
}

// TestTagNullLiteralOnARecursiveStancedType pins the re-check that closes the
// one window a null decision moves in. A field referencing the type it belongs
// to resolves against a $defs entry still being built, so the stance the
// extender records for that type lands after the tag has already accepted the
// literal against it. The generator carries the keys that took a literal to a
// pass running once every stance is final, and that pass reports the occurrence
// the final decision refuses.
func TestTagNullLiteralOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		parent   string // the declaring struct the report must name
		key      string // the tag key the report must name
	}{
		"default": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[nullStancedRecursive](t.Context(),
					forbidNullStance[nullStancedRecursive]())
			},
			parent: "jsonschema_test.nullStancedRecursive",
			key:    "default",
		},
		"examples": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[nullStancedRecursiveExamples](t.Context(),
					forbidNullStance[nullStancedRecursiveExamples]())
			},
			parent: "jsonschema_test.nullStancedRecursiveExamples",
			key:    "examples",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
			require.ErrorContains(t, err,
				tc.parent+` field "next": jsonschema tag: key "`+tc.key+`"`)
		})
	}
}

// TestTagNullLiteralOnAMutuallyRecursiveStancedType pins the same re-check
// where the offending field is not the root's own. The tagged field sits on the
// inner type, so the walk reaches it through the outer type's $defs body, and
// the report names the struct whose schema carries the field rather than the
// root.
func TestTagNullLiteralOnAMutuallyRecursiveStancedType(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.GenerateFor[nullStancedMutualOuter](t.Context(),
		forbidNullStance[nullStancedMutualOuter]())
	require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
	require.ErrorContains(t, err, `nullStancedMutualInner field "outer"`)
}

// TestTagNullLiteralOnANonRecursiveStancedType is the control for the case the
// re-check never sees. A field referencing a type the generator has already
// finished reads the final stance while the tag runs, so the scalar constructor
// refuses the literal there and names the field's Go kind rather than the
// referenced type.
func TestTagNullLiteralOnANonRecursiveStancedType(t *testing.T) {
	t.Parallel()

	type holder struct {
		Next *nullStancedPlain `json:"next" jsonschema:"default=null"`
	}

	_, err := jsonschema.GenerateFor[holder](t.Context(),
		forbidNullStance[nullStancedPlain]())
	require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
	require.ErrorContains(t, err, "cannot assign null to non-nullable type struct")
}

// TestTagValueKeyOnAStructShapedField pins the rejection the null re-check
// tests lean on. A struct-shaped field carries no scalar to pin or to
// enumerate, so the constraint matrix rejects const and enum on it before the
// tag parses the value, and the null decision never runs.
//
// Every row records a NullForbidden stance for the struct type, so the null
// decision would reject the literal if it ran first. The two default= rows are
// the controls. The same shape under the same stance reports the null
// sentinel, so the const and enum rows report the matrix sentinel because the
// matrix runs first, not because the stance left the null decision nothing to
// reject. Two rows spell a non-null literal the null decision could never
// reach, since the matrix is keyed on the field's shape rather than on the
// key's value. The last row generates without definitions, where the same
// struct classifies as a declared object, so the rejection holds under either
// setting.
func TestTagValueKeyOnAStructShapedField(t *testing.T) {
	t.Parallel()

	// Type other stands in for any struct a field can name.
	type other struct {
		X string `json:"x"`
	}

	// The generator extracts a named struct into $defs by default, so a field
	// of that type classifies as a referenced definition. WithDefinitions(false)
	// inlines the same struct, and the matrix names the inlined form "declared
	// object" instead.
	const (
		constOnRef = `key "const": constraint not supported for this shape: ` +
			`pinned value is not supported on a referenced definition`
		enumOnRef = `key "enum": constraint not supported for this shape: ` +
			`enumerated values is not supported on a referenced definition`
		enumOnObject = `key "enum": constraint not supported for this shape: ` +
			`enumerated values is not supported on a declared object`
		nullRefused = `key "default": cannot assign null to non-nullable type`
	)

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     error  // the sentinel the report must carry
		absent   error  // the sentinel the report must not carry
		err      string // substring required in the generation error
	}{
		"null const on a self-referential pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type selfRef struct {
					Next *selfRef `json:"next" jsonschema:"const=null"`
				}

				return jsonschema.GenerateFor[selfRef](t.Context(),
					forbidNullStance[selfRef]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    constOnRef,
		},
		"null enum on a self-referential pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type selfRef struct {
					Next *selfRef `json:"next" jsonschema:"enum=null"`
				}

				return jsonschema.GenerateFor[selfRef](t.Context(),
					forbidNullStance[selfRef]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    enumOnRef,
		},
		"null default on a self-referential pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type selfRef struct {
					Next *selfRef `json:"next" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[selfRef](t.Context(),
					forbidNullStance[selfRef]())
			},
			want:   tagmodel.ErrNullNotAdmitted,
			absent: tagmodel.ErrUnsupported,
			err:    nullRefused,
		},
		"non-null const on a self-referential pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type selfRef struct {
					Next *selfRef `json:"next" jsonschema:"const=x"`
				}

				return jsonschema.GenerateFor[selfRef](t.Context(),
					forbidNullStance[selfRef]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    constOnRef,
		},
		"null const on a struct pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type holder struct {
					Next *other `json:"next" jsonschema:"const=null"`
				}

				return jsonschema.GenerateFor[holder](t.Context(),
					forbidNullStance[other]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    constOnRef,
		},
		"null enum on a struct pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type holder struct {
					Next *other `json:"next" jsonschema:"enum=null"`
				}

				return jsonschema.GenerateFor[holder](t.Context(),
					forbidNullStance[other]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    enumOnRef,
		},
		"null default on a struct pointer": {
			generate: func() (*jsonschema.Schema, error) {
				type holder struct {
					Next *other `json:"next" jsonschema:"default=null"`
				}

				return jsonschema.GenerateFor[holder](t.Context(),
					forbidNullStance[other]())
			},
			want:   tagmodel.ErrNullNotAdmitted,
			absent: tagmodel.ErrUnsupported,
			err:    nullRefused,
		},
		"enum on an inlined struct": {
			generate: func() (*jsonschema.Schema, error) {
				type holder struct {
					Next other `json:"next" jsonschema:"enum=a|b"`
				}

				return jsonschema.GenerateFor[holder](t.Context(),
					jsonschema.WithDefinitions(false), forbidNullStance[other]())
			},
			want:   tagmodel.ErrUnsupported,
			absent: tagmodel.ErrNullNotAdmitted,
			err:    enumOnObject,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.ErrorIs(t, err, tc.want)
			require.NotErrorIs(t, err, tc.absent)
			require.ErrorContains(t, err, tc.err)
		})
	}
}

// TestTagEmptyStringKeyValues covers the empty-value rule for the string-typed
// annotation keys: an empty description=, title=, pattern=, or format= would
// assign the field's zero value, so the keyword would silently never be
// emitted. Empty values are a parse error for every key except const and
// default on a string field, and these four are no exception.
func TestTagEmptyStringKeyValues(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"empty description": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"description="`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"empty title": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"title="`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"empty pattern": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"pattern=,minLength=1"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"empty format": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"format="`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.ErrorContains(t, err, "requires a non-empty value")
		})
	}
}

// TestTagElementEnumVsTypeOverride covers the interaction of an
// element-redirected enum with a type= override. The enum on a sequence field
// lands on the item schemas, structure only the array type keeps, so a
// non-array override must report the same conflict any other array constraint
// gets rather than silently dropping the author's enum with the items it rides
// on. An override naming array keeps the items and the enum survives.
func TestTagElementEnumVsTypeOverride(t *testing.T) {
	t.Parallel()

	t.Run("non-array override conflicts", func(t *testing.T) {
		t.Parallel()

		type T struct {
			F []string `json:"f" jsonschema:"enum=a|b,type=string"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.ErrorContains(t, err, "array constraint conflicts with type=string")
	})

	t.Run("array override keeps the element enum", func(t *testing.T) {
		t.Parallel()

		type T struct {
			F []string `json:"f" jsonschema:"enum=a|b,type=array"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		field := s.Properties["f"]
		require.NotNil(t, field)
		require.NotNil(t, field.Items, "the array override keeps the item schema")
		assert.Equal(t, []any{"a", "b"}, field.Items.Enum)
	})
}

// evenForMultipleOf carries a type-level multipleOf through WithTypeSchemaFor
// in TestTagMultipleOfNullableConsistency.
type evenForMultipleOf int

// TestTagMultipleOfNullableConsistency pins that an authored multipleOf
// resolves with the same replace semantics on nullable and non-nullable
// occurrences of the same type. The nullable split used to move the authored
// value onto the anyOf wrapper while restoring the type-derived value on the
// value branch, silently conjoining the two: identical tags accepted 6 on the
// value field but only multiples of 12 on the pointer field.
func TestTagMultipleOfNullableConsistency(t *testing.T) {
	t.Parallel()

	type doc struct {
		Plain evenForMultipleOf  `json:"plain" jsonschema:"multipleOf=6"`
		Ptr   *evenForMultipleOf `json:"ptr"   jsonschema:"multipleOf=6"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaFor[evenForMultipleOf](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "integer", MultipleOf: new(4.0)},
		}))
	require.NoError(t, err)

	for _, instance := range []map[string]any{
		{"plain": 6, "ptr": 6},
		{"plain": 12, "ptr": 12},
		{"plain": 6, "ptr": nil},
	} {
		require.NoError(t, jsonschema.Validate(t.Context(), s, instance),
			"instance %v must satisfy the authored multipleOf on both occurrences", instance)
	}

	require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": 4, "ptr": 6}),
		"the authored multipleOf replaces the type value on the plain field")
	require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": 6, "ptr": 4}),
		"the authored multipleOf replaces the type value on the pointer field")
}

// digitPatternString carries a type-level pattern through WithTypeSchemaFor in
// TestTagPatternNullableConsistency.
type digitPatternString string

// TestTagPatternNullableConsistency pins that an authored pattern resolves with
// the same replace semantics on nullable and non-nullable occurrences of the
// same type, like multipleOf above. The nullable split used to move the
// authored pattern onto the anyOf wrapper while restoring the type-derived
// pattern on the value branch, silently conjoining the two: disjoint patterns
// rejected every string on the pointer field while the value field accepted
// the authored pattern.
func TestTagPatternNullableConsistency(t *testing.T) {
	t.Parallel()

	type doc struct {
		Plain digitPatternString  `json:"plain" jsonschema:"pattern=^[a-z]+$"`
		Ptr   *digitPatternString `json:"ptr"   jsonschema:"pattern=^[a-z]+$"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaFor[digitPatternString](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "string", Pattern: "^[0-9]+$"},
		}))
	require.NoError(t, err)

	for _, instance := range []map[string]any{
		{"plain": "abc", "ptr": "abc"},
		{"plain": "abc", "ptr": nil},
	} {
		require.NoError(t, jsonschema.Validate(t.Context(), s, instance),
			"instance %v must satisfy the authored pattern on both occurrences", instance)
	}

	require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": "123", "ptr": "abc"}),
		"the authored pattern replaces the type value on the plain field")
	require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": "abc", "ptr": "123"}),
		"the authored pattern replaces the type value on the pointer field")
}

// hostnameFormatString carries a type-level format through WithTypeSchemaFor in
// TestTagFormatNullableStaysOnValueBranch.
type hostnameFormatString string

// TestTagFormatNullableStaysOnValueBranch pins the format analog of
// TestTagPatternNullableConsistency structurally (format is annotation-only by
// default in 2020-12): the authored format replaces the type-derived one on the
// value branch of the nullable anyOf, leaving no wrapper sibling that would
// conjoin the two.
func TestTagFormatNullableStaysOnValueBranch(t *testing.T) {
	t.Parallel()

	type doc struct {
		Ptr *hostnameFormatString `json:"ptr" jsonschema:"format=email"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaFor[hostnameFormatString](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "string", Format: "hostname"},
		}))
	require.NoError(t, err)

	prop := s.Properties["ptr"]
	require.NotNil(t, prop)
	require.Len(t, prop.AnyOf, 2)

	assert.Empty(t, prop.Format,
		"the null wrapper carries no format sibling")
	assert.Equal(t, "email", prop.AnyOf[0].Format,
		"the authored format replaces the type value on the value branch")
}
