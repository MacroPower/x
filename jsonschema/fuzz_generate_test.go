package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzgen"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

var (
	// ProvisionalToken matches the name ir.go mints for a definition before
	// its name is settled, the base name followed by "@" and an index. A
	// token that renders is a dangling reference, so no $ref and no $defs
	// key may match it.
	provisionalToken = regexp.MustCompile(`@\d+$`)

	// MetaSchemaIDs names the vendored metaschema the output of each draft
	// declares, the document the rig validates every generated schema
	// against.
	metaSchemaIDs = map[jsonschema.Draft]string{
		jsonschema.Draft2020: "https://json-schema.org/draft/2020-12/schema",
		jsonschema.Draft7:    "http://json-schema.org/draft-07/schema#",
	}

	// GenerateSeedDraws indexes the shape blobs the seed corpus takes from
	// fuzzshape.Blobs, chosen so the seeds reach each class the tagged draw
	// adds; TestGenerateSeedsReachEveryClass pins what the set covers.
	generateSeedDraws = []int{71, 80, 125, 180, 214, 247, 365}
)

// FuzzGenerateInvariants asserts that every schema Generate emits is
// well-formed by construction, over the tagged shape draw and the options
// draw: shapes carrying jsonschema and validate tags, provider and extender
// types, text and JSON marshalers, and recursive types, generated under
// either draft with definitions on or off, open or closed objects, a root
// title, defaults from an instance, type schema overrides, and extenders.
//
// For a shape and options that generate, the rig asserts:
//
//   - Compile with no resolver succeeds, so every $ref resolves inside the
//     document, and no $ref or $defs key carries a provisional token.
//   - The output validates against the vendored metaschema of its draft.
//   - Generation is pure: a second call yields identical bytes, so does a
//     call after generating an unrelated pool type under the same options,
//     and every schema an override handed in is byte-for-byte what it was
//     before the run.
//   - The extracted and inlined forms agree: WithDefinitions(true) and
//     WithDefinitions(false) accept and reject the same instances, and so
//     does Inline of the extracted form on a shape reaching no recursive
//     type, since Inline refuses a $defs cycle.
//   - T and *T agree on every non-null instance where both generate.
//   - A shape drawn without constraint tags, hook types, or a constraining
//     override accepts the filled value, the property FuzzShapeAccepts
//     asserts on the plain draw.
//   - The validate interpreter ran exactly as often as fuzzgen.InterpreterRuns
//     predicts for each tagged field: once per field of the root, and for a
//     field of a nested tagged type once per definition or per occurrence,
//     inside a subtree a type= pair replaced included.
//
// A refused generation is classified rather than failed: a hook that asked to
// refuse, a tag the dialect refuses on that field, a constraint conflict, a
// defaults instance encoding/json/v2 cannot marshal, or a declaration
// encoding/json/v2 refuses too. A jsonschema tag refusal is a failure when
// fuzzgen.Admitted says every pair the shape carries is one its form admits,
// so a refusal of a documented spelling is caught and not classified away.
// Any other error, and any panic, is a failure.
func FuzzGenerateInvariants(f *testing.F) {
	ctx := context.Background()
	metas := compileMetaSchemas(f)

	addGenerateSeeds(f)

	f.Fuzz(func(t *testing.T, shape, options, values []byte) {
		rt := fuzzgen.TaggedType(shape)
		draw := fuzzgen.Options(options)

		instance := reflect.New(rt)
		fuzzfill.Fill(instance, values, append(fuzzshape.FillOptions(), fuzzfill.WithFull())...)

		runs := map[string]int{}
		interpreter := jsonschema.WithTagInterpreter("validate",
			countingInterpreter{inner: validate.NewInterpreter(), runs: runs})
		unrelated := append(slices.Clone(draw.Opts), interpreter)

		opts := slices.Clone(unrelated)
		if draw.Defaults {
			opts = append(opts, jsonschema.WithDefaultsFrom(instance.Interface()))
		}

		handed := cloneSchemas(draw.Handed)

		schema, err := jsonschema.Generate(ctx, rt, opts...)
		if err != nil {
			classifyGenerateRefusal(t, rt, instance, err)

			return
		}

		require.Equalf(t, fuzzgen.InterpreterRuns(rt, draw.Definitions), runs,
			"the validate interpreter ran a different set of fields of %s than the draw predicts", rt)

		requireHandedUntouched(t, rt, draw.Handed, handed)

		validator, err := jsonschema.Compile(ctx, schema)
		require.NoErrorf(t, err, "compile, with no resolver, the schema generated for %s:\n%s",
			rt, indentSchema(t, schema))

		requireNoProvisionalTokens(t, rt, schema)

		doc := marshalSchema(t, schema)
		require.NoErrorf(t, metas[draw.Draft].ValidateJSON(ctx, []byte(doc)),
			"the schema generated for %s violates its metaschema:\n%s", rt, indentSchema(t, schema))

		again, err := jsonschema.Generate(ctx, rt, opts...)
		require.NoErrorf(t, err, "a second generation of %s refused", rt)
		require.Equalf(t, doc, marshalSchema(t, again),
			"generation of %s is not idempotent", rt)

		_, err = jsonschema.Generate(ctx, reflect.TypeFor[fuzzshape.Leaf](), unrelated...)
		require.NoError(t, err, "generate an unrelated pool type under the drawn options")

		third, err := jsonschema.Generate(ctx, rt, opts...)
		require.NoErrorf(t, err, "a generation of %s after an unrelated one refused", rt)
		require.Equalf(t, doc, marshalSchema(t, third),
			"generation of %s changed after generating an unrelated type", rt)

		requireHandedUntouched(t, rt, draw.Handed, handed)

		corpus := instanceCorpus(t, rt, instance, schema)

		extracted, extractedValidator := generateVariant(ctx, t, rt, opts, jsonschema.WithDefinitions(true))
		_, inlinedValidator := generateVariant(ctx, t, rt, opts, jsonschema.WithDefinitions(false))
		requireSameVerdicts(ctx, t, rt, corpus,
			extractedValidator, "WithDefinitions(true)", inlinedValidator, "WithDefinitions(false)")

		if !fuzzgen.Recursive(rt) {
			inlined, err := jsonschema.Inline(ctx, extracted)
			require.NoErrorf(t, err, "Inline the extracted schema of %s:\n%s", rt, indentSchema(t, extracted))

			compiled, err := jsonschema.Compile(ctx, inlined)
			require.NoErrorf(t, err, "compile the inlined schema of %s:\n%s", rt, indentSchema(t, inlined))

			requireSameVerdicts(ctx, t, rt, corpus, extractedValidator, "the extracted form", compiled, "Inline")
		}

		pointer, err := jsonschema.Generate(ctx, reflect.PointerTo(rt), opts...)
		if err != nil {
			classifyGenerateRefusal(t, reflect.PointerTo(rt), instance, err)
		} else {
			pointerValidator, err := jsonschema.Compile(ctx, pointer)
			require.NoErrorf(t, err, "compile the schema generated for *%s:\n%s", rt, indentSchema(t, pointer))

			delete(corpus, "null")
			requireSameVerdicts(ctx, t, rt, corpus, validator, "T", pointerValidator, "*T")
		}

		if data, ok := corpus["the instance"]; ok && !fuzzgen.Constrained(rt) && !draw.Constrains {
			require.NoErrorf(t, validator.ValidateJSON(ctx, data),
				"the schema generated for an unconstrained %s rejects a value of it\n"+
					"marshaled: %s\nschema:    %s", rt, data, indentSchema(t, schema))
		}
	})
}

