package jsonschema_test

import (
	"context"
	"errors"
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

// TestGoCommentProviderWithLoadDir covers the load-directory option: package
// loading runs in the configured directory, so a directory outside any
// module locates no sources and the provider silently supplies no comments,
// while the package's own directory behaves like the default.
func TestGoCommentProviderWithLoadDir(t *testing.T) {
	t.Parallel()

	t.Run("module directory extracts comments", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[alpha.Widget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider(jsonschema.WithLoadDir("."))),
		)
		require.NoError(t, err)
		require.Contains(t, s.Properties, "size")
		assert.Contains(t, s.Properties["size"].Description, "documents the widget size")
	})

	t.Run("directory outside a module supplies no comments", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[alpha.Widget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider(jsonschema.WithLoadDir(t.TempDir()))),
		)
		require.NoError(t, err)
		require.Contains(t, s.Properties, "size")
		assert.Empty(t, s.Properties["size"].Description)
	})
}

// TestGoCommentProviderCanceledContext pins the one load failure the
// provider reports instead of silently skipping: a Generate context that is
// already done surfaces as an error, since package loading is the
// cancellable work the context exists for.
func TestGoCommentProviderCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := jsonschema.GenerateFor[alpha.Widget](ctx,
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.ErrorIs(t, err, context.Canceled)
}

// mapDescriptionProvider is a deterministic DescriptionProvider backed by maps, the
// kind of pre-extracted comment store WithDescriptionProvider exists for.
type mapDescriptionProvider struct {
	types  map[reflect.Type]string
	fields map[reflect.Type]map[string]string
}

func (p mapDescriptionProvider) TypeDescription(_ context.Context, tc jsonschema.TypeContext) (string, error) {
	return p.types[tc.Type], nil
}

func (p mapDescriptionProvider) FieldDescription(_ context.Context, fc jsonschema.FieldContext) (string, error) {
	return p.fields[fc.Owner][fc.StructField.Name], nil
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

// TestChainDescriptionProviders pins the chain contract: nil links are
// skipped, an empty answer falls through to the next provider per lookup,
// and the first non-empty description wins.
func TestChainDescriptionProviders(t *testing.T) {
	t.Parallel()

	widgetType := reflect.TypeFor[commentedWidget]()
	overrides := mapDescriptionProvider{
		types: map[reflect.Type]string{widgetType: "override widget"},
		// No field entries: field lookups fall through to the next link.
	}

	t.Run("first non-empty answer wins per lookup", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.ChainDescriptionProviders(
				nil, overrides, commentedWidgetProvider())),
		)
		require.NoError(t, err)

		assert.Equal(t, "override widget", s.Description,
			"the first link's type description wins")
		assert.Equal(t, "the size", s.Properties["size"].Description,
			"a field lookup the first link cannot answer falls through")
	})

	t.Run("all-empty chain leaves descriptions unset", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.ChainDescriptionProviders()),
		)
		require.NoError(t, err)

		assert.Empty(t, s.Description)
		assert.Empty(t, s.Properties["size"].Description)
	})

	t.Run("error stops the chain", func(t *testing.T) {
		t.Parallel()

		errLookup := errors.New("description store unreachable")
		failing := jsonschema.DescriptionProviderFuncs{
			TypeFunc: func(context.Context, jsonschema.TypeContext) (string, error) {
				return "", errLookup
			},
		}

		_, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.ChainDescriptionProviders(
				mapDescriptionProvider{}, failing, commentedWidgetProvider())),
		)
		require.ErrorIs(t, err, errLookup)
	})
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

// TestWithDescriptionProvider_FieldContext pins the FieldContext a
// description provider receives: the same value tag interpreters get, with
// the tag pair empty.
func TestWithDescriptionProvider_FieldContext(t *testing.T) {
	t.Parallel()

	type doc struct {
		Size int `json:"size,omitempty" jsonschema:"minimum=1"`
	}

	var got jsonschema.FieldContext

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
			FieldFunc: func(_ context.Context, fc jsonschema.FieldContext) (string, error) {
				got = fc

				return "captured", nil
			},
		}),
	)
	require.NoError(t, err)
	require.Contains(t, s.Properties, "size")
	assert.Equal(t, "captured", s.Properties["size"].Description)

	assert.Equal(t, reflect.TypeFor[doc](), got.Owner)
	assert.Equal(t, "Size", got.StructField.Name)
	assert.Equal(t, "size", got.Name)
	assert.Equal(t, reflect.TypeFor[int](), got.Type)
	assert.NotNil(t, got.Schema)
	assert.NotNil(t, got.Parent)
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
