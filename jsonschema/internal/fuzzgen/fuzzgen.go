// Package fuzzgen synthesizes the struct types the generation invariants rig
// draws: [fuzzshape.Type]'s shapes widened with hook types, recursive types,
// a struct whose own fields carry tags, and a jsonschema tag and a validate
// tag beside the json tag, plus the generation options the rig runs them
// under. It also answers two questions about a drawn type the rig holds
// generation to: whether every jsonschema pair it carries is one the field's
// shape admits ([Admitted]), and how often each tagged field reaches a tag
// interpreter ([InterpreterRuns]). It lives beside fuzzshape
// rather than in it because the hook types name the jsonschema package in
// their method sets, which fuzzshape cannot import: the package's own tests
// draw fuzzshape shapes, and a cycle through a test is still a cycle.
//
// The package is test-only infrastructure; it is not part of the fast gate.
package fuzzgen

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
)

// Provided is a named type declaring its own schema through a
// [jsonschema.JSONSchemaProvider], which generation extracts to $defs. The
// declared schema pins the property the type marshals, with a floor the
// marshaled value need not clear, so a shape holding one is constrained.
type Provided struct {
	N int `json:"n"`
}

// JSONSchema implements [jsonschema.JSONSchemaProvider].
func (Provided) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type: typeObject,
		Properties: map[string]*jsonschema.Schema{
			"n": {Type: typeInteger},
		},
		Required: []string{"n"},
	}}, nil
}

// Edited is a named type whose [jsonschema.JSONSchemaExtender] removes one
// reflected property and retypes another, the documented add-remove-modify
// contract. The retyped property no longer admits what the field marshals,
// so a shape holding one is constrained.
type Edited struct {
	B string `json:"b"`
	A int    `json:"a"`
}

// JSONSchemaExtend implements [jsonschema.JSONSchemaExtender].
func (Edited) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	delete(ts.Value.Properties, "a")

	ts.Value.Properties["b"] = &jsonschema.Schema{Type: typeInteger}
	ts.Value.Required = []string{"b"}

	return nil
}

// Replaced is a named type whose extender swaps the reflected schema for a
// fresh one through TypeSchema.Value, so the render path must not write the
// node-backed fields back into a schema that never carried them.
type Replaced struct {
	A int `json:"a"`
}

// JSONSchemaExtend implements [jsonschema.JSONSchemaExtender].
func (Replaced) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value = &jsonschema.Schema{Type: typeObject, Description: "opaque"}

	return nil
}

// Stamp is a named string type marshaling itself as text, so a field of it
// is a string schema over the text the method writes rather than the Go
// string.
type Stamp string

// MarshalText implements [encoding.TextMarshaler].
func (s Stamp) MarshalText() ([]byte, error) {
	return []byte("T:" + string(s)), nil
}

// marker carries the MarshalJSON that Marked promotes.
type marker struct{}

// MarshalJSON implements [encoding/json.Marshaler].
func (marker) MarshalJSON() ([]byte, error) {
	return []byte(`"marked"`), nil
}

// Marked is a named struct that marshals itself through a method an embedded
// field promotes, the shape reflection cannot know the output of. Generation
// reflects it as unrestricted, and the rig asserts that outcome is
// well-formed rather than what it accepts.
type Marked struct {
	marker

	Extra int `json:"extra"`
}

// Faulty is a named type with no hooks of its own; the [Options] draw may
// attach an extender to it that returns [ErrHook], so a rig can exercise the
// refusal a hook asks for.
type Faulty struct {
	N int `json:"n"`
}

// Tagged is a named struct whose own fields carry dialect tags, so a shape
// holding one reaches field-level hooks below the root: inside an extracted
// definition, an inlined body, and a subtree a type= pair replaced. Its
// rules constrain the filled value, so a shape holding one is constrained.
type Tagged struct {
	S string `json:"s" validate:"required"`
	N int    `json:"n" validate:"min=1"`
	M int    `json:"m" jsonschema:"minimum=1"`
}

// Coded is a named integer type whose provider declares a string schema, so
// a tag on a field of it is judged against the declared string rather than
// the numeric Go kind.
type Coded int64