// compileMetaSchemas compiles the vendored metaschema of each draft once, so
// every iteration validates against a ready validator.
func compileMetaSchemas(f *testing.F) map[jsonschema.Draft]*jsonschema.Validator {
	f.Helper()

	byID, opts := loadMetaSchemas(f)
	out := make(map[jsonschema.Draft]*jsonschema.Validator, len(metaSchemaIDs))

	for draft, id := range metaSchemaIDs {
		meta, ok := byID[id]
		require.True(f, ok, "metaschema %s is not vendored", id)

		compiled, err := jsonschema.Compile(context.Background(), meta, opts...)
		require.NoError(f, err, "compile the %s metaschema", id)

		out[draft] = compiled
	}

	return out
}

// countingInterpreter records how often a tag interpreter runs per field,
// keyed by fuzzgen.RunKey, and defers to the interpreter it wraps.
type countingInterpreter struct {
	inner jsonschema.TagInterpreter
	runs  map[string]int
}

// Interpret implements [jsonschema.TagInterpreter].
func (c countingInterpreter) Interpret(ctx context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
	c.runs[fuzzgen.RunKey(field.Owner, field.StructField.Name)]++

	return c.inner.Interpret(ctx, field, tag) //nolint:wrapcheck // A pass-through wrapper.
}

