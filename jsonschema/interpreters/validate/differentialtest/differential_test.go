package differentialtest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"

	playground "github.com/go-playground/validator/v10"
)

// Rig 2 -- validator differential. The validate interpreter maps
// go-playground/validator tag syntax to JSON Schema constraints without
// depending on the library, so the two implementations of the same tag
// semantics can silently drift. This rig puts go-playground itself in the CI
// loop as the reference implementation and asserts equivalence on a curated,
// fully-modeled tag subset:
//
//	validate.Struct(v) == nil  iff  schema.ValidateJSON(json.Marshal(v)) == nil
//
// Curation is the crux. Only the structural constraints the interpreter models
// faithfully appear here (min/max/len/gt/lt/gte/lte/eq/ne/oneof/required/dive on
// strings, numbers, bools, slices, arrays, and maps, plus unique on slices and
// arrays). The tags the interpreter diverges from go-playground by design are
// excluded: format and pattern tags (go-playground uses its own regexes while
// the schema delegates to internal/format -- that surface is rig 3's job),
// content tags, cross-field and conditional validators, the | OR operator,
// keys...endkeys, and json:",string" numeric *bounds* (the value rules on
// coerced fields are covered, by coercedConstraints below). Every field is
// non-pointer and always present with no omitempty, which eliminates the
// "empty value skipped by one side" divergence class. Unique stays on slices
// and arrays because the interpreter makes it a no-op on maps while
// go-playground rejects duplicate map values.

// stringConstraints exercises the modeled string tags. Length rules are hit
// from both sides by random strings; oneof/eq/ne are biased toward their
// boundary values by the candidate hook.
type stringConstraints struct {
	Min   string `json:"min"    validate:"min=2"`
	Max   string `json:"max"    validate:"max=5"`
	Len   string `json:"len"    validate:"len=3"`
	Gt    string `json:"gt"     validate:"gt=1"`
	Lt    string `json:"lt"     validate:"lt=4"`
	Gte   string `json:"gte"    validate:"gte=2"`
	Lte   string `json:"lte"    validate:"lte=5"`
	OneOf string `json:"one_of" validate:"oneof=alpha beta gamma"`
	Eq    string `json:"eq"     validate:"eq=fixed"`
	Ne    string `json:"ne"     validate:"ne=banned"`
}

// numericConstraints exercises the modeled numeric tags on both an integer and
// a float field. Bounds are chosen well inside the Go type range so the
// interpreter does not clamp, and the float bounds are exactly representable so
// the shortest-decimal marshaling never lands on the far side of a bound.
type numericConstraints struct {
	Min   int     `json:"min"    validate:"min=3"`
	Max   int     `json:"max"    validate:"max=10"`
	Gt    int     `json:"gt"     validate:"gt=3"`
	Lt    int     `json:"lt"     validate:"lt=10"`
	Gte   int     `json:"gte"    validate:"gte=3"`
	Lte   int     `json:"lte"    validate:"lte=10"`
	Eq    int     `json:"eq"     validate:"eq=7"`
	Ne    int     `json:"ne"     validate:"ne=7"`
	LenN  int     `json:"len_n"  validate:"len=5"`
	OneOf int     `json:"one_of" validate:"oneof=1 2 3"`
	FMin  float64 `json:"f_min"  validate:"min=1.5"`
	FMax  float64 `json:"f_max"  validate:"max=9.5"`
}

// boolConstraints exercises the modeled boolean tags go-playground can serve as
// an oracle for. The required+eq=false combination is absent; see
// reasonRequiredEqFalseConflict. Oneof is absent; see reasonOneOfKindPanic.
type boolConstraints struct {
	Eq bool `json:"eq" validate:"eq=true"`
	Ne bool `json:"ne" validate:"ne=false"`
}

// sliceConstraints exercises the modeled slice tags. The unique tag is
// exercised via entropy-produced duplicate elements (the all-equal blobs in the
// seed corpus and duplicates the fuzzer discovers), and dive descends to the
// element schema.
type sliceConstraints struct {
	Min    []int    `json:"min"    validate:"min=1"`
	Max    []int    `json:"max"    validate:"max=3"`
	Len    []int    `json:"len"    validate:"len=2"`
	Eq     []string `json:"eq"     validate:"eq=2"`
	Ne     []string `json:"ne"     validate:"ne=2"`
	Unique []int8   `json:"unique" validate:"unique"`
	Dive   []string `json:"dive"   validate:"dive,min=2"`
}

