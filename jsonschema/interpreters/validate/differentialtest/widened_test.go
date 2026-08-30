package differentialtest_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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

// requiredNullableRig is one requiredNullableShapes row prepared for the fuzz
// loop: the type a blob draws, the validator its schema compiles to, and the
// schema text a failure prints.
type requiredNullableRig struct {
	name      string
	typ       reflect.Type
	validator *jsonschema.Validator
	doc       []byte
}

// buildRequiredNullableRigs generates and compiles a schema per roster row,
// ordered by name so a shape blob draws the same row on every run.
func buildRequiredNullableRigs(f *testing.F) []requiredNullableRig {
	f.Helper()

	ctx := context.Background()
	shapes := requiredNullableShapes()
	rigs := make([]requiredNullableRig, 0, len(shapes))

	for _, name := range requiredNullableOrder() {
		schema, err := jsonschema.Generate(ctx, shapes[name],
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
			forbiddingWordSchema())
		require.NoError(f, err, "generate the schema for %s", name)

		validator, err := jsonschema.Compile(ctx, schema)
		require.NoError(f, err, "compile the schema for %s", name)

		doc, err := json.MarshalIndent(schema, "", "  ")
		require.NoError(f, err, "marshal the schema for %s", name)

		rigs = append(rigs, requiredNullableRig{
			name: name, typ: shapes[name], validator: validator, doc: doc,
		})
	}

	return rigs
}

// emptyBareCollection reports whether v is an allocated empty slice or map,
// the one value in this roster the two validators judge differently; see
// reasonRequiredCollectionEmptyFloor. A pointer occurrence is not a bare one.
// Required stops at the forbidden null there and lays down no size floor, which
// TestValidateInterpreter_RequiredOnPointerSlice pins, so the target compares
// an empty container behind a pointer like any other value.
func emptyBareCollection(v reflect.Value) bool {
	switch v.Kind() { //nolint:exhaustive // only the two nilable collection kinds carry a size floor.
	case reflect.Slice, reflect.Map:
		return !v.IsNil() && v.Len() == 0
	default:
		return false
	}
}

// requiredNullableOrder returns the roster keys in the order a shape blob
// indexes them. Sorting keeps the shape a function of the blob; a map's
// iteration order would not.
func requiredNullableOrder() []string {
	return slices.Sorted(maps.Keys(requiredNullableShapes()))
}

// requiredNullableSeeds returns the shape and value blob pairs
// FuzzValidatorRequiredNullableShapes starts from, a function rather than a
// call sequence so TestRequiredNullableNilSideIsCompared can draw the same
// population without a testing.F.
//
// The cross product carries the shared blobs, whose all-zero member draws the
// roster's first row with its container nil. The two pairs after it put the
// other two bare collections in that same state, the one the target exists
// for. The pairs after those reach the forbid row, which no shared shape blob
// draws. The bare-collection pairs and the forbid pairs look their row up by
// name, since Cursor.Intn reads a shape blob as a big-endian index into a
// roster whose order a new key can shift.
func requiredNullableSeeds() [][2][]byte {
	shared := differentialSeeds()
	pairs := make([][2][]byte, 0, len(shared)*len(shared)+len(shared)+2)

	for _, shape := range shared {
		for _, value := range shared {
			pairs = append(pairs, [2][]byte{shape, value})
		}
	}

	order := requiredNullableOrder()

	for _, name := range []string{"bare map", "bare slice"} {
		index := slices.Index(order, name)
		if index < 0 {
			continue
		}

		//nolint:gosec // index is a bounded, non-negative roster position.
		shape := binary.BigEndian.AppendUint64(nil, uint64(index))
		pairs = append(pairs, [2][]byte{shape, make([]byte, 8)})
	}

	// The forbid row's comparison turns on a rune ceiling rather than on the
	// null alone, so pairing its index with every shared value blob reaches
	// strings on both sides of that ceiling.
	// TestRequiredNullableForbidRowIsCompared pins that the seeds reach both
	// sides of it.
	if index := slices.Index(order, forbidRowKey); index >= 0 {
		//nolint:gosec // index is a bounded, non-negative roster position.
		shape := binary.BigEndian.AppendUint64(nil, uint64(index))

		for _, value := range shared {
			pairs = append(pairs, [2][]byte{shape, value})
		}
	}

	return pairs
}

