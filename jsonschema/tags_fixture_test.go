package jsonschema_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// The cross-dialect fixture table. Tag behavior was pinned one scenario per
// test function with an inline JSONEq, which records what a rule produces but
// not what the produced schema then accepts, and makes a reversal a Go diff
// rather than a data diff. Each row here carries a field shape, the spelling
// each dialect gives one rule, and the instances the resulting schema must
// accept and reject, so the two dialects are compared on verdicts rather than
// on schema text alone.

// fixturePath is the table's location, one file so a row's neighbors are
// visible when it changes.
const fixturePath = "testdata/tags/cases.json"

// tagInstance is one instance a row's schema must accept or reject.
type tagInstance struct {
	JSON json.RawMessage `json:"json"`
	Want string          `json:"want"`
}

// tagCase is one row: a field shape, the rule as each dialect spells it, and
// what the result must do. Either dialect may be absent, and either may state
// an expected error instead of a schema, naming the sentinel it carries or
// anyGenerationError where the interpreter raises a plain error with none.
type tagCase struct {
	Name            string          `json:"name"`
	Note            string          `json:"note"`
	Type            string          `json:"type"`
	JSON            string          `json:"json"`
	JSONSchema      string          `json:"jsonschema"`
	Validate        string          `json:"validate"`
	Schema          json.RawMessage `json:"schema"`
	JSONSchemaError string          `json:"jsonschemaError"`
	ValidateError   string          `json:"validateError"`
	Instances       []tagInstance   `json:"instances"`
}

// fixtureTypes resolves a row's type name. A row names a shape rather than
// spelling a Go type, so the table stays data and the set of shapes it can
// reach is reviewable in one place.
func fixtureTypes() map[string]reflect.Type {
	return map[string]reflect.Type{
		"string":         reflect.TypeFor[string](),
		"int":            reflect.TypeFor[int](),
		"int8":           reflect.TypeFor[int8](),
		"float32":        reflect.TypeFor[float32](),
		"float64":        reflect.TypeFor[float64](),
		"bool":           reflect.TypeFor[bool](),
		"*string":        reflect.TypeFor[*string](),
		"*int":           reflect.TypeFor[*int](),
		"*bool":          reflect.TypeFor[*bool](),
		"[]string":       reflect.TypeFor[[]string](),
		"[]int8":         reflect.TypeFor[[]int8](),
		"[][]string":     reflect.TypeFor[[][]string](),
		"map[string]int": reflect.TypeFor[map[string]int](),
		"[]byte":         reflect.TypeFor[[]byte](),
		"RawMessage":     reflect.TypeFor[json.RawMessage](),
		"time.Time":      reflect.TypeFor[time.Time](),
		"textLevel":      reflect.TypeFor[crossLevel](),
		"[]textLevel":    reflect.TypeFor[[]crossLevel](),
	}
}

// anyGenerationError is the error name a row uses where generation must fail
// but the interpreter raises no sentinel to match on. Naming the gap is what
// keeps the row from silently asserting less than the others; a row states it
// only where no sentinel exists.
const anyGenerationError = "any"

// fixtureErrors resolves a row's expected sentinel, so a row states which error
// it means rather than matching on message text.
func fixtureErrors() map[string]error {
	return map[string]error{
		"ErrUnsupported":            tagmodel.ErrUnsupported,
		"ErrConstraintConflict":     jsonschema.ErrConstraintConflict,
		"ErrConflictingConstraints": validate.ErrConflictingConstraints,
		"ErrInvalidType":            jsonschema.ErrInvalidType,
	}
}

// loadTagCases reads the table.
func loadTagCases(t *testing.T) []tagCase {
	t.Helper()

	data, err := os.ReadFile(filepath.Clean(fixturePath))
	require.NoError(t, err)

	var cases []tagCase

	require.NoError(t, json.Unmarshal(data, &cases))
	require.NotEmpty(t, cases)

	return cases
}

// dialects returns the row's spellings keyed by tag key, skipping a dialect the
// row leaves unstated.
func (tc tagCase) dialects() map[string]string {
	out := make(map[string]string, 2)

	if tc.JSONSchema != "" {
		out["jsonschema"] = tc.JSONSchema
	}

	if tc.Validate != "" {
		out["validate"] = tc.Validate
	}

	return out
}

// wantError returns the sentinel name the row expects from a dialect, or "".
func (tc tagCase) wantError(key string) string {
	if key == "jsonschema" {
		return tc.JSONSchemaError
	}

	return tc.ValidateError
}

