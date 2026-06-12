package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Tests for WithDefaultsFrom: seeding root property defaults from an instance
// of the generated type.

// defaultsNested is a nested struct whose marshaled value becomes a
// whole-value default on its top-level property.
type defaultsNested struct {
	Path string `json:"path"`
}

// defaultsConfig exercises the presence rules: a plain field, a field with a
// tag default, omitempty fields, and a nested struct.
type defaultsConfig struct {
	Host   string         `json:"host"`
	Port   int            `json:"port"            jsonschema:"default=80"`
	Debug  bool           `json:"debug,omitempty"`
	Tags   []string       `json:"tags,omitempty"`
	Nested defaultsNested `json:"nested"`
}

// defaultsRecursive references itself, so its root schema stays a $defs entry
// and the defaults land on that definition.
type defaultsRecursive struct {
	Name string             `json:"name"`
	Next *defaultsRecursive `json:"next,omitempty"`
}

// defaultsString marshals to a JSON string, not an object.
type defaultsString string

func TestWithDefaultsFrom(t *testing.T) {
	t.Parallel()

	t.Run("instance values become property defaults", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Host:   "localhost",
				Port:   8080,
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("omitempty zero value leaves default unset", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		// Debug and Tags are zero and omitempty, so encoding/json omits them
		// and their properties carry no default.
		assert.Nil(t, s.Properties["debug"].Default,
			"a key omitted by omitempty contributes no default")
		assert.Nil(t, s.Properties["tags"].Default,
			"a key omitted by omitempty contributes no default")

		// A present omitempty key still contributes its value.
		s, err = jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Debug: true}),
		)
		require.NoError(t, err)
		assert.JSONEq(t, `true`, string(s.Properties["debug"].Default))
	})

	t.Run("instance overwrites tag default", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Port: 8080}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `8080`, string(s.Properties["port"].Default),
			"the instance value wins over the jsonschema tag default")
	})

	t.Run("tag default survives without the option", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context())
		require.NoError(t, err)

		assert.JSONEq(t, `80`, string(s.Properties["port"].Default))
	})

	t.Run("nested struct becomes whole-value default", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		// The nested property is a $ref to its $defs entry; the default sits
		// beside the $ref as the whole marshaled object.
		assert.JSONEq(t, `{"path":"/var/data"}`, string(s.Properties["nested"].Default))
	})

	t.Run("pointer instance", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(&defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("pointer root applies through nullable wrapper", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost", Port: 8080}),
		)
		require.NoError(t, err)

		// A pointer root generates anyOf[{$ref: #/$defs/...}, {type: null}];
		// the defaults resolve through the wrapper and its $ref to the $defs
		// entry the value branch targets.
		require.Len(t, s.AnyOf, 2)
		require.Equal(t, "#/$defs/defaultsConfig", s.AnyOf[0].Ref)

		def := s.Defs["defaultsConfig"]
		require.NotNil(t, def)
		assert.JSONEq(t, `"localhost"`, string(def.Properties["host"].Default))
		assert.JSONEq(t, `8080`, string(def.Properties["port"].Default),
			"the instance value wins over the jsonschema tag default")
	})

	t.Run("pointer root with nullability disabled", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*defaultsConfig](t.Context(),
			jsonschema.WithNullable(false),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		// Without the null branch the pointer root inlines like a value root,
		// so the defaults land directly on the root properties.
		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("type mismatch", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsNested{Path: "/var/data"}),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance)
	})

	t.Run("nil instance", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(nil),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance)
	})

	t.Run("non-object marshal", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsString](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsString("hello")),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance,
			"an instance marshaling to a JSON string is not an object")
	})

	t.Run("nil pointer instance marshals to null", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom((*defaultsConfig)(nil)),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance,
			"a nil pointer marshals to JSON null, not an object")
	})

	t.Run("Draft-07 wraps a defaulted ref property in allOf", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDraft(jsonschema.Draft7),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		// Draft-07 readers ignore keywords beside $ref, so the default on the
		// definitions-extracted nested property forces the $ref into allOf,
		// the same shape a tag default produces.
		nested := s.Properties["nested"]
		require.NotNil(t, nested)
		assert.Empty(t, nested.Ref)
		require.Len(t, nested.AllOf, 1)
		assert.Equal(t, "#/definitions/defaultsNested", nested.AllOf[0].Ref)
		assert.JSONEq(t, `{"path":"/var/data"}`, string(nested.Default))
	})

	t.Run("self-referential root applies to definition", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsRecursive](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsRecursive{Name: "head"}),
		)
		require.NoError(t, err)

		// The root stays a $ref because the type references itself; the
		// defaults land on the $defs entry, shared by every occurrence.
		require.NotEmpty(t, s.Ref)

		def := s.Defs["defaultsRecursive"]
		require.NotNil(t, def)
		assert.JSONEq(t, `"head"`, string(def.Properties["name"].Default))
	})
}