// FuzzValidatorRequiredNullableShapes asserts the agreement property over the
// requiredNullableShapes roster with the nil-container draw on, so the fuzzer,
// not only the deterministic TestRequiredOnNullableRejectsNull, compares the
// null side of required on a collection.
//
// Each shape holds one field, so the object verdict is that field's verdict and
// the full biconditional applies. The rows it adds over
// FuzzValidatorRequiredPointerConstraints are the six carrying a collection,
// three pointer-scalar pairings that roster omits (a coerced number under a
// bare required, a bool under required with ne, and a repeated required), and
// the type-schema forbid row, the only WithTypeSchema occurrence any roster in
// this package carries. Every other pointer-scalar row is a shape and rule that
// target already compares under the same draw, so a failure on one of those
// reads as a regression in the shared driver rather than a new find. The
// roster is fixed, so the target compiles every row once and draws an index
// per iteration. It takes two blobs so the shape entropy and the value entropy
// evolve independently, the convention FuzzValidatorTaggedShapes uses.
func FuzzValidatorRequiredNullableShapes(f *testing.F) {
	ctx := context.Background()
	reference := playground.New(playground.WithRequiredStructEnabled())
	rigs := buildRequiredNullableRigs(f)

	for _, pair := range requiredNullableSeeds() {
		f.Add(pair[0], pair[1])
	}

	f.Fuzz(func(t *testing.T, shapeBlob, valueBlob []byte) {
		rig := rigs[fuzzfill.NewCursor(shapeBlob).Intn(len(rigs))]

		val := reflect.New(rig.typ)
		fuzzfill.Fill(val, valueBlob, fuzzfill.WithNilContainers())

		if emptyBareCollection(val.Elem().Field(0)) {
			return // reasonRequiredCollectionEmptyFloor
		}

		referenceReject, err := referenceRejects(reference, val.Elem().Interface())
		if err != nil {
			t.Fatalf("go-playground could not handle the %s tag: %v", rig.name, err)
		}

		instance, err := json.Marshal(val.Elem().Interface())
		if err != nil {
			return
		}

		schemaReject := rig.validator.ValidateJSON(ctx, instance) != nil
		if referenceReject == schemaReject {
			return
		}

		t.Fatalf(
			"validators disagree on the %s shape\n"+
				"value:            %#v\n"+
				"marshaled:        %s\n"+
				"go-playground:    reject=%v\n"+
				"schema:           reject=%v\n"+
				"schema doc:       %s",
			rig.name, val.Elem().Interface(), instance, referenceReject, schemaReject, rig.doc,
		)
	})
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

// forbidRowKey names the roster row whose type schema forbids a subschema.
const forbidRowKey = "type-derived subschema forbid"

// requiredNullableShapes are the one-field shapes required is checked against
// on its own, covering both kinds of occurrence that admit null: the pointer,
// and the bare slice, map, and byte slice. One field per struct makes a verdict
// attributable. The schema verdict is per object, so a sibling field that
// correctly rejects null would mask another field that wrongly accepts it, and
// the zero value of a one-field struct is the nil these shapes need.
//
// TestRequiredOnNullableRejectsNull pins the null instance of every row,
// FuzzValidatorRequiredNullableShapes searches the rest of each row's value
// space with internal/fuzzfill's nil-container draw on, and
// TestRequiredNullableNilSideIsCompared guards that draw.
//
// Some pointer rows pair required with a second forbidding rule. Required
// contributes a forbidden null, and the second rule has to compose with it
// without pushing the pair off the branch the null encoding put the null on.
// The forbid row's max is not one of those rules. It is a bound, and it states
// the same ceiling that row's type schema imposes through its not, so the two
// validators judge by one predicate. The bare container rows carry no second
// rule, because their null rides a ["null", base] type list with no wrapper to
// fall off, so they pin only that the forbidden null lands.
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
		//
		// The tag's max=2 spells the same rune ceiling that forbidden
		// subschema imposes, since a not on minLength 3 admits exactly the
		// strings maxLength 2 admits. Both validators therefore judge by one
		// predicate, so the fuzzer compares the row, and
		// TestRequiredOnNullableRejectsNull pins that the ceiling reaches the
		// generated schema.
		forbidRowKey:        field(reflect.TypeFor[*forbiddingWord](), "v", "required,max=2"),
		"string":            field(reflect.TypeFor[*string](), "v", "required"),
		"number":            field(reflect.TypeFor[*int](), "v", "required"),
		"float":             field(reflect.TypeFor[*float64](), "v", "required"),
		"bool":              field(reflect.TypeFor[*bool](), "v", "required"),
		"slice":             field(reflect.TypeFor[*[]int](), "v", "required"),
		"map":               field(reflect.TypeFor[*map[string]int](), "v", "required"),
		"coerced number":    field(reflect.TypeFor[*int](), "v,string", "required"),
		"string with ne":    field(reflect.TypeFor[*string](), "v", "required,ne=banned"),
		"number with ne":    field(reflect.TypeFor[*int](), "v", "required,ne=7"),
		"bool with ne":      field(reflect.TypeFor[*bool](), "v", "required,ne=true"),
		"string with oneof": field(reflect.TypeFor[*string](), "v", "required,oneof=alpha beta"),
		"string with min":   field(reflect.TypeFor[*string](), "v", "required,min=2"),
		"coerced with ne":   field(reflect.TypeFor[*int](), "v,string", "required,ne=3"),
		"slice with ne":     field(reflect.TypeFor[*[]int](), "v", "required,ne=2"),
		"repeated required": field(reflect.TypeFor[*string](), "v", "required,required"),
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

// taggedCeiling returns the rune ceiling the roster shape's validate tag
// spells, and whether it spells one. Callers read the ceiling off the tag
// rather than repeating the number, so the tag stays the only place the
// assertions read that number from.
func taggedCeiling(t *testing.T, typ reflect.Type) (int, bool) {
	t.Helper()

	for part := range strings.SplitSeq(typ.Field(0).Tag.Get("validate"), ",") {
		key, param, found := strings.Cut(part, "=")
		if !found || key != "max" {
			continue
		}

		ceiling, err := strconv.Atoi(param)
		require.NoError(t, err, "the max tag on %s spells no number", typ)

		return ceiling, true
	}

	return 0, false
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

			prop := schema.Properties["v"]

			doc, err := json.Marshal(prop)
			require.NoError(t, err)

			// The forbid row carries a type schema forbidding a subschema that
			// imposes a rune ceiling, and its tag must spell that same ceiling
			// for the two validators to judge by one predicate. The generated
			// property must carry both the ceiling and the forbidden null. The
			// gate reads the field's Go type. Reading the tag would let a
			// dropped tag skip the check, and reading the roster key would let
			// a rename silence it.
			if typ.Field(0).Type == reflect.PointerTo(reflect.TypeFor[forbiddingWord]()) {
				ceiling, ok := taggedCeiling(t, typ)
				require.True(t, ok,
					"the forbid row must spell the ceiling its type schema forbids, "+
						"or FuzzValidatorRequiredNullableShapes cannot compare it")

				require.NotNil(t, prop.MaxLength, "the max tag wrote no maxLength: %s", doc)
				assert.Equal(t, ceiling, *prop.MaxLength, "the maxLength must be the tag's ceiling: %s", doc)

				// The forbidden null has to hold the property's own not slot,
				// which the forbidden subschema competes for.
				require.NotNil(t, prop.Not, "the forbidden null left the property: %s", doc)
				require.NotNil(t, prop.Not.Const, "the not must forbid a value, not a subschema: %s", doc)
				assert.Nil(t, *prop.Not.Const, "the forbidden value must be the null: %s", doc)
			}

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

// TestRequiredNullableNilSideIsCompared guards
// FuzzValidatorRequiredNullableShapes from going inert at the draw. The null a
// nil collection marshals to is the whole subject of that target, so a draw
// that stopped producing one, or that produced nothing else, would leave it
// green and comparing only what FuzzValidatorRequiredPointerConstraints already
// covers.
//
// It asserts both halves of the path to that null. The value draw fills each
// bare collection nil and non-nil over the shared blobs, and the seeded shape
// and value pairs reach each bare row with its field nil, the state the
// target's own seeds claim to reach.
func TestRequiredNullableNilSideIsCompared(t *testing.T) {
	t.Parallel()

	shapes := requiredNullableShapes()
	seededNil := seededNilRows(t)

	for _, name := range []string{"bare slice", "bare map", "bare byte slice"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, shapes, name, "the roster no longer holds this row")

			var drewNil, drewSet int

			for _, blob := range differentialSeeds() {
				val := reflect.New(shapes[name])
				fuzzfill.Fill(val, blob, fuzzfill.WithNilContainers())

				if val.Elem().Field(0).IsNil() {
					drewNil++
				} else {
					drewSet++
				}
			}

			assert.Positive(t, drewNil, "the draw never left the %s nil", name)
			assert.Positive(t, drewSet, "the draw always left the %s nil", name)
			assert.Positive(t, seededNil[name], "no seed pair reaches the %s row with its field nil", name)
		})
	}
}

