package jsonschema_test

import (
	"encoding/json/jsontext"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// TestValidateDraft7EmptyItemsArray locks in Draft-07 array-form semantics for
// a present-but-empty items array (JSON "items": [], which parses to a non-nil
// empty ItemsArray): additionalItems applies to every index at or beyond the
// tuple length, which is every index when the tuple is empty. Conflating the
// empty tuple with an absent items keyword would silently drop additionalItems
// and accept every array.
func TestValidateDraft7EmptyItemsArray(t *testing.T) {
	t.Parallel()

	const draft7 = `"http://json-schema.org/draft-07/schema#"`

	tests := map[string]struct {
		schema   string
		instance string
		valid    bool
		keyword  string
	}{
		"false additionalItems rejects every element": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": false}`,
			instance: `[1]`,
			keyword:  jsonschema.KeywordAdditionalItems,
		},
		"false additionalItems accepts an empty array": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": false}`,
			instance: `[]`,
			valid:    true,
		},
		"schema additionalItems applies from index zero": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": {"type": "string"}}`,
			instance: `[1]`,
			keyword:  jsonschema.KeywordType,
		},
		"schema additionalItems accepts conforming elements": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": {"type": "string"}}`,
			instance: `["a", "b"]`,
			valid:    true,
		},
		"empty items array alone constrains nothing": {
			schema:   `{"$schema": ` + draft7 + `, "items": []}`,
			instance: `[1, "a"]`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err,
				"additionalItems governs every index beyond the empty tuple")

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword)
		})
	}
}

// TestValidateContainsFloorWithoutValidationVocab locks in the vocabulary split
// of the contains cluster: the at-least-one rule with its default minContains=1
// floor belongs to contains itself (2020-12 core section 10.3.1.3, applicator
// vocabulary), while only the explicit minContains/maxContains bounds belong to
// the validation vocabulary. Disabling the validation vocabulary therefore
// skips the explicit bounds but keeps the default floor.
func TestValidateContainsFloorWithoutValidationVocab(t *testing.T) {
	t.Parallel()

	applicatorOnly := jsonschema.WithVocabularies(
		jsonschema.VocabCore2020,
		jsonschema.VocabApplicator2020,
	)

	// The subschemas are the boolean true/false forms: an assertion keyword
	// such as type inside the contains subschema would itself be gated on the
	// disabled validation vocabulary and match vacuously, while the boolean
	// forms accept and reject independent of any vocabulary.
	tests := map[string]struct {
		schema   *jsonschema.Schema
		opts     []jsonschema.ValidateOption
		instance string
		valid    bool
		keyword  string
	}{
		"default floor rejects an empty array without the validation vocabulary": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[]`,
			keyword:  jsonschema.KeywordContains,
		},
		"default floor rejects when no element matches": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{Not: &jsonschema.Schema{}}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1, 2]`,
			keyword:  jsonschema.KeywordContains,
		},
		"a matching element satisfies the floor without the validation vocabulary": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1]`,
			valid:    true,
		},
		"a skipped minContains cannot lower the default floor": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(0),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[]`,
			keyword:  jsonschema.KeywordContains,
		},
		"a skipped minContains cannot raise the default floor": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(3),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1]`,
			valid:    true,
		},
		"explicit maxContains is skipped without the validation vocabulary": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MaxContains: new(1),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1, 2]`,
			valid:    true,
		},
		"explicit minContains applies with the full vocabulary set": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(0),
			},
			instance: `[]`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tc.schema, tc.opts...)
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword,
				"a default-floor shortfall is a contains failure, never a skipped minContains violation")
		})
	}
}

// TestValidateIfThenElseMessages pins the then/else branch messages to the
// package's prevailing wording: the repo convention keeps "failed" and
// "error" out of library error messages, so the branch outcome is stated as a
// did-not-validate clause like the other applicator keywords.
func TestValidateIfThenElseMessages(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		instance string
		want     string
	}{
		"then branch": {
			schema:   `{"if": {"type": "integer"}, "then": {"minimum": 5}}`,
			instance: `1`,
			want:     "if condition was true but did not validate against then subschema",
		},
		"else branch": {
			schema:   `{"if": {"type": "integer"}, "else": {"maxLength": 0}}`,
			instance: `"x"`,
			want:     "if condition was false but did not validate against else subschema",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "failed",
				`library error messages avoid the word "failed"`)
		})
	}
}

