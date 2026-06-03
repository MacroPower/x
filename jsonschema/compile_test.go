package jsonschema_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				for _, c := range cases {
					gotValid := v.Validate(c.instance) == nil
					if gotValid != c.valid {
						t.Errorf("instance %v: got valid=%v, want %v", c.instance, gotValid, c.valid)
					}
				}
			}
		}()
	}

	wg.Wait()
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				if err := v.Validate(map[string]any{"name": "ada"}); err != nil {
					t.Errorf("valid instance rejected: %v", err)
				}
				if err := v.Validate(map[string]any{"name": ""}); err == nil {
					t.Errorf("empty name should fail minLength")
				}
			}
		}()
	}

	wg.Wait()
}
