package jsonschema_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Tests for jsonschema struct-tag parsing: key-value vs bare-description
// detection, value typing, and error paths.

// extenderWithDefs implements JSONSchemaExtender and sets $defs on the schema.
// Used to test that extender-set fields survive extractToDefs wrapping.
type extenderWithDefs struct {
	Value string `json:"value"`
}

func (extenderWithDefs) JSONSchemaExtend(s *jsonschema.Schema) {
	if s.Defs == nil {
		s.Defs = map[string]*jsonschema.Schema{}
	}

	s.Defs["customDef"] = &jsonschema.Schema{Type: "string"}
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

func (p *parentInspector) TagKey() string { return "inspect" }

func (p *parentInspector) Interpret(tag string, field jsonschema.FieldContext) error {
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
	// the description "Hello World".
	type MyType struct {
		Name string `json:"name" jsonschema:"description=Hello World,minimum=1"`
	}

	s, err := jsonschema.GenerateFor[MyType]()
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)

	assert.Equal(t, "Hello World", prop.Description)
}

func TestBareDescriptionWithEqualsSign(t *testing.T) {
	t.Parallel()

	// A bare description whose first token looks like word= but carries a spaced
	// value (e.g. "a=b is the formula") is treated as a description, not as a
	// key-value pair, so it does not produce an unrecognized-key error.
	type MyType struct {
		Name string `json:"name" jsonschema:"a=b is the formula"`
	}

	s, err := jsonschema.GenerateFor[MyType]()
	require.NoError(t, err,
		"bare description starting with word= should not produce an error")

	prop := s.Properties["name"]
	require.NotNil(t, prop)
	assert.Equal(t, "a=b is the formula", prop.Description,
		"bare description starting with word= should be treated as description")
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

				return jsonschema.GenerateFor[T]()
			},
		},
		"negative maxLength": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"maxLength=-1"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"negative minItems": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"minItems=-1"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"negative maxItems": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"maxItems=-1"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"negative minProperties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]string `json:"v" jsonschema:"minProperties=-1"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"negative maxProperties": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V map[string]string `json:"v" jsonschema:"maxProperties=-1"`
				}

				return jsonschema.GenerateFor[T]()
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

				return jsonschema.GenerateFor[T]()
			},
		},
		"maximum=+Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"maximum=+Inf"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"minimum=-Inf": {
			generate: func() (*jsonschema.Schema, error) {
				type T struct {
					V float64 `json:"v" jsonschema:"minimum=-Inf"`
				}

				return jsonschema.GenerateFor[T]()
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

func TestMultipleOfZero(t *testing.T) {
	t.Parallel()

	type MyType struct {
		Value float64 `json:"value" jsonschema:"multipleOf=0"`
	}

	_, err := jsonschema.GenerateFor[MyType]()
	// The multipleOf value MUST be strictly > 0 per JSON Schema spec.
	require.Error(t, err,
		"multipleOf=0 should be rejected")
}

func TestMultipleOfNegative(t *testing.T) {
	t.Parallel()

	type MyType struct {
		Value float64 `json:"value" jsonschema:"multipleOf=-1"`
	}

	_, err := jsonschema.GenerateFor[MyType]()
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

	s, err := jsonschema.GenerateFor[MyType]()
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

func TestParseTypedScalarRejectsUnknownKinds(t *testing.T) {
	t.Parallel()

	type Inner struct {
		X string `json:"x"`
	}

	type MyType struct {
		Data Inner `json:"data" jsonschema:"default=foo"`
	}

	_, err := jsonschema.GenerateFor[MyType]()
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

func TestDraftIotaOrdering(t *testing.T) {
	t.Parallel()

	// Draft7=0, Draft2020=1. Can't insert Draft2019 between them.
	assert.Less(t, int(jsonschema.Draft7), int(jsonschema.Draft2020),
		"Draft7 < Draft2020 for comparison operators to work")

	// If a Draft2019 were added, it would need to be between 0 and 1,
	// which is impossible with the current iota ordering.
}

func TestUnknownDraftDoesNotEmit2020URI(t *testing.T) {
	t.Parallel()

	// An unknown Draft value does not emit the 2020-12 schema URI.
	type MyType struct {
		Name string `json:"name"`
	}

	unknownDraft := jsonschema.Draft(99)
	s, err := jsonschema.GenerateFor[MyType](
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

	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabValidation2020: true,
			// The metaData vocabulary is NOT active.
		}),
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

	s, err := jsonschema.GenerateFor[MyType]()
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

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(interp),
	)
	require.NoError(t, err)

	// Both fields see the complete parent Properties map (all siblings present).
	for _, snap := range interp.snapshots {
		assert.Equal(t, 2, snap.propCount,
			"field %q should see all sibling properties in Parent, got %d",
			snap.fieldName, snap.propCount)
	}
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
	err := jsonschema.Validate(schema, map[string]any{})
	require.NoError(t, err)
}

func TestParseFloatMultipleOfNegative(t *testing.T) {
	t.Parallel()

	// Negative multipleOf should be rejected.
	schema := &jsonschema.Schema{
		Type:       "number",
		MultipleOf: jsonschema.Ptr(-1.0),
	}

	// Validating with a negative multipleOf should produce an error.
	// Per JSON Schema (Section 6.2.1), multipleOf MUST be > 0.
	err := jsonschema.Validate(schema, 5.0)
	require.Error(t, err,
		"negative multipleOf should be rejected during validation")
}

func TestNaNInfInSchema(t *testing.T) {
	t.Parallel()

	// NaN and Inf in schema fields corrupt JSON serialization.
	schema := &jsonschema.Schema{
		Type:    "number",
		Minimum: jsonschema.Ptr(math.NaN()),
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

				return jsonschema.GenerateFor[T]()
			},
		},
		"maxLength=notanumber": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"maxLength=notanumber"`
				}

				return jsonschema.GenerateFor[T]()
			},
		},
		"minItems=notanumber": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					V []string `json:"v" jsonschema:"minItems=notanumber"`
				}

				return jsonschema.GenerateFor[T]()
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

				return jsonschema.GenerateFor[doc]()
			},
			key:  "const",
			want: `0.1`,
		},
		"default decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"default=0.1"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			key:  "default",
			want: `0.1`,
		},
		"enum decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"enum=0.1|0.2"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			key:  "enum",
			want: `[0.1,0.2]`,
		},
		"examples decimal not float32-rounded": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"examples=0.1|0.2"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			key:  "examples",
			want: `[0.1,0.2]`,
		},
		"const exact float32 value unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=0.5"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			key:  "const",
			want: `0.5`,
		},
		"const exact float32 value 1.5 unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=1.5"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			key:  "const",
			want: `1.5`,
		},
		"float64 const decimal unchanged": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float64 `json:"v" jsonschema:"const=0.1"`
				}

				return jsonschema.GenerateFor[doc]()
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

				return jsonschema.GenerateFor[doc]()
			},
		},
		"default overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"default=1e300"`
				}

				return jsonschema.GenerateFor[doc]()
			},
		},
		"enum overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"enum=1.0|1e300"`
				}

				return jsonschema.GenerateFor[doc]()
			},
		},
		"examples overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"examples=1e300"`
				}

				return jsonschema.GenerateFor[doc]()
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

		s, err := jsonschema.GenerateFor[T]()
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

		s, err := jsonschema.GenerateFor[T]()
		require.NoError(t, err)

		got, err := json.Marshal(s.Properties["dur"])
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"string"}`, string(got),
			"the int64-derived range bounds are dropped with the type")
	})

	t.Run("numeric override keeps bounds", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int64 `json:"n" jsonschema:"type=number"`
		}

		s, err := jsonschema.GenerateFor[T]()
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

		s, err := jsonschema.GenerateFor[T]()
		require.NoError(t, err)

		field := s.Properties["tags"]
		assert.Equal(t, "array", field.Type, "the explicit type suppresses null in the type")
		assert.Nil(t, field.Types)
		require.NotNil(t, field.Items, "the element schema is preserved")
		assert.Equal(t, "string", field.Items.Type)
	})

	t.Run("unknown type name rejected", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V string `json:"v" jsonschema:"type=interger"`
		}

		_, err := jsonschema.GenerateFor[T]()
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

		s, err := jsonschema.GenerateFor[T]()
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

		s, err := jsonschema.GenerateFor[T]()
		require.NoError(t, err)

		items := itemsOf(s, "codes")
		require.Len(t, items, 1)
		assert.Equal(t, []any{1, 2, 3}, items[0].Enum)
	})

	t.Run("pointer to slice", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days *[]string `json:"days" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T]()
		require.NoError(t, err)

		items := itemsOf(s, "days")
		require.Len(t, items, 1)
		assert.Equal(t, []any{"monday", "tuesday"}, items[0].Enum)
	})

	t.Run("fixed array uses prefixItems", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Pair [2]string `json:"pair" jsonschema:"enum=a|b"`
		}

		s, err := jsonschema.GenerateFor[T]()
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

		s, err := jsonschema.GenerateFor[T](jsonschema.WithDraft(jsonschema.Draft7))
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

		s, err := jsonschema.GenerateFor[T]()
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

		_, err := jsonschema.GenerateFor[T]()
		require.Error(t, err, "a []byte field encodes as a base64 string with no items")
		assert.Contains(t, err.Error(), "no item schema")
	})

	t.Run("element type still checked", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Codes []int `json:"codes" jsonschema:"enum=1|oops"`
		}

		_, err := jsonschema.GenerateFor[T]()
		require.Error(t, err)
	})

	t.Run("const on slice remains an error", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Days []string `json:"days" jsonschema:"const=monday"`
		}

		_, err := jsonschema.GenerateFor[T]()
		require.Error(t, err, "const is a whole-value constraint and is not redirected to items")
	})

	t.Run("scalar enum unchanged", func(t *testing.T) {
		t.Parallel()

		type T struct {
			Day string `json:"day" jsonschema:"enum=monday|tuesday"`
		}

		s, err := jsonschema.GenerateFor[T]()
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

				return jsonschema.GenerateFor[T]()
			},
			wantErr: true,
		},
		"enum doubled separator": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"enum=red||green"`
				}

				return jsonschema.GenerateFor[T]()
			},
			wantErr: true,
		},
		"examples trailing separator": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"examples=a|"`
				}

				return jsonschema.GenerateFor[T]()
			},
			wantErr: true,
		},
		"valid enum still parses": {
			typeDef: func() (*jsonschema.Schema, error) {
				type T struct {
					F string `json:"f" jsonschema:"enum=red|green"`
				}

				return jsonschema.GenerateFor[T]()
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
