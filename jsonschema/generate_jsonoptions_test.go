package jsonschema_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonopts"
)

// jsonOptionSamples holds the "set" form of every constructor
// internal/jsonopts classifies, keyed as the table keys them. A combinator
// (JoinOptions, DefaultOptionsV2) sets nothing on its own and has no sample.
// TestJSONOptionClassificationBehaviour drives each sample through the public
// API on the class the table assigns it, and fails when the two key sets
// differ, so a row added to the table gets a sample here.
var jsonOptionSamples = map[string]json.Options{
	// Package encoding/json/v2.
	"encoding/json/v2.FormatNilSliceAsNull": json.FormatNilSliceAsNull(true),
	"encoding/json/v2.FormatNilMapAsNull":   json.FormatNilMapAsNull(true),
	"encoding/json/v2.OmitZeroStructFields": json.OmitZeroStructFields(true),
	"encoding/json/v2.StringifyNumbers":     json.StringifyNumbers(true),
	"encoding/json/v2.WithMarshalers": json.WithMarshalers(
		json.MarshalFunc(func(time.Time) ([]byte, error) { return []byte(`""`), nil }),
	),
	"encoding/json/v2.Deterministic":             json.Deterministic(true),
	"encoding/json/v2.MatchCaseInsensitiveNames": json.MatchCaseInsensitiveNames(true),
	"encoding/json/v2.RejectUnknownMembers":      json.RejectUnknownMembers(true),
	"encoding/json/v2.WithUnmarshalers": json.WithUnmarshalers(
		json.UnmarshalFunc(func([]byte, *time.Time) error { return nil }),
	),
	"encoding/json/v2.DefaultOptionsV2": nil,
	"encoding/json/v2.JoinOptions":      nil,

	// Package encoding/json (v1 compat).
	"encoding/json.DefaultOptionsV1":                jsonv1.DefaultOptionsV1(),
	"encoding/json.OmitEmptyWithLegacySemantics":    jsonv1.OmitEmptyWithLegacySemantics(true),
	"encoding/json.FormatByteArrayAsArray":          jsonv1.FormatByteArrayAsArray(true),
	"encoding/json.FormatBytesWithLegacySemantics":  jsonv1.FormatBytesWithLegacySemantics(true),
	"encoding/json.StringifyWithLegacySemantics":    jsonv1.StringifyWithLegacySemantics(true),
	"encoding/json.CallMethodsWithLegacySemantics":  jsonv1.CallMethodsWithLegacySemantics(true),
	"encoding/json.FormatDurationAsNano":            jsonv1.FormatDurationAsNano(true),
	"encoding/json.ReportErrorsWithLegacySemantics": jsonv1.ReportErrorsWithLegacySemantics(true),
	"encoding/json.MatchCaseSensitiveDelimiter":     jsonv1.MatchCaseSensitiveDelimiter(true),
	"encoding/json.MergeWithLegacySemantics":        jsonv1.MergeWithLegacySemantics(true),
	"encoding/json.ParseBytesWithLooseRFC4648":      jsonv1.ParseBytesWithLooseRFC4648(true),
	"encoding/json.ParseTimeWithLooseRFC3339":       jsonv1.ParseTimeWithLooseRFC3339(true),
	"encoding/json.UnmarshalArrayFromAnyLength":     jsonv1.UnmarshalArrayFromAnyLength(true),

	// Package encoding/json/jsontext.
	"encoding/json/jsontext.AllowDuplicateNames":   jsontext.AllowDuplicateNames(true),
	"encoding/json/jsontext.AllowInvalidUTF8":      jsontext.AllowInvalidUTF8(true),
	"encoding/json/jsontext.CanonicalizeRawFloats": jsontext.CanonicalizeRawFloats(true),
	"encoding/json/jsontext.CanonicalizeRawInts":   jsontext.CanonicalizeRawInts(true),
	"encoding/json/jsontext.EscapeForHTML":         jsontext.EscapeForHTML(true),
	"encoding/json/jsontext.EscapeForJS":           jsontext.EscapeForJS(true),
	"encoding/json/jsontext.Multiline":             jsontext.Multiline(true),
	"encoding/json/jsontext.PreserveRawStrings":    jsontext.PreserveRawStrings(true),
	"encoding/json/jsontext.ReorderRawObjects":     jsontext.ReorderRawObjects(true),
	"encoding/json/jsontext.SpaceAfterColon":       jsontext.SpaceAfterColon(true),
	"encoding/json/jsontext.SpaceAfterComma":       jsontext.SpaceAfterComma(true),
	"encoding/json/jsontext.WithIndent":            jsontext.WithIndent("  "),
	"encoding/json/jsontext.WithIndentPrefix":      jsontext.WithIndentPrefix("\t"),
}