// classifyGenerateRefusal accepts a refusal generation documents and fails
// on any other: a hook that asked to refuse, a tag the dialect refuses on
// that field, a constraint conflict, a defaults instance encoding/json/v2
// cannot marshal, or a declaration encoding/json/v2 refuses too. A
// jsonschema tag refusal on a shape whose every pair is admitted is a
// failure.
func classifyGenerateRefusal(t *testing.T, rt reflect.Type, instance reflect.Value, err error) {
	t.Helper()

	switch {
	case errors.Is(err, fuzzgen.ErrHook),
		errors.Is(err, jsonschema.ErrConstraintConflict),
		errors.Is(err, jsonschema.ErrConflictingTypeSchema):
		return

	case errors.Is(err, jsonschema.ErrInvalidDefaultsInstance):
		_, marshalErr := json.Marshal(instance.Interface())
		require.Errorf(
			t,
			marshalErr,
			"generation refused the defaults instance of %s (%v) but encoding/json/v2 marshals it",
			rt,
			err,
		)

		return

	case strings.Contains(err.Error(), "jsonschema tag:"):
		// A drawn pair the dialect refuses on that field. The oracle names
		// the pairs the package documents as admitted on the field's form;
		// a refusal of one of those is the bug class the rig exists for,
		// and any other refusal is one the tag rigs judge.
		require.Falsef(t, fuzzgen.Admitted(rt),
			"generation refused a jsonschema tag every field's form admits on %s: %v", rt, err)

		return

	case strings.Contains(err.Error(), `tag interpreter "validate":`):
		// A drawn validate spelling the dialect refuses on that field. The
		// validate parity rig judges which refusals are right; this rig asks
		// only that a refusal is one the dialect reports.
		return

	default:
		requireV2RefusesValue(t, rt, err)
	}
}

// requireNoProvisionalTokens fails on a $ref or a $defs key carrying the
// token shape ir.go mints before a definition's name is settled.
func requireNoProvisionalTokens(t *testing.T, rt reflect.Type, schema *jsonschema.Schema) {
	t.Helper()

	for loc, sub := range jsonschema.Schemas(schema) {
		require.Falsef(t, provisionalToken.MatchString(sub.Ref),
			"the schema generated for %s carries the provisional reference %q at %s", rt, sub.Ref, loc.Pointer)

		for name := range sub.Defs {
			require.Falsef(t, provisionalToken.MatchString(name),
				"the schema generated for %s carries the provisional definition %q at %s", rt, name, loc.Pointer)
		}

		for name := range sub.Definitions {
			require.Falsef(t, provisionalToken.MatchString(name),
				"the schema generated for %s carries the provisional definition %q at %s", rt, name, loc.Pointer)
		}
	}
}

// cloneSchemas deep-copies the schemas an override handed in, the state a
// pure generation leaves them in.
func cloneSchemas(handed []*jsonschema.Schema) []*jsonschema.Schema {
	out := make([]*jsonschema.Schema, len(handed))
	for i, s := range handed {
		out[i] = schemaclone.Clone(s)
	}

	return out
}

// requireHandedUntouched fails if generation wrote into a schema an override
// handed in.
func requireHandedUntouched(t *testing.T, rt reflect.Type, handed, before []*jsonschema.Schema) {
	t.Helper()

	for i := range handed {
		require.Equalf(t, marshalSchema(t, before[i]), marshalSchema(t, handed[i]),
			"generating %s wrote into a schema an override handed in", rt)
	}
}

// generateVariant generates rt under opts with one more option and compiles
// the result. Options apply in order, so the variant overrides the drawn
// value of the same option.
func generateVariant(
	ctx context.Context, t *testing.T, rt reflect.Type,
	opts []jsonschema.GenerateOption, variant jsonschema.GenerateOption,
) (*jsonschema.Schema, *jsonschema.Validator) {
	t.Helper()

	schema, err := jsonschema.Generate(ctx, rt, append(slices.Clone(opts), variant)...)
	require.NoErrorf(t, err, "generate %s under the variant option", rt)

	validator, err := jsonschema.Compile(ctx, schema)
	require.NoErrorf(t, err, "compile the variant schema of %s:\n%s", rt, indentSchema(t, schema))

	return schema, validator
}

// instanceCorpus builds the instances two schemas of one type must agree
// on: the filled value, its near misses built the way FuzzShapeRejectsNearMiss
// builds them, and the literals no object root admits. The filled value
// joins only when encoding/json/v2 marshals it.
func instanceCorpus(
	t *testing.T, rt reflect.Type, instance reflect.Value, schema *jsonschema.Schema,
) map[string][]byte {
	t.Helper()

	corpus := map[string][]byte{
		"null":            []byte(`null`),
		"an empty object": []byte(`{}`),
		"an empty array":  []byte(`[]`),
		"a number":        []byte(`1`),
	}

	data, err := json.Marshal(instance.Interface())
	if err != nil {
		return corpus
	}

	corpus["the instance"] = data

	var object map[string]jsontext.Value

	if json.Unmarshal(data, &object) != nil || object == nil {
		return corpus
	}

	if _, present := object[nearMissSentinel]; !present {
		extra := maps.Clone(object)
		extra[nearMissSentinel] = jsontext.Value(`1`)

		mutated, err := json.Marshal(extra)
		require.NoErrorf(t, err, "marshal the extra-property near miss for %s", rt)

		corpus["an extra property"] = mutated
	}

	for _, name := range schema.Required {
		missing := maps.Clone(object)
		delete(missing, name)

		mutated, err := json.Marshal(missing)
		require.NoErrorf(t, err, "marshal the missing-property near miss for %s", rt)

		corpus["missing "+name] = mutated
	}

	return corpus
}

