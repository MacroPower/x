package jsonschema_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
)

// TestPromotedEmbeddedFieldDescription covers doc-comment extraction for a field
// promoted from an embedded struct declared in another package: the comment
// lives in the embedded type's source, not the outer type's.
func TestPromotedEmbeddedFieldDescription(t *testing.T) {
	t.Parallel()

	type doc struct {
		alpha.Widget
		Extra string `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Contains(t, s.Properties["size"].Description, "documents the widget size")
}

// TestGenericTypeDescription covers doc-comment extraction for an instantiated
// generic type, whose reflect name ("Box[int]") carries a type-argument list
// that must be stripped to match the source declaration ("Box").
func TestGenericTypeDescription(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Box[int]](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	assert.Contains(t, s.Description, "documented generic type")
	require.Contains(t, s.Properties, "item")
	assert.Contains(t, s.Properties["item"].Description, "documents the boxed value")
}

// mapDescriptionProvider is a deterministic DescriptionProvider backed by maps, the
// kind of pre-extracted comment store WithDescriptionProvider exists for.
type mapDescriptionProvider struct {
	types  map[reflect.Type]string
	fields map[reflect.Type]map[string]string
}

func (p mapDescriptionProvider) TypeDescription(_ context.Context, t reflect.Type) string {
	return p.types[t]
}

func (p mapDescriptionProvider) FieldDescription(_ context.Context, t reflect.Type, fieldName string) string {
	return p.fields[t][fieldName]
}

// commentedWidget is a named type for TestWithDescriptionProvider; the provider
// keys on its reflect.Type.
type commentedWidget struct {
	Size  int    `json:"size"`
	Label string `json:"label" jsonschema:"description=tag wins"`
}

func commentedWidgetProvider() mapDescriptionProvider {
	widgetType := reflect.TypeFor[commentedWidget]()

	return mapDescriptionProvider{
		types: map[reflect.Type]string{widgetType: "a widget"},
		fields: map[reflect.Type]map[string]string{
			widgetType: {"Size": "the size", "Label": "provider comment"},
		},
	}
}

func TestWithDescriptionProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
		jsonschema.WithDescriptionProvider(commentedWidgetProvider()),
	)
	require.NoError(t, err)

	assert.Equal(t, "a widget", s.Description)
	assert.Equal(t, "the size", s.Properties["size"].Description)
	// The jsonschema struct tag description overrides the provider's.
	assert.Equal(t, "tag wins", s.Properties["label"].Description)
}

// TestWithDescriptionProvider_LastRegistrationWins covers the registration
// semantics: the last registration wins, and a nil provider clears an
// earlier one.
func TestWithDescriptionProvider_LastRegistrationWins(t *testing.T) {
	t.Parallel()

	provider := commentedWidgetProvider()

	t.Run("provider replaces AST extraction", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
			jsonschema.WithDescriptionProvider(provider),
		)
		require.NoError(t, err)
		assert.Equal(t, "a widget", s.Description)
	})

	t.Run("nil provider clears earlier registration", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(provider),
			jsonschema.WithDescriptionProvider(nil),
		)
		require.NoError(t, err)
		assert.Empty(t, s.Description)
		assert.Empty(t, s.Properties["size"].Description)
	})
}

// TestWithDescriptionProvider_PromotedFieldDeclaringType covers the FieldDescription
// contract for promoted fields: the provider receives the embedded type that
// declares the field, not the outer struct.
func TestWithDescriptionProvider_PromotedFieldDeclaringType(t *testing.T) {
	t.Parallel()

	type doc struct {
		alpha.Widget

		Extra string `json:"extra"`
	}

	provider := mapDescriptionProvider{
		fields: map[reflect.Type]map[string]string{
			reflect.TypeFor[alpha.Widget](): {"Size": "embedded size"},
		},
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithDescriptionProvider(provider))
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Equal(t, "embedded size", s.Properties["size"].Description)
}