// A pre-parsed instance may mix float64 and jsonv1.Number representations of
// the same JSON number (Validate accepts both). The uniqueItems comparison
// must interpret a float64 through its shortest decimal, the way const, enum,
// and the numeric-bound keywords do, so float64(1.1) and the decoded literal
// 1.1 are one value under every keyword rather than duplicates under const
// but distinct under uniqueItems.
func TestValidateUniqueItemsMixedNumberRepresentations(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{UniqueItems: true}

	tests := map[string]struct {
		arr []any
		err string
	}{
		"float64 and jsonv1.Number non-integer duplicate": {
			arr: []any{1.1, jsonv1.Number("1.1")},
			err: "uniqueItems",
		},
		"float64 and jsonv1.Number fraction duplicate": {
			arr: []any{0.1, jsonv1.Number("0.1")},
			err: "uniqueItems",
		},
		"float64 and jsonv1.Number integer duplicate": {
			arr: []any{1.0, jsonv1.Number("1")},
			err: "uniqueItems",
		},
		"distinct mixed representations": {
			arr: []any{1.1, jsonv1.Number("1.2")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), schema, tc.arr)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// The same value pair must be equal under const exactly when it is a
// duplicate under uniqueItems: both elements of the mixed-representation
// pair above individually satisfy const 1.1.
func TestValidateConstAgreesWithUniqueItems(t *testing.T) {
	t.Parallel()

	constSchema := jsonschema.MustCompileJSON([]byte(`{"const": 1.1}`))

	require.NoError(t, constSchema.Validate(t.Context(), 1.1))
	require.NoError(t, constSchema.Validate(t.Context(), jsonv1.Number("1.1")))
}

// TestValidateEmptyTypeArrayRejectsEverything pins that a present-but-empty
// type array constrains, rather than reading as an absent keyword. The spec
// admits an instance whose type is in the listed set, so an empty list admits
// nothing, the reading the package already gives an empty enum. The type
// check once skipped on len(types) == 0, conflating "type": [] with no type
// keyword and accepting every instance, and a hand-built
// Schema{Types: []string{}} did the same.
func TestValidateEmptyTypeArrayRejectsEverything(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
	}{
		"2020-12": {schema: `{"type": []}`},
		"draft-07": {
			schema: `{"$schema": "http://json-schema.org/draft-07/schema#", "type": []}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			require.NoError(t, err, "an empty type array compiles")

			for _, instance := range []string{`1`, `"s"`, `null`, `[]`, `{}`, `true`} {
				err = v.ValidateJSON(t.Context(), []byte(instance))
				require.Error(t, err, "an empty type list admits no instance: %s", instance)

				var verr *jsonschema.ValidationError

				require.ErrorAs(t, err, &verr)
				assert.Equal(t, jsonschema.KeywordType, verr.Keyword)
			}
		})
	}

	t.Run("hand-built empty Types", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{Types: []string{}})
		require.NoError(t, err)
		require.Error(t, v.Validate(t.Context(), 1))
	})
}

// TestValidateEmptyApplicatorArrayConstrainsNothing pins that an empty
// allOf, anyOf, oneOf, prefixItems, or Draft-07 items array, which the
// metaschema refuses, compiles and constrains nothing, as the absent keyword
// does: the Schema marshals an empty slice as the absent keyword, so no
// other reading survives a round trip. The empty type array is the one
// empty list with a reading of its own, pinned above.
func TestValidateEmptyApplicatorArrayConstrainsNothing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
	}{
		"allOf":       {schema: `{"allOf": []}`},
		"anyOf":       {schema: `{"anyOf": []}`},
		"oneOf":       {schema: `{"oneOf": []}`},
		"prefixItems": {schema: `{"prefixItems": []}`},
		"draft-07 items": {
			schema: `{"$schema": "http://json-schema.org/draft-07/schema#", "items": []}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			require.NoError(t, err, "an empty applicator array compiles")

			for _, instance := range []string{`1`, `"s"`, `null`, `[1]`, `{}`} {
				require.NoError(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"an empty applicator array constrains nothing: %s", instance)
			}
		})
	}

	t.Run("absent keyword still accepts", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), &jsonschema.Schema{})
		require.NoError(t, err)
		require.NoError(t, v.Validate(t.Context(), 1))
	})
}

