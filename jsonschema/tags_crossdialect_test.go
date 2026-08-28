package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// crossLevel is a numeric type that marshals itself as text, so the generator
// gives it a string schema. It is the shape that made the two dialects disagree
// loudest: a scalar constraint has to compare against "L3" rather than against
// 3, and a dialect dispatching on the Go kind alone could not see that.
type crossLevel int

// MarshalText writes the level as L<n>. The generator consults TextMarshaler
// (not json.Marshaler, whose output shape is unknowable), so this is what makes
// the field's schema a string.
func (l crossLevel) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "L%d", int(l)), nil
}

// crossShape is one field shape to test both dialects against, with the literal
// spellings each dialect uses for a value list on it. A shape whose values are
// meaningless for it (a word on a numeric field) is still worth testing: the two
// dialects must agree that it is an error.
type crossShape struct {
	typ      reflect.Type
	jsonTag  string
	name     string
	single   string
	pipeList string
	spaceLis string
	// The sized flag marks a shape whose instance carries a size rather than a single
	// value, where the two dialects deliberately part ways on one spelling:
	// go-playground's eq=N on a slice means len(slice)==N, while the jsonschema
	// tag's const= is always a value. They are not two spellings of one rule
	// there, so pairing them would assert an equivalence neither dialect claims.
	sized bool
	// The uniqueDiverges flag marks a shape where uniqueness splits on the
	// NamedKeywords policy: the model's cell is an ignore (a real rule with
	// nothing faithful to emit), which the rule-shaped validate dialect drops
	// while the keyword-naming jsonschema tag reports as an error rather than
	// an inert keyword. The equivalence pairing skips it; each dialect's own
	// tests pin its half.
	uniqueDiverges bool
}

func crossShapes() []crossShape {
	num := crossShape{single: "1", pipeList: "1|2", spaceLis: "1 2"}
	str := crossShape{single: "a", pipeList: "a|b", spaceLis: "a b"}
	bl := crossShape{single: "true", pipeList: "true|false", spaceLis: "true false"}

	with := func(base crossShape, name string, typ reflect.Type, jsonTag string) crossShape {
		base.name, base.typ, base.jsonTag = name, typ, jsonTag

		return base
	}

	sized := func(base crossShape, name string, typ reflect.Type) crossShape {
		out := with(base, name, typ, "v")
		out.sized = true

		return out
	}

	// The go-playground unique on a map means its values are distinct, a rule
	// with no object-side counterpart to uniqueItems, so the map is the one
	// shape where uniqueness diverges by policy rather than agreeing cell for
	// cell.
	mapShape := sized(num, "map", reflect.TypeFor[map[string]int]())
	mapShape.uniqueDiverges = true

	return []crossShape{
		with(str, "string", reflect.TypeFor[string](), "v"),
		with(num, "int8", reflect.TypeFor[int8](), "v"),
		with(num, "float64", reflect.TypeFor[float64](), "v"),
		with(bl, "bool", reflect.TypeFor[bool](), "v"),
		with(num, "pointer to int", reflect.TypeFor[*int](), "v"),
		sized(num, "slice of int8", reflect.TypeFor[[]int8]()),
		sized(str, "nested string slice", reflect.TypeFor[[][]string]()),
		with(str, "byte slice", reflect.TypeFor[[]byte](), "v"),
		with(str, "raw message", reflect.TypeFor[json.RawMessage](), "v"),
		mapShape,
		with(num, "string-coerced int", reflect.TypeFor[int](), "v,string"),
		with(num, "text-marshaling numeric", reflect.TypeFor[crossLevel](), "v"),
		sized(num, "slice of text-marshaling numeric", reflect.TypeFor[[]crossLevel]()),
		with(str, "time", reflect.TypeFor[time.Time](), "v"),
	}
}

