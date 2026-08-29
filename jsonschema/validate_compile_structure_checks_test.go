package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileRejectsConflictingSchemaFields locks in the compile-time
// rejection of a schema that sets both Go fields spelling one JSON keyword.
// Each pair marshals to a single keyword, so such a schema cannot round-trip
// through JSON and the walk would silently prefer one form. These shapes are
// only expressible on a hand-built Schema, so the cases build the values
// directly rather than parsing JSON.
func TestCompileRejectsConflictingSchemaFields(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		err    error
		path   string
	}{
		"type and types": {
			schema: &jsonschema.Schema{Type: "string", Types: []string{"string"}},
			err:    jsonschema.ErrConflictingSchemaFields,
			path:   "/type",
		},
		"defs and definitions": {
			schema: &jsonschema.Schema{
				Defs:        map[string]*jsonschema.Schema{"a": {}},
				Definitions: map[string]*jsonschema.Schema{"b": {}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/$defs",
		},
		"items and items array": {
			schema: &jsonschema.Schema{
				Items:      &jsonschema.Schema{},
				ItemsArray: []*jsonschema.Schema{{}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/items",
		},
		"dependencies key in both maps": {
			schema: &jsonschema.Schema{
				DependencySchemas: map[string]*jsonschema.Schema{"k": {}},
				DependencyStrings: map[string][]string{"k": {"x"}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/dependencies/k",
		},
		"conflict nested under properties": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "string", Types: []string{"string"}},
				},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/properties/a/type",
		},
		"duplicate property order entry": {
			schema: &jsonschema.Schema{
				Properties:    map[string]*jsonschema.Schema{"a": {}, "b": {}},
				PropertyOrder: []string{"a", "b", "a"},
			},
			err:  jsonschema.ErrDuplicatePropertyOrder,
			path: "/propertyOrder",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the offending keyword's schema path")
		})
	}
}

// TestCompileRejectsNilSubschemaEntries locks in the compile-time rejection of
// a nil *Schema element inside a sub-schema slice or map. The walk skips a nil
// element silently, so the branch the author listed would assert nothing. A
// nil direct field stays an absent keyword and compiles.
func TestCompileRejectsNilSubschemaEntries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		err    error
		path   string
	}{
		"nil element in allOf": {
			schema: &jsonschema.Schema{AllOf: []*jsonschema.Schema{nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/allOf/0",
		},
		"nil element beside a real branch": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "string"}, nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/anyOf/1",
		},
		"nil member in properties": {
			schema: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"a": nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/properties/a",
		},
		"nil member nested in defs": {
			schema: &jsonschema.Schema{
				Defs: map[string]*jsonschema.Schema{
					"inner": {OneOf: []*jsonschema.Schema{nil}},
				},
			},
			err:  jsonschema.ErrNilSubschema,
			path: "/$defs/inner/oneOf/0",
		},
		"nil direct field is an absent keyword": {
			schema: &jsonschema.Schema{Not: nil, Items: nil, Type: "string"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the nil element's schema path")
		})
	}
}

// TestCompileRejectsNonTreeSchema locks in the two root-document graph checks.
// A *Schema value reachable through two paths, or a pointer cycle, fails
// Compile with [jsonschema.ErrSchemaNotTree] naming both reaching paths. A loop
// closing through a value field fails with the same sentinel, naming the graph
// the check walked and the pointer where the loop closes. Two distinct
// pointers with identical content stay a tree and compile.
func TestCompileRejectsNonTreeSchema(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string"}

	cyclic := &jsonschema.Schema{Defs: map[string]*jsonschema.Schema{}}
	cyclic.Defs["loop"] = cyclic

	valueCyclic := &jsonschema.Schema{Type: "object"}
	valueCyclic.Extra = map[string]any{"x-self": valueCyclic}

	tests := map[string]struct {
		schema  *jsonschema.Schema
		err     error
		paths   []string
		subject string
	}{
		"aliased subschema": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
			},
			err:   jsonschema.ErrSchemaNotTree,
			paths: []string{"/properties/a", "/properties/b"},
		},
		"pointer cycle": {
			schema: cyclic,
			err:    jsonschema.ErrSchemaNotTree,
			paths:  []string{"/$defs/loop"},
		},
		"cycle through a value field": {
			schema:  valueCyclic,
			err:     jsonschema.ErrSchemaNotTree,
			paths:   []string{"/x-self"},
			subject: "the root document",
		},
		"distinct pointers with identical content": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "string"},
					"b": {Type: "string"},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)

			for _, path := range tc.paths {
				assert.Contains(t, err.Error(), path,
					"the violation must name every pointer the check reports")
			}

			if tc.subject != "" {
				assert.Contains(t, err.Error(), tc.subject,
					"a loop the tree check skips must name the graph the walk found it in")
				assert.NotContains(t, err.Error(), "reach the same schema",
					"the value-field check reports the loop, not a pair of paths")
			}
		})
	}
}