// A metaschema may declare a vocabulary with false, marking it optional for
// implementations that do not recognize it (core section 8.1.2). This
// implementation recognizes every standard 2020-12 vocabulary, so the value
// has no impact here: a recognized vocabulary's keywords apply whenever its
// URI is listed. Only omitting the URI deactivates the group.
func TestValidateVocabularyDeclaredOptional(t *testing.T) {
	t.Parallel()

	metaFor := func(vocabs map[string]bool) jsonschema.ValidateOption {
		return jsonschema.WithMetaSchemaResolver(jsonschema.SchemaMap{
			"https://example.com/my-meta": {
				ID:         "https://example.com/my-meta",
				Vocabulary: vocabs,
			},
		})
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		vocabs   map[string]bool
		err      string
	}{
		"applicator declared false still applies properties": {
			schema: &jsonschema.Schema{
				Schema:     "https://example.com/my-meta",
				Properties: map[string]*jsonschema.Schema{"a": {Type: "string"}},
			},
			instance: map[string]any{"a": 5.0},
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabApplicator2020: false,
				jsonschema.VocabValidation2020: true,
			},
			err: "(type)",
		},
		"validation declared false still asserts type": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Type:   "string",
			},
			instance: 42.0,
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabValidation2020: false,
			},
			err: "(type)",
		},
		"format-assertion declared false still asserts format": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Format: "ipv4",
			},
			instance: "not-an-ip",
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:            true,
				jsonschema.VocabFormatAssertion2020: false,
			},
			err: "(format)",
		},
		"omitted validation vocabulary stays inactive": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Type:   "string",
			},
			instance: 42.0,
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabApplicator2020: false,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tt.schema, tt.instance, metaFor(tt.vocabs))
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

