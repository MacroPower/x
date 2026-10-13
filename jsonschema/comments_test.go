package jsonschema_test

import (
	"context"
	"encoding/json/v2"
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

// TestEmbeddedNamedFieldDescription covers doc-comment extraction for an
// embedded field that becomes a single named property (embedded under an
// explicit JSON name rather than promoted). Such a field has no name
// identifier in the source AST, so its comment is located by the embedded
// type's name.
func TestEmbeddedNamedFieldDescription(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Envelope](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	require.Contains(t, s.Properties, "stamp")
	assert.Contains(t, s.Properties["stamp"].Description, "documents the envelope stamp")
}

// TestDefinedFromStructFieldDescription covers a defined type whose underlying
// type is another struct (type Foo Bar). Reflection reports it as a struct with
// the underlying's fields, but its AST declaration is a bare type name, so field
// comments are found only by following the defined type to the underlying.
func TestDefinedFromStructFieldDescription(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.DefinedFromStruct](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	require.Contains(t, s.Properties, "quantity")
	assert.Contains(t, s.Properties["quantity"].Description, "documents the underlying quantity")
}

// TestZeroValueGoCommentProvider covers using the exported type as a zero-value
// literal (&GoCommentProvider{}) instead of the constructor: the comment cache
// must initialize lazily on first store rather than panicking on a nil map.
func TestZeroValueGoCommentProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Widget](
		t.Context(),
		jsonschema.WithDescriptionProvider(&jsonschema.GoCommentProvider{}),
	)
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Contains(t, s.Properties["size"].Description, "documents the widget size")
}

// TestGoCommentProviderContextWithoutType pins that the built-in provider
// answers an empty description for a caller-built context naming no type,
// the zero TypeContext and a FieldContext with no Owner, instead of
// dereferencing the nil reflect.Type. The generator always fills both, so only
// a test or a wrapping provider reaches the provider this way, and the package
// promises no exported method panics on a zero context.
func TestGoCommentProviderContextWithoutType(t *testing.T) {
	t.Parallel()

	provider := jsonschema.NewGoCommentProvider()

	tests := map[string]func(ctx context.Context) (string, error){
		"zero TypeContext": func(ctx context.Context) (string, error) {
			return provider.TypeDescription(ctx, jsonschema.TypeContext{})
		},
		"FieldContext without an Owner": func(ctx context.Context) (string, error) {
			return provider.FieldDescription(ctx, jsonschema.FieldContext{
				StructField: reflect.StructField{Name: "X"},
			})
		},
	}

	for name, lookup := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := lookup(t.Context())
			require.NoError(t, err)
			assert.Empty(t, got)
		})
	}
}

// TestEmbeddedGenericFieldDescription covers comment extraction for an embedded
// generic instantiation. The source AST embeds the type as an index expression
// (Box[int]) with no name identifier, so embeddedFieldName must unwrap it to
// "Box" to match reflect's field name and recover the field's doc comment.
func TestEmbeddedGenericFieldDescription(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.GenericEnvelope](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	require.Contains(t, s.Properties, "crate")
	assert.Contains(t, s.Properties["crate"].Description, "documents the embedded generic box")
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

	// A warm package cache changes nothing: the check used to sit inside the
	// load alone, so a provider that had served the package once returned a
	// schema under a canceled context.
	warm := jsonschema.NewGoCommentProvider()

	_, err = jsonschema.GenerateFor[alpha.Widget](t.Context(), jsonschema.WithDescriptionProvider(warm))
	require.NoError(t, err)

	_, err = jsonschema.GenerateFor[alpha.Widget](ctx, jsonschema.WithDescriptionProvider(warm))
	require.ErrorIs(t, err, context.Canceled, "a cached package reports the canceled context too")
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
				nil, overrides, commentedWidgetProvider(),
			)),
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
				mapDescriptionProvider{}, failing, commentedWidgetProvider(),
			)),
		)
		require.ErrorIs(t, err, errLookup)
	})
}

// TestTypeHookDescriptionWinsOverComment pins that a description a type-level
// hook declares in its TypeSchema.Value survives the comment provider, for
// every hook family, and that the comment fills the slot a hook left empty.
// Before the guard, applyTypeDescription assigned whenever the comment was
// non-empty, so a WithTypeSchema override and a JSONSchemaProvider lost the
// description they declared while an extender kept its own.
func TestTypeHookDescriptionWinsOverComment(t *testing.T) {
	t.Parallel()

	commenter := jsonschema.DescriptionProviderFuncs{
		TypeFunc: func(context.Context, jsonschema.TypeContext) (string, error) {
			return "from comment", nil
		},
	}

	t.Run("override keeps its description", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(commenter),
			jsonschema.WithTypeSchemaFor[commentedWidget](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "object", Description: "from override"},
			}),
		)
		require.NoError(t, err)
		assert.Equal(t, "from override", s.Description)
	})

	t.Run("provider keeps its description", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[describedProviderType](t.Context(),
			jsonschema.WithDescriptionProvider(commenter),
		)
		require.NoError(t, err)
		assert.Equal(t, "from provider", s.Description)
	})

	t.Run("comment fills an empty slot", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[commentedWidget](t.Context(),
			jsonschema.WithDescriptionProvider(commenter),
			jsonschema.WithTypeSchemaFor[commentedWidget](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "object"},
			}),
		)
		require.NoError(t, err)
		assert.Equal(t, "from comment", s.Description)
	})
}