// JSONSchema implements [jsonschema.JSONSchemaProvider].
func (Coded) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: formString, Pattern: "^c[0-9]+$"}}, nil
}

// Labeled is a named struct whose extender replaces the reflected object with
// a string schema, so a tag on a field of it is judged against the declared
// string rather than the struct kind.
type Labeled struct {
	V string `json:"v"`
}

// JSONSchemaExtend implements [jsonschema.JSONSchemaExtender].
func (Labeled) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value = &jsonschema.Schema{Type: formString, Description: "label"}

	return nil
}

// Pairs is a named slice whose provider declares an object schema, so a tag
// on a field of it is judged against the declared object rather than the
// slice kind.
type Pairs []string

// JSONSchema implements [jsonschema.JSONSchemaProvider].
func (Pairs) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type:                 typeObject,
		AdditionalProperties: &jsonschema.Schema{Type: formString},
	}}, nil
}

// Ring reaches itself through a pointer, a slice, and a map, the three edges
// a $defs cycle can take.
type Ring struct {
	Index map[string]*Ring `json:"index"`
	Left  *Ring            `json:"left"`
	Right []Ring           `json:"right"`
}

// Ping and Pong reach each other, a two-type cycle no single definition
// closes.
type Ping struct {
	Pong *Pong `json:"pong"`
}

// Pong is Ping's counterpart; see Ping.
type Pong struct {
	Ping *Ping `json:"ping"`
}

// Chain re-enters itself by value through a slice and through a two-level
// pointer, the shape a fill that only marks pointer types would follow
// forever.
type Chain struct {
	Next **Chain `json:"next"`
	Kids []Chain `json:"kids"`
}

// The JSON forms the tag draws key on, and the JSON type names the hook
// schemas declare.
const (
	formString   = "string"
	formNumber   = "number"
	formBool     = "bool"
	formSequence = "sequence"
	formMap      = "map"
	formObject   = "object"
	formText     = "text"
	formOther    = "other"

	typeObject  = "object"
	typeInteger = "integer"

	// CrossOdds is the share of hook-typed and tagged-typed fields the draw
	// hands a type= pair, a key the declared type admits, and a validate
	// spelling for that type together, so the three seams meet on one field.
	crossOdds = 4
)

