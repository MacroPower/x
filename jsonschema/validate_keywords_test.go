package jsonschema_test

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
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

// TestValidateDependenciesFalseSchemaKeyword locks in the error contract for a
// boolean-false dependency subschema: like every other applicator call site,
// the dependency keywords stamp their keyword on the false-schema leaf, so a
// consumer branching on ValidationError.Keyword sees dependentSchemas (or the
// legacy dependencies) rather than an empty keyword.
func TestValidateDependenciesFalseSchemaKeyword(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema     string
		keyword    string
		schemaPath jsontext.Pointer
	}{
		"dependentSchemas false subschema": {
			schema:     `{"dependentSchemas": {"a": false}}`,
			keyword:    jsonschema.KeywordDependentSchemas,
			schemaPath: "/dependentSchemas/a",
		},
		"legacy dependencies false subschema": {
			schema:     `{"$schema": "http://json-schema.org/draft-07/schema#", "dependencies": {"a": false}}`,
			keyword:    jsonschema.KeywordDependencies,
			schemaPath: "/dependencies/a",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), map[string]any{"a": 1.0})
			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword,
				"the false-schema leaf must carry the applying dependency keyword")
			assert.Equal(t, tc.schemaPath, ve.SchemaPath)
			assert.Equal(t, "value is not allowed", ve.Message)

			require.NoError(t, v.Validate(t.Context(), map[string]any{"b": 1.0}),
				"an absent trigger property leaves the dependency inert")
		})
	}
}

// TestValidateUnknownFormatUnderFormatAssertionVocabulary locks in the 2020-12
// requirement (validation section 7.2.3) that an implementation fails on
// unknown formats when the format-assertion vocabulary is specified: with
// assertion driven by that vocabulary, a format name with no registered
// checker rejects every string instance. The WithFormats(true) opt-in and
// Draft-07's default assertion are the package's own contracts and stay
// lenient for unknown names.
func TestValidateUnknownFormatUnderFormatAssertionVocabulary(t *testing.T) {
	t.Parallel()

	assertionVocabs := jsonschema.WithVocabularies(
		jsonschema.VocabCore2020,
		jsonschema.VocabValidation2020,
		jsonschema.VocabApplicator2020,
		jsonschema.VocabFormatAssertion2020,
	)

	tests := map[string]struct {
		schema   *jsonschema.Schema
		opts     []jsonschema.ValidateOption
		instance any
		valid    bool
		keyword  string
	}{
		"unknown format fails under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "x",
			keyword:  jsonschema.KeywordFormat,
		},
		"known format still asserted under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "ipv4"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "not-an-ip",
			keyword:  jsonschema.KeywordFormat,
		},
		"known format accepts a conforming instance under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "ipv4"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "192.168.0.1",
			valid:    true,
		},
		"unknown format ignores non-string instances": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: 42.0,
			valid:    true,
		},
		"unknown format stays lenient under WithFormats force": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
			instance: "x",
			valid:    true,
		},
		"WithFormats force overrides the vocabulary-driven strictness": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs, jsonschema.WithFormats(true)},
			instance: "x",
			valid:    true,
		},
		"unknown format stays lenient under draft-07 default assertion": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Format: "no-such-format",
			},
			instance: "x",
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tc.schema, tc.instance, tc.opts...)
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword)
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