// requireSameVerdicts fails on the first instance the two validators judge
// differently.
func requireSameVerdicts(
	ctx context.Context, t *testing.T, rt reflect.Type, corpus map[string][]byte,
	a *jsonschema.Validator, aName string, b *jsonschema.Validator, bName string,
) {
	t.Helper()

	for _, name := range slices.Sorted(maps.Keys(corpus)) {
		data := corpus[name]
		aRejects := a.ValidateJSON(ctx, data) != nil
		bRejects := b.ValidateJSON(ctx, data) != nil

		require.Equalf(t, aRejects, bRejects,
			"%s and %s disagree on %s for %s\ninstance: %s\n%s rejects: %v\n%s rejects: %v",
			aName, bName, name, rt, data, aName, aRejects, bName, bRejects)
	}
}

// optionSeedBlobs are the options blobs every shape seed is paired with.
func optionSeedBlobs() [][]byte {
	return [][]byte{
		make([]byte, 32),
		bytes.Repeat([]byte{0xff}, 32),
		bytes.Repeat([]byte{0x5a, 0xa5, 0x3c}, 11),
	}
}

// addGenerateSeeds seeds the generation invariants rig with every shape seed
// under every options seed, each with a zeroed and a saturated value blob.
func addGenerateSeeds(f *testing.F) {
	f.Helper()

	blobs := fuzzshape.Blobs(slices.Max(generateSeedDraws) + 1)

	for _, draw := range generateSeedDraws {
		for _, options := range optionSeedBlobs() {
			f.Add(blobs[draw], options, make([]byte, 96))
			f.Add(blobs[draw], options, bytes.Repeat([]byte{0xff}, 96))
		}
	}
}

// TestGenerateSeedsReachEveryClass asserts the seed corpus of
// FuzzGenerateInvariants, the only part of it the fast gate runs, reaches
// each class the tagged draw and the options draw add, so the corpus cannot
// quietly stop exercising one.
func TestGenerateSeedsReachEveryClass(t *testing.T) {
	t.Parallel()

	blobs := fuzzshape.Blobs(slices.Max(generateSeedDraws) + 1)
	seen := map[string]bool{}

	for _, draw := range generateSeedDraws {
		rt := fuzzgen.TaggedType(blobs[draw])
		seen["a constrained shape"] = seen["a constrained shape"] || fuzzgen.Constrained(rt)
		seen["an unconstrained shape"] = seen["an unconstrained shape"] || !fuzzgen.Constrained(rt)
		seen["a recursive shape"] = seen["a recursive shape"] || fuzzgen.Recursive(rt)

		for field := range rt.Fields() {
			seen["a jsonschema tag"] = seen["a jsonschema tag"] || field.Tag.Get("jsonschema") != ""
			seen["a validate tag"] = seen["a validate tag"] || field.Tag.Get("validate") != ""
			seen["a crossed field"] = seen["a crossed field"] ||
				strings.HasPrefix(field.Tag.Get("jsonschema"), "type=") && field.Tag.Get("validate") != ""
		}

		if runs := fuzzgen.InterpreterRuns(rt, false); len(runs) > 0 {
			seen["a hooked field"] = true
		}

		if fuzzgen.InterpreterRuns(rt, false)[fuzzgen.RunKey(reflect.TypeFor[fuzzgen.Tagged](), "N")] > 0 {
			seen["a nested tagged type"] = true
		}

		if fuzzgen.Admitted(rt) {
			for field := range rt.Fields() {
				seen["an admitted jsonschema tag"] = seen["an admitted jsonschema tag"] ||
					field.Tag.Get("jsonschema") != ""
			}
		}
	}

	for _, options := range optionSeedBlobs() {
		d := fuzzgen.Options(options)
		seen["draft 7"] = seen["draft 7"] || d.Draft == jsonschema.Draft7
		seen["definitions off"] = seen["definitions off"] || !d.Definitions
		seen["an override"] = seen["an override"] || len(d.Handed) > 0
		seen["defaults"] = seen["defaults"] || d.Defaults
	}

	for _, class := range []string{
		"a constrained shape", "an unconstrained shape", "a recursive shape",
		"a jsonschema tag", "a validate tag", "a crossed field", "a hooked field",
		"a nested tagged type", "an admitted jsonschema tag",
		"draft 7", "definitions off", "an override", "defaults",
	} {
		require.True(t, seen[class], "the seed corpus reaches no %s", class)
	}
}
