package validate_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
	"go.jacobcolvin.com/jsonschema/interpreters/validate"
)

func TestValidateInterpreter_StringConstraints(t *testing.T) {
	t.Parallel()

	type Form struct {
		Name     string `json:"name"      validate:"min=1,max=100"`
		Code     string `json:"code"      validate:"len=5"`
		Greater  string `json:"greater"   validate:"gt=3"`
		Less     string `json:"less"      validate:"lt=10"`
		OneOf    string `json:"one_of"    validate:"oneof=a b c"`
		Equals   string `json:"equals"    validate:"eq=hello"`
		NotEqual string `json:"not_equal" validate:"ne=world"`
	}

	s, err := jsonschema.GenerateFor[Form](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string","minLength":1,"maxLength":100},
			"code":{"type":"string","minLength":5,"maxLength":5},
			"greater":{"type":"string","minLength":4},
			"less":{"type":"string","maxLength":9},
			"one_of":{"type":"string","enum":["a","b","c"]},
			"equals":{"type":"string","const":"hello"},
			"not_equal":{"type":"string","not":{"const":"world"}}
		},
		"required":["name","code","greater","less","one_of","equals","not_equal"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_NumericConstraints(t *testing.T) {
	t.Parallel()

	type Ranges struct {
		MinMax int     `json:"min_max" validate:"gte=0,lte=100"`
		GT     int     `json:"gt"      validate:"gt=5"`
		LT     float64 `json:"lt"      validate:"lt=10"`
		OneOf  int     `json:"one_of"  validate:"oneof=1 2 3"`
		Eq     int     `json:"eq"      validate:"eq=42"`
		Ne     int     `json:"ne"      validate:"ne=0"`
	}

	s, err := jsonschema.GenerateFor[Ranges](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"min_max":{"type":"integer","minimum":0,"maximum":100},
			"gt":{"type":"integer","exclusiveMinimum":5},
			"lt":{"type":"number","exclusiveMaximum":10},
			"one_of":{"type":"integer","enum":[1,2,3]},
			"eq":{"type":"integer","const":42},
			"ne":{"type":"integer","not":{"const":0}}
		},
		"required":["min_max","gt","lt","one_of","eq","ne"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_SliceConstraints(t *testing.T) {
	t.Parallel()

	type Lists struct {
		Tags     []string `json:"tags"      validate:"min=1,max=10"`
		Unique   []int    `json:"unique"    validate:"unique"`
		FixedLen []string `json:"fixed_len" validate:"len=3"`
	}

	s, err := jsonschema.GenerateFor[Lists](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"tags":{"type":["null","array"],"items":{"type":"string"},"minItems":1,"maxItems":10},
			"unique":{"type":["null","array"],"items":{"type":"integer"},"uniqueItems":true},
			"fixed_len":{"type":["null","array"],"items":{"type":"string"},"minItems":3,"maxItems":3}
		},
		"required":["tags","unique","fixed_len"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_MapConstraints(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"min=1,max=5"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"labels":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":1,"maxProperties":5}
		},
		"required":["labels"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_FormatTags(t *testing.T) {
	t.Parallel()

	type Contact struct {
		Email    string `json:"email"    validate:"email"`
		URL      string `json:"url"      validate:"url"`
		URI      string `json:"uri"      validate:"uri"`
		UUID     string `json:"uuid"     validate:"uuid"`
		IPv4     string `json:"ipv4"     validate:"ipv4"`
		IPv6     string `json:"ipv6"     validate:"ipv6"`
		Hostname string `json:"hostname" validate:"hostname"`
	}

	s, err := jsonschema.GenerateFor[Contact](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "email", s.Properties["email"].Format)
	assert.Equal(t, "uri", s.Properties["url"].Format)
	assert.Equal(t, "uri-reference", s.Properties["uri"].Format)
	assert.Equal(t, "uuid", s.Properties["uuid"].Format)
	assert.Equal(t, "ipv4", s.Properties["ipv4"].Format)
	assert.Equal(t, "ipv6", s.Properties["ipv6"].Format)
	assert.Equal(t, "hostname", s.Properties["hostname"].Format)
}

func TestValidateInterpreter_PatternTags(t *testing.T) {
	t.Parallel()

	type Patterns struct {
		Alpha    string `json:"alpha"    validate:"alpha"`
		AlphaNum string `json:"alphanum" validate:"alphanum"`
		Numeric  string `json:"numeric"  validate:"numeric"`
		Number   string `json:"number"   validate:"number"`
		ASCII    string `json:"ascii"    validate:"ascii"`
	}

	s, err := jsonschema.GenerateFor[Patterns](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, `^[a-zA-Z]+$`, s.Properties["alpha"].Pattern)
	assert.Equal(t, `^[a-zA-Z0-9]+$`, s.Properties["alphanum"].Pattern)
	assert.Equal(t, `^[-+]?[0-9]+(?:\.[0-9]+)?$`, s.Properties["numeric"].Pattern)
	assert.Equal(t, `^[0-9]+$`, s.Properties["number"].Pattern)
	assert.Equal(t, "^[\\x00-\\x7F]*$", s.Properties["ascii"].Pattern)
}

func TestValidateInterpreter_ContentTags(t *testing.T) {
	t.Parallel()

	type Content struct {
		Data    string `json:"data"    validate:"json"`
		Encoded string `json:"encoded" validate:"base64"`
	}

	s, err := jsonschema.GenerateFor[Content](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "application/json", s.Properties["data"].ContentMediaType)
	assert.Equal(t, "base64", s.Properties["encoded"].ContentEncoding)
}

func TestValidateInterpreter_RequiredOnOmitempty(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name string `json:"name,omitempty" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// validate:"required" should add to required even with omitempty.
	assert.Contains(t, s.Required, "name")
	// Non-pointer string: minLength=1.
	assert.Equal(t, jsonschema.Ptr(1), s.Properties["name"].MinLength)
}

func TestValidateInterpreter_RequiredOnPointer(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name *string `json:"name" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Pointer: required but no minLength.
	assert.Contains(t, s.Required, "name")
	assert.Nil(t, s.Properties["name"].MinLength)
}

func TestValidateInterpreter_Dive(t *testing.T) {
	t.Parallel()

	type Config struct {
		Tags []string `json:"tags" validate:"min=1,dive,min=3"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"tags":{
				"type":["null","array"],
				"items":{"type":"string","minLength":3},
				"minItems":1
			}
		},
		"required":["tags"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_NestedDive(t *testing.T) {
	t.Parallel()

	type Config struct {
		Matrix [][]string `json:"matrix" validate:"min=1,dive,max=5,dive,min=3"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"matrix":{
				"type":["null","array"],
				"minItems":1,
				"items":{
					"type":["null","array"],
					"maxItems":5,
					"items":{
						"type":"string",
						"minLength":3
					}
				}
			}
		},
		"required":["matrix"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_DivePointerElement(t *testing.T) {
	t.Parallel()

	type Config struct {
		Values []*int `json:"values" validate:"min=1,dive,gte=0"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Constraints after dive apply to the element's schema. The element is a
	// pointer, so its nullability is expressed via anyOf with a null branch.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"values":{
				"type":["null","array"],
				"minItems":1,
				"items":{
					"anyOf":[{"type":"integer"},{"type":"null"}],
					"minimum":0
				}
			}
		},
		"required":["values"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_DiveMap(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"min=1,dive,min=1"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Dive on map descends into additionalProperties (value type).
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"labels":{
				"type":["null","object"],
				"additionalProperties":{"type":"string","minLength":1},
				"minProperties":1
			}
		},
		"required":["labels"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_OrOperator(t *testing.T) {
	t.Parallel()

	type Config struct {
		Value int `json:"value" validate:"min=1|max=10"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Only first group before | is used.
	assert.Equal(t, jsonschema.Ptr(float64(1)), s.Properties["value"].Minimum)
	assert.Nil(t, s.Properties["value"].Maximum)
}

func TestValidateInterpreter_CrossFieldIgnored(t *testing.T) {
	t.Parallel()

	type Config struct {
		Start string `json:"start" validate:"required"`
		End   string `json:"end"   validate:"required,gtfield=Start"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Gtfield should be silently ignored.
	assert.Contains(t, s.Required, "start")
	assert.Contains(t, s.Required, "end")
}

func TestValidateInterpreter_MapKeyValidatorsIgnored(t *testing.T) {
	t.Parallel()

	type Config struct {
		Data map[string]string `json:"data" validate:"min=1,dive,keys,min=3,endkeys,min=2"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Keys/endkeys and everything between them should be silently ignored.
	// Min=1 applies to the map (minProperties), dive descends into
	// additionalProperties (value type), keys...endkeys block (including
	// min=3) is skipped, min=2 applies to value's minLength.
	assert.Equal(t, jsonschema.Ptr(1), s.Properties["data"].MinProperties)
	assert.Equal(t, jsonschema.Ptr(2), s.Properties["data"].AdditionalProperties.MinLength)
}

func TestValidateInterpreter_ExclusiveCollectionConstraints(t *testing.T) {
	t.Parallel()

	type Config struct {
		SliceGT []string          `json:"slice_gt" validate:"gt=2"`
		SliceLT []string          `json:"slice_lt" validate:"lt=10"`
		MapGT   map[string]string `json:"map_gt"   validate:"gt=0"`
		MapLT   map[string]string `json:"map_lt"   validate:"lt=5"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Gt=N → minItems: N+1 (exclusive), lt=N → maxItems: N-1 (exclusive).
	assert.Equal(t, jsonschema.Ptr(3), s.Properties["slice_gt"].MinItems)
	assert.Equal(t, jsonschema.Ptr(9), s.Properties["slice_lt"].MaxItems)
	// Gt=N → minProperties: N+1, lt=N → maxProperties: N-1.
	assert.Equal(t, jsonschema.Ptr(1), s.Properties["map_gt"].MinProperties)
	assert.Equal(t, jsonschema.Ptr(4), s.Properties["map_lt"].MaxProperties)
}

func TestValidateInterpreter_MapLenConstraint(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"len=3"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Len=N → both minProperties and maxProperties.
	assert.Equal(t, jsonschema.Ptr(3), s.Properties["labels"].MinProperties)
	assert.Equal(t, jsonschema.Ptr(3), s.Properties["labels"].MaxProperties)
}

func TestValidateInterpreter_RequiredOnPointerSlice(t *testing.T) {
	t.Parallel()

	type Config struct {
		Tags *[]string `json:"tags" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Pointer: required but no minItems (pointer means "must be non-nil").
	assert.Contains(t, s.Required, "tags")
	assert.Nil(t, s.Properties["tags"].MinItems)
}

func TestValidateInterpreter_RequiredOnPointerMap(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels *map[string]string `json:"labels" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Pointer: required but no minProperties.
	assert.Contains(t, s.Required, "labels")
	assert.Nil(t, s.Properties["labels"].MinProperties)
}

func TestValidateInterpreter_RequiredOnNonPointerSlice(t *testing.T) {
	t.Parallel()

	type Config struct {
		Tags []string `json:"tags" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Non-pointer slice: required + minItems=1.
	assert.Contains(t, s.Required, "tags")
	assert.Equal(t, jsonschema.Ptr(1), s.Properties["tags"].MinItems)
}

func TestValidateInterpreter_RequiredOnNonPointerMap(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Non-pointer map: required + minProperties=1.
	assert.Contains(t, s.Required, "labels")
	assert.Equal(t, jsonschema.Ptr(1), s.Properties["labels"].MinProperties)
}

func TestParseNumericValueTruncatesLargeIntegers(t *testing.T) {
	t.Parallel()

	// ParseNumericValue uses ParseFloat then int(n), losing precision for large ints.
	type Large struct {
		Value int64 `json:"value" validate:"eq=9007199254740993"`
	}

	s, err := jsonschema.GenerateFor[Large](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["value"]
	require.NotNil(t, prop)

	// 9007199254740993 (2^53 + 1) should not lose precision.
	require.NotNil(t, prop.Const)
	assert.Equal(t, int(9007199254740993), *prop.Const,
		"large int64 const should not lose precision")
}

func TestNumericOneOfProducesIntNotFloat64(t *testing.T) {
	t.Parallel()

	// validate:"eq=42" on int produces int(42), while jsonschema:"const=42" produces float64(42).
	type IntField struct {
		Value int `json:"value" validate:"eq=42"`
	}

	type FloatField struct {
		Value int `json:"value" jsonschema:"const=42"`
	}

	sInt, err := jsonschema.GenerateFor[IntField](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	sFloat, err := jsonschema.GenerateFor[FloatField]()
	require.NoError(t, err)

	// Both should produce the same const type.
	intConst := sInt.Properties["value"].Const
	floatConst := sFloat.Properties["value"].Const

	require.NotNil(t, intConst)
	require.NotNil(t, floatConst)

	assert.IsType(t, *floatConst, *intConst,
		"validate and jsonschema tags should produce same const type")
}

func TestApplyDiveSilentlyReturnsNilWhenItemsNil(t *testing.T) {
	t.Parallel()

	// When a JSONSchemaProvider returns a schema without items,
	// all dive constraints are silently lost.
	s := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"items": {
				// Items is nil — no sub-schema to dive into.
				Types: []string{"null", "array"},
			},
		},
	}

	interp := validate.NewInterpreter()
	err := interp.Interpret("dive,min=1", jsonschema.FieldContext{
		Type:   reflect.TypeFor[[]string](),
		Schema: s.Properties["items"],
		Parent: s,
		Name:   "items",
	})

	// Should produce an error or warning, not silently discard constraints.
	require.Error(t, err,
		"dive into nil Items should produce an error, not silently discard constraints")
}

func TestUnrecognizedValidateTagsSilentlyConsumed(t *testing.T) {
	t.Parallel()

	// Typos like validate:"emial" are silently ignored.
	type MyType struct {
		Email string `json:"email" validate:"emial"` //nolint:govet // intentional typo
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	// Should produce an error for unrecognized tag.
	require.Error(t, err,
		"unrecognized validate tag 'emial' should produce an error")
}

func TestCollectionGtLtProducesNegativeBounds(t *testing.T) {
	t.Parallel()

	// Lt=0 on a collection computes n-- = -1, producing MaxItems: -1.
	type MyType struct {
		Items []string `json:"items" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxItems, "lt=0 on collection should set maxItems")

	// MaxItems MUST be a non-negative integer per JSON Schema spec (Section 6.4.1).
	assert.GreaterOrEqual(t, *prop.MaxItems, 0,
		"maxItems MUST be non-negative per JSON Schema spec")
}

func TestStringGtLtProducesNegativeLength(t *testing.T) {
	t.Parallel()

	// Lt=0 on a string produces MaxLength: -1.
	type MyType struct {
		Name string `json:"name" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxLength, "lt=0 on string should set maxLength")

	// MaxLength MUST be a non-negative integer per JSON Schema spec (Section 6.3.1).
	assert.GreaterOrEqual(t, *prop.MaxLength, 0,
		"maxLength MUST be non-negative per JSON Schema spec")
}

func TestRequiredOnNumericTypesIsNoOp(t *testing.T) {
	t.Parallel()

	// In go-playground/validator, required on int means "must not be zero".
	// The interpreter adds to required array but no type-specific constraint.
	type MyType struct {
		Count int `json:"count" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)

	// Should have not:{const:0} or similar to reject zero values.
	assert.NotNil(t, prop.Not,
		"required on int should produce a not-zero constraint")
}

func TestLenOnNumericTypesIsNoOp(t *testing.T) {
	t.Parallel()

	// In go-playground/validator, len=N on a numeric type means value must equal N.
	type MyType struct {
		Count int `json:"count" validate:"len=5"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)

	// Should produce const:5 or min:5,max:5.
	assert.NotNil(t, prop.Const,
		"len=5 on int should produce a const constraint")
}

func TestOneOfEmptyValueProducesUnsatisfiableEnum(t *testing.T) {
	t.Parallel()

	// validate:"oneof=" with no values produces enum:[] which is unsatisfiable.
	type MyType struct {
		Status string `json:"status" validate:"oneof="`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	// Should produce an error for empty oneof.
	require.Error(t, err,
		"oneof= with no values should produce an error")
}

func TestUniqueOnNonCollectionSetsUniqueItems(t *testing.T) {
	t.Parallel()

	// Unique unconditionally sets uniqueItems regardless of field type.
	type MyType struct {
		Name string `json:"name" validate:"unique"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)

	// UniqueItems on a string field is meaningless; should only be set on arrays.
	assert.False(t, prop.UniqueItems,
		"uniqueItems should not be set on non-collection types")
}

func TestEqOnCollectionProducesStringConst(t *testing.T) {
	t.Parallel()

	// Eq on a slice should mean "length equals N", not produce a string const.
	type MyType struct {
		Items []string `json:"items" validate:"eq=5"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)

	// Should produce minItems:5, maxItems:5, not const:"5".
	assert.Nil(t, prop.Const, "eq=5 on slice should not produce a string const")
	assert.NotNil(t, prop.MinItems, "eq=5 on slice should set minItems")
	assert.NotNil(t, prop.MaxItems, "eq=5 on slice should set maxItems")
}

func TestEqOnBoolProducesStringConst(t *testing.T) {
	t.Parallel()

	// validate:"eq=true" on bool produces {"const": "true"} (string) instead of {"const": true}.
	type MyType struct {
		Active bool `json:"active" validate:"eq=true"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["active"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.Const)

	assert.Equal(t, true, *prop.Const,
		"eq=true on bool should produce boolean const, not string")
}

func TestOneOfOnBoolProducesStringEnum(t *testing.T) {
	t.Parallel()

	type MyType struct {
		Active bool `json:"active" validate:"oneof=true false"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["active"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.Enum)

	// Should be [true, false] (booleans), not ["true", "false"] (strings).
	for _, v := range prop.Enum {
		assert.IsType(t, true, v,
			"oneof on bool should produce boolean enum values, not strings")
	}
}

func TestRequiredOnBoolIsNoOp(t *testing.T) {
	t.Parallel()

	// In go-playground/validator, required on bool means "must be true".
	type MyType struct {
		Accepted bool `json:"accepted" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["accepted"]
	require.NotNil(t, prop)

	// Should produce const:true.
	assert.NotNil(t, prop.Const,
		"required on bool should produce a const:true constraint")
}

func TestTrailingDiveIsNoOp(t *testing.T) {
	t.Parallel()

	// A trailing dive with no subsequent validators should error.
	type MyType struct {
		Items []string `json:"items" validate:"dive"`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	// In go-playground/validator, trailing dive is an error.
	require.Error(t, err,
		"trailing dive with no subsequent constraints should produce an error")
}

func TestMissingEndkeysSwallowsConstraints(t *testing.T) {
	t.Parallel()

	// Without endkeys, inKeys stays true and all remaining constraints are lost.
	// The tag has keys,min=1 (key constraint),min=3 (intended value constraint) but
	// no endkeys, so min=3 is swallowed by the inKeys loop.
	type MyType struct {
		Data map[string]string `json:"data" validate:"dive,keys,min=1,min=3"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["data"]
	require.NotNil(t, prop)

	// The min=3 after the missing endkeys should apply to value's minLength,
	// but instead it is silently swallowed because inKeys stays true.
	require.NotNil(t, prop.AdditionalProperties,
		"map should have additionalProperties schema")
	assert.NotNil(t, prop.AdditionalProperties.MinLength,
		"min=3 after missing endkeys should not be silently swallowed")
}

func TestFormatOverwritesJsonSchemaTag(t *testing.T) {
	t.Parallel()

	// Validate interpreter runs after jsonschema tag, so it overwrites.
	type MyType struct {
		Email string `json:"email" jsonschema:"format=date-time" validate:"email"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["email"]
	require.NotNil(t, prop)

	// The explicit jsonschema tag should take precedence.
	assert.Equal(t, "date-time", prop.Format,
		"explicit jsonschema tag format should take precedence over validate tag")
}

func TestMinMaxOnUnsupportedTypesNoOp(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type MyType struct {
		Data Inner `json:"data" validate:"min=1"`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	// Should produce an error for unsupported type, not silently ignore.
	require.Error(t, err,
		"min on struct type should produce an error")
}

func TestValidateInterpreter_RequiredPreservesStrongerBound(t *testing.T) {
	t.Parallel()

	// validator rules in a single tag compose conjunctively and are
	// order-independent, so "required" must never lower a stronger min/len bound
	// set by another part of the tag, whatever the order.
	type Form struct {
		MinThenReq string            `json:"min_then_req" validate:"min=5,required"`
		ReqThenMin string            `json:"req_then_min" validate:"required,min=5"`
		Tags       []string          `json:"tags"         validate:"min=3,required"`
		Labels     map[string]string `json:"labels"       validate:"required,min=2"`
		Bare       string            `json:"bare"         validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Form](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"min_then_req":{"type":"string","minLength":5},
			"req_then_min":{"type":"string","minLength":5},
			"tags":{"type":["null","array"],"items":{"type":"string"},"minItems":3},
			"labels":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":2},
			"bare":{"type":"string","minLength":1}
		},
		"required":["min_then_req","req_then_min","tags","labels","bare"],
		"additionalProperties":false
	}`, string(got))
}
