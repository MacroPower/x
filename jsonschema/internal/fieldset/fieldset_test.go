package fieldset //nolint:testpackage // In-package by design: the resolution phases are guarded from inside their own package (see jsonschema/CLAUDE.md); the no-in-package-test policy is main-package only.

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

// The key-set oracle: the names the resolution phases emit for a Go type are
// exactly the keys encoding/json marshals from a value of that type. It checks
// dominance against the standard library directly, where the package's two
// end-to-end fuzz rigs see a dominance bug only as a validation failure.
//
// Three classes of type carry no verdict, each with a reason constant and a
// guard test pinning its cause.

// reasonPromotedMarshaler is why a type whose method set carries MarshalJSON or
// MarshalText is skipped.
const reasonPromotedMarshaler = "encoding/json routes the whole value through the marshaler instead of emitting a field-by-field object, so the marshaled output has no relationship to the resolved field set. Whether a pointer-receiver marshaler applies further depends on the value's addressability, which the caller controls, so the type is skipped rather than reasoned about"

// reasonMarshalFailed is why a value encoding/json rejects is skipped.
const reasonMarshalFailed = "encoding/json produced no output for the value, so there is no key set to compare against: a cyclic value, a map with an unsupported key type, or a non-representable float"

// reasonNotObject is why output that is not a JSON object is skipped.
const reasonNotObject = "the marshaled output is not a JSON object, so it has no top-level keys; this is the backstop for a marshaler the method-set probe missed"

// Base is the embed most roster types promote or compose.
type Base struct {
	Alpha string `json:"alpha"`
	Beta  int    `json:"beta,omitempty"`
}

// Other collides with Base on "alpha", so embedding both annihilates the name.
type Other struct {
	Alpha string `json:"alpha"`
	Delta string `json:"delta"`
}

// Deep embeds Base, so a type embedding Deep promotes Base's names at depth 2.
type Deep struct {
	Base

	Epsilon string `json:"epsilon"`
}

// TaggedShared claims "Shared" through an explicit tag name.
type TaggedShared struct {
	Value string `json:"Shared"`
}

// UntaggedShared claims "Shared" through its Go field name, losing the
// same-depth tie-break to TaggedShared.
type UntaggedShared struct {
	Shared string
}

// TaggedDupA and TaggedDupB both claim "dup" through an explicit tag, so
// neither wins and the name annihilates.
type TaggedDupA struct {
	Value string `json:"dup"`
}

// TaggedDupB is TaggedDupA's counterpart; see TaggedDupA.
type TaggedDupB struct {
	Value string `json:"dup"`
}

// WrapA and WrapB each embed Base, so a type embedding both sights Base's
// fields twice at one depth.
type WrapA struct {
	Base
}

// WrapB is WrapA's counterpart; see WrapA.
type WrapB struct {
	Base
}

// Leafish is an embedded non-struct type, keyed by the field name.
type Leafish string

// Marker is an embedded interface, which encoding/json never flattens.
type Marker interface{ Mark() }

type valueEmbed struct {
	Base

	Gamma bool `json:"gamma"`
}

type pointerEmbed struct {
	*Base

	Gamma bool `json:"gamma"`
}

type namedEmbed struct {
	Base `json:"base"`

	Gamma bool `json:"gamma"`
}

type shadowOuter struct {
	Base

	Alpha int `json:"alpha"`
}

type tieBreakWins struct {
	TaggedShared
	UntaggedShared
}

type deepChain struct {
	Deep

	Zeta string `json:"zeta"`
}

// embedding builds a struct type embedding each of types. The three roster
// entries below use it rather than source declarations because they embed two
// types that claim one JSON name, which is the collision the resolution exists
// to settle and which govet's structtag check rejects in source.
func embedding(types ...reflect.Type) reflect.Type {
	fields := make([]reflect.StructField, len(types))
	for i, ft := range types {
		fields[i] = reflect.StructField{Name: ft.Name(), Type: ft, Anonymous: true}
	}

	return reflect.StructOf(fields)
}

type nonStructEmbed struct {
	Leafish

	Gamma bool `json:"gamma"`
}

type interfaceEmbed struct {
	Marker

	Gamma bool `json:"gamma"`
}

type selfEmbed struct {
	*selfEmbed //nolint:unused // The self-embed is the shape under test.

	X int `json:"x"`
}