// describedProviderType is a JSONSchemaProvider whose schema declares its own
// description.
type describedProviderType struct{}

func (describedProviderType) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "string", Description: "from provider"}}, nil
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
	assert.NotNil(t, got.Canvas)
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

// fieldDocProvider is a DescriptionProvider that gives every field the doc
// comment that doc returns for its Go name, and every type none.
func fieldDocProvider(doc func(name string) string) jsonschema.DescriptionProvider {
	return jsonschema.DescriptionProviderFuncs{
		FieldFunc: func(_ context.Context, fc jsonschema.FieldContext) (string, error) {
			return doc(fc.StructField.Name), nil
		},
	}
}

// deprecationOf returns the deprecated keyword of s and the message its
// deprecationMessage keyword holds, or "" when it holds none.
func deprecationOf(s *jsonschema.Schema) (bool, string) {
	msg, _ := s.Extra[jsonschema.KeywordDeprecationMessage].(string) //nolint:errcheck // A missing key reads as "".

	return s.Deprecated, msg
}

// TestFieldDescriptionDeprecation pins how generation reads the Deprecated:
// paragraph of the description a provider gives a field. The paragraph sets
// deprecated, and its text sets deprecationMessage, without the prefix and
// with each line break replaced by a space. A tag description replaces the
// description alone, so the paragraph still marks a field whose tag
// describes it.
func TestFieldDescriptionDeprecation(t *testing.T) {
	t.Parallel()

	type doc struct {
		Size  int `json:"size"`
		Named int `json:"named" jsonschema:"description=from the tag"`
	}

	tests := map[string]struct {
		doc        string
		message    string
		deprecated bool
	}{
		"no paragraph": {
			doc: "Size is the size.",
		},
		"later paragraph": {
			doc:        "Size is the size.\n\nDeprecated: use length.",
			message:    "use length.",
			deprecated: true,
		},
		"first paragraph": {
			doc:        "Deprecated: use length.",
			message:    "use length.",
			deprecated: true,
		},
		"line breaks": {
			doc:        "Size is the size.\n\nDeprecated: use length\nor width.\n\nIt counts bytes.",
			message:    "use length or width.",
			deprecated: true,
		},
		"message on the next line": {
			doc:        "Deprecated:\nuse length.",
			message:    "use length.",
			deprecated: true,
		},
		"no message": {
			doc:        "Size is the size.\n\nDeprecated:",
			deprecated: true,
		},
		"first of two paragraphs": {
			doc:        "Deprecated: use length.\n\nDeprecated: use width.",
			message:    "use length.",
			deprecated: true,
		},
		"prefix inside a paragraph": {
			doc: "Size is the size.\nDeprecated: use length.",
		},
		"prefix without a space": {
			doc: "Deprecated:use length.",
		},
		"code block": {
			doc: "Size is the size.\n\nExample:\n\n\tDeprecated: this is code",
		},
		"code block after a paragraph line": {
			doc: "Size is the size, as in\n\tDeprecated: this is code",
		},
		"list item": {
			doc: "Size is one of:\n\n  - Deprecated: a list item",
		},
		"paragraph after a code block": {
			doc:        "Size is the size.\n\n\tsize := 1\n\nDeprecated: use length.",
			message:    "use length.",
			deprecated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(),
				jsonschema.WithDescriptionProvider(fieldDocProvider(func(string) string { return tc.doc })),
			)
			require.NoError(t, err)

			for _, prop := range []string{"size", "named"} {
				deprecated, message := deprecationOf(s.Properties[prop])
				assert.Equal(t, tc.deprecated, deprecated, "property %q", prop)
				assert.Equal(t, tc.message, message, "property %q", prop)
			}

			assert.Equal(t, tc.doc, s.Properties["size"].Description)
			assert.Equal(t, "from the tag", s.Properties["named"].Description)
		})
	}
}

// TestGoCommentProviderDeprecation covers the Deprecated: paragraphs of
// real doc comments, with the jsonschema tag applied after them and an
// embedded field that gives its deprecation to the fields it promotes.
func TestGoCommentProviderDeprecation(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Menu](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	tests := map[string]struct {
		message    string
		deprecated bool
	}{
		"legacyPricing": {message: "legacyPricing has no effect. Remove it.", deprecated: true},
		"label":         {},
		"notes":         {message: "keep notes in the staff wiki", deprecated: true},
		"discount":      {message: "cafe 5.0 drops the old menu fields.", deprecated: true},
		"coupon":        {message: "coupons moved to promotions.", deprecated: true},
	}

	for prop, want := range tests {
		require.Contains(t, s.Properties, prop)

		deprecated, message := deprecationOf(s.Properties[prop])
		assert.Equal(t, want.deprecated, deprecated, "property %q", prop)
		assert.Equal(t, want.message, message, "property %q", prop)
	}

	got, err := json.Marshal(s.Properties["legacyPricing"])
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"type": "boolean",
		"description": "LegacyPricing rounds each price to whole kronor.\n\nDeprecated: legacyPricing has no effect.\nRemove it.",
		"deprecated": true,
		"deprecationMessage": "legacyPricing has no effect. Remove it."
	}`, string(got))
}
