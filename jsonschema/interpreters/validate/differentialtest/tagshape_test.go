package differentialtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"

	playground "github.com/go-playground/validator/v10"
)

// The shape-fuzzing half of rig 2. The hand-written rosters fuzz values over
// shapes someone thought to write down; this target fuzzes the shape as well,
// drawing the axes tag semantics actually turn on: the field's Go kind, whether
// it is a pointer, which json option it carries, and which validate rule sits
// on it.
//
// The draw deliberately does not come from internal/fuzzshape. That package
// synthesizes embeds, unexported fields, and colliding JSON names to probe
// encoding/json field collection, which is a different property and an active
// hazard here. Go-playground recurses into an embedded struct as a nested
// struct, and reflect.StructOf synthesizes a promoted interface method as a
// stub that panics when called. Every field this draw produces is exported,
// flat, and uniquely named.

// maxTagFields bounds the drawn struct. Each field costs a runtime type
// descriptor that is never freed, the same budget internal/fuzzshape documents.
const maxTagFields = 4

// tagKind is one field shape the draw can produce: its Go type, whether
// json:",string" applies to it, and which rule pool it draws from.
type tagKind struct {
	typ       reflect.Type
	pool      []string
	coercible bool
}

// tagKinds is the draw pool. Each entry's rules are the ones both engines model
// for that shape; a rule excluded here carries a reason constant.
func tagKinds() []tagKind {
	scalarString := []string{
		"min=2", "max=5", "len=3", "gt=1", "lt=4", "gte=2", "lte=5",
		"oneof=alpha beta gamma", "eq=fixed", "ne=banned", "required",
	}
	scalarNumber := []string{
		"min=3", "max=10", "gt=3", "lt=10", "gte=3", "lte=10",
		"eq=7", "ne=7", "len=5", "oneof=1 2 3", "required",
	}
	// A float draws the same rules without oneof; see reasonOneOfKindPanic.
	// Filtering rather than reslicing is what keeps the pool from silently
	// losing a rule when scalarNumber grows or is reordered.
	scalarFloat := slices.DeleteFunc(slices.Clone(scalarNumber), func(spelling string) bool {
		return spells(spelling, "oneof")
	})
	// A bool draws no oneof. It never draws required
	// alongside eq=false, which the interpreter reports as a conflict; see
	// reasonRequiredEqFalseConflict. Drawing one rule per field keeps that pair
	// unreachable.
	scalarBool := []string{"eq=true", "ne=false", "required"}
	// A sequence draws no required, since the two validators diverge on the
	// empty collection; see reasonRequiredCollectionEmptyFloor.
	sequence := []string{"min=1", "max=3", "len=2", "eq=2", "ne=2", "unique", "dive,min=2"}
	// A map draws neither required nor unique; see
	// reasonRequiredCollectionEmptyFloor and reasonUniqueMapNoOp.
	mapping := []string{"min=1", "max=3", "len=2", "eq=2", "ne=2"}
	// A byte slice encodes as one base64 string, so it takes the string size
	// rules but has no element schema for oneof to reach. It draws no required
	// either, since an empty non-nil slice passes go-playground and trips the
	// minLength the schema puts on the base64 string; see
	// reasonRequiredCollectionEmptyFloor.
	byteSlice := []string{"min=1", "max=5", "len=3"}

	return []tagKind{
		{typ: reflect.TypeFor[string](), pool: scalarString, coercible: true},
		{typ: reflect.TypeFor[int](), pool: scalarNumber, coercible: true},
		{typ: reflect.TypeFor[int8](), pool: scalarNumber, coercible: true},
		{typ: reflect.TypeFor[float64](), pool: scalarFloat, coercible: true},
		{typ: reflect.TypeFor[bool](), pool: scalarBool, coercible: true},
		{typ: reflect.TypeFor[[]string](), pool: sequence},
		{typ: reflect.TypeFor[[]int8](), pool: sequence},
		{typ: reflect.TypeFor[map[string]int](), pool: mapping},
		{typ: reflect.TypeFor[[]byte](), pool: byteSlice},
	}
}