// The pools the tagged draw adds to fuzzshape's component pool, each
// recording the constraint that keeps [TaggedType] total, and the spelling
// tables the tag draws pair with them.
//
//nolint:goconst // The spelling tables repeat their tokens by design.
var (
	// ErrHook is the error the drawn Faulty extender returns, so a rig can
	// tell a refusal a hook asked for from one generation found on its own.
	ErrHook = errors.New("fuzzshape: the drawn hook refuses")

	// HookTypes carry a provider, an extender, or a marshal method, the
	// classes the plain pool excludes on purpose. None is anonymous-embedded,
	// since [reflect.StructOf] refuses a method-bearing embed beside another
	// field, so each is drawn as a plain field type, behind a pointer or
	// inside a collection as well.
	hookTypes = []reflect.Type{
		reflect.TypeFor[Provided](),
		reflect.TypeFor[*Provided](),
		reflect.TypeFor[[]Provided](),
		reflect.TypeFor[Edited](),
		reflect.TypeFor[map[string]Edited](),
		reflect.TypeFor[Replaced](),
		reflect.TypeFor[Stamp](),
		reflect.TypeFor[*Stamp](),
		reflect.TypeFor[Marked](),
		reflect.TypeFor[Faulty](),
		reflect.TypeFor[Coded](),
		reflect.TypeFor[*Coded](),
		reflect.TypeFor[Labeled](),
		reflect.TypeFor[Pairs](),
	}

	// TaggedTypes hold [Tagged] behind each edge a body is reached through,
	// so the hooks its fields reach run under a definition, a pointer, and a
	// collection element.
	taggedTypes = []reflect.Type{
		reflect.TypeFor[Tagged](),
		reflect.TypeFor[*Tagged](),
		reflect.TypeFor[[]Tagged](),
		reflect.TypeFor[map[string]Tagged](),
	}

	// RecursiveTypes reach themselves, directly or through another type, so
	// a shape holding one generates a $defs cycle.
	recursiveTypes = []reflect.Type{
		reflect.TypeFor[Ring](),
		reflect.TypeFor[*Ring](),
		reflect.TypeFor[Ping](),
		reflect.TypeFor[[]Chain](),
		reflect.TypeFor[*Chain](),
		reflect.TypeFor[map[string]*Pong](),
	}

	// The hook, recursive, and tagged types together are the plain-field
	// types the tagged draw adds to fuzzshape's component pool.
	extraTypes = slices.Concat(hookTypes, recursiveTypes, taggedTypes)

	// The named types each class contains, for [Constrained] and
	// [Recursive] to look a field type up by.
	hookNamed = map[reflect.Type]bool{
		reflect.TypeFor[Provided](): true,
		reflect.TypeFor[Edited]():   true,
		reflect.TypeFor[Replaced](): true,
		reflect.TypeFor[Stamp]():    true,
		reflect.TypeFor[Marked]():   true,
		reflect.TypeFor[Faulty]():   true,
		reflect.TypeFor[Coded]():    true,
		reflect.TypeFor[Labeled]():  true,
		reflect.TypeFor[Pairs]():    true,
		reflect.TypeFor[Tagged]():   true,
	}

	// The crossed draw spellings: a declared type, one key that type admits,
	// and the form whose validate spellings pair with it.
	crossDeclared = []struct {
		typ  string
		pair string
		form string
	}{
		{formString, "minLength=1", formString},
		{typeInteger, "minimum=0", formNumber},
		{formNumber, "maximum=1e3", formNumber},
		{"boolean", "const=true", formBool},
		{"array", "minItems=1", formSequence},
		{typeObject, "minProperties=1", formMap},
	}

	recursiveNamed = map[reflect.Type]bool{
		reflect.TypeFor[Ring]():  true,
		reflect.TypeFor[Ping]():  true,
		reflect.TypeFor[Pong]():  true,
		reflect.TypeFor[Chain](): true,
	}

	// The parameter spellings the jsonschema tag draw pairs with each key.
	// Every key of [tagparse.Keys] has an entry, pinned by a guard test, and
	// each entry mixes the spellings the tag admits with ones it refuses, so
	// a refusal classification is exercised beside a generated schema.
	tagBounds   = []string{"0", "1", "-2.5", "1e3", "abc"}
	tagCounts   = []string{"0", "1", "3", "-1", "010"}
	tagFlags    = []string{"true", "false", "maybe"}
	tagLiterals = []string{"1", "x", "", "null", "1.5", "true", "aGk="}
	tagLists    = []string{"1|2", "a|b", "1|x", "true|false"}

	tagParams = map[string][]string{
		"description":      {"a field", `Hello\, World`},
		"title":            {"Title"},
		"type":             {"string", "integer", "number", "boolean", "array", "object", "null"},
		"minimum":          tagBounds,
		"maximum":          tagBounds,
		"exclusiveMinimum": tagBounds,
		"exclusiveMaximum": tagBounds,
		"multipleOf":       {"1", "0.5", "0", "-1"},
		"minLength":        tagCounts,
		"maxLength":        tagCounts,
		"minItems":         tagCounts,
		"maxItems":         tagCounts,
		"minProperties":    tagCounts,
		"maxProperties":    tagCounts,
		"pattern":          {"^a", "[0-9]+", "["},
		"format":           {"email", "date-time", "uuid", "bogus"},
		"deprecated":       tagFlags,
		"readOnly":         tagFlags,
		"writeOnly":        tagFlags,
		"uniqueItems":      tagFlags,
		"default":          tagLiterals,
		"const":            tagLiterals,
		"enum":             tagLists,
		"examples":         tagLists,
	}

	// TagKeys is the sorted vocabulary, so a draw indexes it deterministically.
	tagKeys = tagparse.Keys()

	// The keys any field can carry, and the keys each JSON form admits on top.
	// A draw prefers the form's keys so most tagged shapes generate, and
	// draws from the whole vocabulary otherwise so every refusal is reached.
	tagKeysAny = []string{
		"description",
		"title",
		"deprecated",
		"readOnly",
		"writeOnly",
		"type",
		"default",
		"examples",
	}
	tagKeysString = []string{"minLength", "maxLength", "pattern", "format", "const", "enum"}
	tagKeysNumber = []string{
		"minimum",
		"maximum",
		"exclusiveMinimum",
		"exclusiveMaximum",
		"multipleOf",
		"const",
		"enum",
	}
	tagKeysBool     = []string{"const", "enum"}
	tagKeysSequence = []string{"minItems", "maxItems", "uniqueItems"}
	tagKeysMap      = []string{"minProperties", "maxProperties"}
	tagKeysText     = []string{"minLength", "maxLength", "pattern", "format"}

	// The validate spellings the draw pairs with each JSON form. They are
	// spellings go-playground loads, since the validate parity rig judges the
	// grammar and this draw exists to put both dialects on one field.
	validateSpellings = map[string][]string{
		"string": {
			"required",
			"min=1",
			"max=3",
			"len=2",
			"eq=a",
			"ne=b",
			"oneof=a b",
			"email",
			"alphanum",
			"omitempty,min=1",
			"required|min=3",
			"min=1|max=2",
			"omitnil",
		},
		"number": {
			"required",
			"min=1",
			"max=3",
			"gt=0",
			"lt=10",
			"gte=1",
			"lte=5",
			"eq=1",
			"ne=0",
			"oneof=1 2",
			"omitempty,gt=0",
		},
		"bool": {"required", "eq=true", "ne=false"},
		formSequence: {
			"required",
			"min=1",
			"max=3",
			"len=2",
			"unique",
			"dive,required",
			"dive,min=1",
			"dive,dive,min=1",
			"omitempty,dive,max=3",
			"dive,omitempty",
		},
		"map": {
			"required",
			"min=1",
			"dive,required",
			"dive,keys,min=1,endkeys,max=3",
			"unique",
			"dive,keys,endkeys",
		},
		formObject: {"required", "omitempty", "structonly", "nostructlevel", "omitnil"},
		formText:   {"required", "omitempty", "omitnil"},
		"other":    {"required", "omitempty", "structonly", "nostructlevel", "omitnil"},
	}
)

