package jsonschema_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

// Tag, design, and error tests, originally tracked as TODO.md items.

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

	// A tag like description=Hello, World,minimum=1 corrupts the description.
	type MyType struct {
		Name string `json:"name" jsonschema:"description=Hello World,minimum=1"`
	}

	s, err := jsonschema.GenerateFor[MyType]()
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop)

	// The description should be "Hello World" (comma used as separator).
	// But a tag with commas IN values can't be expressed.
	assert.Equal(t, "Hello World", prop.Description)
}

func TestKvPrefixRegexpFalsePositive(t *testing.T) {
	t.Parallel()

	// A bare description starting with "word=" is misclassified as key-value.
	// The regex ^[^ \t\n]*= treats "a=b is the formula" as key "a" with
	// value "b is the formula", producing an "unrecognized key" error.
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

func TestParseIntAllowsNegativeValues(t *testing.T) {
	t.Parallel()

	// All of these keywords MUST be non-negative integers per JSON Schema.
	// Currently parseInt accepts negative values without error.
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

func TestParseFloatAcceptsNaNInf(t *testing.T) {
	t.Parallel()

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

func TestParseTypedScalarUnknownKindsAsString(t *testing.T) {
	t.Parallel()

	type Inner struct {
		X string `json:"x"`
	}

	type MyType struct {
		Data Inner `json:"data" jsonschema:"default=foo"`
	}

	_, err := jsonschema.GenerateFor[MyType]()
	// Setting default=foo on a struct field should be an error.
	require.Error(t, err,
		"default= on struct type should produce an error, not silently use string")
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

func TestSchemaURIRefPrefixSilentDefault(t *testing.T) {
	t.Parallel()

	// An unknown Draft value (e.g., Draft(99)) silently defaults to Draft2020
	// semantics instead of panicking or returning an error. Any future Draft
	// constant would silently use 2020-12 without explicit handling.
	type MyType struct {
		Name string `json:"name"`
	}

	// Using a Draft value beyond the known constants should produce an error
	// or panic, not silently use Draft2020 behavior.
	unknownDraft := jsonschema.Draft(99)
	s, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithDraft(unknownDraft),
	)
	require.NoError(t, err)

	// Currently the schema URI silently defaults to 2020-12.
	// This should be an error for unknown draft values.
	assert.NotEqual(t, "https://json-schema.org/draft/2020-12/schema", s.Schema,
		"unknown Draft value should not silently default to Draft2020 URI")
}

func TestVocabSetOmitsMetaData(t *testing.T) {
	t.Parallel()

	// If metaData vocabulary is disabled, annotation keywords like title,
	// description, default should be skipped. Currently no tracking for this.
	schema := &jsonschema.Schema{
		Schema:      "https://json-schema.org/draft/2020-12/schema",
		Type:        "string",
		Title:       "My Title",
		Description: "My Description",
	}

	// With metaData vocabulary disabled, annotations should be skipped.
	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabValidation2020: true,
			// The metaData vocabulary is NOT active.
		}),
	)
	require.NoError(t, err)
	// Currently passes but metaData keywords are not tracked by vocabSet.
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

	// Tag interpreters receive Parent that's still under construction.
	// Fields processed after the current one aren't yet in Properties.
	// A tag interpreter inspecting Parent.Properties gets inconsistent
	// results depending on field processing order.
	interp := &parentInspector{}

	type MyType struct {
		Alpha string `inspect:"true" json:"alpha"`
		Beta  string `inspect:"true" json:"beta"`
	}

	_, err := jsonschema.GenerateFor[MyType](
		jsonschema.WithTagInterpreter(interp),
	)
	require.NoError(t, err)

	// Both fields should see the complete parent Properties map.
	// Currently, earlier fields see fewer sibling properties.
	for _, snap := range interp.snapshots {
		assert.Equal(t, 2, snap.propCount,
			"field %q should see all sibling properties in Parent, got %d",
			snap.fieldName, snap.propCount)
	}
}

func TestSchemaTypeAliasBlocksExtension(t *testing.T) {
	t.Parallel()

	// Schema = jsonschema.Schema is a type alias. Keywords not in the upstream
	// struct (e.g., $recursiveAnchor from 2019-09) only appear in Extra,
	// but the validator never checks Extra for any keywords.
	schema := &jsonschema.Schema{
		Type: "object",
		Extra: map[string]any{
			"$recursiveAnchor": true,
		},
	}

	// The validator should recognize $recursiveAnchor, but it doesn't
	// because it's in Extra, not a struct field.
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

	// Only one error case is tested (UnrecognizedKey). These test various
	// error paths in tag processing.
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
