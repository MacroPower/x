package jsonschema_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
)

// jsonOptionClass says what WithJSONOptions does with one option constructor.
type jsonOptionClass int

const (
	// An honored option changes the generated schema; the behavior tests
	// further down this file pin its exact effect.
	honoredOption jsonOptionClass = iota
	// A refused option makes generation fail with ErrUnsupportedJSONOption.
	refusedOption
	// An ignored option leaves the schema identical to the no-option baseline.
	ignoredOption
	// A combinator sets nothing on its own (JoinOptions, DefaultOptionsV2).
	optionCombinator
)

var (
	// The toolchain packages that export Options constructors, as import
	// paths under GOROOT/src.
	jsonOptionPackages = []string{"encoding/json", "encoding/json/v2", "encoding/json/jsontext"}

	// Every exported Options constructor in encoding/json, encoding/json/v2,
	// and encoding/json/jsontext, keyed by import path and name.
	// TestJSONOptionClassificationCoversToolchain fails when the toolchain's
	// set differs, so a new constructor has to be classified here (and, if it
	// changes the marshaled shape, refused in WithJSONOptions) before a
	// toolchain bump passes. The opt column holds the "set" form of the option
	// and is nil for a combinator.
	jsonOptionClassification = map[string]struct {
		class jsonOptionClass
		opt   json.Options
	}{
		// Package encoding/json/v2.
		"encoding/json/v2.FormatNilSliceAsNull": {honoredOption, json.FormatNilSliceAsNull(true)},
		"encoding/json/v2.FormatNilMapAsNull":   {honoredOption, json.FormatNilMapAsNull(true)},
		"encoding/json/v2.OmitZeroStructFields": {honoredOption, json.OmitZeroStructFields(true)},
		// StringifyNumbers stringifies numbers inside containers, beyond what the
		// per-field ,string tag machinery reaches, and a marshaler's output shape
		// is unknowable.
		"encoding/json/v2.StringifyNumbers": {refusedOption, json.StringifyNumbers(true)},
		"encoding/json/v2.WithMarshalers": {refusedOption, json.WithMarshalers(
			json.MarshalFunc(func(time.Time) ([]byte, error) { return []byte(`""`), nil }),
		)},
		"encoding/json/v2.Deterministic":             {ignoredOption, json.Deterministic(true)},
		"encoding/json/v2.MatchCaseInsensitiveNames": {ignoredOption, json.MatchCaseInsensitiveNames(true)},
		"encoding/json/v2.RejectUnknownMembers":      {ignoredOption, json.RejectUnknownMembers(true)},
		"encoding/json/v2.WithUnmarshalers": {ignoredOption, json.WithUnmarshalers(
			json.UnmarshalFunc(func([]byte, *time.Time) error { return nil }),
		)},
		"encoding/json/v2.DefaultOptionsV2": {optionCombinator, nil},
		"encoding/json/v2.JoinOptions":      {optionCombinator, nil},

		// Package encoding/json (v1 compat). The refused rows change the marshaled shape
		// too (a legacy omitempty drops fields v2 keeps, the byte-array form swaps
		// base64 for a number array), so each must be refused rather than silently
		// generating a schema that rejects the configured marshal's output.
		// ReportErrorsWithLegacySemantics makes v2 marshal declarations generation
		// refuses (a ,string tag on a bool, conflicting names, a tagged unexported
		// field), so it is refused as well. DefaultOptionsV1 bundles them all.
		"encoding/json.DefaultOptionsV1":                {refusedOption, jsonv1.DefaultOptionsV1()},
		"encoding/json.OmitEmptyWithLegacySemantics":    {refusedOption, jsonv1.OmitEmptyWithLegacySemantics(true)},
		"encoding/json.FormatByteArrayAsArray":          {refusedOption, jsonv1.FormatByteArrayAsArray(true)},
		"encoding/json.FormatBytesWithLegacySemantics":  {refusedOption, jsonv1.FormatBytesWithLegacySemantics(true)},
		"encoding/json.StringifyWithLegacySemantics":    {refusedOption, jsonv1.StringifyWithLegacySemantics(true)},
		"encoding/json.CallMethodsWithLegacySemantics":  {refusedOption, jsonv1.CallMethodsWithLegacySemantics(true)},
		"encoding/json.FormatDurationAsNano":            {refusedOption, jsonv1.FormatDurationAsNano(true)},
		"encoding/json.ReportErrorsWithLegacySemantics": {refusedOption, jsonv1.ReportErrorsWithLegacySemantics(true)},
		// The remaining v1 rows affect unmarshaling only.
		"encoding/json.MatchCaseSensitiveDelimiter": {ignoredOption, jsonv1.MatchCaseSensitiveDelimiter(true)},
		"encoding/json.MergeWithLegacySemantics":    {ignoredOption, jsonv1.MergeWithLegacySemantics(true)},
		"encoding/json.ParseBytesWithLooseRFC4648":  {ignoredOption, jsonv1.ParseBytesWithLooseRFC4648(true)},
		"encoding/json.ParseTimeWithLooseRFC3339":   {ignoredOption, jsonv1.ParseTimeWithLooseRFC3339(true)},
		"encoding/json.UnmarshalArrayFromAnyLength": {ignoredOption, jsonv1.UnmarshalArrayFromAnyLength(true)},

		// Package encoding/json/jsontext. Whitespace, escaping, raw-value re-encoding,
		// and decoder tolerance never change the JSON value a marshal writes.
		"encoding/json/jsontext.AllowDuplicateNames":   {ignoredOption, jsontext.AllowDuplicateNames(true)},
		"encoding/json/jsontext.AllowInvalidUTF8":      {ignoredOption, jsontext.AllowInvalidUTF8(true)},
		"encoding/json/jsontext.CanonicalizeRawFloats": {ignoredOption, jsontext.CanonicalizeRawFloats(true)},
		"encoding/json/jsontext.CanonicalizeRawInts":   {ignoredOption, jsontext.CanonicalizeRawInts(true)},
		"encoding/json/jsontext.EscapeForHTML":         {ignoredOption, jsontext.EscapeForHTML(true)},
		"encoding/json/jsontext.EscapeForJS":           {ignoredOption, jsontext.EscapeForJS(true)},
		"encoding/json/jsontext.Multiline":             {ignoredOption, jsontext.Multiline(true)},
		"encoding/json/jsontext.PreserveRawStrings":    {ignoredOption, jsontext.PreserveRawStrings(true)},
		"encoding/json/jsontext.ReorderRawObjects":     {ignoredOption, jsontext.ReorderRawObjects(true)},
		"encoding/json/jsontext.SpaceAfterColon":       {ignoredOption, jsontext.SpaceAfterColon(true)},
		"encoding/json/jsontext.SpaceAfterComma":       {ignoredOption, jsontext.SpaceAfterComma(true)},
		"encoding/json/jsontext.WithIndent":            {ignoredOption, jsontext.WithIndent("  ")},
		"encoding/json/jsontext.WithIndentPrefix":      {ignoredOption, jsontext.WithIndentPrefix("\t")},
	}
)

