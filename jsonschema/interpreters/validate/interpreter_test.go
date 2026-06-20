package validate_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
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

func TestValidateInterpreter_StringCoercedValueConstraints(t *testing.T) {
	t.Parallel()

	// A json:",string" field serializes its numeric or bool value as a quoted
	// string, so the generated schema is a string. The eq/ne/oneof family must
	// compare against that serialized form (a string const/enum), not the
	// numeric or bool value, or the constraint is unsatisfiable against the
	// quoted instance.
	type Form struct {
		Count   int  `json:"count,string"    validate:"eq=5"`
		Choice  int  `json:"choice,string"   validate:"oneof=1 2 3"`
		NotZero int  `json:"not_zero,string" validate:"ne=0"`
		Flag    bool `json:"flag,string"     validate:"eq=true"`
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
			"count":{"type":"string","const":"5"},
			"choice":{"type":"string","enum":["1","2","3"]},
			"not_zero":{"type":"string","not":{"const":"0"}},
			"flag":{"type":"string","const":"true"}
		},
		"required":["count","choice","not_zero","flag"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(),
		[]byte(`{"count":"5","choice":"2","not_zero":"3","flag":"true"}`)),
		"the serialized string form satisfies the coerced constraints")
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

	s, err := jsonschema.GenerateFor[Ranges](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

// TestValidateInterpreter_OneOfOnSequenceFields pins that oneof on a slice or
// array field constrains each element, mirroring the jsonschema tag's enum
// behavior for sequence fields.
func TestValidateInterpreter_OneOfOnSequenceFields(t *testing.T) {
	t.Parallel()

	type Lists struct {
		Days  []string  `json:"days"  validate:"oneof=monday tuesday wednesday"`
		Codes []int     `json:"codes" validate:"oneof=1 2 3"`
		Pair  [2]string `json:"pair"  validate:"oneof=a b"`
	}

	s, err := jsonschema.GenerateFor[Lists](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"days":{"type":["null","array"],"items":{"type":"string","enum":["monday","tuesday","wednesday"]}},
			"codes":{"type":["null","array"],"items":{"type":"integer","enum":[1,2,3]}},
			"pair":{
				"type":"array",
				"minItems":2,"maxItems":2,
				"prefixItems":[
					{"type":"string","enum":["a","b"]},
					{"type":"string","enum":["a","b"]}
				]
			}
		},
		"required":["days","codes","pair"],
		"additionalProperties":false
	}`, string(got))
}

// TestValidateInterpreter_OneOfOnByteSlice pins that oneof on a []byte field is
// rejected: the field encodes as a single base64 string, so there is no item
// schema to constrain and silently dropping the rule would be misleading.
func TestValidateInterpreter_OneOfOnByteSlice(t *testing.T) {
	t.Parallel()

	type Data struct {
		Raw []byte `json:"raw" validate:"oneof=a b"`
	}

	_, err := jsonschema.GenerateFor[Data](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no item schema")
}

func TestValidateInterpreter_SliceConstraints(t *testing.T) {
	t.Parallel()

	type Lists struct {
		Tags     []string `json:"tags"      validate:"min=1,max=10"`
		Unique   []int    `json:"unique"    validate:"unique"`
		FixedLen []string `json:"fixed_len" validate:"len=3"`
	}

	s, err := jsonschema.GenerateFor[Lists](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Contact](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Patterns](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Content](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// validate:"required" should add to required even with omitempty.
	assert.Contains(t, s.Required, "name")
	// Non-pointer string: minLength=1.
	assert.Equal(t, new(1), s.Properties["name"].MinLength)
}

func TestValidateInterpreter_RequiredOnPointer(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name *string `json:"name" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

// TestValidateInterpreter_OneOfOnPointerElement pins that oneof on a slice of
// nullable pointers lands the enum on the element's value branch, not as a
// sibling of the anyOf[value, null] wrapper where it would reject a valid null
// element.
func TestValidateInterpreter_OneOfOnPointerElement(t *testing.T) {
	t.Parallel()

	type Config struct {
		Tags []*string `json:"tags" validate:"oneof=a b c"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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
				"items":{
					"anyOf":[{"type":"string","enum":["a","b","c"]},{"type":"null"}]
				}
			}
		},
		"required":["tags"],
		"additionalProperties":false
	}`, string(got))
}

// TestValidateInterpreter_DiveEqOnPointerElement pins that dive,eq on a slice of
// nullable pointers lands the const on the element's value branch rather than
// the anyOf wrapper, keeping a null element valid.
func TestValidateInterpreter_DiveEqOnPointerElement(t *testing.T) {
	t.Parallel()

	type Config struct {
		Values []*int `json:"values" validate:"dive,eq=5"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"values":{
				"type":["null","array"],
				"items":{
					"anyOf":[{"type":"integer","const":5},{"type":"null"}]
				}
			}
		},
		"required":["values"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_DiveDropsSizedIntBounds(t *testing.T) {
	t.Parallel()

	// A sized-integer element carries kind-derived minimum/maximum from its Go
	// type. Once dive,eq or dive,oneof pins the element's value, those bounds are
	// redundant; dropping them keeps dive and oneof element schemas consistent
	// with the field-level path, which drops them after interpreters run.
	type Config struct {
		Exact []int8 `json:"exact" validate:"dive,eq=5"`
		Set   []int8 `json:"set"   validate:"dive,oneof=1 2 3"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"exact":{"type":["null","array"],"items":{"type":"integer","const":5}},
			"set":{"type":["null","array"],"items":{"type":"integer","enum":[1,2,3]}}
		},
		"required":["exact","set"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_DiveMap(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"min=1,dive,min=1"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Only first group before | is used.
	assert.Equal(t, new(float64(1)), s.Properties["value"].Minimum)
	assert.Nil(t, s.Properties["value"].Maximum)
}

func TestValidateInterpreter_CrossFieldIgnored(t *testing.T) {
	t.Parallel()

	type Config struct {
		Start string `json:"start" validate:"required"`
		End   string `json:"end"   validate:"required,gtfield=Start"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Keys/endkeys and everything between them should be silently ignored.
	// Min=1 applies to the map (minProperties), dive descends into
	// additionalProperties (value type), keys...endkeys block (including
	// min=3) is skipped, min=2 applies to value's minLength.
	assert.Equal(t, new(1), s.Properties["data"].MinProperties)
	assert.Equal(t, new(2), s.Properties["data"].AdditionalProperties.MinLength)
}

func TestValidateInterpreter_ExclusiveCollectionConstraints(t *testing.T) {
	t.Parallel()

	type Config struct {
		SliceGT []string          `json:"slice_gt" validate:"gt=2"`
		SliceLT []string          `json:"slice_lt" validate:"lt=10"`
		MapGT   map[string]string `json:"map_gt"   validate:"gt=0"`
		MapLT   map[string]string `json:"map_lt"   validate:"lt=5"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Gt=N → minItems: N+1 (exclusive), lt=N → maxItems: N-1 (exclusive).
	assert.Equal(t, new(3), s.Properties["slice_gt"].MinItems)
	assert.Equal(t, new(9), s.Properties["slice_lt"].MaxItems)
	// Gt=N → minProperties: N+1, lt=N → maxProperties: N-1.
	assert.Equal(t, new(1), s.Properties["map_gt"].MinProperties)
	assert.Equal(t, new(4), s.Properties["map_lt"].MaxProperties)
}

func TestValidateInterpreter_MapLenConstraint(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"len=3"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Len=N → both minProperties and maxProperties.
	assert.Equal(t, new(3), s.Properties["labels"].MinProperties)
	assert.Equal(t, new(3), s.Properties["labels"].MaxProperties)
}

func TestValidateInterpreter_RequiredOnPointerSlice(t *testing.T) {
	t.Parallel()

	type Config struct {
		Tags *[]string `json:"tags" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Non-pointer slice: required + minItems=1.
	assert.Contains(t, s.Required, "tags")
	assert.Equal(t, new(1), s.Properties["tags"].MinItems)
}

func TestValidateInterpreter_RequiredOnNonPointerMap(t *testing.T) {
	t.Parallel()

	type Config struct {
		Labels map[string]string `json:"labels" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// Non-pointer map: required + minProperties=1.
	assert.Contains(t, s.Required, "labels")
	assert.Equal(t, new(1), s.Properties["labels"].MinProperties)
}

func TestNumericConstPreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	// Integer kinds parse with strconv.ParseInt, so an eq value beyond
	// float64's exact integer range keeps full precision.
	type Large struct {
		Value int64 `json:"value" validate:"eq=9007199254740993"`
	}

	s, err := jsonschema.GenerateFor[Large](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["value"]
	require.NotNil(t, prop)

	// 9007199254740993 (2^53 + 1) keeps full precision.
	require.NotNil(t, prop.Const)
	assert.Equal(t, int64(9007199254740993), *prop.Const,
		"large int64 const keeps full precision")
}

func TestNumericConstPreservesLargeUnsignedIntegers(t *testing.T) {
	t.Parallel()

	// Unsigned kinds parse with strconv.ParseUint, so an eq value above
	// math.MaxInt64 is representable rather than overflowing int64.
	type Large struct {
		Value uint64 `json:"value" validate:"eq=18446744073709551615"`
	}

	s, err := jsonschema.GenerateFor[Large](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	sInt, err := jsonschema.GenerateFor[IntField](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	sFloat, err := jsonschema.GenerateFor[FloatField](t.Context())
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
				// Items is nil: no sub-schema to dive into.
				Types: []string{"null", "array"},
			},
		},
	}

	interp := validate.NewInterpreter()
	err := interp.Interpret(t.Context(), jsonschema.FieldContext{
		Type:   reflect.TypeFor[[]string](),
		Schema: s.Properties["items"],
		Parent: s,
		Name:   "items",
	}, jsonschema.Tag{Key: "validate", Value: "dive,min=1"})

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

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.Error(t, err,
		"unrecognized validate tag 'emial' produces an error")
}

func TestNumericEqLenConflictRejected(t *testing.T) {
	t.Parallel()

	// Both eq=N and len=N pin a numeric field to a single value, so two rules
	// pinning different values can never both hold. The conflict is reported
	// rather than silently letting whichever rule runs last win, regardless of
	// tag order; equal values agree and are accepted.
	t.Run("eq then len conflict", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int `json:"n" validate:"eq=5,len=10"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.ErrorIs(t, err, validate.ErrConflictingConstraints)
	})

	t.Run("len then eq conflict", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int `json:"n" validate:"len=10,eq=5"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.ErrorIs(t, err, validate.ErrConflictingConstraints)
	})

	t.Run("matching values agree", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int `json:"n" validate:"eq=5,len=5"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)
		require.NotNil(t, s.Properties["n"].Const)
	})
}

func TestStringEqConflictRejected(t *testing.T) {
	t.Parallel()

	// An eq=val rule pins a string field's const, so two eq rules pinning
	// different values can never both hold. The conflict is reported rather than
	// letting whichever rule runs last win, matching the numeric and bool paths.
	t.Run("conflicting eq values", func(t *testing.T) {
		t.Parallel()

		type T struct {
			S string `json:"s" validate:"eq=foo,eq=bar"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.ErrorIs(t, err, validate.ErrConflictingConstraints)
	})

	t.Run("matching eq values agree", func(t *testing.T) {
		t.Parallel()

		type T struct {
			S string `json:"s" validate:"eq=foo,eq=foo"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)
		require.NotNil(t, s.Properties["s"].Const)
	})
}

func TestOneOfConflictsWithJSONSchemaEnum(t *testing.T) {
	t.Parallel()

	// A jsonschema enum tag and a validate oneof both fully enumerate the
	// allowed values, so two different enumerations can never both hold. The
	// conflict is reported rather than letting oneof silently overwrite the
	// tag's enum, matching the eq/const family.
	t.Run("numeric", func(t *testing.T) {
		t.Parallel()

		type T struct {
			N int `json:"n" jsonschema:"enum=1|2|3" validate:"oneof=4 5"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.ErrorIs(t, err, validate.ErrConflictingConstraints)
	})

	t.Run("string", func(t *testing.T) {
		t.Parallel()

		type T struct {
			S string `json:"s" jsonschema:"enum=a|b" validate:"oneof=c d"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.ErrorIs(t, err, validate.ErrConflictingConstraints)
	})
}

func TestCollectionLtZeroIsUnsatisfiable(t *testing.T) {
	t.Parallel()

	// Lt=0 on a collection demands a length below zero, which no array can
	// have, so go-playground rejects every value, including the empty array.
	// The schema mirrors that with an unsatisfiable minItems/maxItems range
	// rather than clamping to a permissive maxItems:0 that would accept [].
	type MyType struct {
		Items []string `json:"items" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxItems, "lt=0 on collection sets maxItems")
	require.NotNil(t, prop.MinItems, "lt=0 on collection sets a contradicting minItems")

	// Both bounds stay non-negative per the JSON Schema spec, and the floor
	// exceeds the ceiling so even the empty array is rejected.
	assert.GreaterOrEqual(t, *prop.MaxItems, 0,
		"maxItems must be non-negative per JSON Schema spec")
	assert.Greater(t, *prop.MinItems, *prop.MaxItems,
		"the range must reject every array, including the empty one")
}

func TestCollectionEqNegativeIsUnsatisfiable(t *testing.T) {
	t.Parallel()

	// An eq=-1 (like len=-1) demands a negative length, which no array can have,
	// so go-playground rejects every value including the empty array. The schema
	// mirrors that with an unsatisfiable minItems/maxItems range rather than
	// clamping a negative length to a permissive maxItems:0 that would accept [].
	type MyType struct {
		Items []string `json:"items" validate:"eq=-1"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["items"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxItems)
	require.NotNil(t, prop.MinItems)

	assert.GreaterOrEqual(t, *prop.MaxItems, 0,
		"maxItems must be non-negative per JSON Schema spec")
	assert.Greater(t, *prop.MinItems, *prop.MaxItems,
		"the range must reject every array, including the empty one")
}

func TestStringLtZeroIsUnsatisfiable(t *testing.T) {
	t.Parallel()

	// Lt=0 on a string demands a length below zero, which no string can have,
	// so go-playground rejects every value, including the empty string. The
	// schema mirrors that with an unsatisfiable minLength/maxLength range rather
	// than clamping to a permissive maxLength:0 that would accept "".
	type MyType struct {
		Name string `json:"name" validate:"lt=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.MaxLength, "lt=0 on string sets maxLength")
	require.NotNil(t, prop.MinLength, "lt=0 on string sets a contradicting minLength")

	// Both bounds stay non-negative per the JSON Schema spec, and the floor
	// exceeds the ceiling so even the empty string is rejected.
	assert.GreaterOrEqual(t, *prop.MaxLength, 0,
		"maxLength must be non-negative per JSON Schema spec")
	assert.Greater(t, *prop.MinLength, *prop.MaxLength,
		"the range must reject every string, including the empty one")
}

func TestRequiredOnNumericForbidsZero(t *testing.T) {
	t.Parallel()

	// Go-playground/validator treats required on an int as "must not be zero",
	// so the schema carries a not-zero constraint.
	type MyType struct {
		Count int `json:"count" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	// A trailing dive needs a real element constraint. A bare dive, or one
	// followed only by control or cross-field tags, has nothing to apply to the
	// element and is an error, matching go-playground/validator. A dive followed
	// by a genuine constraint is accepted.
	tests := map[string]struct {
		tag     string
		wantErr bool
	}{
		"bare dive":             {tag: "dive", wantErr: true},
		"dive then omitempty":   {tag: "dive,omitempty", wantErr: true},
		"dive then structonly":  {tag: "dive,structonly", wantErr: true},
		"dive then cross-field": {tag: "dive,eqfield=Other", wantErr: true},
		"dive then constraint":  {tag: "dive,min=1", wantErr: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			field := &jsonschema.Schema{
				Type:  "array",
				Items: &jsonschema.Schema{Type: "string"},
			}
			parent := &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"items": field},
			}

			err := validate.NewInterpreter().Interpret(t.Context(), jsonschema.FieldContext{
				Type:   reflect.TypeFor[[]string](),
				Schema: field,
				Parent: parent,
				Name:   "items",
			}, jsonschema.Tag{Key: "validate", Value: tc.tag})

			if tc.wantErr {
				require.Error(t, err,
					"a trailing dive with only control/cross-field tags is an error")
			} else {
				require.NoError(t, err)
			}
		})
	}
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	_, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

func TestValidateInterpreter_LenIntersectsBounds(t *testing.T) {
	t.Parallel()

	// A len tag pins the length/size to exactly N by intersecting with any
	// min/max or required set elsewhere in the same tag, regardless of order. An
	// incompatible len yields an unsatisfiable range (floor above ceiling), as a
	// conflicting min/max pair already does, rather than clobbering the other
	// bound.
	type Form struct {
		MaxThenLen string            `json:"max_then_len" validate:"max=3,len=5"`
		LenThenMax string            `json:"len_then_max" validate:"len=5,max=3"`
		ReqThenLen string            `json:"req_then_len" validate:"required,len=0"`
		LenThenReq string            `json:"len_then_req" validate:"len=0,required"`
		ItemsMax   []string          `json:"items_max"    validate:"max=3,len=5"`
		PropsMax   map[string]string `json:"props_max"    validate:"max=3,len=5"`
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
			"max_then_len":{"type":"string","minLength":5,"maxLength":3},
			"len_then_max":{"type":"string","minLength":5,"maxLength":3},
			"req_then_len":{"type":"string","minLength":1,"maxLength":0},
			"len_then_req":{"type":"string","minLength":1,"maxLength":0},
			"items_max":{"type":["null","array"],"items":{"type":"string"},"minItems":5,"maxItems":3},
			"props_max":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":5,"maxProperties":3}
		},
		"required":["max_then_len","len_then_max","req_then_len","len_then_req","items_max","props_max"],
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

	s, err := jsonschema.GenerateFor[Lists](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	tags := s.Properties["tags"]
	require.NotNil(t, tags.Not)
	assert.Equal(t, new(3), tags.Not.MinItems)
	assert.Equal(t, new(3), tags.Not.MaxItems)

	labels := s.Properties["labels"]
	require.NotNil(t, labels.Not)
	assert.Equal(t, new(2), labels.Not.MinProperties)
	assert.Equal(t, new(2), labels.Not.MaxProperties)
}

func TestValidateInterpreter_CollectionNeComposesWithAllOf(t *testing.T) {
	t.Parallel()

	// A second ne=N on the same collection cannot ride on the first not, so the
	// existing not moves under allOf and each forbidden length gets its own not.
	type Lists struct {
		Tags []string `json:"tags" validate:"ne=2,ne=3"`
	}

	s, err := jsonschema.GenerateFor[Lists](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	tags := s.Properties["tags"]
	assert.Nil(t, tags.Not, "the first not is moved under allOf")
	require.Len(t, tags.AllOf, 2)
	require.NotNil(t, tags.AllOf[0].Not)
	require.NotNil(t, tags.AllOf[1].Not)
	assert.Equal(t, new(2), tags.AllOf[0].Not.MinItems)
	assert.Equal(t, new(3), tags.AllOf[1].Not.MinItems)
}

func TestValidateInterpreter_DiveIntoFixedArray(t *testing.T) {
	t.Parallel()

	// A fixed array's element schemas live in prefixItems under Draft 2020-12 and
	// in the items-array form under Draft-07; dive applies constraints to each.
	type Arr struct {
		Codes [3]string `json:"codes" validate:"dive,min=2"`
	}

	s2020, err := jsonschema.GenerateFor[Arr](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	codes := s2020.Properties["codes"]
	require.NotEmpty(t, codes.PrefixItems, "draft 2020-12 fixed arrays use prefixItems")

	for _, item := range codes.PrefixItems {
		assert.Equal(t, new(2), item.MinLength)
	}

	s7, err := jsonschema.GenerateFor[Arr](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	codes7 := s7.Properties["codes"]
	require.NotEmpty(t, codes7.ItemsArray, "draft-07 fixed arrays use the items-array form")

	for _, item := range codes7.ItemsArray {
		assert.Equal(t, new(2), item.MinLength)
	}
}

func TestValidateInterpreter_DiveIntoByteSliceIsNoOp(t *testing.T) {
	t.Parallel()

	// A []byte field marshals to a single base64 string with no per-element
	// schema, so diving into it is a no-op rather than a generation error.
	type B struct {
		Data []byte `json:"data" validate:"dive,min=1"`
	}

	s, err := jsonschema.GenerateFor[B](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "base64", s.Properties["data"].ContentEncoding)
}

// TestValidateInterpreter_LengthConstraintOnByteSlice pins that a direct
// length, size, or uniqueness validator on a []byte field is rejected rather
// than silently stamped as an inert array keyword on the base64 string schema,
// matching how oneof on a []byte field is handled.
func TestValidateInterpreter_LengthConstraintOnByteSlice(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{"min=4", "max=10", "len=8", "eq=8", "ne=8", "unique"} {
		t.Run(tag, func(t *testing.T) {
			t.Parallel()

			s := &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"data": {Types: []string{"null", "string"}, ContentEncoding: "base64"},
				},
			}

			interp := validate.NewInterpreter()
			err := interp.Interpret(t.Context(), jsonschema.FieldContext{
				Type:   reflect.TypeFor[[]byte](),
				Schema: s.Properties["data"],
				Parent: s,
				Name:   "data",
			}, jsonschema.Tag{Key: "validate", Value: tag})
			require.Error(t, err)
		})
	}
}

func TestValidateInterpreter_StringKeywordOnByteSlice(t *testing.T) {
	t.Parallel()

	// A []byte field's schema is a base64 string, so a string-only content tag
	// applies even though the Go kind is not string.
	type Doc struct {
		Blob []byte `json:"blob" validate:"base64"`
	}

	s, err := jsonschema.GenerateFor[Doc](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)
	assert.Equal(t, "base64", s.Properties["blob"].ContentEncoding)

	// On a non-string kind whose schema is not a string, the same tag is
	// rejected rather than silently stamped onto an integer schema.
	type Bad struct {
		Count int `json:"count" validate:"base64"`
	}

	_, err = jsonschema.GenerateFor[Bad](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[P](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, "^[0-9]{4}$", s.Properties["code"].Pattern,
		"explicit jsonschema pattern must win over the validate alpha tag")
}

func TestCollectionGtMaxIntDoesNotWrap(t *testing.T) {
	t.Parallel()

	// Gt=MaxInt increments the bound by one, which without an overflow guard
	// wraps to math.MinInt and then collapses to a permissive minItems: 0.
	// The guard preserves math.MaxInt as the tightest representable bound.
	cases := map[string]struct {
		fieldType reflect.Type
		wantMin   *int
	}{
		"slice": {
			fieldType: reflect.TypeFor[[]string](),
			wantMin:   new(math.MaxInt),
		},
		"map": {
			fieldType: reflect.TypeFor[map[string]string](),
			wantMin:   new(math.MaxInt),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{}
			parent := &jsonschema.Schema{}
			interp := validate.NewInterpreter()
			err := interp.Interpret(t.Context(), jsonschema.FieldContext{
				Type:   tc.fieldType,
				Schema: schema,
				Parent: parent,
				Name:   "field",
			}, jsonschema.Tag{Key: "validate", Value: "gt=" + strconv.Itoa(math.MaxInt)})
			require.NoError(t, err)

			if tc.fieldType.Kind() == reflect.Map {
				assert.Equal(t, tc.wantMin, schema.MinProperties,
					"gt=MaxInt on map must not overflow to a permissive bound")
			} else {
				assert.Equal(t, tc.wantMin, schema.MinItems,
					"gt=MaxInt on slice must not overflow to a permissive bound")
			}
		})
	}
}

func TestCollectionLtMinIntDoesNotWrap(t *testing.T) {
	t.Parallel()

	// Lt=MinInt decrements the bound by one, which without an underflow guard
	// wraps to math.MaxInt (a large positive) before the non-negative clamp,
	// leaving a huge permissive maxItems instead of collapsing to 0.
	cases := map[string]struct {
		fieldType reflect.Type
	}{
		"slice": {fieldType: reflect.TypeFor[[]string]()},
		"map":   {fieldType: reflect.TypeFor[map[string]string]()},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{}
			parent := &jsonschema.Schema{}
			interp := validate.NewInterpreter()
			err := interp.Interpret(t.Context(), jsonschema.FieldContext{
				Type:   tc.fieldType,
				Schema: schema,
				Parent: parent,
				Name:   "field",
			}, jsonschema.Tag{Key: "validate", Value: "lt=" + strconv.Itoa(math.MinInt)})
			require.NoError(t, err)

			if tc.fieldType.Kind() == reflect.Map {
				require.NotNil(t, schema.MaxProperties,
					"lt=MinInt on map must still set maxProperties")
				assert.GreaterOrEqual(t, *schema.MaxProperties, 0,
					"lt=MinInt on map must not wrap to a large positive maxProperties")
			} else {
				require.NotNil(t, schema.MaxItems,
					"lt=MinInt on slice must still set maxItems")
				assert.GreaterOrEqual(t, *schema.MaxItems, 0,
					"lt=MinInt on slice must not wrap to a large positive maxItems")
			}
		})
	}
}

func TestValidateInterpreter_ParamEscapes(t *testing.T) {
	t.Parallel()

	// Tags split on commas and pipes, so a param that must contain either
	// character uses the documented go-playground/validator escapes 0x2C -> ","
	// and 0x7C -> "|". The interpreter unescapes the param value the same way, so
	// an enum or const value can hold a literal comma or pipe. A param without an
	// escape sequence is left unchanged.
	cases := map[string]struct {
		tag       string
		wantEnum  []any
		wantConst any
	}{
		"oneof comma escape": {
			tag:      "oneof=a0x2Cb c",
			wantEnum: []any{"a,b", "c"},
		},
		"eq comma escape": {
			tag:       "eq=a0x2Cb",
			wantConst: "a,b",
		},
		"eq pipe escape": {
			tag:       "eq=a0x7Cb",
			wantConst: "a|b",
		},
		"no escapes unchanged": {
			tag:       "eq=plain",
			wantConst: "plain",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Type: "string"}
			parent := &jsonschema.Schema{Type: "object"}

			interp := validate.NewInterpreter()
			err := interp.Interpret(t.Context(), jsonschema.FieldContext{
				Type:   reflect.TypeFor[string](),
				Schema: schema,
				Parent: parent,
				Name:   "value",
			}, jsonschema.Tag{Key: "validate", Value: tc.tag})
			require.NoError(t, err)

			if tc.wantEnum != nil {
				assert.Equal(t, tc.wantEnum, schema.Enum,
					"escaped commas produce literal-comma enum members")
				assert.Nil(t, schema.Const)

				return
			}

			require.NotNil(t, schema.Const)
			assert.Equal(t, tc.wantConst, *schema.Const,
				"escaped param yields the unescaped const value")
		})
	}
}

func TestValidateInterpreter_PipeBindsWithinCommaGroup(t *testing.T) {
	t.Parallel()

	// The | OR operator binds within a single comma group: go-playground splits
	// on commas first, then treats the pipe as OR. A pipe in an earlier group
	// must not swallow later comma-separated constraints. Here oneof=a|b keeps
	// only its first alternative ("a"), and the trailing min=2 still applies.
	type Form struct {
		Name string `json:"name" validate:"oneof=a|b,min=2"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	field := s.Properties["name"]
	require.NotNil(t, field)
	assert.Equal(t, []any{"a"}, field.Enum,
		"only the first OR alternative of oneof=a|b is interpreted")
	require.NotNil(t, field.MinLength,
		"min after a pipe-bearing constraint must still apply")
	assert.Equal(t, 2, *field.MinLength)
}

func TestValidateInterpreter_RequiredNeZeroOnUnsignedDedups(t *testing.T) {
	t.Parallel()

	// The required path forbids the untyped int 0 while ne=0 on an unsigned field
	// parses 0 via strconv.ParseUint to uint64(0). A plain == comparison treats
	// those as distinct and emits a duplicate forbidden value; the numeric-aware
	// dedup recognizes them as the same number and keeps a single not.const.
	type MyType struct {
		Count uint `json:"count" validate:"required,ne=0"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["count"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.Not, "required and ne=0 forbid the zero value")

	// A single forbidden 0 is kept as not.const with no duplicate enum.
	require.NotNil(t, prop.Not.Const, "the single forbidden 0 stays a not.const")
	assert.Nil(t, prop.Not.Enum, "the forbidden 0 is not duplicated into not.enum")

	got, err := json.Marshal(prop.Not)
	require.NoError(t, err)

	assert.JSONEq(t, `{"const":0}`, string(got),
		"the unsigned and int forms of 0 dedup to a single not.const")
}

func TestValidateInterpreter_RequiredMinZeroKeepsFloor(t *testing.T) {
	t.Parallel()

	// "required" floors the length at 1; a weaker "min=0" in the same tag must
	// not lower that floor, in either order. Go-playground/validator rejects an
	// empty value regardless of where min=0 appears, since rules are ANDed.
	type Form struct {
		ReqMinStr string            `json:"req_min_str" validate:"required,min=0"`
		MinReqStr string            `json:"min_req_str" validate:"min=0,required"`
		ReqMinVec []string          `json:"req_min_vec" validate:"required,min=0"`
		MinReqVec []string          `json:"min_req_vec" validate:"min=0,required"`
		ReqMinMap map[string]string `json:"req_min_map" validate:"required,min=0"`
		MinReqMap map[string]string `json:"min_req_map" validate:"min=0,required"`
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
			"req_min_str":{"type":"string","minLength":1},
			"min_req_str":{"type":"string","minLength":1},
			"req_min_vec":{"type":["null","array"],"items":{"type":"string"},"minItems":1},
			"min_req_vec":{"type":["null","array"],"items":{"type":"string"},"minItems":1},
			"req_min_map":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":1},
			"min_req_map":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":1}
		},
		"required":["req_min_str","min_req_str","req_min_vec","min_req_vec","req_min_map","min_req_map"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_RepeatedMinIntersects(t *testing.T) {
	t.Parallel()

	// Repeated/overlapping bounds in a validate tag are ANDed, so the floor is
	// the maximum of the mins and the ceiling is the minimum of the maxes,
	// regardless of order.
	type Form struct {
		MinStr string            `json:"min_str" validate:"min=5,min=2"`
		MaxStr string            `json:"max_str" validate:"max=5,max=10"`
		MinVec []string          `json:"min_vec" validate:"min=5,min=2"`
		MaxVec []string          `json:"max_vec" validate:"max=5,max=10"`
		MinMap map[string]string `json:"min_map" validate:"min=5,min=2"`
		MaxMap map[string]string `json:"max_map" validate:"max=5,max=10"`
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
			"min_str":{"type":"string","minLength":5},
			"max_str":{"type":"string","maxLength":5},
			"min_vec":{"type":["null","array"],"items":{"type":"string"},"minItems":5},
			"max_vec":{"type":["null","array"],"items":{"type":"string"},"maxItems":5},
			"min_map":{"type":["null","object"],"additionalProperties":{"type":"string"},"minProperties":5},
			"max_map":{"type":["null","object"],"additionalProperties":{"type":"string"},"maxProperties":5}
		},
		"required":["min_str","max_str","min_vec","max_vec","min_map","max_map"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_UniqueOnMapIsNoOp(t *testing.T) {
	t.Parallel()

	// UniqueItems is array-only in JSON Schema and go-playground's unique-on-map
	// checks unique values (inexpressible for objects), so unique on a map field
	// produces no uniqueItems keyword.
	type MyType struct {
		Labels map[string]string `json:"labels" validate:"unique"`
	}

	s, err := jsonschema.GenerateFor[MyType](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["labels"]
	require.NotNil(t, prop)

	assert.False(t, prop.UniqueItems,
		"uniqueItems is not set on a map type")
}

func TestValidateInterpreter_IntegerBoundExactRepresentability(t *testing.T) {
	t.Parallel()

	// The upstream Minimum/Maximum are *float64, so integer min/max bounds are
	// parsed exactly and rejected when they cannot be stored as a float64 without
	// rounding. A bound within +/-2^53 round-trips; one beyond it would silently
	// round, turning a forbidden value into an accepted one, so it errors.
	type exactInt struct {
		Value int64 `json:"value" validate:"min=9007199254740992"`
	}

	type inexactInt struct {
		Value int64 `json:"value" validate:"min=9007199254740993"`
	}

	type inexactUint struct {
		Value uint64 `json:"value" validate:"max=18446744073709551615"`
	}

	type smallInt struct {
		Value int `json:"value" validate:"min=1,max=100"`
	}

	t.Run("exact bound is stored", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[exactInt](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)

		prop := s.Properties["value"]
		require.NotNil(t, prop.Minimum)
		assert.InDelta(t, float64(9007199254740992), *prop.Minimum, 0)
	})

	t.Run("inexact integer bound errors", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[inexactInt](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.Error(t, err,
			"a min bound beyond 2^53 is not exactly representable")
		assert.Contains(t, err.Error(), "9007199254740993")
	})

	t.Run("inexact uint bound errors", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[inexactUint](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.Error(t, err,
			"MaxUint64 rounds up past the float64 mantissa and is rejected")
		assert.Contains(t, err.Error(), "18446744073709551615")
	})

	t.Run("small bounds are unaffected", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[smallInt](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)

		prop := s.Properties["value"]
		require.NotNil(t, prop.Minimum)
		require.NotNil(t, prop.Maximum)
		assert.InDelta(t, float64(1), *prop.Minimum, 0)
		assert.InDelta(t, float64(100), *prop.Maximum, 0)
	})
}

func TestValidateInterpreter_RequiredOnBoolPreservesConst(t *testing.T) {
	t.Parallel()

	// On a bool field, "required" means the value must be true, and an earlier eq
	// tag on the same field that pins the const must not be silently overwritten.
	// So eq=false,required is rejected as an impossible combination, since the
	// value cannot be both false and true; this matches how conflicting rules are
	// handled elsewhere. Both eq=true,required and bare required still yield
	// const:true.
	type eqFalseRequired struct {
		Flag bool `json:"flag" validate:"eq=false,required"`
	}

	type requiredEqFalse struct {
		Flag bool `json:"flag" validate:"required,eq=false"`
	}

	type eqTrueRequired struct {
		Flag bool `json:"flag" validate:"eq=true,required"`
	}

	type bareRequired struct {
		Flag bool `json:"flag" validate:"required"`
	}

	t.Run("eq=false then required conflicts", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[eqFalseRequired](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.Error(t, err,
			"required on a bool already pinned to false is an impossible combination")
	})

	t.Run("required then eq=false conflicts", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[requiredEqFalse](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.Error(t, err,
			"the conflict is detected regardless of tag order")
	})

	t.Run("eq=true and required agree", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[eqTrueRequired](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)

		prop := s.Properties["flag"]
		require.NotNil(t, prop.Const)
		assert.Equal(t, true, *prop.Const,
			"eq=true and required both pin the const to true")
	})

	t.Run("bare required pins const to true", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[bareRequired](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		)
		require.NoError(t, err)

		prop := s.Properties["flag"]
		require.NotNil(t, prop.Const)
		assert.Equal(t, true, *prop.Const,
			"required on a bool pins the const to true")
	})
}

func TestValidateInterpreter_NumericBoundsIntersectTypeBounds(t *testing.T) {
	t.Parallel()

	// A min/max tag bound intersects with the type-derived bound rather than
	// overwriting it, so a bound the field's Go type can never satisfy is clamped
	// to the type's own limit instead of widening the accepted range. The ceiling
	// only falls and the floor only rises.
	type Form struct {
		I8Max   int8  `json:"i8_max"    validate:"max=200"`
		U8Max   uint8 `json:"u8_max"    validate:"max=500"`
		I8Min   int8  `json:"i8_min"    validate:"min=-300"`
		I8MaxOK int8  `json:"i8_max_ok" validate:"max=100"`
		I8MinOK int8  `json:"i8_min_ok" validate:"min=-50"`
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
			"i8_max":{"type":"integer","minimum":-128,"maximum":127},
			"u8_max":{"type":"integer","minimum":0,"maximum":255},
			"i8_min":{"type":"integer","minimum":-128,"maximum":127},
			"i8_max_ok":{"type":"integer","minimum":-128,"maximum":100},
			"i8_min_ok":{"type":"integer","minimum":-50,"maximum":127}
		},
		"required":["i8_max","u8_max","i8_min","i8_max_ok","i8_min_ok"],
		"additionalProperties":false
	}`, string(got))
}

func TestValidateInterpreter_NumericValueOverflowErrors(t *testing.T) {
	t.Parallel()

	// An eq/ne/oneof/len value for a sized integer field is range-checked against
	// the field's Go type, so a value the field can never hold overflows during
	// parsing and surfaces as an error (wrapping strconv.ErrRange), mirroring the
	// jsonschema-tag path. An out-of-range value never reaches an unsatisfiable or
	// inert schema.
	type eqInt8 struct {
		Value int8 `json:"value" validate:"eq=200"`
	}

	type neInt16 struct {
		Value int16 `json:"value" validate:"ne=40000"`
	}

	type oneofUint8 struct {
		Value uint8 `json:"value" validate:"oneof=1 300"`
	}

	type lenInt8 struct {
		Value int8 `json:"value" validate:"len=200"`
	}

	type inRangeInt8 struct {
		Value int8 `json:"value" validate:"eq=100"`
	}

	cases := map[string]struct {
		gen func() (*jsonschema.Schema, error)
		err bool
	}{
		"eq overflow on int8": {
			gen: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[eqInt8](t.Context(),
					jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				)
			},
			err: true,
		},
		"ne overflow on int16": {
			gen: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[neInt16](t.Context(),
					jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				)
			},
			err: true,
		},
		"oneof overflow on uint8": {
			gen: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[oneofUint8](t.Context(),
					jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				)
			},
			err: true,
		},
		"len overflow on int8": {
			gen: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[lenInt8](t.Context(),
					jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				)
			},
			err: true,
		},
		"in-range eq on int8": {
			gen: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[inRangeInt8](t.Context(),
					jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				)
			},
			err: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.gen()
			if tc.err {
				require.Error(t, err,
					"an out-of-range value overflows during parsing")
				require.ErrorIs(t, err, strconv.ErrRange,
					"the overflow wraps strconv.ErrRange like the jsonschema-tag path")

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateInterpreter_LenOnCollectionUnaffectedByFieldWidth(t *testing.T) {
	t.Parallel()

	// A len on a string or slice counts elements, not a field value, so a large
	// length is never range-checked against an integer field's width. Only eq/ne/
	// oneof on a numeric field carry a value that must fit the field's Go type.
	type Form struct {
		Code  string   `json:"code"  validate:"len=200"`
		Items []string `json:"items" validate:"len=300"`
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
			"code":{"type":"string","minLength":200,"maxLength":200},
			"items":{"type":["null","array"],"items":{"type":"string"},"minItems":300,"maxItems":300}
		},
		"required":["code","items"],
		"additionalProperties":false
	}`, string(got))
}