type omitFields struct {
	A string `json:"a,omitempty"`
	B *int   `json:"b,omitempty"`
	C string `json:"c,omitzero"`
	D string `json:"d"`
}

type excludedFields struct {
	Skip   string `json:"-"`
	Keep   string `json:"-,"` //nolint:staticcheck // Pins the literal "-" name encoding/json gives a trailing-comma tag.
	hidden string //nolint:unused // Pins that an unexported field stays out of the key set.
	Shown  int    `json:"shown"`
}

type stringOptFields struct {
	N int    `json:"n,string"`
	S string `json:"s,string"`
}

type promotedMarshaler struct {
	json.RawMessage

	Gamma bool `json:"gamma"`
}

var (
	typeJSONMarshaler = reflect.TypeFor[json.Marshaler]()
	typeTextMarshaler = reflect.TypeFor[encoding.TextMarshaler]()

	// AmbiguousEmbeds embeds two types claiming "alpha" at one depth, which the
	// tag tie-break cannot settle because both are tagged.
	ambiguousEmbeds = embedding(reflect.TypeFor[Base](), reflect.TypeFor[Other]())
	// TieBreakAnnihilates embeds two types claiming "dup", both tagged.
	tieBreakAnnihilates = embedding(
		reflect.TypeFor[TaggedDupA](), reflect.TypeFor[TaggedDupB](),
	)
	// RepeatedEmbed reaches Base twice at one depth, so its fields annihilate.
	repeatedEmbed = embedding(reflect.TypeFor[WrapA](), reflect.TypeFor[WrapB]())

	// Roster is the hand-written half of the oracle's type population, covering
	// the embed and tag classes reflect.StructOf cannot synthesize or fuzzshape
	// does not draw.
	roster = map[string]reflect.Type{
		"value embed":           reflect.TypeFor[valueEmbed](),
		"pointer embed":         reflect.TypeFor[pointerEmbed](),
		"named embed":           reflect.TypeFor[namedEmbed](),
		"outer shadows embed":   reflect.TypeFor[shadowOuter](),
		"same-depth ambiguity":  ambiguousEmbeds,
		"tag tie-break wins":    reflect.TypeFor[tieBreakWins](),
		"tag tie-break drops":   tieBreakAnnihilates,
		"deep embed chain":      reflect.TypeFor[deepChain](),
		"repeated embed":        repeatedEmbed,
		"embedded non-struct":   reflect.TypeFor[nonStructEmbed](),
		"embedded interface":    reflect.TypeFor[interfaceEmbed](),
		"self-embedding":        reflect.TypeFor[selfEmbed](),
		"omitted fields":        reflect.TypeFor[omitFields](),
		"excluded fields":       reflect.TypeFor[excludedFields](),
		"string-coerced fields": reflect.TypeFor[stringOptFields](),
	}

	// ComposedCandidates are the embed types the predicate sets below designate
	// as allOf-composed. A candidate no type in the population embeds is a
	// no-op, which keeps one list serving both halves.
	composedCandidates = map[string]reflect.Type{
		"Base":         reflect.TypeFor[Base](),
		"Other":        reflect.TypeFor[Other](),
		"Deep":         reflect.TypeFor[Deep](),
		"TaggedShared": reflect.TypeFor[TaggedShared](),
		"WrapA":        reflect.TypeFor[WrapA](),
		"Alpha":        reflect.TypeFor[fuzzshape.Alpha](),
		"Beta":         reflect.TypeFor[fuzzshape.Beta](),
	}

	// PredicateSets names the composed-type sets every type in the population is
	// crossed with. The empty set is the generator's behavior for a type no
	// provider intercepts; the rest reach the ghost machinery, for which nothing
	// driven through the public API can synthesize a shape.
	predicateSets = map[string][]string{
		"none":       {},
		"base":       {"Base"},
		"other":      {"Other"},
		"base+other": {"Base", "Other"},
		"deep":       {"Deep"},
		"tagged":     {"TaggedShared"},
		"wrap":       {"WrapA"},
		"alpha":      {"Alpha"},
		"beta":       {"Beta"},
		"alpha+beta": {"Alpha", "Beta"},
		"all embeds": {"Base", "Other", "Deep", "TaggedShared", "WrapA", "Alpha", "Beta"},
	}
)

