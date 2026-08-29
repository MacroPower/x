package differentialtest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"

	playground "github.com/go-playground/validator/v10"
)

// The widened half of the go-playground differential. The roster in
// differential_test.go holds every field non-pointer, always present, and
// coerced only on plain scalars, which leaves three shapes the interpreter has
// separate code for unexercised. A nullable occurrence, a field encoding/json
// may drop, and a coerced pointer each get a target here.

// pointerConstraints exercises the value rules on nullable occurrences, where
// the interpreter puts a value constraint on the value branch of the null
// encoding so a permitted null stays valid. The bool fields carry only eq and
// ne; see reasonOneOfKindPanic. A nil value rule field puts the pair in the
// weaker half of the agreement property; see reasonNullableValueRule. The
// required fields are the exception, and the reason they are here. Required is
// the one rule that does assert something about null, so its nil stays under
// the biconditional and a schema that let null through would fail.
type pointerConstraints struct {
	Min    *string  `json:"min"     validate:"min=2"`
	Max    *string  `json:"max"     validate:"max=5"`
	OneOf  *string  `json:"one_of"  validate:"oneof=alpha beta gamma"`
	Eq     *string  `json:"eq"      validate:"eq=fixed"`
	Ne     *string  `json:"ne"      validate:"ne=banned"`
	NumMin *int     `json:"num_min" validate:"min=3"`
	NumMax *int     `json:"num_max" validate:"max=10"`
	NumEq  *int     `json:"num_eq"  validate:"eq=7"`
	NumNe  *int     `json:"num_ne"  validate:"ne=7"`
	FMin   *float64 `json:"f_min"   validate:"min=1.5"`
	Flag   *bool    `json:"flag"    validate:"eq=true"`
	FlagN  *bool    `json:"flag_n"  validate:"ne=false"`
}

// requiredPointerConstraints is the nullable roster required gets to itself.
// Every field carries it, so no sibling can drop the object into the weaker
// half of the agreement property and hide a null the schema should reject. The
// pairings are the load-bearing rows. Required composes its
// forbidden null with another rule's forbidden value, and a composition that
// escalated the pair off the null wrapper would let null through here.
type requiredPointerConstraints struct {
	Str     *string  `json:"str"            validate:"required"`
	Num     *int     `json:"num"            validate:"required"`
	FNum    *float64 `json:"f_num"          validate:"required"`
	Flag    *bool    `json:"flag"           validate:"required"`
	StrNe   *string  `json:"str_ne"         validate:"required,ne=banned"`
	NumNe   *int     `json:"num_ne"         validate:"required,ne=7"`
	StrOne  *string  `json:"str_one"        validate:"required,oneof=alpha beta"`
	StrMin  *string  `json:"str_min"        validate:"required,min=2"`
	Coerced *int     `json:"coerced,string" validate:"required,ne=3"`
}

// omitemptyConstraints exercises the scalar rules on fields encoding/json drops
// at their zero value, the case the roster in differential_test.go excludes. The
// agreement property weakens to a one-way implication where a field is
// dropped; see reasonOmitemptyDropsField.
type omitemptyConstraints struct {
	Min   string  `json:"min,omitempty"    validate:"min=2"`
	Max   string  `json:"max,omitempty"    validate:"max=5"`
	Len   string  `json:"len,omitempty"    validate:"len=3"`
	OneOf string  `json:"one_of,omitempty" validate:"oneof=alpha beta gamma"`
	NumM  int     `json:"num_m,omitempty"  validate:"min=3"`
	NumX  int     `json:"num_x,omitempty"  validate:"max=10"`
	FMin  float64 `json:"f_min,omitempty"  validate:"min=1.5"`
}

// coercedPointerConstraints exercises the value rules on a coerced pointer,
// whose schema is a string while the Go value is a nullable number or bool.
// Only the value rules appear; see reasonCoercedNumericBounds.
type coercedPointerConstraints struct {
	Eq    *int     `json:"eq,string"     validate:"eq=7"`
	Ne    *int     `json:"ne,string"     validate:"ne=7"`
	OneOf *int8    `json:"one_of,string" validate:"oneof=1 2 3"`
	FEq   *float64 `json:"f_eq,string"   validate:"eq=2.5"`
	Flag  *bool    `json:"flag,string"   validate:"eq=true"`
}

// FuzzValidatorPointerConstraints asserts the agreement property over nullable
// occurrences.
func FuzzValidatorPointerConstraints(f *testing.F) {
	fuzzWidenedDifferential[pointerConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"OneOf":  {"alpha", "beta", "gamma", "delta"},
		"Eq":     {"fixed", "other"},
		"Ne":     {"banned", "allowed"},
		"NumEq":  {"7", "8"},
		"NumNe":  {"7", "8"},
		"NumMin": {"2", "3", "4"},
		"NumMax": {"9", "10", "11"},
	}))
}

