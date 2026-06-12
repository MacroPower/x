package jsonschema_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
)

// TestPromotedEmbeddedFieldComment covers doc-comment extraction for a field
// promoted from an embedded struct declared in another package: the comment
// lives in the embedded type's source, not the outer type's.
func TestPromotedEmbeddedFieldComment(t *testing.T) {
	t.Parallel()

	type doc struct {
		alpha.Widget
		Extra string `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](jsonschema.WithComments(true))
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Contains(t, s.Properties["size"].Description, "documents the widget size")
}

// TestGenericTypeComment covers doc-comment extraction for an instantiated
// generic type, whose reflect name ("Box[int]") carries a type-argument list
// that must be stripped to match the source declaration ("Box").
func TestGenericTypeComment(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Box[int]](jsonschema.WithComments(true))
	require.NoError(t, err)

	assert.Contains(t, s.Description, "documented generic type")
	require.Contains(t, s.Properties, "item")
	assert.Contains(t, s.Properties["item"].Description, "documents the boxed value")
}

// mapCommentProvider is a deterministic CommentProvider backed by maps, the
// kind of pre-extracted comment store WithCommentProvider exists for.
type mapCommentProvider struct {
	types  map[reflect.Type]string
	fields map[reflect.Type]map[string]string
}

func (p mapCommentProvider) TypeComment(t reflect.Type) string { return p.types[t] }

func (p mapCommentProvider) FieldComment(t reflect.Type, fieldName string) string {
	return p.fields[t][fieldName]
}

// commentedWidget is a named type for TestWithCommentProvider; the provider
// keys on its reflect.Type.
type commentedWidget struct {
	Size  int    `json:"size"`
	Label string `json:"label" jsonschema:"description=tag wins"`
}

func commentedWidgetProvider() mapCommentProvider {
	widgetType := reflect.TypeFor[commentedWidget]()

	return mapCommentProvider{
		types: map[reflect.Type]string{widgetType: "a widget"},
		fields: map[reflect.Type]map[string]string{
			widgetType: {"Size": "the size", "Label": "provider comment"},
		},
	}
}

func TestWithCommentProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[commentedWidget](
		jsonschema.WithCommentProvider(commentedWidgetProvider()),
	)
	require.NoError(t, err)

	assert.Equal(t, "a widget", s.Description)
	assert.Equal(t, "the size", s.Properties["size"].Description)
	// The jsonschema struct tag description overrides the provider's.
	assert.Equal(t, "tag wins", s.Properties["label"].Description)
}

// TestWithCommentProvider_LastRegistrationWins covers the interplay with
// WithComments: the last registration wins, and WithComments(false) clears a
// registered provider.
func TestWithCommentProvider_LastRegistrationWins(t *testing.T) {
	t.Parallel()

	provider := commentedWidgetProvider()

	t.Run("provider replaces AST extraction", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](
			jsonschema.WithComments(true),
			jsonschema.WithCommentProvider(provider),
		)
		require.NoError(t, err)
		assert.Equal(t, "a widget", s.Description)
	})

	t.Run("WithComments false clears provider", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](
			jsonschema.WithCommentProvider(provider),
			jsonschema.WithComments(false),
		)
		require.NoError(t, err)
		assert.Empty(t, s.Description)
		assert.Empty(t, s.Properties["size"].Description)
	})

	t.Run("nil provider is ignored", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](
			jsonschema.WithCommentProvider(provider),
			jsonschema.WithCommentProvider(nil),
		)
		require.NoError(t, err)
		assert.Equal(t, "a widget", s.Description)
	})
}

// TestWithCommentProvider_PromotedFieldDeclaringType covers the FieldComment
// contract for promoted fields: the provider receives the embedded type that
// declares the field, not the outer struct.
func TestWithCommentProvider_PromotedFieldDeclaringType(t *testing.T) {
	t.Parallel()

	type doc struct {
		alpha.Widget

		Extra string `json:"extra"`
	}

	provider := mapCommentProvider{
		fields: map[reflect.Type]map[string]string{
			reflect.TypeFor[alpha.Widget](): {"Size": "embedded size"},
		},
	}

	s, err := jsonschema.GenerateFor[doc](jsonschema.WithCommentProvider(provider))
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Equal(t, "embedded size", s.Properties["size"].Description)
}