// composedIn returns a ComposedFunc over the named candidates.
func composedIn(names []string) ComposedFunc {
	set := map[reflect.Type]bool{}
	for _, n := range names {
		set[composedCandidates[n]] = true
	}

	return func(t reflect.Type) bool { return set[t] }
}

// resolvedNames is what the phases say about one type: the names the marshaled
// object may carry, the subset encoding/json is allowed to leave out, the field
// that won each name, and the names whose value encoding/json re-encodes as a
// quoted string.
type resolvedNames struct {
	names     map[string]bool
	omissible map[string]bool
	winner    map[string]reflect.StructField
	coerced   map[string]bool
}

// resolve runs all three phases, asserts what holds within each, and derives
// the name sets the oracle compares against encoding/json. A property reads its
// options off the classified field, so the option folding the classification
// does is under test; a ghost-won name produces no field, so it reads them off
// the winning sighting instead.
func resolve(t *testing.T, typ reflect.Type, composed ComposedFunc) resolvedNames {
	t.Helper()

	c := NewCollector(composed)

	col, res, out := c.phases(typ)

	// Phase 1 invariants: a key is listed once, and a scanned type is a
	// composed embed the walk reached outside the in-flight guard.
	assert.Len(t, col.Order, len(col.ByName), "every key is listed once")

	seen := map[reflect.Type]bool{}

	for _, ft := range col.Scanned {
		assert.True(t, composed(ft), "a scanned type is a composed embed")
		assert.False(t, seen[ft], "the scanned types are deduplicated")

		seen[ft] = true
	}

	rn := resolvedNames{
		names:     map[string]bool{},
		omissible: map[string]bool{},
		winner:    map[string]reflect.StructField{},
		coerced:   map[string]bool{},
	}

	// A property's options come off the classified field, so the oracle checks
	// the option folding the classification does rather than redoing it.
	for i := range out.Fields {
		f := &out.Fields[i]

		// A composed embed contributes no name of its own, and nothing else may
		// carry the empty name into the key set.
		require.Equal(t, f.ComposeViaAllOf, f.JSONName == "",
			"a field has a JSON name exactly when it is not a composed embed")

		if f.ComposeViaAllOf {
			continue
		}

		rn.names[f.JSONName] = true
		rn.winner[f.JSONName] = f.StructField

		if f.Omitempty || f.Omitzero {
			rn.omissible[f.JSONName] = true
		}

		if f.JSONString {
			rn.coerced[f.JSONName] = true
		}
	}

	ghost := map[string]bool{}

	for _, name := range out.GhostWon {
		ghost[name] = true
		rn.names[name] = true
	}

	winners := map[string]bool{}

	for i := range res.Winners {
		w := &res.Winners[i]
		if w.ComposeAllOf {
			continue
		}

		winners[w.Name] = true

		// A ghost-won name produces no field, so its options come off the
		// winning sighting. Reading them there is what the exported dominance
		// phase is for.
		if !ghost[w.Name] {
			continue
		}

		rn.winner[w.Name] = w.StructField

		info := jsontag.Parse(w.StructField)
		if info.Omitempty || info.Omitzero || w.Optional {
			rn.omissible[w.Name] = true
		}

		if info.JSONString {
			rn.coerced[w.Name] = true
		}
	}

	assert.Equal(t, winners, rn.names,
		"the classified names must be exactly the non-composed winners")

	return rn
}

// marshalObject returns the top-level members encoding/json emits for v, or the
// reason the value carries no verdict.
func marshalObject(v any) (map[string]json.RawMessage, string) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, reasonMarshalFailed
	}

	var obj map[string]json.RawMessage

	err = json.Unmarshal(data, &obj)
	if err != nil {
		return nil, reasonNotObject
	}

	return obj, ""
}

// pointerOnlyMarshaler reports whether only the pointer to t marshals itself.
// Reading such a field back out of the struct copies it, so it loses the
// addressability encoding/json had and would marshal differently.
func pointerOnlyMarshaler(t reflect.Type) bool {
	return hasMarshaler(t) &&
		!t.Implements(typeJSONMarshaler) && !t.Implements(typeTextMarshaler)
}