// TaggedType synthesizes a struct type the way [fuzzshape.Type] does, from
// the wider pool: a plain field may take a hook type or a recursive type, and
// its tag may carry a jsonschema pair and a validate spelling beside the json
// tag. It is total and deterministic under the same argument as
// [fuzzshape.Type]. Marshaling is not total for the same reason, and
// generation is not either: a drawn tag may name a constraint the field's
// shape refuses, which the rigs classify.
func TaggedType(data []byte) reflect.Type {
	return fuzzshape.Synthesize(data, extraTypes, drawDialectTags)
}

// Constrained reports whether a drawn type carries a constraint a filled value
// need not satisfy: a jsonschema or validate tag on a field, or a field whose
// type reaches a hook type, whose declared schema is not the reflected one.
// The accept property runs only on a shape that is not.
func Constrained(rt reflect.Type) bool {
	for field := range rt.Fields() {
		if field.Tag.Get("jsonschema") != "" || field.Tag.Get("validate") != "" {
			return true
		}

		if hookNamed[namedIn(field.Type)] {
			return true
		}
	}

	return false
}

// Recursive reports whether a drawn type reaches a recursive pool type, so
// its extracted schema carries a $defs cycle that [jsonschema.Inline] refuses.
func Recursive(rt reflect.Type) bool {
	for field := range rt.Fields() {
		if recursiveNamed[namedIn(field.Type)] {
			return true
		}
	}

	return false
}

