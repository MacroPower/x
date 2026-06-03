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

func TestNumericConstPreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	// Integer kinds parse with strconv.ParseInt, so an eq value beyond
	// float64's exact integer range keeps full precision.
	type Large struct {
		Value int64 `json:"value" validate:"eq=9007199254740993"`
	}

	s, err := jsonschema.GenerateFor[Large](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["value"]
	require.NotNil(t, prop)

	// 9007199254740993 (2^53 + 1) keeps full precision.
	require.NotNil(t, prop.Const)
	assert.Equal(t, int(9007199254740993), *prop.Const,
		"large int64 const keeps full precision")
}

func TestNumericConstPreservesLargeUnsignedIntegers(t *testing.T) {
	t.Parallel()

	// Unsigned kinds parse with strconv.ParseUint, so an eq value above
	// math.MaxInt64 is representable rather than overflowing int64.
	type Large struct {
		Value uint64 `json:"value" validate:"eq=18446744073709551615"`
	}

	s, err := jsonschema.GenerateFor[Large](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["value"]
	require.NotNil(t, prop)

	// Math.MaxUint64 round-trips as a uint64 const instead of overflowing.
	require.NotNil(t, prop.Const)
	assert.Equal(t, uint64(18446744073709551615), *prop.Const)
}

func TestValidateAndJSONSchemaTagsProduceSameConstType(t *testing.T) {
	t.Parallel()

	// The validate eq tag and the jsonschema const tag both parse an integer
	// field as a Go int, so the two dialects yield the same const type.
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

	intConst := sInt.Properties["value"].Const
	floatConst := sFloat.Properties["value"].Const

	require.NotNil(t, intConst)
	require.NotNil(t, floatConst)

	assert.IsType(t, *floatConst, *intConst,
		"validate and jsonschema tags produce the same const type")
}

func TestApplyDiveErrorsWhenItemsNil(t *testing.T) {
	t.Parallel()

	// Diving into a schema that has no element sub-schema returns an error
	// rather than discarding the dive constraints.
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

	require.Error(t, err,
		"dive into nil Items returns an error rather than discarding the constraints")
}

func TestUnrecognizedValidateTagErrors(t *testing.T) {
	t.Parallel()

	// A typo like validate:"emial" is an unrecognized validator and surfaces as
	// an error so the mistake does not pass unnoticed.
	type MyType struct {
		Email string `json:"email" validate:"emial"` //nolint:govet // intentional typo
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.Error(t, err,
		"unrecognized validate tag 'emial' produces an error")
}

func TestCollectionGtLtClampsBoundsToZero(t *testing.T) {
	t.Parallel()

	// Lt=0 on a collection sets an exclusive upper bound of one below zero,
	// which is clamped to a non-negative maxItems as JSON Schema requires.
	type MyType struct {
		Items []string `json:"items" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxItems, "lt=0 on collection sets maxItems")

	// MaxItems must be a non-negative integer per the JSON Schema spec.
	assert.GreaterOrEqual(t, *prop.MaxItems, 0,
		"maxItems must be non-negative per JSON Schema spec")
}

func TestStringGtLtClampsLengthToZero(t *testing.T) {
	t.Parallel()

	// Lt=0 on a string sets an exclusive upper bound of one below zero, which is
	// clamped to a non-negative maxLength as JSON Schema requires.
	type MyType struct {
		Name string `json:"name" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxLength, "lt=0 on string sets maxLength")

	// MaxLength must be a non-negative integer per the JSON Schema spec.
	assert.GreaterOrEqual(t, *prop.MaxLength, 0,
		"maxLength must be non-negative per JSON Schema spec")
}

func TestRequiredOnNumericForbidsZero(t *testing.T) {
	t.Parallel()

	// Go-playground/validator treats required on an int as "must not be zero",
	// so the schema carries a not-zero constraint.
	type MyType struct {
		Count int `json:"count" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)

	// not:{const:0} (or an enum) rejects the zero value.
	assert.NotNil(t, prop.Not,
		"required on int produces a not-zero constraint")
}

func TestLenOnNumericProducesConst(t *testing.T) {
	t.Parallel()

	// Go-playground/validator treats len=N on a numeric type as "value equals N",
	// so the schema carries a const constraint.
	type MyType struct {
		Count int `json:"count" validate:"len=5"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)

	// Len=5 fixes the value with const:5.
	assert.NotNil(t, prop.Const,
		"len=5 on int produces a const constraint")
}

func TestOneOfEmptyValueErrors(t *testing.T) {
	t.Parallel()

	// validate:"oneof=" carries no values, so rather than emitting an
	// unsatisfiable empty enum the interpreter returns an error.
	type MyType struct {
		Status string `json:"status" validate:"oneof="`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.Error(t, err,
		"oneof= with no values produces an error")
}

func TestUniqueOnNonCollectionLeavesUniqueItemsUnset(t *testing.T) {
	t.Parallel()

	// UniqueItems is only meaningful for arrays, so unique on a string field
	// leaves it unset.
	type MyType struct {
		Name string `json:"name" validate:"unique"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)

	assert.False(t, prop.UniqueItems,
		"uniqueItems is not set on non-collection types")
}

func TestEqOnCollectionProducesLengthBounds(t *testing.T) {
	t.Parallel()

	// Eq=N on a slice means "length equals N", so it sets matching minItems and
	// maxItems rather than a const.
	type MyType struct {
		Items []string `json:"items" validate:"eq=5"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)

	// Eq=5 yields minItems:5, maxItems:5 and no const.
	assert.Nil(t, prop.Const, "eq=5 on slice does not produce a const")
	assert.NotNil(t, prop.MinItems, "eq=5 on slice sets minItems")
	assert.NotNil(t, prop.MaxItems, "eq=5 on slice sets maxItems")
}

func TestEqOnBoolProducesBooleanConst(t *testing.T) {
	t.Parallel()

	// Eq=true on a bool field sets const to the boolean value true.
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
		"eq=true on bool produces a boolean const")
}

func TestOneOfOnBoolProducesBooleanEnum(t *testing.T) {
	t.Parallel()

	// Oneof on a bool field parses each value into a boolean, so the enum holds
	// [true, false] rather than the string forms.
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

	for _, v := range prop.Enum {
		assert.IsType(t, true, v,
			"oneof on bool produces boolean enum values")
	}
}

func TestRequiredOnBoolProducesConstTrue(t *testing.T) {
	t.Parallel()

	// Go-playground/validator treats required on a bool as "must be true", so
	// the schema carries a const constraint.
	type MyType struct {
		Accepted bool `json:"accepted" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["accepted"]
	require.NotNil(t, prop)

	// Required fixes the value with const:true.
	assert.NotNil(t, prop.Const,
		"required on bool produces a const:true constraint")
}

func TestTrailingDiveErrors(t *testing.T) {
	t.Parallel()

	// A trailing dive has no element constraints to apply, so it is an error,
	// matching go-playground/validator.
	type MyType struct {
		Items []string `json:"items" validate:"dive"`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.Error(t, err,
		"trailing dive with no subsequent constraints produces an error")
}

func TestMissingEndkeysStillAppliesValueConstraints(t *testing.T) {
	t.Parallel()

	// A keys marker without a matching endkeys is malformed. Rather than
	// swallowing every later constraint, the keys marker is ignored so the
	// remaining constraints still apply to the value schema. Here min=3 reaches
	// the value's minLength.
	type MyType struct {
		Data map[string]string `json:"data" validate:"dive,keys,min=1,min=3"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["data"]
	require.NotNil(t, prop)

	require.NotNil(t, prop.AdditionalProperties,
		"map has an additionalProperties schema")
	assert.NotNil(t, prop.AdditionalProperties.MinLength,
		"min=3 after the unmatched keys marker applies to the value's minLength")
}

func TestExplicitJSONSchemaTagTakesPrecedenceOverValidate(t *testing.T) {
	t.Parallel()

	// An explicit jsonschema tag wins over the format the validate interpreter
	// would otherwise derive, so the date-time format set by the jsonschema tag
	// survives alongside the email validator.
	type MyType struct {
		Email string `json:"email" jsonschema:"format=date-time" validate:"email"`
	}

	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["email"]
	require.NotNil(t, prop)

	assert.Equal(t, "date-time", prop.Format,
		"explicit jsonschema tag format takes precedence over the validate tag")
}

func TestMinMaxOnUnsupportedTypeErrors(t *testing.T) {
	t.Parallel()

	// Min/max only apply to strings, numbers, and collections, so a struct field
	// produces an error rather than being ignored.
	type Inner struct {
		Value string `json:"value"`
	}

	type MyType struct {
		Data Inner `json:"data" validate:"min=1"`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.Error(t, err,
		"min on a struct type produces an error")
}

func TestValidateInterpreter_RequiredPreservesStrongerBound(t *testing.T) {
	t.Parallel()

	// Validator rules in a single tag compose conjunctively and are
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

func TestValidateInterpreter_CollectionNe(t *testing.T) {
	t.Parallel()

	// Ne=N on a collection forbids the exact length via a not subschema: minItems
	// and maxItems for slices, minProperties and maxProperties for maps.
	type Lists struct {
		Tags   []string          `json:"tags"   validate:"ne=3"`
		Labels map[string]string `json:"labels" validate:"ne=2"`
	}

	s, err := jsonschema.GenerateFor[Lists](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	tags := s.Properties["tags"]
	require.NotNil(t, tags.Not)
	assert.Equal(t, jsonschema.Ptr(3), tags.Not.MinItems)
	assert.Equal(t, jsonschema.Ptr(3), tags.Not.MaxItems)

	labels := s.Properties["labels"]
	require.NotNil(t, labels.Not)
	assert.Equal(t, jsonschema.Ptr(2), labels.Not.MinProperties)
	assert.Equal(t, jsonschema.Ptr(2), labels.Not.MaxProperties)
}

func TestValidateInterpreter_CollectionNeComposesWithAllOf(t *testing.T) {
	t.Parallel()

	// A second ne=N on the same collection cannot ride on the first not, so the
	// existing not moves under allOf and each forbidden length gets its own not.
	type Lists struct {
		Tags []string `json:"tags" validate:"ne=2,ne=3"`
	}

	s, err := jsonschema.GenerateFor[Lists](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	tags := s.Properties["tags"]
	assert.Nil(t, tags.Not, "the first not is moved under allOf")
	require.Len(t, tags.AllOf, 2)
	require.NotNil(t, tags.AllOf[0].Not)
	require.NotNil(t, tags.AllOf[1].Not)
	assert.Equal(t, jsonschema.Ptr(2), tags.AllOf[0].Not.MinItems)
	assert.Equal(t, jsonschema.Ptr(3), tags.AllOf[1].Not.MinItems)
}

func TestValidateInterpreter_DiveIntoFixedArray(t *testing.T) {
	t.Parallel()

	// A fixed array's element schemas live in prefixItems under Draft 2020-12 and
	// in the items-array form under Draft-07; dive applies constraints to each.
	type Arr struct {
		Codes [3]string `json:"codes" validate:"dive,min=2"`
	}

	s2020, err := jsonschema.GenerateFor[Arr](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	codes := s2020.Properties["codes"]
	require.NotEmpty(t, codes.PrefixItems, "draft 2020-12 fixed arrays use prefixItems")

	for _, item := range codes.PrefixItems {
		assert.Equal(t, jsonschema.Ptr(2), item.MinLength)
	}

	s7, err := jsonschema.GenerateFor[Arr](
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	codes7 := s7.Properties["codes"]
	require.NotEmpty(t, codes7.ItemsArray, "draft-07 fixed arrays use the items-array form")

	for _, item := range codes7.ItemsArray {
		assert.Equal(t, jsonschema.Ptr(2), item.MinLength)
	}
}

func TestValidateInterpreter_DiveIntoByteSliceIsNoOp(t *testing.T) {
	t.Parallel()

	// A []byte field marshals to a single base64 string with no per-element
	// schema, so diving into it is a no-op rather than a generation error.
	type B struct {
		Data []byte `json:"data" validate:"dive,min=1"`
	}

	s, err := jsonschema.GenerateFor[B](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "base64", s.Properties["data"].ContentEncoding)
}

func TestValidateInterpreter_StringKeywordOnByteSlice(t *testing.T) {
	t.Parallel()

	// A []byte field's schema is a base64 string, so a string-only content tag
	// applies even though the Go kind is not string.
	type Doc struct {
		Blob []byte `json:"blob" validate:"base64"`
	}

	s, err := jsonschema.GenerateFor[Doc](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)
	assert.Equal(t, "base64", s.Properties["blob"].ContentEncoding)

	// On a non-string kind whose schema is not a string, the same tag is
	// rejected rather than silently stamped onto an integer schema.
	type Bad struct {
		Count int `json:"count" validate:"base64"`
	}

	_, err = jsonschema.GenerateFor[Bad](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.Error(t, err)
}

func TestValidateInterpreter_PatternKeywordDoesNotOverwrite(t *testing.T) {
	t.Parallel()

	// An explicit jsonschema pattern is preserved; a validate pattern tag only
	// fills pattern when it is not already set.
	type P struct {
		Code string `json:"code" jsonschema:"pattern=^[0-9]{4}$" validate:"alpha"`
	}

	s, err := jsonschema.GenerateFor[P](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "^[0-9]{4}$", s.Properties["code"].Pattern,
		"explicit jsonschema pattern must win over the validate alpha tag")
}
