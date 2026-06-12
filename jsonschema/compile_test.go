package jsonschema_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// draft2020RefSchema is a Draft 2020-12 schema that exercises $ref resolution
// and unevaluatedProperties, so reuse and concurrency tests touch the mutable
// walk state (visiting set, dynamic scope, JSON-pointer cache).
func draft2020RefSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Ref: "#/$defs/nonEmpty"},
		},
		Required:              []string{"name"},
		UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		Defs: map[string]*jsonschema.Schema{
			"nonEmpty": {Type: "string", MinLength: jsonschema.Ptr(1)},
		},
	}
}

func TestCompileEquivalentToValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		valid    bool
	}{
		"empty schema accepts": {
			schema:   &jsonschema.Schema{},
			instance: "hello",
			valid:    true,
		},
		"type mismatch": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: 1.0,
			valid:    false,
		},
		"ref ok": {
			schema:   draft2020RefSchema(),
			instance: map[string]any{"name": "ada"},
			valid:    true,
		},
		"ref empty string fails minLength": {
			schema:   draft2020RefSchema(),
			instance: map[string]any{"name": ""},
			valid:    false,
		},
		"unevaluated property rejected": {
			schema:   draft2020RefSchema(),
			instance: map[string]any{"name": "ada", "extra": 1.0},
			valid:    false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tt.schema)
			require.NoError(t, err)

			compiledErr := v.Validate(t.Context(), tt.instance)
			directErr := jsonschema.Validate(t.Context(), tt.schema, tt.instance)

			// Compiling once and validating must agree with the one-shot helper.
			assert.Equal(t, directErr == nil, compiledErr == nil)

			if tt.valid {
				assert.NoError(t, compiledErr)
			} else {
				assert.Error(t, compiledErr)
			}
		})
	}
}

func TestCompileReuseAcrossInstances(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(t.Context(), draft2020RefSchema())
	require.NoError(t, err)

	cases := []struct {
		instance any
		valid    bool
	}{
		{map[string]any{"name": "ada"}, true},
		{map[string]any{"name": ""}, false},
		{map[string]any{"name": "ada", "extra": 1.0}, false},
		{map[string]any{"name": "grace"}, true},
		{map[string]any{}, false},
	}

	// Run the whole set twice to prove a compiled validator carries no state
	// between calls.
	for range 2 {
		for _, c := range cases {
			err := v.Validate(t.Context(), c.instance)
			if c.valid {
				assert.NoErrorf(t, err, "instance %v", c.instance)
			} else {
				assert.Errorf(t, err, "instance %v", c.instance)
			}
		}
	}
}