// TestRequiredNullableForbidRowIsCompared pins that
// FuzzValidatorRequiredNullableShapes reaches the forbid row on its seeds, not
// only under an explicit -fuzz run. No shared shape blob draws that row, so
// without the seeds written for it the deterministic run never compares it and
// the row is excluded in all but name.
//
// The row's whole subject is the ceiling both validators judge it by, so the
// seeds must draw it on both sides of that ceiling. If every draw produced a
// string within the ceiling, the two validators would agree for the wrong
// reason.
func TestRequiredNullableForbidRowIsCompared(t *testing.T) {
	t.Parallel()

	shapes := requiredNullableShapes()
	require.Contains(t, shapes, forbidRowKey, "the roster no longer holds this row")

	ceiling, ok := taggedCeiling(t, shapes[forbidRowKey])
	require.True(t, ok, "the forbid row no longer spells a ceiling")

	order := requiredNullableOrder()

	var within, above int

	for _, pair := range requiredNullableSeeds() {
		if order[fuzzfill.NewCursor(pair[0]).Intn(len(order))] != forbidRowKey {
			continue
		}

		val := reflect.New(shapes[forbidRowKey])
		fuzzfill.Fill(val, pair[1], fuzzfill.WithNilContainers())

		field := val.Elem().Field(0)
		if field.IsNil() {
			continue
		}

		if utf8.RuneCountInString(field.Elem().String()) > ceiling {
			above++
		} else {
			within++
		}
	}

	assert.Positive(t, within, "no seed pair draws the forbid row within its ceiling")
	assert.Positive(t, above, "no seed pair draws the forbid row above its ceiling")
}

