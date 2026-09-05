package jsonschema_test

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestUnsupportedJSONOptionRefused(t *testing.T) {
	t.Parallel()

	type payload struct {
		N int `json:"n"`
	}

	tests := map[string]struct {
		opt json.Options
	}{
		"StringifyNumbers": {opt: json.StringifyNumbers(true)},
		"WithMarshalers": {opt: json.WithMarshalers(
			json.MarshalFunc(func(time.Time) ([]byte, error) { return []byte(`""`), nil }),
		)},
		// The v1 compat options change the marshaled shape too (a legacy
		// omitempty drops fields v2 keeps, the byte-array form swaps base64
		// for a number array), so each must be refused rather than silently
		// generating a schema that rejects the configured marshal's output.
		// DefaultOptionsV1 bundles them all.
		"OmitEmptyWithLegacySemantics":   {opt: jsonv1.OmitEmptyWithLegacySemantics(true)},
		"FormatByteArrayAsArray":         {opt: jsonv1.FormatByteArrayAsArray(true)},
		"FormatBytesWithLegacySemantics": {opt: jsonv1.FormatBytesWithLegacySemantics(true)},
		"StringifyWithLegacySemantics":   {opt: jsonv1.StringifyWithLegacySemantics(true)},
		"CallMethodsWithLegacySemantics": {opt: jsonv1.CallMethodsWithLegacySemantics(true)},
		"FormatDurationAsNano":           {opt: jsonv1.FormatDurationAsNano(true)},
		"DefaultOptionsV1 bundle":        {opt: jsonv1.DefaultOptionsV1()},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.GenerateFor[payload](t.Context(), jsonschema.WithJSONOptions(tt.opt))
			require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)

			// The refusal surfaces from a reusable Generator's runs too.
			gen := jsonschema.NewGenerator(jsonschema.WithJSONOptions(tt.opt))
			_, err = jsonschema.GenerateWith[payload](t.Context(), gen)
			require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)
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

func TestNilContainerNullPrecedence(t *testing.T) {
	t.Parallel()

	type payload struct {
		Slice []int          `json:"slice"`
		Map   map[string]int `json:"map"`
		Ptr   *int           `json:"ptr"`
	}

	schema, err := jsonschema.GenerateFor[payload](t.Context(),
		jsonschema.WithNullable(false),
		jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true)),
	)
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "null",
		"WithNullable(false) stays the kill-switch: no null branch anywhere")
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