func TestCompileValidateJSON(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(t.Context(), draft2020RefSchema())
	require.NoError(t, err)

	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"name":"ada"}`)))
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"name":""}`)))

	err = v.ValidateJSON(t.Context(), []byte(`{not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON decode")
}

func TestCompileError(t *testing.T) {
	t.Parallel()

	// A 2020-12 $vocabulary map that does not require core is invalid, so the
	// failure surfaces at compile time rather than per validation.
	meta := &jsonschema.Schema{
		ID: "https://example.com/core-not-required-meta",
		Vocabulary: map[string]bool{
			jsonschema.VocabCore2020: false,
		},
	}
	schema := &jsonschema.Schema{
		Schema: "https://example.com/core-not-required-meta",
		Type:   "string",
	}

	_, err := jsonschema.Compile(t.Context(), schema,
		jsonschema.WithMetaSchemaResolver(jsonschema.SchemaMap{meta.ID: meta}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "core vocabulary must be required")
}

// TestCompileRejectsUnknownTypeNames pins that a typo'd type keyword fails at
// Compile with ErrInvalidType instead of compiling into a validator that
// rejects every instance at runtime.
func TestCompileRejectsUnknownTypeNames(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		err      error
		contains string
	}{
		"top-level typo": {
			schema:   &jsonschema.Schema{Type: "interger"},
			err:      jsonschema.ErrInvalidType,
			contains: `"interger" at /type`,
		},
		"nested under properties": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"age": {Type: "numbr"},
				},
			},
			err:      jsonschema.ErrInvalidType,
			contains: `"numbr" at /properties/age/type`,
		},
		"bad entry in types array": {
			schema: &jsonschema.Schema{
				Types: []string{"string", "nul"},
			},
			err:      jsonschema.ErrInvalidType,
			contains: `"nul" at /type`,
		},
		"nested under items and anyOf": {
			schema: &jsonschema.Schema{
				Type: "array",
				Items: &jsonschema.Schema{
					AnyOf: []*jsonschema.Schema{
						{Type: "string"},
						{Type: "Object"},
					},
				},
			},
			err:      jsonschema.ErrInvalidType,
			contains: `"Object" at /items/anyOf/1/type`,
		},
		"all seven valid names": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "null"},
					"b": {Type: "boolean"},
					"c": {Type: "string"},
					"d": {Type: "integer"},
					"e": {Type: "number"},
					"f": {Type: "object"},
					"g": {Type: "array"},
				},
			},
		},
		"valid types array": {
			schema: &jsonschema.Schema{Types: []string{"string", "null"}},
		},
		"absent type": {
			schema: &jsonschema.Schema{MinLength: jsonschema.Ptr(1)},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tt.schema)
			if tt.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.err)
			assert.Contains(t, err.Error(), tt.contains)
		})
	}
}

func TestCheckTypeNames(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		err      error
		contains string
	}{
		"nil schema": {
			schema: nil,
		},
		"valid nested schema": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"age": {Type: "integer"},
				},
			},
		},
		"top-level typo": {
			schema:   &jsonschema.Schema{Type: "interger"},
			err:      jsonschema.ErrInvalidType,
			contains: `"interger" at /type`,
		},
		"typo nested under defs": {
			schema: &jsonschema.Schema{
				Defs: map[string]*jsonschema.Schema{
					"thing": {Type: "strng"},
				},
			},
			err:      jsonschema.ErrInvalidType,
			contains: `"strng" at /$defs/thing/type`,
		},
		"bad entry in types array": {
			schema:   &jsonschema.Schema{Types: []string{"string", "nul"}},
			err:      jsonschema.ErrInvalidType,
			contains: `"nul" at /type`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.CheckTypeNames(tt.schema)
			if tt.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.err)
			assert.Contains(t, err.Error(), tt.contains)
		})
	}
}

// TestCheckTypeNamesMatchesCompile pins that the standalone check and the one
// Compile runs are the same entry point: for a schema whose only defect is a
// bad type name, the two error strings are textually identical.
func TestCheckTypeNamesMatchesCompile(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"age": {Type: "numbr"},
		},
	}

	standaloneErr := jsonschema.CheckTypeNames(schema)
	require.ErrorIs(t, standaloneErr, jsonschema.ErrInvalidType)

	_, compileErr := jsonschema.Compile(t.Context(), schema)
	require.ErrorIs(t, compileErr, jsonschema.ErrInvalidType)

	assert.Equal(t, compileErr.Error(), standaloneErr.Error())
}

// TestCheckTypeNamesToleratesUncompilableSchemas pins the standalone use case:
// CheckTypeNames vets type names without the registry, reference resolution,
// and vocabulary work Compile performs, so schemas Compile rejects — here a
// cyclic pointer graph upstream Resolve cannot represent — still get a
// verdict on their type keywords.
func TestCheckTypeNamesToleratesUncompilableSchemas(t *testing.T) {
	t.Parallel()

	t.Run("valid cyclic schema passes", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "object"}
		schema.Properties = map[string]*jsonschema.Schema{"self": schema}

		require.NoError(t, jsonschema.CheckTypeNames(schema))
	})

	t.Run("cyclic schema with typo still rejected", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "object"}
		schema.Properties = map[string]*jsonschema.Schema{
			"self": schema,
			"age":  {Type: "numbr"},
		}

		err := jsonschema.CheckTypeNames(schema)
		require.ErrorIs(t, err, jsonschema.ErrInvalidType)
		assert.Contains(t, err.Error(), `"numbr" at /properties/age/type`)
	})
}

func TestCompileConcurrent(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(t.Context(), draft2020RefSchema())
	require.NoError(t, err)

	cases := []struct {
		instance any
		valid    bool
	}{
		{map[string]any{"name": "ada"}, true},
		{map[string]any{"name": ""}, false},
		{map[string]any{"name": "ada", "extra": 1.0}, false},
		{map[string]any{}, false},
	}

	// Many goroutines share one compiled validator. Run under -race to catch any
	// data race on shared state; the per-instance results must stay correct.
	var wg sync.WaitGroup

	for range 32 {
		wg.Go(func() {
			for range 25 {
				for _, c := range cases {
					gotValid := v.Validate(t.Context(), c.instance) == nil
					if gotValid != c.valid {
						t.Errorf("instance %v: got valid=%v, want %v", c.instance, gotValid, c.valid)
					}
				}
			}
		})
	}

	wg.Wait()
}

// numericPatternSchema exercises the Compile-time caches: numeric bound
// keywords (multipleOf, minimum, maximum, exclusiveMinimum, exclusiveMaximum)
// and both pattern forms (the string pattern keyword and patternProperties).
func numericPatternSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"count": {
				Type:             "number",
				MultipleOf:       jsonschema.Ptr(2.0),
				Minimum:          jsonschema.Ptr(0.0),
				Maximum:          jsonschema.Ptr(10.0),
				ExclusiveMinimum: jsonschema.Ptr(-1.0),
				ExclusiveMaximum: jsonschema.Ptr(11.0),
			},
			"code": {Type: "string", Pattern: "^[A-Z]{3}$"},
		},
		PatternProperties: map[string]*jsonschema.Schema{
			"^x-": {Type: "string"},
		},
	}
}

// TestCompileNumericAndPatternCaches checks that a compiled validator's
// precomputed numeric-bound and pattern caches yield the same results as the
// one-shot helper, both on first use and on reuse across many instances, so the
// caches never change validation outcomes.
func TestCompileNumericAndPatternCaches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		instance any
		valid    bool
	}{
		{map[string]any{"count": 4.0, "code": "ABC", "x-tag": "ok"}, true},
		{map[string]any{"count": 3.0}, false},  // not a multiple of 2
		{map[string]any{"count": 12.0}, false}, // above maximum
		{map[string]any{"count": 11.0}, false}, // at exclusiveMaximum
		{map[string]any{"code": "abc"}, false}, // lowercase fails pattern
		{map[string]any{"code": "ABCD"}, false},
		{map[string]any{"x-tag": 1.0}, false}, // patternProperties requires string
		{map[string]any{"x-tag": "ok"}, true},
	}

	v, err := jsonschema.Compile(t.Context(), numericPatternSchema())
	require.NoError(t, err)

	// Two passes prove the caches carry no per-run state and stay consistent.
	for range 2 {
		for _, c := range cases {
			compiledErr := v.Validate(t.Context(), c.instance)
			directErr := jsonschema.Validate(t.Context(), numericPatternSchema(), c.instance)

			assert.Equalf(t, directErr == nil, compiledErr == nil,
				"compiled and direct disagree for %v", c.instance)

			if c.valid {
				assert.NoErrorf(t, compiledErr, "instance %v", c.instance)
			} else {
				assert.Errorf(t, compiledErr, "instance %v", c.instance)
			}
		}
	}
}

// TestCompileNumericAndPatternConcurrent shares one compiled validator across
// goroutines so the read-only numeric-bound and pattern caches are exercised
// concurrently; run under -race it confirms those caches are never written after
// Compile.
func TestCompileNumericAndPatternConcurrent(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(t.Context(), numericPatternSchema())
	require.NoError(t, err)

	cases := []struct {
		instance any
		valid    bool
	}{
		{map[string]any{"count": 4.0, "code": "ABC"}, true},
		{map[string]any{"count": 3.0}, false},
		{map[string]any{"code": "abc"}, false},
		{map[string]any{"x-tag": "ok"}, true},
	}

	var wg sync.WaitGroup

	for range 32 {
		wg.Go(func() {
			for range 25 {
				for _, c := range cases {
					gotValid := v.Validate(t.Context(), c.instance) == nil
					if gotValid != c.valid {
						t.Errorf("instance %v: got valid=%v, want %v", c.instance, gotValid, c.valid)
					}
				}
			}
		})
	}

	wg.Wait()
}

// TestCompileInvalidPatternRejected pins that an uncompilable pattern never
// produces an accept-all validator. Structural pre-validation rejects it at
// Compile, so the failure surfaces there; the cached fail-closed branch in
// validateString backs the same contract for patterns reached only at
// validation time (see TestInvalidPatternFailsClosed for the one-shot path).
func TestCompileInvalidPatternRejected(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{Type: "string", Pattern: "[invalid"})
	require.Error(t, err, "an uncompilable pattern must not yield an accept-all validator")
}

// TestCompileRemoteBoundsAndPatternFallback exercises the cache-miss fallback in
// boundsFor and patternFor: a remote schema is reached only at validation time,
// so it is absent from the Compile-time caches and its numeric bound and pattern
// are computed on the fly. The results must match what a directly compiled schema
// produces.
func TestCompileRemoteBoundsAndPatternFallback(t *testing.T) {
	t.Parallel()

	resolver := mapResolver{
		"https://example.com/bounded.json": {
			Type:       "number",
			Minimum:    jsonschema.Ptr(0.0),
			Maximum:    jsonschema.Ptr(10.0),
			MultipleOf: jsonschema.Ptr(2.0),
		},
		"https://example.com/code.json": {Type: "string", Pattern: "^[A-Z]{3}$"},
	}
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"count": {Ref: "https://example.com/bounded.json"},
			"code":  {Ref: "https://example.com/code.json"},
		},
	}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	require.NoError(t, v.Validate(t.Context(), map[string]any{"count": 4.0, "code": "ABC"}))
	require.Error(t, v.Validate(t.Context(), map[string]any{"count": 3.0}), "3 is not a multiple of 2")
	require.Error(t, v.Validate(t.Context(), map[string]any{"count": 12.0}), "12 exceeds the maximum")
	require.Error(t, v.Validate(t.Context(), map[string]any{"code": "abc"}), "lowercase fails the pattern")
}

func TestParseSchemaValue(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc      any
		instance any
		err      error
		contains string
		valid    bool
	}{
		"true is the empty schema": {
			doc:      true,
			instance: "anything",
			valid:    true,
		},
		"false rejects every instance": {
			doc:      false,
			instance: "anything",
			valid:    false,
		},
		"object document": {
			doc: map[string]any{
				"type":      "string",
				"minLength": 2.0,
			},
			instance: "x",
			valid:    false,
		},
		"normalized document with json.Number leaves": {
			doc: map[string]any{
				"type":    "integer",
				"minimum": json.Number("3"),
			},
			instance: 2.0,
			valid:    false,
		},
		"json.Number survives at instance-exceeding magnitude": {
			doc: map[string]any{
				"type": "number",
				// Within float64 range, so the schema-side float64 cap does
				// not round it; the point is the marshal round-trip keeps the
				// json.Number literal intact.
				"minimum": json.Number("12345678901234"),
			},
			instance: 12345678901233.0,
			valid:    false,
		},
		"nil document": {
			doc:      nil,
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "<nil>",
		},
		"string document": {
			doc:      "not a schema",
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "string",
		},
		"array document": {
			doc:      []any{map[string]any{}},
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "[]interface {}",
		},
		"number document": {
			doc:      1.0,
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "float64",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchemaValue(tt.doc)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				assert.Contains(t, err.Error(), tt.contains)

				return
			}

			require.NoError(t, err)

			validateErr := jsonschema.Validate(t.Context(), schema, tt.instance)
			if tt.valid {
				assert.NoError(t, validateErr)
			} else {
				assert.Error(t, validateErr)
			}
		})
	}
}

// TestParseSchemaValueBooleanForms pins the exact schema shapes the boolean
// documents convert to, so they round-trip through the package's own
// predicates and marshal back to JSON true and false.
func TestParseSchemaValueBooleanForms(t *testing.T) {
	t.Parallel()

	trueSchema, err := jsonschema.ParseSchemaValue(true)
	require.NoError(t, err)
	assert.True(t, jsonschema.IsTrueSchema(trueSchema))

	falseSchema, err := jsonschema.ParseSchemaValue(false)
	require.NoError(t, err)
	assert.True(t, jsonschema.IsFalseSchema(falseSchema))
}

func TestParseSchema(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err      error
		data     string
		contains string
		check    func(t *testing.T, s *jsonschema.Schema)
	}{
		"object document decodes keywords": {
			data: `{"type":"string","minLength":2}`,
			check: func(t *testing.T, s *jsonschema.Schema) {
				t.Helper()
				assert.Equal(t, "string", s.Type)
				require.NotNil(t, s.MinLength)
				assert.Equal(t, 2, *s.MinLength)
			},
		},
		"true is the empty schema": {
			data: `true`,
			check: func(t *testing.T, s *jsonschema.Schema) {
				t.Helper()
				assert.True(t, jsonschema.IsTrueSchema(s))
			},
		},
		"false is the rejecting schema": {
			data: `false`,
			check: func(t *testing.T, s *jsonschema.Schema) {
				t.Helper()
				assert.True(t, jsonschema.IsFalseSchema(s))
			},
		},
		"null document": {
			data:     `null`,
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "<nil>",
		},
		"string document": {
			data: `"oops"`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"array document": {
			data: `[true]`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"number document": {
			data: `1`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"malformed JSON": {
			data:     `{"type":`,
			contains: "JSON decode",
		},
		"trailing data": {
			data:     `{"type":"object"} {}`,
			contains: "unexpected data after top-level value",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.ParseSchema([]byte(tt.data))
			if tt.err != nil || tt.contains != "" {
				require.Error(t, err)

				if tt.err != nil {
					require.ErrorIs(t, err, tt.err)
				} else {
					// Decode failures carry no sentinel: the document never
					// reached the top-level shape check.
					require.NotErrorIs(t, err, jsonschema.ErrInvalidSchemaDocument)
				}

				if tt.contains != "" {
					assert.Contains(t, err.Error(), tt.contains)
				}

				return
			}

			require.NoError(t, err)
			tt.check(t, s)
		})
	}
}

// TestParseSchemaPreservesLargeIntegerLiterals pins the decode discipline:
// raw-JSON fields such as default hold the json.Number literal verbatim, so an
// integer beyond float64 precision survives into the Schema exactly.
func TestParseSchemaPreservesLargeIntegerLiterals(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.ParseSchema([]byte(`{"default":9007199254740993}`))
	require.NoError(t, err)
	// Assert.JSONEq would parse both sides into float64, rounding the literal
	// and defeating the precision check; compare the raw text instead.
	assert.Equal(t, `9007199254740993`, string(s.Default)) //nolint:testifylint // See above.
}

func TestCompileJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err      error
		data     string
		instance string
		contains string
		valid    bool
	}{
		"object schema accepts valid instance": {
			data:     `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`,
			instance: `{"name":"ada"}`,
			valid:    true,
		},
		"object schema rejects invalid instance": {
			data:     `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`,
			instance: `{"name":1}`,
			valid:    false,
		},
		"true accepts everything": {
			data:     `true`,
			instance: `[1,2,3]`,
			valid:    true,
		},
		"false rejects everything": {
			data:     `false`,
			instance: `{}`,
			valid:    false,
		},
		"numeric keywords decode": {
			data:     `{"type":"integer","minimum":3}`,
			instance: `2`,
			valid:    false,
		},
		"null document": {
			data:     `null`,
			err:      jsonschema.ErrInvalidSchemaDocument,
			contains: "<nil>",
		},
		"string document": {
			data: `"oops"`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"array document": {
			data: `[true]`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"number document": {
			data: `1`,
			err:  jsonschema.ErrInvalidSchemaDocument,
		},
		"malformed JSON": {
			data:     `{"type":`,
			contains: "JSON decode",
		},
		"trailing data": {
			data:     `{"type":"object"} {}`,
			contains: "unexpected data after top-level value",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tt.data))
			if tt.err != nil || tt.contains != "" {
				require.Error(t, err)

				if tt.err != nil {
					require.ErrorIs(t, err, tt.err)
				} else {
					// Decode failures carry no sentinel: the document never
					// reached the top-level shape check.
					require.NotErrorIs(t, err, jsonschema.ErrInvalidSchemaDocument)
				}

				if tt.contains != "" {
					assert.Contains(t, err.Error(), tt.contains)
				}

				return
			}

			require.NoError(t, err)

			validateErr := v.ValidateJSON(t.Context(), []byte(tt.instance))
			if tt.valid {
				assert.NoError(t, validateErr)
			} else {
				assert.Error(t, validateErr)
			}
		})
	}
}

func TestMustCompile(t *testing.T) {
	t.Parallel()

	t.Run("returns the Compile validator", func(t *testing.T) {
		t.Parallel()

		v := jsonschema.MustCompile(draft2020RefSchema())
		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"name":"ada"}`)))
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"name":""}`)))
	})

	t.Run("panics on compile error", func(t *testing.T) {
		t.Parallel()

		assert.Panics(t, func() {
			jsonschema.MustCompile(&jsonschema.Schema{Type: "strng"})
		})
	})
}

func TestMustCompileJSON(t *testing.T) {
	t.Parallel()

	t.Run("returns the CompileJSON validator", func(t *testing.T) {
		t.Parallel()

		v := jsonschema.MustCompileJSON([]byte(`{"type":"integer","minimum":3}`))
		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`4`)))
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`2`)))
	})

	t.Run("panics on decode error", func(t *testing.T) {
		t.Parallel()

		assert.Panics(t, func() {
			jsonschema.MustCompileJSON([]byte(`null`))
		})
	})
}

func TestCompileConcurrentWithRefResolver(t *testing.T) {
	t.Parallel()

	// A remote $ref forces resolveRemote during the walk, which writes the
	// registries; forInstance gives each run its own copies, so concurrent use
	// must remain race-free and correct.
	resolver := mapResolver{
		"https://example.com/name.json": {Type: "string", MinLength: jsonschema.Ptr(1)},
	}
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Ref: "https://example.com/name.json"},
		},
		Required: []string{"name"},
	}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() {
			for range 25 {
				err := v.Validate(t.Context(), map[string]any{"name": "ada"})
				if err != nil {
					t.Errorf("valid instance rejected: %v", err)
				}

				err = v.Validate(t.Context(), map[string]any{"name": ""})
				if err == nil {
					t.Errorf("empty name should fail minLength")
				}
			}
		})
	}

	wg.Wait()
}

// TestValidatorAccessors pins Schema and Draft: a compiled validator exposes
// the very schema it was compiled for and the draft it validates under, so
// it can be passed across package boundaries without the schema riding
// alongside it.
func TestValidatorAccessors(t *testing.T) {
	t.Parallel()

	t.Run("Schema returns the compiled root", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "string"}
		v, err := jsonschema.Compile(t.Context(), schema)
		require.NoError(t, err)

		assert.Same(t, schema, v.Schema())
	})

	t.Run("Draft detects from the root $schema", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{
			Schema: "http://json-schema.org/draft-07/schema#",
			Type:   "string",
		})
		require.NoError(t, err)

		assert.Equal(t, jsonschema.Draft7, v.Draft())
	})

	t.Run("Draft defaults to Draft2020 without $schema", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{Type: "string"})
		require.NoError(t, err)

		assert.Equal(t, jsonschema.Draft2020, v.Draft())
	})

	t.Run("Draft reports the WithDraft override", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{Type: "string"},
			jsonschema.WithDraft(jsonschema.Draft7))
		require.NoError(t, err)

		assert.Equal(t, jsonschema.Draft7, v.Draft())
	})
}