// generateWithTag builds a one-field struct carrying tag and generates its
// schema. Building the type reflectively is what makes the cross product
// exhaustive rather than a hand-written sample.
//
// The validate interpreter is registered only for a validate row, so a
// jsonschema-tag row generates through the same path a caller who never
// registered an interpreter would take.
func generateOneField(
	t *testing.T,
	typ reflect.Type,
	jsonTag, tagKey, tagValue string,
) (*jsonschema.Schema, error) {
	t.Helper()

	doc := reflect.StructOf([]reflect.StructField{{
		Name: "V",
		Type: typ,
		Tag:  reflect.StructTag(fmt.Sprintf(`json:%q %s:%q`, jsonTag, tagKey, tagValue)),
	}})

	var opts []jsonschema.GenerateOption

	if tagKey == "validate" {
		opts = append(opts, jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
	}

	//nolint:wrapcheck // The test asserts on the generation error itself.
	return jsonschema.Generate(t.Context(), doc, opts...)
}

// generateWithTag is generateOneField over a crossShape.
func generateWithTag(t *testing.T, sh crossShape, tagKey, tagValue string) (*jsonschema.Schema, error) {
	t.Helper()

	return generateOneField(t, sh.typ, sh.jsonTag, tagKey, tagValue)
}

// TestCrossDialectEquivalence is the keystone: for every field shape, a rule
// spelled in the jsonschema tag and the same rule spelled in the validate tag
// must produce the same schema, or both must fail.
//
// The two dialects used to interpret one constraint vocabulary through two
// engines, and every divergence between them was a place one engine was silently
// wrong: a coerced element the sequence path could not see, a hardcoded zero
// that made a non-zero check inert, an enum that kept bounds the other dropped.
// Each of those is one failing cell here.
func TestCrossDialectEquivalence(t *testing.T) {
	t.Parallel()

	for _, sh := range crossShapes() {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()

			// The bound operations -- the min/max endpoints and the sizes -- are
			// deliberately absent. The dialects diverge on them by design,
			// through the two named Policy fields: BoundKind gives the
			// jsonschema tag the keyword-shaped literal domain, where
			// minimum=1.5 on an int8 is a legitimate keyword value, while a
			// validate rule parses at the field's own kind, where gte=1.5 on an
			// int is correctly an error; and Sizes makes a negative size a
			// rejection in one dialect and the unsatisfiable fold in the other.
			// Pairing them here would assert an equivalence neither dialect
			// claims. The matrix golden pins which shapes carry a bound at all,
			// and each dialect's own tests pin its literal domain.
			pairs := map[string][2]string{
				"enumeration": {"enum=" + sh.pipeList, "oneof=" + sh.spaceLis},
			}

			// Both dialects reach the same uniqueness cell, so every shape
			// must agree on the keyword or on the rejection -- except the one
			// shape whose cell is an ignore, where the NamedKeywords policy
			// deliberately splits the outcome (see crossShape.uniqueDiverges).
			if !sh.uniqueDiverges {
				pairs["element uniqueness"] = [2]string{"uniqueItems=true", "unique"}
			}

			if !sh.sized {
				pairs["pinned value"] = [2]string{"const=" + sh.single, "eq=" + sh.single}
			}

			for rule, spellings := range pairs {
				t.Run(rule, func(t *testing.T) {
					t.Parallel()

					assertDialectsAgree(t, sh, spellings[0], spellings[1])
				})
			}
		})
	}
}

// assertDialectsAgree generates both spellings and requires the same outcome.
func assertDialectsAgree(t *testing.T, sh crossShape, jsonSpelling, validateSpelling string) {
	t.Helper()

	fromTag, tagErr := generateWithTag(t, sh, "jsonschema", jsonSpelling)
	fromInterp, interpErr := generateWithTag(t, sh, "validate", validateSpelling)

	if tagErr != nil || interpErr != nil {
		require.Error(t, tagErr,
			"validate rejected %q but the jsonschema tag accepted %q", validateSpelling, jsonSpelling)
		require.Error(t, interpErr,
			"the jsonschema tag rejected %q but validate accepted %q", jsonSpelling, validateSpelling)

		return
	}

	wantJSON, err := json.Marshal(fromTag)
	require.NoError(t, err)

	gotJSON, err := json.Marshal(fromInterp)
	require.NoError(t, err)

	assert.JSONEq(t, string(wantJSON), string(gotJSON),
		"jsonschema:%q and validate:%q must produce the same schema", jsonSpelling, validateSpelling)
}

// TestCrossDialectCoercedScalarsUseTheSerializedForm pins the specific outcome
// the equivalence test only pins the agreement of: a field whose schema is a
// string because it serializes itself as one gets constraints against the text
// it emits, in both dialects. A dialect reading the Go kind alone would pin a
// number here, which no instance the field produces could ever match.
func TestCrossDialectCoercedScalarsUseTheSerializedForm(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		typ      reflect.Type
		jsonTag  string
		wantJSON string
	}{
		"json string coercion": {
			typ: reflect.TypeFor[int](), jsonTag: "v,string",
			wantJSON: `{"type":"string","const":"3"}`,
		},
		"text-marshaling numeric": {
			typ: reflect.TypeFor[crossLevel](), jsonTag: "v",
			wantJSON: `{"type":"string","const":"L3"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sh := crossShape{typ: tc.typ, jsonTag: tc.jsonTag, single: "3"}

			for tag, spelling := range map[string]string{
				"jsonschema": "const=3",
				"validate":   "eq=3",
			} {
				s, err := generateWithTag(t, sh, tag, spelling)
				require.NoError(t, err, "%s:%q", tag, spelling)

				got, err := json.Marshal(s.Properties["v"])
				require.NoError(t, err)
				assert.JSONEq(t, tc.wantJSON, string(got),
					"%s:%q constrains the serialized text", tag, spelling)
			}
		})
	}
}

// TestCrossDialectElementEnumDropsKindBounds pins that an element enumeration
// resolves its bounds the same way whichever dialect wrote it. The rule reads
// the keyword, not the provenance: a []int8 restricted to 1, 2, 3 has no use
// for the -128/127 range either way.
func TestCrossDialectElementEnumDropsKindBounds(t *testing.T) {
	t.Parallel()

	sh := crossShape{typ: reflect.TypeFor[[]int8](), jsonTag: "v"}

	for tag, spelling := range map[string]string{
		"jsonschema": "enum=1|2|3",
		"validate":   "oneof=1 2 3",
	} {
		s, err := generateWithTag(t, sh, tag, spelling)
		require.NoError(t, err)

		items := s.Properties["v"].Items
		require.NotNil(t, items, "%s:%q constrains the element schema", tag, spelling)
		require.Len(t, items.Enum, 3)
		assert.Nil(t, items.Minimum, "%s:%q drops the kind-derived floor", tag, spelling)
		assert.Nil(t, items.Maximum, "%s:%q drops the kind-derived ceiling", tag, spelling)
	}
}