// checkWinners asserts that the value under each resolved name is what the
// winning field marshals to. Without it the oracle sees only the name set, and
// a dominance rule that picks the wrong field of two claiming one name still
// produces the right names.
func checkWinners(t *testing.T, rv reflect.Value, rn resolvedNames, obj map[string]json.RawMessage) {
	t.Helper()

	for name, raw := range obj {
		sf, ok := rn.winner[name]
		if !ok {
			continue // Soundness already reported the unresolved name.
		}

		// A ",string" field is re-encoded as a quoted string, so the field's
		// own marshaling is not what the object carries.
		if rn.coerced[name] || pointerOnlyMarshaler(sf.Type) {
			continue
		}

		fv, err := rv.FieldByIndexErr(sf.Index)
		if err != nil {
			continue // A nil pointer embed on the path; the name is omitted.
		}

		want, err := json.Marshal(fv.Interface())
		if err != nil {
			continue
		}

		assert.JSONEq(t, string(want), string(raw),
			"name %q carries a value the winning field does not marshal", name)
	}
}

// hasMarshaler reports whether encoding/json would route a value of t through a
// marshaler rather than emitting an object of its fields.
func hasMarshaler(t reflect.Type) bool {
	for _, probe := range []reflect.Type{t, reflect.PointerTo(t)} {
		if probe.Implements(typeJSONMarshaler) || probe.Implements(typeTextMarshaler) {
			return true
		}
	}

	return false
}

// filled returns a settable value of typ populated from blob.
func filled(typ reflect.Type, blob []byte) reflect.Value {
	rv := reflect.New(typ)
	fuzzfill.Fill(rv, blob, fuzzshape.FillOptions()...)

	return rv.Elem()
}

// checkKeySet asserts the oracle for one type under one predicate, over every
// blob: every marshaled key is a resolved name (soundness), and every resolved
// name the options do not excuse appears in the filled value's keys
// (completeness). A blob whose value carries no verdict contributes its reason
// instead, and the caller skips only when no blob reached an assertion.
func checkKeySet(
	t *testing.T,
	typ reflect.Type,
	composed ComposedFunc,
	blobs [][]byte,
) {
	t.Helper()

	if hasMarshaler(typ) {
		t.Skip(reasonPromotedMarshaler)
	}

	rn := resolve(t, typ, composed)

	zeroKeys, skip := marshalObject(reflect.New(typ).Elem().Interface())
	if skip != "" {
		t.Skip(skip)
	}

	for key := range zeroKeys {
		assert.True(t, rn.names[key], "zero value marshals key %q that no phase resolved", key)
	}

	var checked int

	for _, blob := range blobs {
		rv := filled(typ, blob)

		fullKeys, skip := marshalObject(rv.Interface())
		if skip != "" {
			t.Log(skip)

			continue
		}

		checked++

		checkWinners(t, rv, rn, fullKeys)

		for key := range fullKeys {
			assert.True(t, rn.names[key],
				"filled value marshals key %q that no phase resolved", key)
		}

		for name := range rn.names {
			// An omitempty field with a struct type is never omitted by
			// encoding/json, so excusing it only weakens the assertion.
			if rn.omissible[name] {
				continue
			}

			_, present := fullKeys[name]
			assert.True(t, present,
				"resolved name %q is absent from the marshaled object", name)
		}
	}

	if checked == 0 {
		t.Skip(reasonMarshalFailed)
	}
}

// TestKeySetParityRoster runs the oracle over the hand-written roster.
func TestKeySetParityRoster(t *testing.T) {
	t.Parallel()

	blobs := fuzzshape.Blobs(8)

	for name, typ := range roster {
		for setName, set := range predicateSets {
			t.Run(name+"/"+setName, func(t *testing.T) {
				t.Parallel()

				checkKeySet(t, typ, composedIn(set), blobs)
			})
		}
	}
}

// TestKeySetParityShapes runs the oracle over synthesized struct shapes, so the
// shape is drawn as well as the value.
func TestKeySetParityShapes(t *testing.T) {
	t.Parallel()

	blobs := fuzzshape.Blobs(64)

	for setName, set := range predicateSets {
		t.Run(setName, func(t *testing.T) {
			t.Parallel()

			for i, blob := range blobs {
				typ := fuzzshape.Type(blob)
				checkKeySet(t, typ, composedIn(set), blobs[(i+1)%len(blobs):])
			}
		})
	}
}