// drawTaggedStruct synthesizes a struct type from an entropy blob, one field at
// a time. It draws the kind, the pointer wrapper, the json option, and the
// validate rule from independent cursor reads, so the cross product is explored
// rather than a hand-picked corner of it. The draw is total, so every blob
// yields a type reflect.StructOf accepts, encoding/json can marshal, and
// go-playground can walk.
func drawTaggedStruct(c *fuzzfill.Cursor) reflect.Type {
	kinds := tagKinds()
	n := 1 + c.Intn(maxTagFields)
	fields := make([]reflect.StructField, 0, n)

	for i := range n {
		kind := kinds[c.Intn(len(kinds))]

		typ := kind.typ
		if c.Bool() {
			typ = reflect.PointerTo(typ)
		}

		// Both option draws run for every kind, so the cursor advances the same
		// distance whatever the kind is and the rule index below does not shift
		// with the option. A non-coercible kind discards the first draw.
		quoted, omit := c.Bool(), c.Bool()

		option := ""

		switch {
		case kind.coercible && quoted:
			option = ",string"
		case omit:
			option = ",omitempty"
		}

		fields = append(fields, reflect.StructField{
			Name: fmt.Sprintf("F%d", i),
			Type: typ,
			Tag: reflect.StructTag(fmt.Sprintf(
				`json:"f%d%s" validate:%q`, i, option, kind.pool[c.Intn(len(kind.pool))])),
		})
	}

	return reflect.StructOf(fields)
}

// interpreterRejects reports whether err is one the interpreter raises by
// design for a shape that cannot carry a drawn rule. Requiring a known sentinel
// rather than swallowing every error is what keeps a generation the rig broke
// from passing as a shape the interpreter declined.
func interpreterRejects(t *testing.T, err error) {
	t.Helper()

	switch {
	case errorsIsAny(err, tagmodel.ErrUnsupported, jsonschema.ErrConstraintConflict,
		validate.ErrConflictingConstraints, jsonschema.ErrInvalidType):
		return
	default:
		t.Fatalf("generation failed with an unexpected error: %v", err)
	}
}

// errorsIsAny reports whether err matches any of the targets.
func errorsIsAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}

	return false
}

// FuzzValidatorTaggedShapes asserts the agreement property over synthesized
// shapes. It takes two blobs so the shape entropy and the value entropy evolve
// independently, the convention the shape rig in the parent package uses.
func FuzzValidatorTaggedShapes(f *testing.F) {
	f.Helper()

	ctx := context.Background()
	reference := playground.New(playground.WithRequiredStructEnabled())

	for _, shape := range differentialSeeds() {
		for _, value := range differentialSeeds() {
			f.Add(shape, value)
		}
	}

	f.Fuzz(func(t *testing.T, shapeBlob, valueBlob []byte) {
		typ := drawTaggedStruct(fuzzfill.NewCursor(shapeBlob))

		schema, err := jsonschema.Generate(ctx, typ,
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		if err != nil {
			interpreterRejects(t, err)

			return
		}

		validator, err := jsonschema.Compile(ctx, schema)
		require.NoError(t, err, "compile the schema for %s", typ)

		val := reflect.New(typ)
		fuzzfill.Fill(val, valueBlob)

		referenceReject, err := referenceRejects(reference, val.Elem().Interface())
		if err != nil {
			t.Fatalf("go-playground could not handle a drawn tag on %s: %v", typ, err)
		}

		instance, err := json.Marshal(val.Elem().Interface())
		if err != nil {
			return
		}

		schemaReject := validator.ValidateJSON(ctx, instance) != nil

		if !agreementHolds(t, jsonNames(typ), instance, referenceReject, schemaReject) {
			schemaJSON, marshalErr := json.MarshalIndent(schema, "", "  ")
			require.NoError(t, marshalErr)

			t.Fatalf(
				"validators disagree on a value of %s\n"+
					"value:            %#v\n"+
					"marshaled:        %s\n"+
					"every field set:  %v\n"+
					"go-playground:    reject=%v\n"+
					"schema:           reject=%v\n"+
					"schema doc:       %s",
				typ, val.Elem().Interface(), instance,
				everyFieldSet(t, jsonNames(typ), instance),
				referenceReject, schemaReject, schemaJSON,
			)
		}
	})
}

// TestTaggedShapeDrawIsTotal pins the draw's totality claim over the shared
// seed population: every blob must yield a type the generator either accepts or
// declines with a known sentinel, and at least one must be accepted, or the
// target exercises nothing.
func TestTaggedShapeDrawIsTotal(t *testing.T) {
	t.Parallel()

	accepted := 0

	for _, blob := range differentialSeeds() {
		typ := drawTaggedStruct(fuzzfill.NewCursor(blob))

		_, err := jsonschema.Generate(t.Context(), typ,
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		if err != nil {
			interpreterRejects(t, err)

			continue
		}

		accepted++
	}

	assert.Positive(t, accepted, "no drawn shape generated a schema")
}