// TestApplicatorFalseSubschemaKeyword pins the error contract for a false
// subschema: the "value is not allowed" leaf carries the applicator keyword
// that applied it and the schema path of the false schema itself, so a
// consumer can tell an additionalProperties:false failure from a false
// property or item subschema without parsing SchemaPath. The walk reads the
// keyword off the schema location every descent records, so the case list
// here is the only place an applicator can be missed, and the closing
// assertion pins that list to the applicators keywordmeta declares. The three
// applicators that consume their child's verdict instead of surfacing its
// errors (contains, not, if) are recorded as non-surfacing cases, so a change
// in how one of them reports is a deliberate edit here. A standalone false
// root has no applicator and keeps an empty keyword.
func TestApplicatorFalseSubschemaKeyword(t *testing.T) {
	t.Parallel()

	const draft7 = `"http://json-schema.org/draft-07/schema#"`

	tests := map[string]struct {
		keyword      string
		schema       string
		instance     string
		schemaPath   jsontext.Pointer
		instancePath jsontext.Pointer
		// The surfaces flag is false for an applicator that consumes the
		// false subschema's verdict, so no "value is not allowed" leaf reaches
		// the caller.
		surfaces bool
	}{
		"additionalProperties": {
			keyword:      jsonschema.KeywordAdditionalProperties,
			schema:       `{"additionalProperties": false}`,
			instance:     `{"extra": 1}`,
			schemaPath:   "/additionalProperties",
			instancePath: "/extra",
			surfaces:     true,
		},
		"properties": {
			keyword:      jsonschema.KeywordProperties,
			schema:       `{"properties": {"forbidden": false}}`,
			instance:     `{"forbidden": 1}`,
			schemaPath:   "/properties/forbidden",
			instancePath: "/forbidden",
			surfaces:     true,
		},
		"patternProperties": {
			keyword:      jsonschema.KeywordPatternProperties,
			schema:       `{"patternProperties": {"^x-": false}}`,
			instance:     `{"x-a": 1}`,
			schemaPath:   "/patternProperties/^x-",
			instancePath: "/x-a",
			surfaces:     true,
		},
		"propertyNames": {
			keyword:      jsonschema.KeywordPropertyNames,
			schema:       `{"propertyNames": false}`,
			instance:     `{"k": 1}`,
			schemaPath:   "/propertyNames",
			instancePath: "/k",
			surfaces:     true,
		},
		"unevaluatedProperties": {
			keyword:      jsonschema.KeywordUnevaluatedProperties,
			schema:       `{"unevaluatedProperties": false}`,
			instance:     `{"extra": 1}`,
			schemaPath:   "/unevaluatedProperties",
			instancePath: "/extra",
			surfaces:     true,
		},
		"items 2020-12": {
			keyword:      jsonschema.KeywordItems,
			schema:       `{"items": false}`,
			instance:     `[1]`,
			schemaPath:   "/items",
			instancePath: "/0",
			surfaces:     true,
		},
		"items draft-07 array form": {
			keyword:      jsonschema.KeywordItems,
			schema:       `{"$schema": ` + draft7 + `, "items": [false]}`,
			instance:     `[1]`,
			schemaPath:   "/items/0",
			instancePath: "/0",
			surfaces:     true,
		},
		"additionalItems draft-07": {
			keyword:      jsonschema.KeywordAdditionalItems,
			schema:       `{"$schema": ` + draft7 + `, "items": [{}], "additionalItems": false}`,
			instance:     `[1, 2]`,
			schemaPath:   "/additionalItems",
			instancePath: "/1",
			surfaces:     true,
		},
		"prefixItems": {
			keyword:      jsonschema.KeywordPrefixItems,
			schema:       `{"prefixItems": [false]}`,
			instance:     `[1]`,
			schemaPath:   "/prefixItems/0",
			instancePath: "/0",
			surfaces:     true,
		},
		"unevaluatedItems": {
			keyword:      jsonschema.KeywordUnevaluatedItems,
			schema:       `{"unevaluatedItems": false}`,
			instance:     `[1]`,
			schemaPath:   "/unevaluatedItems",
			instancePath: "/0",
			surfaces:     true,
		},
		"contains": {
			keyword:  jsonschema.KeywordContains,
			schema:   `{"contains": false}`,
			instance: `[1]`,
		},
		"dependentSchemas": {
			keyword:    jsonschema.KeywordDependentSchemas,
			schema:     `{"dependentSchemas": {"a": false}}`,
			instance:   `{"a": 1}`,
			schemaPath: "/dependentSchemas/a",
			surfaces:   true,
		},
		"dependencies draft-07": {
			keyword:    jsonschema.KeywordDependencies,
			schema:     `{"$schema": ` + draft7 + `, "dependencies": {"a": false}}`,
			instance:   `{"a": 1}`,
			schemaPath: "/dependencies/a",
			surfaces:   true,
		},
		"allOf": {
			keyword:    jsonschema.KeywordAllOf,
			schema:     `{"allOf": [false]}`,
			instance:   `1`,
			schemaPath: "/allOf/0",
			surfaces:   true,
		},
		"anyOf": {
			keyword:    jsonschema.KeywordAnyOf,
			schema:     `{"anyOf": [false]}`,
			instance:   `1`,
			schemaPath: "/anyOf/0",
			surfaces:   true,
		},
		"oneOf": {
			keyword:    jsonschema.KeywordOneOf,
			schema:     `{"oneOf": [false]}`,
			instance:   `1`,
			schemaPath: "/oneOf/0",
			surfaces:   true,
		},
		"not": {
			keyword:  jsonschema.KeywordNot,
			schema:   `{"not": false}`,
			instance: `1`,
		},
		"if": {
			keyword:  jsonschema.KeywordIf,
			schema:   `{"if": false, "else": {"type": "string"}}`,
			instance: `1`,
		},
		"then": {
			keyword:    jsonschema.KeywordThen,
			schema:     `{"if": {}, "then": false}`,
			instance:   `1`,
			schemaPath: "/then",
			surfaces:   true,
		},
		"else": {
			keyword:    jsonschema.KeywordElse,
			schema:     `{"if": {"type": "string"}, "else": false}`,
			instance:   `1`,
			schemaPath: "/else",
			surfaces:   true,
		},
		"$ref": {
			keyword:    jsonschema.KeywordRef,
			schema:     `{"$ref": "#/$defs/never", "$defs": {"never": false}}`,
			instance:   `1`,
			schemaPath: "/$ref",
			surfaces:   true,
		},
		"$dynamicRef": {
			keyword:    jsonschema.KeywordDynamicRef,
			schema:     `{"$dynamicRef": "#/$defs/never", "$defs": {"never": false}}`,
			instance:   `1`,
			schemaPath: "/$dynamicRef",
			surfaces:   true,
		},
	}

	var covered []string

	for name, tc := range tests {
		covered = append(covered, tc.keyword)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			var instance any

			require.NoError(t, jsonv1.Unmarshal([]byte(tc.instance), &instance))

			err = v.Validate(t.Context(), instance)

			var (
				ve     *jsonschema.ValidationError
				leaves []*jsonschema.ValidationError
			)

			if errors.As(err, &ve) {
				leaves = falseSubschemaLeaves(ve)
			}

			if !tc.surfaces {
				assert.Empty(t, leaves, "the applicator consumes the false subschema's verdict")

				return
			}

			require.Error(t, err)
			require.Len(t, leaves, 1, "exactly one false-subschema leaf surfaces")
			assert.Equal(t, tc.keyword, leaves[0].Keyword, "the leaf carries the applying keyword")
			assert.Equal(t, tc.schemaPath, leaves[0].SchemaPath)
			assert.Equal(t, tc.instancePath, leaves[0].InstancePath)
			assert.Empty(t, leaves[0].Causes)
		})
	}

	t.Run("standalone false root keeps an empty keyword", func(t *testing.T) {
		t.Parallel()

		schema, err := jsonschema.ParseSchema([]byte(`false`))
		require.NoError(t, err)

		err = jsonschema.Validate(t.Context(), schema, "anything")

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)
		assert.Empty(t, ve.Keyword, "a root false schema has no applicator context")
		assert.Equal(t, "value is not allowed", ve.Message)
	})

	slices.Sort(covered)
	assert.Equal(t, keywordmeta.Names(keywordmeta.Applicators), slices.Compact(covered),
		"every applicator keywordmeta declares needs a false-subschema case")
}

