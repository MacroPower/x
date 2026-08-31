package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// The tests below exercise encoding/json name resolution across a composed
// embed's boundary. Composed embeds join the field walk as ghost subtrees, so
// their promoted names must compete exactly as encoding/json's flat walk
// resolves them.

// TestGhostEmbedInternalAnnihilation pins that a JSON name annihilated inside
// a composed embed still competes in the outer resolution. The encoding/json
// walk sees X at depth 2 three times (F.A.X, F.B.X, P.Q.X) and annihilates
// them all, so the marshaled object carries no X; a replay of the embed's
// resolved winners would miss the two sightings inside F, let P.Q.X win, and
// emit a required X property the marshaled object never has.
func TestGhostEmbedInternalAnnihilation(t *testing.T) {
	t.Parallel()

	type ghostFA struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"`
	}

	type ghostFB struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"` //nolint:govet // The duplicate json name is the annihilation under test.
	}

	type ghostF struct {
		ghostFA //nolint:unused // Embedded only; exercised via reflection.
		ghostFB //nolint:unused,govet // The duplicate json name is the annihilation under test.

		Y int `json:"Y"`
	}

	type ghostQ struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"` //nolint:govet // The duplicate json name is the annihilation under test.
	}

	type ghostP struct { //nolint:unused // Embedded only; exercised via reflection.
		ghostQ //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostTop struct {
		ghostF //nolint:unused // Embedded only; exercised via reflection.
		ghostP //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostTop](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostF](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "object"},
		}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostTop{
		ghostF: ghostF{Y: 3},
		Z:      5,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"Y": 3, "Z": 5}`, string(raw),
		"encoding/json annihilates X at depth 2")

	require.NotContains(t, s.Properties, "X",
		"an annihilated name never appears in the marshaled object")
	require.NotContains(t, s.Required, "X")

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the generated schema must accept the type's own marshaled JSON")
}

// TestGhostDupDoesNotPropagateIntoNestedEmbed pins that a struct embedded
// twice at one depth annihilates only its direct fields: encoding/json queues
// a nested embed once, so the nested embed's promoted fields survive. W is
// promoted once through T2 even though A2 appears twice, so the composed T2
// branch must stay unconditional -- a self-annihilated ghost would wrongly
// mark it shadowed, accepting the near-miss object that omits W.
func TestGhostDupDoesNotPropagateIntoNestedEmbed(t *testing.T) {
	t.Parallel()

	type ghostT2 struct {
		W int `json:"W"`
	}

	type ghostA2 struct {
		ghostT2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostP1 struct {
		ghostA2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostP2 struct { //nolint:unused // Embedded only; exercised via reflection.
		ghostA2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostTop2 struct {
		ghostP1 //nolint:unused // Embedded only; exercised via reflection.
		ghostP2 //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostTop2](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostT2](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"W": {Type: "integer"}},
				Required:   []string{"W"},
			},
		}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostTop2{
		ghostP1: ghostP1{ghostA2{ghostT2{W: 7}}},
		Z:       5,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"W": 7, "Z": 5}`, string(raw),
		"encoding/json promotes W once; the dup does not reach the nested embed")

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the generated schema must accept the type's own marshaled JSON")

	assert.Error(t, c.ValidateJSON(t.Context(), []byte(`{"Z": 5}`)),
		"W always appears in the marshaled object, so the branch requiring it must be unconditional")
}

// TestGhostWonNameEvaluatedUnderClose pins that a ghost-won name stays
// evaluated when the object closes with unevaluatedProperties: false. The
// documented unrestricted zero TypeSchema renders the embed's branch as true,
// which evaluates no properties, so without a parent-side true property the
// close would reject the struct's own marshaled JSON.
func TestGhostWonNameEvaluatedUnderClose(t *testing.T) {
	t.Parallel()

	type ghostMeta struct {
		Kind string `json:"Kind"`
	}

	type ghostDoc struct {
		ghostMeta //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostDoc](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostMeta](jsonschema.TypeSchema{}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostDoc{ghostMeta: ghostMeta{Kind: "k"}, Z: 5})
	require.NoError(t, err)

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the branch evaluates nothing, so the parent must evaluate Kind itself")

	assert.Error(t, c.ValidateJSON(t.Context(), []byte(`{"Kind": "k", "Z": 5, "Other": 1}`)),
		"the object stays closed to names the type never marshals")
}