// arrayConstraints exercises the modeled tags on fixed-length arrays, including
// unique on a small element type so the fuzzer readily finds colliding values.
type arrayConstraints struct {
	Unique [4]int8   `json:"unique" validate:"unique"`
	Dive   [3]string `json:"dive"   validate:"dive,max=4"`
}

// mapConstraints exercises the modeled map size tags. The count both sides see
// is the deduplicated Go map length, so key collisions in fuzzfill do not
// desynchronize them.
type mapConstraints struct {
	Min map[string]int `json:"min" validate:"min=1"`
	Max map[string]int `json:"max" validate:"max=3"`
	Len map[string]int `json:"len" validate:"len=2"`
}

// requiredConstraints exercises required's type-specific non-zero constraint on
// the scalar kinds, where it is equivalent: go-playground rejects the zero
// value and the schema's non-zero keyword (minLength:1, not const:0, const:true)
// rejects the same marshaled form.
//
// Slices and maps are excluded on purpose; see
// reasonRequiredCollectionEmptyFloor. The two validators diverge only on the
// empty collection, which the interpreter rejects by design, so slices and maps
// stay out of the roster. Their nil occurrence agrees, and
// FuzzValidatorRequiredNullableShapes compares it one shape at a time.
type requiredConstraints struct {
	Str  string  `json:"str"   validate:"required"`
	Num  int     `json:"num"   validate:"required"`
	FNum float64 `json:"f_num" validate:"required"`
	Flag bool    `json:"flag"  validate:"required"`
}

// coercedConstraints exercises the value rules on json:",string" fields, whose
// schema is a string while the Go value is a number or bool. Both sides see the
// same instance -- go-playground validates the Go value, the schema validates
// the quoted text encoding/json writes -- so the equivalence holds only because
// the interpreter parses each tag value at the real Go kind and re-serializes
// it. A dialect that compared against the raw literal would pin "5.0" where the
// field emits "5", and this rig would catch it.
//
// Only the value rules appear. The numeric bounds are excluded by design, not
// by omission; see reasonCoercedNumericBounds.
type coercedConstraints struct {
	Eq    int     `json:"eq,string"     validate:"eq=7"`
	Ne    int     `json:"ne,string"     validate:"ne=7"`
	OneOf int8    `json:"one_of,string" validate:"oneof=1 2 3"`
	Req   int     `json:"req,string"    validate:"required"`
	FlagR bool    `json:"flag_r,string" validate:"required"`
	FEq   float64 `json:"f_eq,string"   validate:"eq=2.5"`
	FReq  float64 `json:"f_req,string"  validate:"required"`
	FNe   float64 `json:"f_ne,string"   validate:"ne=0"`
}

func FuzzValidatorStringConstraints(f *testing.F) {
	fuzzValidatorDifferential[stringConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"OneOf": {"alpha", "beta", "gamma", "delta"},
		"Eq":    {"fixed", "other"},
		"Ne":    {"banned", "allowed"},
		"Len":   {"abc", "ab", "abcd"},
	}))
}

func FuzzValidatorNumericConstraints(f *testing.F) {
	fuzzValidatorDifferential[numericConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"Eq":    {"7", "8"},
		"Ne":    {"7", "8"},
		"LenN":  {"5", "6"},
		"OneOf": {"1", "2", "3", "4"},
		"Min":   {"2", "3", "4"},
		"Max":   {"9", "10", "11"},
	}))
}

func FuzzValidatorBoolConstraints(f *testing.F) {
	fuzzValidatorDifferential[boolConstraints](f)
}

func FuzzValidatorSliceConstraints(f *testing.F) {
	fuzzValidatorDifferential[sliceConstraints](f)
}

func FuzzValidatorArrayConstraints(f *testing.F) {
	fuzzValidatorDifferential[arrayConstraints](f)
}

func FuzzValidatorMapConstraints(f *testing.F) {
	fuzzValidatorDifferential[mapConstraints](f)
}

func FuzzValidatorRequiredConstraints(f *testing.F) {
	fuzzValidatorDifferential[requiredConstraints](f)
}

