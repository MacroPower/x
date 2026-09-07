package jsonschema_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestRefResolverFunc(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.RefResolverFunc(
		func(_ context.Context, uri string) (*jsonschema.Schema, error) {
			if uri != "https://example.com/s.json" {
				return nil, fmt.Errorf("unexpected uri %q", uri)
			}

			return &jsonschema.Schema{Type: "string"}, nil
		},
	)

	schema := &jsonschema.Schema{Ref: "https://example.com/s.json"}

	require.NoError(t, jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithRefResolver(resolver)))

	err := jsonschema.Validate(t.Context(), schema, float64(5), jsonschema.WithRefResolver(resolver))

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
}

func TestRefResolverFunc_Error(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	resolver := jsonschema.RefResolverFunc(
		func(_ context.Context, _ string) (*jsonschema.Schema, error) {
			return nil, errBoom
		},
	)

	schema := &jsonschema.Schema{Ref: "https://example.com/missing.json"}

	err := jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, errBoom)
}

// TestSchemaMap pins the map resolver contract: a hit returns the stored
// schema, while a missing URI or a nil stored schema answers ErrNotResolved
// so the validator treats it as unresolved rather than failing.
func TestSchemaMap(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.SchemaMap{
		"https://example.com/s.json":   {Type: "string"},
		"https://example.com/nil.json": nil,
	}

	s, err := resolver.ResolveRef(t.Context(), "https://example.com/s.json")
	require.NoError(t, err)
	assert.Equal(t, "string", s.Type)

	s, err = resolver.ResolveRef(t.Context(), "https://example.com/missing.json")
	require.ErrorIs(t, err, jsonschema.ErrNotResolved)
	require.ErrorContains(t, err, "https://example.com/missing.json")
	assert.Nil(t, s)

	s, err = resolver.ResolveRef(t.Context(), "https://example.com/nil.json")
	require.ErrorIs(t, err, jsonschema.ErrNotResolved)
	assert.Nil(t, s)
}

// TestChainResolvers pins the chain contract: nil links are skipped, a miss
// falls through to the next link, the first schema or error answers, and a
// chain of misses is itself a miss.
func TestChainResolvers(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	miss := jsonschema.SchemaMap{}
	hit := jsonschema.SchemaMap{"https://example.com/s.json": {Type: "string"}}
	failing := jsonschema.RefResolverFunc(
		func(context.Context, string) (*jsonschema.Schema, error) {
			return nil, errBoom
		},
	)

	t.Run("miss falls through to the first answer", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss, hit, failing)

		s, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		assert.Equal(t, "string", s.Type)
	})

	t.Run("error stops the chain", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(miss, failing, hit)

		_, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("all misses miss", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss)

		s, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.ErrorIs(t, err, jsonschema.ErrNotResolved)
		assert.Nil(t, s)
	})

	t.Run("empty chain misses", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.ChainResolvers().ResolveRef(t.Context(), "https://example.com/s.json")
		require.ErrorIs(t, err, jsonschema.ErrNotResolved)
		assert.Nil(t, s)
	})
}

// TestDescriptionProviderFuncs pins the struct adapter contract: each
// function backs its method, and a nil function answers "" so the
// description stays unset for that half.
func TestDescriptionProviderFuncs(t *testing.T) {
	t.Parallel()

	type doc struct {
		Name string `json:"name"`
	}

	t.Run("both functions set", func(t *testing.T) {
		t.Parallel()

		provider := jsonschema.DescriptionProviderFuncs{
			TypeFunc: func(_ context.Context, tc jsonschema.TypeContext) (string, error) {
				return "type " + tc.Type.Name(), nil
			},
			FieldFunc: func(_ context.Context, fc jsonschema.FieldContext) (string, error) {
				return "field " + fc.StructField.Name, nil
			},
		}

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithDescriptionProvider(provider))
		require.NoError(t, err)
		assert.Equal(t, "type doc", s.Description)
		assert.Equal(t, "field Name", s.Properties["name"].Description)
	})

	t.Run("nil functions leave descriptions unset", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{}))
		require.NoError(t, err)
		assert.Empty(t, s.Description)
		assert.Empty(t, s.Properties["name"].Description)
	})

	t.Run("type error aborts generation", func(t *testing.T) {
		t.Parallel()

		errLookup := errors.New("description store unreachable")
		_, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
				TypeFunc: func(context.Context, jsonschema.TypeContext) (string, error) {
					return "", errLookup
				},
			}))
		require.ErrorIs(t, err, errLookup)
	})

	t.Run("field error aborts generation", func(t *testing.T) {
		t.Parallel()

		errLookup := errors.New("description store unreachable")
		_, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
				FieldFunc: func(context.Context, jsonschema.FieldContext) (string, error) {
					return "", errLookup
				},
			}))
		require.ErrorIs(t, err, errLookup)
		require.ErrorContains(t, err, `describe field "name"`)
	})
}