// FuzzValidatorRequiredPointerConstraints asserts the agreement property over
// nullable occurrences that all carry required.
func FuzzValidatorRequiredPointerConstraints(f *testing.F) {
	fuzzWidenedDifferential[requiredPointerConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"StrNe":  {"banned", "allowed"},
		"NumNe":  {"7", "8"},
		"StrOne": {"alpha", "beta", "gamma"},
		"StrMin": {"a", "ab", "abc"},
	}))
}

// FuzzValidatorOmitemptyConstraints asserts the agreement property over fields
// encoding/json may drop.
func FuzzValidatorOmitemptyConstraints(f *testing.F) {
	fuzzWidenedDifferential[omitemptyConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"OneOf": {"alpha", "beta", "gamma", "delta"},
		"Len":   {"abc", "ab", "abcd"},
		"NumM":  {"2", "3", "4"},
		"NumX":  {"9", "10", "11"},
	}))
}

// FuzzValidatorCoercedPointerConstraints asserts the agreement property over
// coerced pointers.
func FuzzValidatorCoercedPointerConstraints(f *testing.F) {
	fuzzWidenedDifferential[coercedPointerConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"Eq":    {"7", "8"},
		"Ne":    {"7", "8"},
		"OneOf": {"1", "2", "3", "4"},
		"FEq":   {"2.5", "3.5"},
	}))
}

// fieldProbe is what the harness needs to know about one field to decide which
// half of the agreement property its instance falls under.
type fieldProbe struct {
	name string
	// The required field marks a rule that asserts something about null. Every
	// other rule passes on a null instance in JSON Schema (see
	// reasonNullableValueRule), so a null there weakens the comparison; a
	// required field's null is exactly what the schema is supposed to reject,
	// so it stays under the biconditional.
	required bool
}

// jsonNames returns a probe per field of typ that encoding/json writes, so the
// harness can tell a dropped or null field from a present one without
// re-deriving the tag grammar per target.
func jsonNames(typ reflect.Type) []fieldProbe {
	out := make([]fieldProbe, 0, typ.NumField())

	for f := range typ.Fields() {
		if !f.IsExported() {
			continue
		}

		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}

		if name == "" {
			name = f.Name
		}

		out = append(out, fieldProbe{name: name, required: spells(f.Tag.Get("validate"), "required")})
	}

	return out
}

// everyFieldSet reports whether the marshaled object carries every named
// property with a non-null value, which is the region where the two validators
// see the same information and the biconditional holds.
func everyFieldSet(t *testing.T, probes []fieldProbe, instance []byte) bool {
	t.Helper()

	var members map[string]json.RawMessage

	require.NoError(t, json.Unmarshal(instance, &members), "decode %s", instance)

	for _, probe := range probes {
		raw, ok := members[probe.name]
		if !ok {
			return false
		}

		if string(raw) == "null" && !probe.required {
			return false
		}
	}

	return true
}

// agreementHolds applies the split property: the biconditional where every
// field is set, the one-way implication that the schema is never the stricter
// of the two otherwise.
func agreementHolds(t *testing.T, probes []fieldProbe, instance []byte, referenceReject, schemaReject bool) bool {
	t.Helper()

	if everyFieldSet(t, probes, instance) {
		return referenceReject == schemaReject
	}

	return !schemaReject || referenceReject
}

// fuzzWidenedDifferential is fuzzValidatorDifferential with the agreement
// property split on what the marshaled object actually carries. Where every
// field is present with a non-null value the two validators must agree exactly,
// which is the property at full strength. Where encoding/json drops a field or
// writes null for one, the schema has nothing to assert about it and
// can only be the more permissive of the two, so the property weakens to the
// one-way implication; see reasonOmitemptyDropsField and
// reasonNullableValueRule.
func fuzzWidenedDifferential[T any](f *testing.F, fillOpts ...fuzzfill.Option) {
	f.Helper()

	ctx := context.Background()

	schema, err := jsonschema.GenerateFor[T](ctx,
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(f, err, "generate schema for %T", *new(T))

	validator, err := jsonschema.Compile(ctx, schema)
	require.NoError(f, err, "compile schema for %T", *new(T))

	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(f, err, "marshal schema for %T", *new(T))

	reference := playground.New(playground.WithRequiredStructEnabled())
	names := jsonNames(reflect.TypeFor[T]())

	addDifferentialSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		var val T

		fuzzfill.Fill(reflect.ValueOf(&val), data, fillOpts...)

		referenceReject, err := referenceRejects(reference, val)
		if err != nil {
			t.Fatalf("go-playground could not handle a roster tag on %T: %v", val, err)
		}

		instance, err := json.Marshal(val)
		if err != nil {
			return
		}

		schemaReject := validator.ValidateJSON(ctx, instance) != nil

		if agreementHolds(t, names, instance, referenceReject, schemaReject) {
			return
		}

		t.Fatalf(
			"validators disagree on a value of %T\n"+
				"value:            %#v\n"+
				"marshaled:        %s\n"+
				"every field set:  %v\n"+
				"go-playground:    reject=%v\n"+
				"schema:           reject=%v\n"+
				"schema doc:       %s",
			val, val, instance, everyFieldSet(t, names, instance),
			referenceReject, schemaReject, schemaJSON,
		)
	})
}