func FuzzValidatorCoercedConstraints(f *testing.F) {
	fuzzValidatorDifferential[coercedConstraints](f, fuzzfill.WithCandidates(map[string][]string{
		"Eq":    {"7", "8"},
		"Ne":    {"7", "8"},
		"OneOf": {"1", "2", "3", "4"},
		"FEq":   {"2.5", "3.5"},
		// Cursor.Float64 reads a whole uint64 as a bit pattern, so exactly one
		// draw in 2^64 yields the negative zero. Naming it as a candidate is
		// what puts that draw within reach.
		"FReq": {"0", "-0", "1"},
		"FNe":  {"0", "-0", "1"},
	}))
}

// fuzzValidatorDifferential is the shared body for every rig-2 target. It
// generates T's schema through the validate interpreter, compiles it, and
// builds a go-playground validator once, then for each blob fills a T and
// asserts the two validators agree on accept/reject. The f.Fuzz callback
// cannot call t.Parallel (the fuzzing framework forbids it).
func fuzzValidatorDifferential[T any](f *testing.F, fillOpts ...fuzzfill.Option) {
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

	addDifferentialSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		var val T

		fuzzfill.Fill(reflect.ValueOf(&val), data, fillOpts...)

		referenceReject, err := referenceRejects(reference, val)
		if err != nil {
			// An InvalidValidationError means the curated roster used a tag
			// go-playground itself cannot handle: a roster bug, not a real
			// rejection to fold into the comparison.
			t.Fatalf("go-playground could not handle a roster tag on %T: %v", val, err)
		}

		instance, err := json.Marshal(val)
		if err != nil {
			return
		}

		schemaReject := validator.ValidateJSON(ctx, instance) != nil

		if referenceReject != schemaReject {
			t.Fatalf(
				"validators disagree on a value of %T\n"+
					"value:            %#v\n"+
					"marshaled:        %s\n"+
					"go-playground:    reject=%v\n"+
					"schema:           reject=%v\n"+
					"schema doc:       %s",
				val, val, instance, referenceReject, schemaReject, schemaJSON,
			)
		}
	})
}

// referenceRejects reports whether go-playground rejects val. It distinguishes
// a real rejection (ValidationErrors) from an *InvalidValidationError (a misused
// or unhandled tag), which it surfaces as an error so the harness fails loudly
// rather than treating the internal error as a rejection.
func referenceRejects(reference *playground.Validate, val any) (bool, error) {
	err := reference.Struct(val)
	if err == nil {
		return false, nil
	}

	if _, ok := errors.AsType[*playground.InvalidValidationError](err); ok {
		return false, fmt.Errorf("go-playground rejected the roster tag: %w", err)
	}

	if _, ok := errors.AsType[playground.ValidationErrors](err); ok {
		return true, nil
	}

	// Struct returns only nil, *InvalidValidationError, or ValidationErrors;
	// anything else is an unexpected contract change worth surfacing.
	return false, fmt.Errorf("go-playground returned an unexpected error type: %w", err)
}

// differentialSeeds returns the entropy blobs every rig-2 target starts from:
// the zero value and a spread of set values, including all-equal runs that
// produce duplicate collection elements for the unique constraint. The list is
// a function rather than a call sequence so a test can draw the same
// population without a testing.F.
func differentialSeeds() [][]byte {
	return [][]byte{
		{},
		make([]byte, 64),
		bytes.Repeat([]byte{0x01}, 96),
		bytes.Repeat([]byte{0x07}, 128),
		bytes.Repeat([]byte{0xff}, 80),
		{0x02, 0x03, 0x05, 0x07, 0x0b, 0x0d, 0x11, 0x13, 0x17, 0x1d, 0x1f, 0x25, 0x29, 0x2b, 0x2f, 0x35},
		// Two blobs long enough to set every field of the widest roster type,
		// drawn from different byte values so the strict region does not rest
		// on one. The cursor zero-extends past its data, so a short blob leaves
		// the tail fields nil or zero and never reaches the region where the
		// two validators see the same information; see
		// TestWidenedDifferentialReachesStrictAgreement.
		bytes.Repeat([]byte{0xff}, 384),
		bytes.Repeat([]byte{0x7f}, 384),
	}
}

// addDifferentialSeeds seeds f from differentialSeeds.
func addDifferentialSeeds(f *testing.F) {
	f.Helper()

	for _, seed := range differentialSeeds() {
		f.Add(seed)
	}
}