// toolchainJSONOptionConstructors parses the option packages under
// build.Default.GOROOT and returns "importpath.Name" for every exported
// top-level func whose sole result is the Options identifier. Parsing rather
// than importing sidesteps the goexperiment.jsonv2 build tag, and skips a
// constructor that exists only as commented-out source. A missing GOROOT/src
// fails the caller rather than skipping it, since a skip would defeat the guard.
func toolchainJSONOptionConstructors(t *testing.T) map[string]struct{} {
	t.Helper()

	found := map[string]struct{}{}

	for _, pkg := range jsonOptionPackages {
		dir := filepath.Join(build.Default.GOROOT, "src", pkg)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err, "the guard needs the toolchain source under GOROOT/src")

		fset := token.NewFileSet()

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			require.NoError(t, err)

			for _, decl := range file.Decls {
				if ctor, ok := optionsConstructorName(decl); ok {
					found[pkg+"."+ctor] = struct{}{}
				}
			}
		}
	}

	return found
}

// optionsConstructorName returns the name of decl when it is an exported
// top-level func with no receiver whose sole result is the Options identifier.
func optionsConstructorName(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil || !fn.Name.IsExported() ||
		fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return "", false
	}

	result, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	if !ok || result.Name != "Options" {
		return "", false
	}

	return fn.Name.Name, true
}

// TestJSONOptionClassificationCoversToolchain pins the classification table to
// the constructor set the toolchain exports, in both directions.
func TestJSONOptionClassificationCoversToolchain(t *testing.T) {
	t.Parallel()

	want := slices.Sorted(maps.Keys(toolchainJSONOptionConstructors(t)))
	got := slices.Sorted(maps.Keys(jsonOptionClassification))
	assert.Equal(t, want, got, "classify every Options constructor the toolchain exports")
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

// TestJSONOptionClassificationBehaviour checks each row's class through the
// public API: a refused option fails generation with ErrUnsupportedJSONOption,
// an ignored one leaves the schema identical to the no-option baseline, and an
// honored one changes it.
func TestJSONOptionClassificationBehaviour(t *testing.T) {
	t.Parallel()

	type payload struct {
		N int            `json:"n"`
		S []int          `json:"s"`
		M map[string]int `json:"m"`
	}

	baseline := schemaJSON[payload](t)

	for name, row := range jsonOptionClassification {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if row.class == optionCombinator {
				assert.Nil(t, row.opt, "a combinator row sets nothing")

				return
			}

			require.NotNil(t, row.opt, "every non-combinator row carries its set form")

			switch row.class {
			case refusedOption:
				_, err := jsonschema.GenerateFor[payload](t.Context(), jsonschema.WithJSONOptions(row.opt))
				require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)

				// The refusal surfaces from a reusable Generator's runs too.
				gen := jsonschema.NewGenerator(jsonschema.WithJSONOptions(row.opt))
				_, err = jsonschema.GenerateWith[payload](t.Context(), gen)
				require.ErrorIs(t, err, jsonschema.ErrUnsupportedJSONOption)

			case ignoredOption:
				assert.Equal(t, baseline, schemaJSON[payload](t, jsonschema.WithJSONOptions(row.opt)),
					"an ignored option must leave the schema untouched")

			case honoredOption:
				assert.NotEqual(t, baseline, schemaJSON[payload](t, jsonschema.WithJSONOptions(row.opt)),
					"an honored option must change the schema (its exact effect is pinned elsewhere)")

			case optionCombinator:
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