// strictSeedCount reports how many of the shared seed blobs fill a T whose
// every field is set, which is the region the biconditional applies to.
func strictSeedCount[T any](t *testing.T, fillOpts ...fuzzfill.Option) int {
	t.Helper()

	names := jsonNames(reflect.TypeFor[T]())
	strict := 0

	for _, seed := range differentialSeeds() {
		var val T

		fuzzfill.Fill(reflect.ValueOf(&val), seed, fillOpts...)

		instance, err := json.Marshal(val)
		require.NoError(t, err, "marshal %T", val)

		if everyFieldSet(t, names, instance) {
			strict++
		}
	}

	return strict
}

// TestWidenedDifferentialReachesStrictAgreement guards the widened targets
// against passing vacuously. Both halves of the split property are satisfiable
// by a schema that accepts everything, so the seed population has to reach the
// half that asserts the biconditional; a filler or a roster change that stopped
// producing fully-set values would leave the targets green and inert.
func TestWidenedDifferentialReachesStrictAgreement(t *testing.T) {
	t.Parallel()

	for name, count := range map[string]func(*testing.T) int{
		"pointer": func(t *testing.T) int {
			t.Helper()

			return strictSeedCount[pointerConstraints](t)
		},
		"omitempty": func(t *testing.T) int {
			t.Helper()

			return strictSeedCount[omitemptyConstraints](t)
		},
		"coerced pointer": func(t *testing.T) int {
			t.Helper()

			return strictSeedCount[coercedPointerConstraints](t)
		},
		"required pointer": func(t *testing.T) int {
			t.Helper()

			return strictSeedCount[requiredPointerConstraints](t)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// More than one, since a single strict seed would leave the
			// biconditional resting on one blob. The cursor zero-extends, so a
			// short blob leaves every tail field nil and lands in the weak half.
			assert.GreaterOrEqual(t, count(t), 2,
				"too few seeds reach the biconditional half of the property")
		})
	}
}

// requiredNullableShapes are the one-field shapes required is checked against
// on its own, covering both kinds of occurrence that admit null: the pointer,
// and the bare slice, map, and byte slice. A fuzz target cannot serve here for
// two reasons. The schema verdict is per object, so a sibling field that
// correctly rejects null masks another field that wrongly accepts it. The nil
// occurrence is also out of the draw's reach, since internal/fuzzfill builds
// every container through reflect.MakeSlice or reflect.MakeMap, which return a
// non-nil container even at length zero. One field per struct makes the verdict
// attributable, and the zero value of that struct is the nil these shapes need.
//
// Some pointer rows pair required with a second forbidding rule. Required
// contributes a forbidden null, and the second rule has to compose with it
// without pushing the pair off the branch the null encoding put the null on.
// The bare container rows carry no second rule, because their null rides a
// ["null", base] type list with no wrapper to fall off, so they pin only that
// the forbidden null lands.
func requiredNullableShapes() map[string]reflect.Type {
	field := func(typ reflect.Type, jsonTag, rule string) reflect.Type {
		return reflect.StructOf([]reflect.StructField{{
			Name: "V", Type: typ,
			Tag: reflect.StructTag(fmt.Sprintf(`json:%q validate:%q`, jsonTag, rule)),
		}})
	}

	return map[string]reflect.Type{
		// A type whose own schema forbids a subschema is the shape that broke
		// the composition thinnest on. A forbidden subschema cannot join the
		// forbidden values, so whichever of the two gives way to allOf stops
		// applying to null. The caller draws it through WithTypeSchema.
		"type-derived subschema forbid": field(reflect.TypeFor[*forbiddingWord](), "v", "required"),
		"string":                        field(reflect.TypeFor[*string](), "v", "required"),
		"number":                        field(reflect.TypeFor[*int](), "v", "required"),
		"float":                         field(reflect.TypeFor[*float64](), "v", "required"),
		"bool":                          field(reflect.TypeFor[*bool](), "v", "required"),
		"slice":                         field(reflect.TypeFor[*[]int](), "v", "required"),
		"map":                           field(reflect.TypeFor[*map[string]int](), "v", "required"),
		"coerced number":                field(reflect.TypeFor[*int](), "v,string", "required"),
		"string with ne":                field(reflect.TypeFor[*string](), "v", "required,ne=banned"),
		"number with ne":                field(reflect.TypeFor[*int](), "v", "required,ne=7"),
		"bool with ne":                  field(reflect.TypeFor[*bool](), "v", "required,ne=true"),
		"string with oneof":             field(reflect.TypeFor[*string](), "v", "required,oneof=alpha beta"),
		"string with min":               field(reflect.TypeFor[*string](), "v", "required,min=2"),
		"coerced with ne":               field(reflect.TypeFor[*int](), "v,string", "required,ne=3"),
		"slice with ne":                 field(reflect.TypeFor[*[]int](), "v", "required,ne=2"),
		"repeated required":             field(reflect.TypeFor[*string](), "v", "required,required"),
		// A bare slice, map, or byte slice is nil-able in Go, so its schema
		// admits null exactly as a pointer's does and required has to forbid it
		// there too. The size floor beside it never sees a null instance.
		"bare slice":      field(reflect.TypeFor[[]int](), "v", "required"),
		"bare map":        field(reflect.TypeFor[map[string]int](), "v", "required"),
		"bare byte slice": field(reflect.TypeFor[[]byte](), "v", "required"),
	}
}

