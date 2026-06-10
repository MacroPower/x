package jsonschema_test

import (
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

			v, err := jsonschema.Compile(tt.schema)
			require.NoError(t, err)

			compiledErr := v.Validate(tt.instance)
			directErr := jsonschema.Validate(tt.schema, tt.instance)

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

	v, err := jsonschema.Compile(draft2020RefSchema())
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
			err := v.Validate(c.instance)
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

	v, err := jsonschema.Compile(draft2020RefSchema())
	require.NoError(t, err)

	require.NoError(t, v.ValidateJSON([]byte(`{"name":"ada"}`)))
	require.Error(t, v.ValidateJSON([]byte(`{"name":""}`)))

	err = v.ValidateJSON([]byte(`{not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON decode")
}

func TestCompileError(t *testing.T) {
	t.Parallel()

	// A 2020-12 $vocabulary map that does not require core is invalid, so the
	// failure surfaces at compile time rather than per validation.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
	}

	_, err := jsonschema.Compile(schema,
		jsonschema.WithVocabularies(map[string]bool{jsonschema.VocabCore2020: false}),
	)
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

			_, err := jsonschema.Compile(tt.schema)
			if tt.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.err)
			assert.Contains(t, err.Error(), tt.contains)
		})
	}
}

func TestCompileConcurrent(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.Compile(draft2020RefSchema())
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
					gotValid := v.Validate(c.instance) == nil
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

	v, err := jsonschema.Compile(numericPatternSchema())
	require.NoError(t, err)

	// Two passes prove the caches carry no per-run state and stay consistent.
	for range 2 {
		for _, c := range cases {
			compiledErr := v.Validate(c.instance)
			directErr := jsonschema.Validate(numericPatternSchema(), c.instance)

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

	v, err := jsonschema.Compile(numericPatternSchema())
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
					gotValid := v.Validate(c.instance) == nil
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

	_, err := jsonschema.Compile(&jsonschema.Schema{Type: "string", Pattern: "[invalid"})
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

	v, err := jsonschema.Compile(schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	require.NoError(t, v.Validate(map[string]any{"count": 4.0, "code": "ABC"}))
	require.Error(t, v.Validate(map[string]any{"count": 3.0}), "3 is not a multiple of 2")
	require.Error(t, v.Validate(map[string]any{"count": 12.0}), "12 exceeds the maximum")
	require.Error(t, v.Validate(map[string]any{"code": "abc"}), "lowercase fails the pattern")
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

	v, err := jsonschema.Compile(schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() {
			for range 25 {
				err := v.Validate(map[string]any{"name": "ada"})
				if err != nil {
					t.Errorf("valid instance rejected: %v", err)
				}

				err = v.Validate(map[string]any{"name": ""})
				if err == nil {
					t.Errorf("empty name should fail minLength")
				}
			}
		})
	}

	wg.Wait()
}