func TestTagInterpreterFunc(t *testing.T) {
	t.Parallel()

	type doc struct {
		Size int `json:"size" units:"meters"`
	}

	t.Run("interprets the registered tag", func(t *testing.T) {
		t.Parallel()

		interp := jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
				field.Canvas.Description = "in " + tag.Value + " via " + tag.Key
				return nil
			},
		)

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.NoError(t, err)
		assert.Equal(t, "in meters via units", s.Properties["size"].Description)
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		errBad := errors.New("bad tag")
		interp := jsonschema.TagInterpreterFunc(
			func(context.Context, jsonschema.FieldContext, jsonschema.Tag) error { return errBad },
		)

		_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.ErrorIs(t, err, errBad)
		assert.ErrorContains(t, err, `tag interpreter "units"`)
	})
}

type ownerEmbedded struct {
	Inner string `json:"inner" units:"seconds"`
}

type ownerOuter struct {
	ownerEmbedded //nolint:unused // Exercised via reflection.

	Outer string `json:"outer" units:"meters"`
}

func TestTagInterpreterFieldContextOwner(t *testing.T) {
	t.Parallel()

	owners := map[string]reflect.Type{}
	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			owners[field.Name] = field.Owner
			return nil
		},
	)

	_, err := jsonschema.GenerateFor[ownerOuter](t.Context(), jsonschema.WithTagInterpreter("units", interp))
	require.NoError(t, err)

	// A field declared on the struct itself is owned by that struct; a
	// promoted field is owned by the embedded type declaring it, matching
	// the Owner a DescriptionProvider reads for the same field.
	assert.Equal(t, reflect.TypeFor[ownerOuter](), owners["outer"])
	assert.Equal(t, reflect.TypeFor[ownerEmbedded](), owners["inner"])
}

// cyclicPtr is a pointer type whose element chain never leaves the Pointer
// kind, the shape that requires a cycle guard in every deref loop.
type cyclicPtr *cyclicPtr

// TestFieldContextElementContextsCyclicPointer pins that ElementContexts
// terminates on a field whose type is a cyclic pointer. A WithTypeSchema
// override lets generation build a node for the type (bypassing the
// kind-dispatch rejection), so an interpreter asking for element contexts
// reaches elementType's pointer deref with a chain that never bottoms out;
// the guarded deref treats it as a non-container and yields no elements.
func TestFieldContextElementContextsCyclicPointer(t *testing.T) {
	t.Parallel()

	type host struct {
		F cyclicPtr `mytag:"x"`
	}

	var elems []jsonschema.FieldContext

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			elems = field.ElementContexts()

			return nil
		},
	)

	_, err := jsonschema.GenerateFor[host](
		t.Context(),
		jsonschema.WithTypeSchema(
			reflect.TypeFor[cyclicPtr](),
			jsonschema.TypeSchema{Value: &jsonschema.Schema{}},
		),
		jsonschema.WithTagInterpreter("mytag", interp),
	)
	require.NoError(t, err)
	require.Empty(t, elems)
}