// TestTagFixtures runs every row through each dialect it states: generation
// succeeds or reports the named sentinel, the property schema matches the row's
// expectation, and the compiled schema returns the row's verdict on each
// instance.
func TestTagFixtures(t *testing.T) {
	t.Parallel()

	types := fixtureTypes()
	sentinels := fixtureErrors()

	for _, tc := range loadTagCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			typ, ok := types[tc.Type]
			require.True(t, ok, "unknown type name %q", tc.Type)
			require.NotEmpty(t, tc.dialects(), "a row must state at least one dialect")

			for key, spelling := range tc.dialects() {
				t.Run(key, func(t *testing.T) {
					t.Parallel()

					s, err := generateOneField(t, typ, tc.JSON, key, spelling)

					if name := tc.wantError(key); name != "" {
						require.Error(t, err, "%s:%q must fail", key, spelling)

						if name == anyGenerationError {
							return
						}

						sentinel, ok := sentinels[name]
						require.True(t, ok, "unknown sentinel %q", name)
						require.ErrorIs(t, err, sentinel)

						return
					}

					require.NoError(t, err, "%s:%q", key, spelling)
					assertFixtureSchema(t, tc, s)
					assertFixtureVerdicts(t, tc, s)
				})
			}
		})
	}
}

// assertFixtureSchema checks the row's expected property schema, which pins
// what the rule produced rather than only what it accepts. A row carrying both
// dialects asserts it against both, which is what makes the verdict agreement
// below a statement about one rule rather than two.
func assertFixtureSchema(t *testing.T, tc tagCase, s *jsonschema.Schema) {
	t.Helper()

	if len(tc.Schema) == 0 {
		return
	}

	got, err := json.Marshal(s.Properties["v"])
	require.NoError(t, err)
	assert.JSONEq(t, string(tc.Schema), string(got))
}

// assertFixtureVerdicts compiles the row's schema and checks each instance.
func assertFixtureVerdicts(t *testing.T, tc tagCase, s *jsonschema.Schema) {
	t.Helper()

	if len(tc.Instances) == 0 {
		return
	}

	validator, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	for _, inst := range tc.Instances {
		doc, err := json.Marshal(map[string]json.RawMessage{"v": inst.JSON})
		require.NoError(t, err)

		err = validator.ValidateJSON(t.Context(), doc)

		switch inst.Want {
		case "accept":
			require.NoError(t, err, "instance %s must be accepted", inst.JSON)
		case "reject":
			require.Error(t, err, "instance %s must be rejected", inst.JSON)
		default:
			t.Fatalf("instance %s: want must be accept or reject, got %q", inst.JSON, inst.Want)
		}
	}
}

// TestTagFixturesCrossDialectVerdictsAgree is the data-driven form of the
// equivalence guard: where a row states both spellings of one rule, the two
// generated schemas must return the same verdict on every instance.
//
// The two dialects interpret one constraint vocabulary, and every divergence
// between them was a place one of them was silently wrong. Comparing verdicts
// rather than schema text is what makes the row state the behavior a user sees.
func TestTagFixturesCrossDialectVerdictsAgree(t *testing.T) {
	t.Parallel()

	types := fixtureTypes()
	paired := 0

	for _, tc := range loadTagCases(t) {
		if tc.JSONSchema == "" || tc.Validate == "" || len(tc.Instances) == 0 {
			continue
		}

		if tc.JSONSchemaError != "" || tc.ValidateError != "" {
			continue
		}

		paired++

		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			verdicts := make(map[string][]bool, 2)

			for key, spelling := range tc.dialects() {
				s, err := generateOneField(t, types[tc.Type], tc.JSON, key, spelling)
				require.NoError(t, err, "%s:%q", key, spelling)

				validator, err := jsonschema.Compile(t.Context(), s)
				require.NoError(t, err)

				for _, inst := range tc.Instances {
					doc, err := json.Marshal(map[string]json.RawMessage{"v": inst.JSON})
					require.NoError(t, err)

					verdicts[key] = append(verdicts[key],
						validator.ValidateJSON(t.Context(), doc) == nil)
				}
			}

			assert.Equal(t, verdicts["jsonschema"], verdicts["validate"],
				"jsonschema:%q and validate:%q must accept and reject the same instances",
				tc.JSONSchema, tc.Validate)
		})
	}

	assert.Positive(t, paired, "no row pairs the two dialects, so the guard asserts nothing")
}

// TestTagFixturesCoverage keeps the table honest: a row name is unique, every
// row states a note naming the fix it pins, and every shape the type registry
// offers is exercised by at least one row.
func TestTagFixturesCoverage(t *testing.T) {
	t.Parallel()

	cases := loadTagCases(t)
	seen := make(map[string]bool, len(cases))
	used := make(map[string]bool, len(cases))

	for _, tc := range cases {
		assert.False(t, seen[tc.Name], "duplicate row name %q", tc.Name)

		seen[tc.Name] = true
		used[tc.Type] = true

		assert.NotEmpty(t, tc.Note, "row %q states no note naming what it pins", tc.Name)
	}

	for name := range fixtureTypes() {
		assert.True(t, used[name], "the type registry offers %q but no row uses it", name)
	}

	named := make(map[string]bool, len(cases))
	for _, tc := range cases {
		for _, key := range []string{"jsonschema", "validate"} {
			if name := tc.wantError(key); name != "" {
				named[name] = true
			}
		}
	}

	for name := range fixtureErrors() {
		assert.True(t, named[name], "the sentinel registry offers %q but no row names it", name)
	}
}