// falseSubschemaLeaves collects every "value is not allowed" error in the
// tree under e, descending through causes. It walks the causes itself rather
// than through [jsonschema.ValidationError.Leaves], which treats a
// propertyNames error as a leaf and would hide the false-subschema error
// beneath it.
func falseSubschemaLeaves(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	var out []*jsonschema.ValidationError

	if e.Message == "value is not allowed" {
		out = append(out, e)
	}

	for _, cause := range e.Causes {
		out = append(out, falseSubschemaLeaves(cause)...)
	}

	return out
}

// TestValidateAdditionalPropertiesOrderWithoutPatterns pins the order of
// additionalProperties errors when no patternProperties sibling is set: the
// names outside properties are sorted among themselves, so the errors come
// in name order whatever order the object's members are read in.
func TestValidateAdditionalPropertiesOrderWithoutPatterns(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"kept": {Type: "string"},
		},
		AdditionalProperties: &jsonschema.Schema{Type: "string"},
	}

	err := jsonschema.Validate(t.Context(), schema, map[string]any{
		"kept":  "x",
		"zeta":  1.0,
		"alpha": 2.0,
		"mid":   "ok",
	})

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)

	var paths []string

	for _, cause := range verr.Causes {
		paths = append(paths, string(cause.InstancePath))
	}

	assert.Equal(t, []string{"/alpha", "/zeta"}, paths)
}