// TestCompositionInvariance asserts that composing an embed rather than
// promoting it moves a name between the emitted properties and the ghost-won
// list without changing the set. It is the unguarded half of the oracle: it
// holds for every type, including those the key-set parity check must skip.
func TestCompositionInvariance(t *testing.T) {
	t.Parallel()

	types := map[string]reflect.Type{}
	maps.Copy(types, roster)

	for i, blob := range fuzzshape.Blobs(64) {
		types[fmt.Sprintf("shape %d", i)] = fuzzshape.Type(blob)
	}

	for name, typ := range types {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			want := resolve(t, typ, composedIn(nil)).names

			for setName, set := range predicateSets {
				got := resolve(t, typ, composedIn(set)).names
				assert.Equal(t, want, got,
					"composing %s changed the resolved name set", setName)
			}
		})
	}
}

type cycA struct {
	*cycB //nolint:unused // The mutual embed is the shape under test.

	X int `json:"x"`
}

type cycB struct {
	*cycA //nolint:unused // The mutual embed is the shape under test.

	Y int `json:"y"`
}

// TestCycleSkipKeepsBranchUnconditional pins the mutually composed cycle. The
// outermost resolution of cycA queues cycB's ghost subtree, since only cycA is
// in flight; the skip fires one level down, inside the resolution of cycB that
// feeds the shadow marking, and leaves promoted[cycB] short of cycA's names. So
// cycB's branch keeps the unconditional form it had before ghost tracking
// existed. A change here is a deliberate diff, not an accident.
func TestCycleSkipKeepsBranchUnconditional(t *testing.T) {
	t.Parallel()

	composed := func(typ reflect.Type) bool {
		return typ == reflect.TypeFor[cycA]() || typ == reflect.TypeFor[cycB]()
	}

	out := NewCollector(composed).Of(reflect.TypeFor[cycA]())

	var embeds int

	for i := range out.Fields {
		f := &out.Fields[i]
		if !f.ComposeViaAllOf {
			continue
		}

		embeds++

		assert.False(t, f.Shadowed, "the cycle leaves the composed branch unconditional")
		assert.False(t, f.ShadowPartial, "the cycle leaves the parent object closed")
	}

	assert.Equal(t, 1, embeds, "cycA composes cycB")
}

// TestPromotedMarshalerHasNoKeySet pins reasonPromotedMarshaler: a promoted
// MarshalJSON replaces the whole object, so the resolved fields describe
// nothing the marshaled output carries.
func TestPromotedMarshalerHasNoKeySet(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[promotedMarshaler]()
	require.True(t, hasMarshaler(typ), reasonPromotedMarshaler)

	data, err := json.Marshal(promotedMarshaler{RawMessage: json.RawMessage(`[1]`)})
	require.NoError(t, err)
	assert.JSONEq(t, `[1]`, string(data), reasonPromotedMarshaler)
}

// TestMarshalFailureHasNoKeySet pins reasonMarshalFailed.
func TestMarshalFailureHasNoKeySet(t *testing.T) {
	t.Parallel()

	type unmarshalable struct {
		F float64 `json:"f"`
	}

	keys, skip := marshalObject(unmarshalable{F: math.Inf(1)})
	assert.Nil(t, keys)
	assert.Equal(t, reasonMarshalFailed, skip)
}

// TestNonObjectHasNoKeySet pins reasonNotObject.
func TestNonObjectHasNoKeySet(t *testing.T) {
	t.Parallel()

	keys, skip := marshalObject([]int{1, 2})
	assert.Nil(t, keys)
	assert.Equal(t, reasonNotObject, skip)
}

// FuzzFieldSetKeys searches for a type shape and value whose marshaled key set
// the resolution phases disagree with. The composed predicate is drawn from the
// shape blob, so a counterexample minimizes down to the composition that
// produced it.
func FuzzFieldSetKeys(f *testing.F) {
	for _, blob := range fuzzshape.Blobs(16) {
		f.Add(blob, blob)
	}

	setNames := make([]string, 0, len(predicateSets))
	for name := range predicateSets {
		setNames = append(setNames, name)
	}

	slices.Sort(setNames)

	f.Fuzz(func(t *testing.T, shape, value []byte) {
		typ := fuzzshape.Type(shape)

		pick := 0
		if len(shape) > 0 {
			pick = int(shape[0]) % len(setNames)
		}

		checkKeySet(t, typ, composedIn(predicateSets[setNames[pick]]), [][]byte{value})
	})
}