// seededNilRows counts, per roster row, the seed pairs that draw that row and
// leave its bare collection nil. It resolves the row the way the target does,
// so a shape blob that stopped selecting the row its seed was written for shows
// up here rather than as a silently narrower search.
func seededNilRows(t *testing.T) map[string]int {
	t.Helper()

	order := requiredNullableOrder()
	shapes := requiredNullableShapes()
	counts := make(map[string]int, len(order))

	for _, pair := range requiredNullableSeeds() {
		name := order[fuzzfill.NewCursor(pair[0]).Intn(len(order))]

		val := reflect.New(shapes[name])
		fuzzfill.Fill(val, pair[1], fuzzfill.WithNilContainers())

		field := val.Elem().Field(0)
		if kind := field.Kind(); (kind == reflect.Slice || kind == reflect.Map) && field.IsNil() {
			counts[name]++
		}
	}

	return counts
}

// TestEmptyBareCollectionSkipsTheFloorAlone guards
// FuzzValidatorRequiredNullableShapes from going inert at the skip. The skip
// predicate has to let through every occurrence that marshals to null, since
// those are the comparisons the target exists for, and skip only the allocated
// empty collection the size floor divides the two validators on.
func TestEmptyBareCollectionSkipsTheFloorAlone(t *testing.T) {
	t.Parallel()

	var (
		nilSlice   []int
		emptySlice = []int{}
	)

	for name, tc := range map[string]struct {
		value any
		want  bool
	}{
		"a nil bare slice":              {value: []int(nil)},
		"a nil bare map":                {value: map[string]int(nil)},
		"a nil bare byte slice":         {value: []byte(nil)},
		"a filled bare slice":           {value: []int{1}},
		"a filled bare map":             {value: map[string]int{"a": 1}},
		"a nil pointer to a slice":      {value: (*[]int)(nil)},
		"a pointer to a nil slice":      {value: &nilSlice},
		"a pointer to an empty slice":   {value: &emptySlice},
		"an allocated empty slice":      {value: []int{}, want: true},
		"an allocated empty map":        {value: map[string]int{}, want: true},
		"an allocated empty byte slice": {value: []byte{}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, emptyBareCollection(reflect.ValueOf(tc.value)),
				"the skip predicate disagrees on %s", name)
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