// schemaJSON generates the schema for T under opts and returns its marshaled
// form, so two runs compare as documents.
func schemaJSON[T any](t *testing.T, opts ...jsonschema.GenerateOption) string {
	t.Helper()

	schema, err := jsonschema.GenerateFor[T](t.Context(), opts...)
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	return string(out)
}

// TestJSONOptionClassificationBehaviour checks each table row's class through
// the public API: a refused option fails generation with
// ErrUnsupportedJSONOption, an ignored one leaves the schema identical to the
// no-option baseline, and an honored one changes it. The samples and the table
// must name the same constructors.
func TestJSONOptionClassificationBehaviour(t *testing.T) {
	t.Parallel()

	type payload struct {
		N int            `json:"n"`
		S []int          `json:"s"`
		M map[string]int `json:"m"`
	}

	keys := make([]string, 0, len(jsonopts.Table))
	for _, row := range jsonopts.Table {
		keys = append(keys, row.Key)
	}

	slices.Sort(keys)
	require.Equal(t, keys, slices.Sorted(maps.Keys(jsonOptionSamples)),
		"every table row needs a sample here, and every sample a row")

	baseline := schemaJSON[payload](t)

	for _, row := range jsonopts.Table {
		opt := jsonOptionSamples[row.Key]

		t.Run(row.Key, func(t *testing.T) {
			t.Parallel()

			if row.Class == jsonopts.ClassCombinator {
				assert.Nil(t, opt, "a combinator row sets nothing")

				return
			}

			require.NotNil(t, opt, "every non-combinator row carries its set form")

			if row.Set != nil {
				assert.True(t, row.Set(opt), "the row's probe must recognize its own set form")
			}

			switch row.Class {
			case jsonopts.ClassRefused:
				_, err := jsonschema.GenerateFor[payload](t.Context(), jsonschema.WithJSONOptions(opt))
				require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)

				// The refusal surfaces from a reusable Generator's runs too.
				gen := jsonschema.NewGenerator(jsonschema.WithJSONOptions(opt))
				_, err = jsonschema.GenerateWith[payload](t.Context(), gen)
				require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)

			case jsonopts.ClassIgnored:
				assert.Equal(t, baseline, schemaJSON[payload](t, jsonschema.WithJSONOptions(opt)),
					"an ignored option must leave the schema untouched")

			case jsonopts.ClassHonored:
				assert.NotEqual(t, baseline, schemaJSON[payload](t, jsonschema.WithJSONOptions(opt)),
					"an honored option must change the schema (its exact effect is pinned elsewhere)")

			case jsonopts.ClassCombinator:
				t.Fatal("unreachable: combinators return above")
			}
		})
	}

	t.Run("a later call can unset the refusal", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[payload](t.Context(),
			jsonschema.WithJSONOptions(json.StringifyNumbers(true)),
			jsonschema.WithJSONOptions(json.StringifyNumbers(false)),
		)
		assert.NoError(t, err)
	})
}