// namedIn returns the named type a field type reaches through its pointer,
// slice, array, and map value levels, or the type itself.
func namedIn(t reflect.Type) reflect.Type {
	for t.Name() == "" {
		switch t.Kind() { //nolint:exhaustive // only the composing kinds descend.
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}

	return t
}

// drawDialectTags draws the json tag fuzzshape's draw draws, then a
// jsonschema tag and a validate tag each half the time, keyed by the JSON
// form the field takes under the json tag's ,string option.
func drawDialectTags(c *fuzzfill.Cursor, ft reflect.Type) reflect.StructTag {
	jsonTag := string(fuzzshape.DrawTag(c, ft))
	form := jsonForm(ft, strings.Contains(jsonTag, ",string"))

	parts := make([]string, 0, 3)
	if jsonTag != "" {
		parts = append(parts, jsonTag)
	}

	// A field of a hook type or a tagged type is where a type= pair, the
	// keys after it, and an interpreter's rules meet a declared or nested
	// schema, so a share of them draw all three at once.
	if hookNamed[namedIn(ft)] && c.Intn(crossOdds) == 0 {
		decl := crossDeclared[c.Intn(len(crossDeclared))]
		spellings := validateSpellings[decl.form]

		return reflect.StructTag(strings.Join(append(parts,
			"jsonschema:"+strconv.Quote("type="+decl.typ+","+decl.pair),
			"validate:"+strconv.Quote(spellings[c.Intn(len(spellings))]),
		), " "))
	}

	if c.Bool() {
		if pairs := drawJSONSchemaTag(c, form); pairs != "" {
			parts = append(parts, "jsonschema:"+strconv.Quote(pairs))
		}
	}

	if c.Bool() {
		spellings := validateSpellings[form]
		parts = append(parts, "validate:"+strconv.Quote(spellings[c.Intn(len(spellings))]))
	}

	return reflect.StructTag(strings.Join(parts, " "))
}

// drawJSONSchemaTag draws up to two key=value pairs, three times in four from
// the keys the form admits and otherwise from the whole vocabulary.
func drawJSONSchemaTag(c *fuzzfill.Cursor, form string) string {
	n := c.Intn(3)
	pairs := make([]string, 0, n)

	for range n {
		pool := tagKeys
		if c.Intn(4) != 0 {
			pool = formKeys(form)
		}

		key := pool[c.Intn(len(pool))]
		params := tagParams[key]
		pairs = append(pairs, key+"="+params[c.Intn(len(params))])
	}

	return strings.Join(pairs, ",")
}

// formKeys returns the keys a field of the form admits beside the keys any
// field takes.
func formKeys(form string) []string {
	switch form {
	case formString:
		return slices.Concat(tagKeysAny, tagKeysString)
	case formNumber:
		return slices.Concat(tagKeysAny, tagKeysNumber)
	case formBool:
		return slices.Concat(tagKeysAny, tagKeysBool)
	case formSequence:
		return slices.Concat(tagKeysAny, tagKeysSequence)
	case formMap, formObject:
		return slices.Concat(tagKeysAny, tagKeysMap)
	case formText:
		return slices.Concat(tagKeysAny, tagKeysText)
	default:
		return tagKeysAny
	}
}

// jsonForm approximates the JSON form a field of ft takes: the form of the
// kind behind its pointers, with a ,string coercion and a byte slice read as
// a string, a text-marshaling type as a string, a hook type as the form its
// hook declares, a struct as an object, [time.Time] as text, and every other
// named or opaque type as other.
func jsonForm(ft reflect.Type, quoted bool) string {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	kind := ft.Kind()

	switch {
	case ft == reflect.TypeFor[Stamp](), ft == reflect.TypeFor[[]byte]():
		return formString
	case ft == reflect.TypeFor[Coded](), ft == reflect.TypeFor[Labeled]():
		return formString
	case ft == reflect.TypeFor[Pairs]():
		return formObject
	case ft == reflect.TypeFor[time.Time]():
		return formText
	case ft == reflect.TypeFor[Marked]():
		return formOther
	case kind == reflect.Struct:
		return formObject
	case kind == reflect.String:
		return formString
	case kind >= reflect.Int && kind <= reflect.Float64:
		if quoted {
			return formString
		}

		return formNumber

	case kind == reflect.Bool:
		if quoted {
			return formString
		}

		return formBool

	case kind == reflect.Slice, kind == reflect.Array:
		return formSequence
	case kind == reflect.Map:
		return formMap
	default:
		return formOther
	}
}

// Draw is what [Options] returns: the generation options drawn, the schemas
// handed to overrides for a purity check, and the facts about the draw a rig
// branches on.
type Draw struct {
	// Opts are the drawn options, without the interpreter registration and
	// the defaults instance, which the rig owns.
	Opts []jsonschema.GenerateOption
	// Handed holds every schema an override passed into generation, so a rig
	// can pin that generation never writes into one.
	Handed []*jsonschema.Schema
	// Draft is the target draft drawn.
	Draft jsonschema.Draft
	// Definitions is the WithDefinitions value drawn.
	Definitions bool
	// Defaults says the draw asked for defaults from the instance.
	Defaults bool
	// Constrains says an override was drawn whose schema a filled value need
	// not satisfy, so the accept property does not hold.
	Constrains bool
}

// Options draws a set of generation options from an entropy blob: the draft,
// whether definitions are extracted, whether objects stay open, the root
// title, defaults from the instance, a type schema override for pool types,
// and an extender for pool types, one of which refuses with [ErrHook]. It is
// total and deterministic.
func Options(data []byte) Draw {
	c := fuzzfill.NewCursor(data)

	var d Draw

	d.Draft = jsonschema.Draft2020
	if c.Bool() {
		d.Draft = jsonschema.Draft7
	}

	d.Definitions = c.Bool()
	d.Defaults = c.Intn(3) == 0

	d.Opts = append(d.Opts,
		jsonschema.WithDraft(d.Draft),
		jsonschema.WithDefinitions(d.Definitions),
		jsonschema.WithAdditionalProperties(c.Bool()),
		jsonschema.WithRootTitle(c.Bool()),
	)

	overrides := []struct {
		t      reflect.Type
		schema func() *jsonschema.Schema
		tight  bool
	}{
		{reflect.TypeFor[fuzzshape.Level](), func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: typeInteger, Description: "level"}
		}, false},
		{reflect.TypeFor[fuzzshape.Leaf](), func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: typeObject, Description: "leaf"}
		}, false},
		{reflect.TypeFor[Stamp](), func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: formString, Pattern: "^T:[a-z]*$"}
		}, true},
		{reflect.TypeFor[Ring](), func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: typeObject, Description: "ring"}
		}, true},
	}

	for _, o := range overrides {
		if c.Intn(4) != 0 {
			continue
		}

		schema := o.schema()
		d.Handed = append(d.Handed, schema)
		d.Opts = append(d.Opts, jsonschema.WithTypeSchema(o.t, jsonschema.TypeSchema{Value: schema}))
		d.Constrains = d.Constrains || o.tight
	}

	for _, t := range []reflect.Type{reflect.TypeFor[fuzzshape.Leaf](), reflect.TypeFor[Edited](), reflect.TypeFor[Provided]()} {
		if c.Intn(4) != 0 {
			continue
		}

		d.Opts = append(d.Opts, jsonschema.WithTypeSchemaExtender(extendDescription(t)))
	}

	if c.Intn(4) == 0 {
		d.Opts = append(d.Opts, jsonschema.WithTypeSchemaExtender(refuse(reflect.TypeFor[Faulty]())))
	}

	return d
}

// extendDescription returns an extender that annotates the given type's
// schema and passes every other type through.
func extendDescription(target reflect.Type) jsonschema.TypeSchemaExtenderFunc {
	return func(_ context.Context, tc jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
		if tc.Type != target || ts.Value == nil {
			return nil
		}

		ts.Value.Description = "extended"

		return nil
	}
}

// refuse returns an extender that fails generation with [ErrHook] for the
// given type and passes every other type through.
func refuse(target reflect.Type) jsonschema.TypeSchemaExtenderFunc {
	return func(_ context.Context, tc jsonschema.TypeContext, _ *jsonschema.TypeSchema) error {
		if tc.Type != target {
			return nil
		}

		return ErrHook
	}
}