// TestValidateNumericIntegerPathMatchesRationals pins the machine-integer
// path of the numeric keywords against the rational one: an integer
// instance under integral bounds is checked in int64 arithmetic, and the
// same instance under the same bounds plus an upper bound outside int64
// range, which forces the rationals and can fail no instance the integer
// path judges, reports the same errors word for word. The expected messages
// pin the wording both paths share. No row sets both upper bounds, so the
// widening always has one free.
func TestValidateNumericIntegerPathMatchesRationals(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance jsonv1.Number
		want     []string
	}{
		"inside every bound": {
			schema: &jsonschema.Schema{
				MultipleOf: new(3.0), Minimum: new(-9.0), Maximum: new(9.0),
				ExclusiveMinimum: new(-9.0),
			},
			instance: "6",
		},
		"every bound violated": {
			schema: &jsonschema.Schema{
				MultipleOf: new(3.0), Minimum: new(-9.0), Maximum: new(-11.0),
				ExclusiveMinimum: new(-10.0),
			},
			instance: "-10",
			want: []string{
				"-10 is not a multiple of 3",
				"-10 is less than -9",
				"-10 is greater than -11",
				"-10 is less than or equal to -10",
			},
		},
		"exclusive maximum at the boundary": {
			schema:   &jsonschema.Schema{ExclusiveMaximum: new(5.0)},
			instance: "5",
			want:     []string{"5 is greater than or equal to 5"},
		},
		"integer spelled with an exponent": {
			schema:   &jsonschema.Schema{Minimum: new(100.0)},
			instance: "9.9e1",
			want:     []string{"99 is less than 100"},
		},
		"negative zero": {
			schema:   &jsonschema.Schema{ExclusiveMinimum: new(0.0)},
			instance: "-0",
			want:     []string{"0 is less than or equal to 0"},
		},
		"eighteen digit multiple": {
			schema:   &jsonschema.Schema{MultipleOf: new(17.0)},
			instance: "999999999999999999",
			want:     []string{"999999999999999999 is not a multiple of 17"},
		},
		"nineteen digit instance": {
			schema:   &jsonschema.Schema{Maximum: new(1.0)},
			instance: "9223372036854775807",
			want:     []string{"9223372036854775807 is greater than 1"},
		},
		"bound past the float64 integer range": {
			// The float64 nearest 1.0000000000000001e18 is 1000000000000000128,
			// but a bound compares as its shortest decimal, 1000000000000000100,
			// on the integer path as on the rational one.
			schema:   &jsonschema.Schema{Maximum: new(1.0000000000000001e18)},
			instance: "1000000000000000112",
			want:     []string{"1000000000000000112 is greater than 1.0000000000000001e+18"},
		},
		"fractional divisor": {
			schema:   &jsonschema.Schema{MultipleOf: new(0.5), Minimum: new(2.0)},
			instance: "1",
			want:     []string{"1 is less than 2"},
		},
		"fractional instance": {
			schema:   &jsonschema.Schema{MultipleOf: new(2.0), Maximum: new(1.0)},
			instance: "1.5",
			want:     []string{"1.5 is not a multiple of 2", "1.5 is greater than 1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := numericMessages(t, tc.schema, tc.instance)
			assert.Equal(t, tc.want, got)

			widened := *tc.schema
			if widened.ExclusiveMaximum == nil {
				widened.ExclusiveMaximum = new(1e19)
			} else {
				require.Nil(t, widened.Maximum)

				widened.Maximum = new(1e19)
			}

			assert.Equal(t, got, numericMessages(t, &widened, tc.instance))
		})
	}
}

// numericMessages returns the messages of the numeric keyword errors schema
// reports for instance, in report order, nil when it accepts the instance. A
// lone failure is the error itself; several are its causes.
func numericMessages(t *testing.T, schema *jsonschema.Schema, instance jsonv1.Number) []string {
	t.Helper()

	err := jsonschema.Validate(t.Context(), schema, instance)
	if err == nil {
		return nil
	}

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)

	if len(verr.Causes) == 0 {
		return []string{verr.Message}
	}

	msgs := make([]string, 0, len(verr.Causes))
	for _, cause := range verr.Causes {
		msgs = append(msgs, cause.Message)
	}

	return msgs
}