// forbiddingWord is a string type whose own schema forbids a subschema rather
// than a value, so the field arrives with a not the forbidden null cannot merge
// into.
type forbiddingWord string

// forbiddingWordSchema returns the generate option declaring forbiddingWord's
// schema, a string whose own not forbids a minLength subschema.
func forbiddingWordSchema() jsonschema.GenerateOption {
	three := 3

	return jsonschema.WithTypeSchema(reflect.TypeFor[forbiddingWord](), jsonschema.TypeSchema{
		Value: &jsonschema.Schema{Type: "string", Not: &jsonschema.Schema{MinLength: &three}},
	})
}

// TestRequiredOnNullableRejectsNull pins that required on a field whose schema
// admits null rejects the null instance. Go-playground rejects the nil
// occurrence, and encoding/json writes null for that nil. A pointer and a bare
// slice, map, or byte slice all marshal their nil the same way.
//
// The schema's required entry cannot say that on its own, since a property
// whose value is null is still present, so the assertion rides on a forbidden
// null that has to survive composition with whatever else the field forbids.
func TestRequiredOnNullableRejectsNull(t *testing.T) {
	t.Parallel()

	reference := playground.New(playground.WithRequiredStructEnabled())

	for name, typ := range requiredNullableShapes() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.Generate(t.Context(), typ,
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				forbiddingWordSchema())
			require.NoError(t, err)

			validator, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			doc, err := json.Marshal(schema.Properties["v"])
			require.NoError(t, err)

			// The nil occurrence: go-playground rejects it, so the schema must
			// too. The zero value of a one-field struct is exactly that.
			referenceReject, err := referenceRejects(reference, reflect.New(typ).Elem().Interface())
			require.NoError(t, err)
			require.True(t, referenceReject, "go-playground must reject the nil occurrence")

			assert.Error(t, validator.ValidateJSON(t.Context(), []byte(`{"v":null}`)),
				"the schema must reject null for %s: %s", name, doc)
		})
	}
}

// TestCoercedFloatRejectsNegativeZero pins the value the fuzzer found, a float
// under json:",string" whose Go value is the negative zero. Go compares that
// value equal to zero, so go-playground's required and its ne=0 both reject it.
// Because encoding/json writes the sign bit, the instance carries "-0" rather
// than "0", and a forbid side naming one text would accept a value the reference
// validator rejects.
//
// The committed corpus entry that found this is entropy, and any change to the
// draw pools retires it. This test names the shape outright, so no draw change
// can retire it.
func TestCoercedFloatRejectsNegativeZero(t *testing.T) {
	t.Parallel()

	reference := playground.New(playground.WithRequiredStructEnabled())

	for name, rule := range map[string]string{
		"required": "required",
		"ne":       "ne=0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := reflect.StructOf([]reflect.StructField{{
				Name: "V", Type: reflect.TypeFor[float64](),
				Tag: reflect.StructTag(fmt.Sprintf(`json:"v,string" validate:%q`, rule)),
			}})

			val := reflect.New(typ).Elem()
			val.Field(0).SetFloat(math.Copysign(0, -1))

			doc, err := json.Marshal(val.Interface())
			require.NoError(t, err)
			require.JSONEq(t, `{"v":"-0"}`, string(doc),
				"encoding/json must write the sign bit for the negative zero")

			referenceReject, err := referenceRejects(reference, val.Interface())
			require.NoError(t, err)
			require.True(t, referenceReject, "go-playground must reject the negative zero")

			schema, err := jsonschema.Generate(t.Context(), typ,
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
			require.NoError(t, err)

			validator, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			assert.Error(t, validator.ValidateJSON(t.Context(), doc),
				"the schema must reject the negative zero for %s", rule)
		})
	}
}