// TestFieldContextZeroValueSurvives pins the read half of the facade
// boundary: every exported method on the zero FieldContext, and every method
// on the zero Constraints, returns rather than panicking, answering as a
// field with nothing set, no elements, and an opaque shape. The case counts
// are checked against the method sets, so a method added later takes a row
// before it ships.
func TestFieldContextZeroValueSurvives(t *testing.T) {
	t.Parallel()

	var fc jsonschema.FieldContext

	fields := map[string]func(t *testing.T){
		"ElementContexts": func(t *testing.T) { t.Helper(); assert.Nil(t, fc.ElementContexts()) },
		"Constraints":     func(t *testing.T) { t.Helper(); assert.NotNil(t, fc.Constraints()) },
		"ConstraintsFor": func(t *testing.T) {
			t.Helper()
			assert.NotNil(t, fc.ConstraintsFor(jsonschema.ShapeOf(nil, nil)))
		},
		"Shape":            func(t *testing.T) { t.Helper(); assert.Equal(t, jsonschema.FormOpaque, fc.Shape().Form) },
		"EffectiveFormat":  func(t *testing.T) { t.Helper(); assert.Empty(t, fc.EffectiveFormat()) },
		"EffectivePattern": func(t *testing.T) { t.Helper(); assert.Empty(t, fc.EffectivePattern()) },
		"EffectiveContentEncoding": func(t *testing.T) {
			t.Helper()
			assert.Empty(t, fc.EffectiveContentEncoding())
		},
		"EffectiveContentMediaType": func(t *testing.T) {
			t.Helper()
			assert.Empty(t, fc.EffectiveContentMediaType())
		},
	}

	var zero jsonschema.Constraints

	facade := map[string]func(t *testing.T){
		"Apply": func(t *testing.T) {
			t.Helper()
			require.ErrorIs(t, zero.Apply(jsonschema.OpFloorIncl, jsonschema.AxisAuto, "1"), jsonschema.ErrNilCanvas)
		},
		"SetMultipleOf": func(t *testing.T) { t.Helper(); require.ErrorIs(t, zero.SetMultipleOf(2), jsonschema.ErrNilCanvas) },
		"SetConst":      func(t *testing.T) { t.Helper(); require.ErrorIs(t, zero.SetConst("x"), jsonschema.ErrNilCanvas) },
		"SetEnum": func(t *testing.T) {
			t.Helper()
			require.ErrorIs(t, zero.SetEnum([]any{"x"}), jsonschema.ErrNilCanvas)
		},
		"Forbid": func(t *testing.T) { t.Helper(); require.ErrorIs(t, zero.Forbid("x"), jsonschema.ErrNilCanvas) },
		"ForbidSchema": func(t *testing.T) {
			t.Helper()
			require.ErrorIs(t, zero.ForbidSchema(&jsonschema.Schema{}), jsonschema.ErrNilCanvas)
		},
		"Const": func(t *testing.T) {
			t.Helper()

			_, ok := zero.Const()
			assert.False(t, ok)
		},
		"Enum": func(t *testing.T) {
			t.Helper()

			_, ok := zero.Enum()
			assert.False(t, ok)
		},
	}

	assert.Len(t, fields, reflect.TypeFor[jsonschema.FieldContext]().NumMethod(),
		"every exported FieldContext method takes a row here")
	assert.Len(t, facade, reflect.TypeFor[*jsonschema.Constraints]().NumMethod(),
		"every Constraints method takes a row here")

	for name, check := range fields {
		t.Run("FieldContext."+name, func(t *testing.T) {
			t.Parallel()
			check(t)
		})
	}

	for name, check := range facade {
		t.Run("Constraints."+name, func(t *testing.T) {
			t.Parallel()
			check(t)
		})
	}

	// The nil-Type shape is the same answer ShapeOf gives, so an interpreter
	// dispatching on either sees one opaque value.
	assert.Equal(t, jsonschema.FormOpaque, jsonschema.ShapeOf(nil, nil).Form)
	require.ErrorIs(t,
		jsonschema.FieldContext{Canvas: &jsonschema.Schema{}}.Constraints().
			Apply(jsonschema.OpFloorIncl, jsonschema.AxisAuto, "1"),
		jsonschema.ErrConstraintUnsupported,
		"a rule on the opaque value is the shape refusal, not a panic")
}

// TestFieldContextEffectiveAccessorsNilCanvas pins that the Effective accessors
// tolerate a caller-built FieldContext with a nil Canvas the same way they
// tolerate a nil Base: the nil side reads as unauthored, so a hand-built
// context (an interpreter unit test driving an accessor directly) falls back
// to the type-derived Base value instead of panicking on a nil dereference.
func TestFieldContextEffectiveAccessorsNilCanvas(t *testing.T) {
	t.Parallel()

	base := &jsonschema.Schema{
		Format:           "email",
		Pattern:          "^a+$",
		ContentEncoding:  "base64",
		ContentMediaType: "application/json",
	}

	tests := map[string]struct {
		fc   jsonschema.FieldContext
		get  func(jsonschema.FieldContext) string
		want string
	}{
		"format falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "email",
		},
		"pattern falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectivePattern,
			want: "^a+$",
		},
		"content encoding falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveContentEncoding,
			want: "base64",
		},
		"content media type falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveContentMediaType,
			want: "application/json",
		},
		"format empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "",
		},
		"pattern empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectivePattern,
			want: "",
		},
		"content encoding empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveContentEncoding,
			want: "",
		},
		"content media type empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveContentMediaType,
			want: "",
		},
		"canvas still wins over base": {
			fc: jsonschema.FieldContext{
				Canvas: &jsonschema.Schema{Format: "uri"},
				Base:   base,
			},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "uri",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.get(tc.fc))
		})
	}
}