// TestNilContainersNullUnderJSONOptions pins the accept property directly:
// under the format options, whatever json.Marshal writes for a value with nil
// containers -- bare fields, elements, named slice types shared between a
// bare and a pointer occurrence -- validates against the schema generated
// under the same options.
func TestNilContainersNullUnderJSONOptions(t *testing.T) {
	t.Parallel()

	type named []int

	type payload struct {
		Slice  []int          `json:"slice"`
		Map    map[string]int `json:"map"`
		Bytes  []byte         `json:"bytes"`
		Nested [][]int        `json:"nested"`
		Named  named          `json:"named"`
		Ptr    *named         `json:"ptr"`
	}

	opts := []json.Options{json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true)}

	schema, err := jsonschema.GenerateFor[payload](t.Context(), jsonschema.WithJSONOptions(opts...))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), schema)
	require.NoError(t, err)

	for name, instance := range map[string]payload{
		"all nil":       {},
		"nested nil":    {Nested: [][]int{nil, {1}}},
		"all populated": {Slice: []int{1}, Map: map[string]int{"a": 1}, Bytes: []byte("x"), Named: named{2}, Ptr: &named{3}},
	} {
		data, err := json.Marshal(instance, opts...)
		require.NoError(t, err)
		assert.NoError(t, v.ValidateJSON(t.Context(), data), "%s: %s", name, data)
	}

	// Non-vacuity: without the options the same nil-container marshal writes
	// null, which the default-options schema must reject.
	plain, err := jsonschema.GenerateFor[payload](t.Context())
	require.NoError(t, err)

	pv, err := jsonschema.Compile(t.Context(), plain)
	require.NoError(t, err)

	data, err := json.Marshal(payload{}, opts...)
	require.NoError(t, err)
	assert.Error(t, pv.ValidateJSON(t.Context(), data),
		"the default-options schema must reject null containers")
}

// stancedSlice and stancedMap are the named containers a NullForbidden stance
// covers in TestNilContainerNullPrecedence.
type (
	stancedSlice []int
	stancedMap   map[string]int
)

// TestNilContainerNullPrecedence pins that a NullForbidden stance on a
// container type wins over the format flag that would otherwise make every
// occurrence of it admit null.
func TestNilContainerNullPrecedence(t *testing.T) {
	t.Parallel()

	type payload struct {
		Slice stancedSlice `json:"slice"`
		Map   stancedMap   `json:"map"`
	}

	schema, err := jsonschema.GenerateFor[payload](t.Context(),
		forbidNullStance[stancedSlice](),
		forbidNullStance[stancedMap](),
		jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true)),
	)
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "null",
		"a NullForbidden stance drops the null the format flags add")
}

func TestOmitZeroDropsRequired(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	schema, err := jsonschema.GenerateFor[payload](t.Context(),
		jsonschema.WithJSONOptions(json.OmitZeroStructFields(true)))
	require.NoError(t, err)
	assert.Empty(t, schema.Required)

	// Non-vacuity: the default-options schema requires both fields.
	plain, err := jsonschema.GenerateFor[payload](t.Context())
	require.NoError(t, err)
	assert.Len(t, plain.Required, 2)

	// The accept property holds: an all-zero value marshals to {} under the
	// option and must validate.
	v, err := jsonschema.Compile(t.Context(), schema)
	require.NoError(t, err)

	data, err := json.Marshal(payload{}, json.OmitZeroStructFields(true))
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), data))
}

// TestTagNullLiteralOnBareSliceUnderJSONOptions pins the tag-interpretation
// consistency of the container-null decision: a tag's null default on a bare
// slice is refused where the occurrence admits no null, and lands where
// FormatNilSliceAsNull makes the same occurrence admit it, so the tag
// machinery and the generator answer nullability from one decision.
func TestTagNullLiteralOnBareSliceUnderJSONOptions(t *testing.T) {
	t.Parallel()

	type payload struct {
		S []int `json:"s" jsonschema:"default=null"`
	}

	_, err := jsonschema.GenerateFor[payload](t.Context())
	require.Error(t, err, "a null default on a bare slice admits no null by default")

	schema, err := jsonschema.GenerateFor[payload](t.Context(),
		jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true)))
	require.NoError(t, err)

	prop := schema.Properties["s"]
	require.NotNil(t, prop)
	assert.Equal(t, "null", string(prop.Default))
}

func TestDefaultsFromSeedsUnderJSONOptions(t *testing.T) {
	t.Parallel()

	type payload struct {
		Slice []int `json:"slice"`
	}

	schema, err := jsonschema.GenerateFor[payload](t.Context(),
		jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true)),
		jsonschema.WithDefaultsFrom(payload{}),
	)
	require.NoError(t, err)

	prop := schema.Properties["slice"]
	require.NotNil(t, prop)
	assert.Equal(t, "null", string(prop.Default),
		"a nil slice seeds the null the configured marshal writes")
}
