package fieldset //nolint:testpackage // In-package by design: the resolution phases are tested from inside their own package, and the no-in-package-test policy applies to the main jsonschema package only.

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// The key-set oracle asserts that the names the resolution phases emit for a Go
// type are exactly the keys encoding/json marshals from a value of that type. It
// checks dominance against the standard library directly, whereas the jsonschema
// package's two schema-level rigs see a dominance bug only as a validation
// failure several layers away.
//
// A reflect.VisibleFields cross-guard is deliberately not a second oracle
// beside encoding/json. It would assert that the names the phases resolve are
// the exported field names reflect.VisibleFields reports for the same type. Go
// promotion keys on the Go field name while encoding/json keys on the JSON
// name, so the two rule sets agree only over types where no tag renames or
// drops an exported field. What this population leaves of that subset holds
// nothing worth comparing. The roster types that agree resolve no name at all,
// and a synthesized shape agrees only where no tag touches a name, which is
// where reflect.VisibleFields restates what the phases already emit. Three
// divergences make the rest disagree: JSON-name dominance, the same-depth tag
// tie-break, and a composed embed trading its own name for its promoted ones.
// Excusing those rather than skipping the type needs the JSON-name resolution
// such a guard would be checking. The standard library is the ground truth
// these phases reimplement, and the oracle compares against it directly.
//
// Three classes of type carry no verdict, each with a reason constant and a
// guard test pinning its cause.

// reasonPromotedMarshaler is why a type whose method set carries any of the
// four marshal-side methods is skipped.
const reasonPromotedMarshaler = "encoding/json/v2 routes the whole value through the marshaler instead of emitting a field-by-field object, so the marshaled output has no relationship to the resolved field set"

// reasonMarshalFailed is why a value encoding/json/v2 rejects is skipped. The
// faults v2 turned from lenient parses into errors (a malformed tag, a
// json:",string" on a non-numeric field) land here too: the phases report
// them as errors beside their recovered output, and the marshaled side has no
// key set to compare.
const reasonMarshalFailed = "encoding/json/v2 emits no output for the value, so there is no key set to compare against: a cyclic value, a map with an unsupported key type, a non-representable float, or a field declaration v2 refuses"

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

// embedding builds a struct type embedding each of types. Each type built with
// it embeds two types that claim one JSON name, the collision the resolution
// exists to settle. A source declaration cannot express that, since govet's
// structtag check rejects a repeated json tag.
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
	hidden string //nolint:unused // Pins that an unexported field stays out of the key set.
	Shown  int    `json:"shown"`
}

// nestedComposition embeds Deep, whose own Epsilon the outer field shadows.
// Composing both Deep and Base leaves the nested composition of Base as Deep's
// only unshadowed contribution, the one arm of the shadow marking no other
// roster type reaches.
type nestedComposition struct {
	Deep

	Epsilon int `json:"epsilon"`
}

// NonStructOwner embeds a non-struct, so composing it exercises the ghost flags
// that a sighting of an embedded leaf carries.
type NonStructOwner struct {
	Leafish

	Q int `json:"q"`
}

// composedNonStruct composes NonStructOwner, which is how the promoted leaf
// reaches the enclosing resolution as a ghost.
type composedNonStruct struct {
	NonStructOwner

	R int `json:"r"`
}

// shadowedPointerEmbed embeds *Base and takes one of its promoted names with a
// real field. Composing Base is what makes the shadow marking dereference the
// pointer to find the embed's fields.
type shadowedPointerEmbed struct {
	*Base

	Alpha int `json:"alpha"`
}

// allOfNameClash carries a field whose JSON name is the synthetic key a
// composition of Base takes. Key's disjoint namespaces exist to keep that
// collision apart.
type allOfNameClash struct {
	Base

	Clash string `json:"__allof__Base__0"`
}

type stringOptFields struct {
	N int `json:"n,string"`
}

// FallbackKey is a named string-kind map key, which qualifies a map as an
// embedded fallback exactly like the builtin string.
type FallbackKey string

// FallbackCarrier is an embeddable struct carrying a fallback, so a type
// embedding it promotes the fallback at depth 1.
type FallbackCarrier struct {
	Extra map[string]int `json:",embed"`
	Q     int            `json:"q"`
}

// FallbackCarrierB is a second carrier for the same-depth tie.
type FallbackCarrierB struct {
	More map[string]bool `json:",embed"`
	S    int             `json:"s"`
}

// FallbackWrapA and FallbackWrapB each embed FallbackCarrier, so a type
// embedding both reaches its fallback twice at one depth.
type FallbackWrapA struct{ FallbackCarrier }

// FallbackWrapB is FallbackWrapA's counterpart; see FallbackWrapA.
type FallbackWrapB struct{ FallbackCarrier }

type fallbackMap struct {
	Name  string         `json:"name"`
	Extra map[string]int `json:",embed"`
}

type fallbackValue struct {
	Name  string         `json:"name"`
	Extra jsontext.Value `json:",embed"`
}

type fallbackPtrMap struct {
	Name  string          `json:"name"`
	Extra *map[string]int `json:",embed"`
}

type fallbackNamedKey struct {
	Name  string              `json:"name"`
	Extra map[FallbackKey]int `json:",embed"`
}

// twoFallbacks declares two fallback fields, the per-declaration refusal; v2
// still records both, so the same-depth tie drops them.
type twoFallbacks struct {
	A map[string]int `json:",embed"`
	B jsontext.Value `json:",embed"`
}

// fallbackBadKey has a string-kind key carrying marshal methods
// (json.Number's), which disqualifies the map and recovers as a leaf field.
type fallbackBadKey struct {
	Extra map[jsonv1.Number]int `json:",embed"`
}

// namedRawValue has jsontext.Value's underlying type without its identity or
// methods, so it is no fallback and recovers as a leaf field.
type namedRawValue []byte

type fallbackNamedValueType struct {
	Extra namedRawValue `json:",embed"`
}

// marshalerMap carries its own MarshalJSON, which v2 refuses under ",embed"
// and drops the field.
type marshalerMap map[string]int

// MarshalJSON marshals the map as an empty object.
func (marshalerMap) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type fallbackMarshalerMap struct {
	Extra marshalerMap `json:",embed"`
	Y     int          `json:"y"`
}

// Bag is an anonymous-embeddable map, which v2 refuses however it is tagged.
type Bag map[string]int

type fallbackAnonymous struct {
	Bag `json:",embed"` //nolint:staticcheck // Tag under test: ",embed" on an anonymous non-struct.
}

type fallbackPromoted struct {
	FallbackCarrier

	R int `json:"r"`
}

// fallbackShallowWins declares its own fallback beside a promoted one, so
// depth 0 wins.
type fallbackShallowWins struct {
	FallbackCarrier

	Own map[string]string `json:",embed"`
	R   int               `json:"r"`
}

// fallbackSameDepthTie promotes two fallbacks at depth 1, which silently
// drops both.
type fallbackSameDepthTie struct {
	FallbackCarrier
	FallbackCarrierB

	R int `json:"r"`
}

type promotedMarshaler struct {
	jsonv1.RawMessage

	Gamma bool `json:"gamma"`
}

var (
	typeJSONMarshaler   = reflect.TypeFor[json.Marshaler]()
	typeJSONMarshalerTo = reflect.TypeFor[json.MarshalerTo]()
	typeTextMarshaler   = reflect.TypeFor[encoding.TextMarshaler]()
	typeTextAppender    = reflect.TypeFor[encoding.TextAppender]()

	// AmbiguousEmbeds embeds two types claiming "alpha" at one depth, which the
	// tag tie-break cannot settle because both are tagged.
	ambiguousEmbeds = embedding(reflect.TypeFor[Base](), reflect.TypeFor[Other]())
	// TieBreakAnnihilates embeds two types claiming "dup", both tagged.
	tieBreakAnnihilates = embedding(
		reflect.TypeFor[TaggedDupA](), reflect.TypeFor[TaggedDupB](),
	)
	// RepeatedEmbed reaches Base twice at one depth, so its fields annihilate.
	repeatedEmbed = embedding(reflect.TypeFor[WrapA](), reflect.TypeFor[WrapB]())
	// FallbackRepeated reaches FallbackCarrier twice at one depth via the two
	// wrappers, annihilating its fallback along with its names.
	fallbackRepeated = embedding(
		reflect.TypeFor[FallbackWrapA](), reflect.TypeFor[FallbackWrapB](),
	)

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
		"nested composition":    reflect.TypeFor[nestedComposition](),
		"allOf name clash":      reflect.TypeFor[allOfNameClash](),
		"composed non-struct":   reflect.TypeFor[composedNonStruct](),
		"shadowed pointer":      reflect.TypeFor[shadowedPointerEmbed](),

		"map fallback":            reflect.TypeFor[fallbackMap](),
		"value fallback":          reflect.TypeFor[fallbackValue](),
		"pointer fallback":        reflect.TypeFor[fallbackPtrMap](),
		"named-key fallback":      reflect.TypeFor[fallbackNamedKey](),
		"promoted fallback":       reflect.TypeFor[fallbackPromoted](),
		"shallow fallback wins":   reflect.TypeFor[fallbackShallowWins](),
		"fallback same-depth tie": reflect.TypeFor[fallbackSameDepthTie](),
		"fallback repeated":       fallbackRepeated,
		"two fallbacks":           reflect.TypeFor[twoFallbacks](),
		"fallback bad key":        reflect.TypeFor[fallbackBadKey](),
		"fallback named raw":      reflect.TypeFor[fallbackNamedValueType](),
		"fallback marshaler map":  reflect.TypeFor[fallbackMarshalerMap](),
		"fallback anonymous":      reflect.TypeFor[fallbackAnonymous](),
	}

	// ComposedCandidates are the embed types the predicate sets below designate
	// as allOf-composed. A candidate no type in the population embeds is a
	// no-op, which keeps one list serving both halves.
	composedCandidates = map[string]reflect.Type{
		"Base":            reflect.TypeFor[Base](),
		"Other":           reflect.TypeFor[Other](),
		"Deep":            reflect.TypeFor[Deep](),
		"TaggedShared":    reflect.TypeFor[TaggedShared](),
		"WrapA":           reflect.TypeFor[WrapA](),
		"NonStructOwner":  reflect.TypeFor[NonStructOwner](),
		"FallbackCarrier": reflect.TypeFor[FallbackCarrier](),
		"Alpha":           reflect.TypeFor[fuzzshape.Alpha](),
		"Beta":            reflect.TypeFor[fuzzshape.Beta](),
	}

	// PredicateSets names the composed-type sets every type in the population is
	// crossed with. The empty set is the generator's behavior for a type no
	// provider intercepts; the rest reach the ghost machinery, for which nothing
	// driven through the public API can synthesize a shape.
	predicateSets = map[string][]string{
		"none":             {},
		"base":             {"Base"},
		"other":            {"Other"},
		"base+other":       {"Base", "Other"},
		"deep":             {"Deep"},
		"tagged":           {"TaggedShared"},
		"wrap":             {"WrapA"},
		"alpha":            {"Alpha"},
		"beta":             {"Beta"},
		"alpha+beta":       {"Alpha", "Beta"},
		"non-struct owner": {"NonStructOwner"},
		"fallback carrier": {"FallbackCarrier"},
		"all embeds": {
			"Base", "Other", "Deep", "TaggedShared", "WrapA", "NonStructOwner",
			"FallbackCarrier", "Alpha", "Beta",
		},
	}
)

// valueBlobs sets how many values fill each synthesized shape.
const valueBlobs = 4

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
// that won each name, the names whose value encoding/json re-encodes as a
// quoted string, and the embedded fallback whose members join the object
// beside the names.
type resolvedNames struct {
	names     map[string]bool
	omissible map[string]bool
	winner    map[string]reflect.StructField
	coerced   map[string]bool
	fallback  *Fallback
}

// resolve runs all three phases, asserts what holds within each, and derives
// the name sets the oracle compares against encoding/json. A property reads its
// options off the classified field, which puts the option folding the
// classification does under test. A ghost-won name produces no field, so it
// reads them off the winning sighting instead.
func resolve(t *testing.T, typ reflect.Type, composed ComposedFunc) resolvedNames {
	t.Helper()

	c := NewCollector(composed)

	// The error is the walk's report of a declaration v2 refuses; the phases
	// still produce their recovered output, which is what the oracle compares
	// (a refused declaration also refuses to marshal, so the key-set check
	// skips it through reasonMarshalFailed).
	col, res, out, _ := c.phases(typ) //nolint:errcheck // See above.

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
		fallback:  out.Fallback,
	}

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

		if f.Omitempty || f.Omitzero || f.Optional {
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

		if !ghost[w.Name] {
			continue
		}

		rn.winner[w.Name] = w.StructField

		info := w.Info
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
func marshalObject(v any) (map[string]jsontext.Value, string) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, reasonMarshalFailed
	}

	var obj map[string]jsontext.Value

	err = json.Unmarshal(data, &obj)
	if err != nil {
		return nil, reasonNotObject
	}

	return obj, ""
}

// checkWinners asserts that the value under each resolved name is what the
// winning field marshals to. Without it the oracle sees only the name set, and
// a dominance rule that picks the wrong field of two claiming one name still
// produces the right names.
func checkWinners(t *testing.T, rv reflect.Value, rn resolvedNames, obj map[string]jsontext.Value) {
	t.Helper()

	for name, raw := range obj {
		sf, ok := rn.winner[name]
		if !ok {
			continue // Soundness already reported the unresolved name.
		}

		// A ",string" field is re-encoded as a quoted string, so the field's
		// own marshaling is not what the object carries.
		if rn.coerced[name] {
			continue
		}

		fv, err := rv.FieldByIndexErr(sf.Index)
		if err != nil {
			continue // A nil pointer embed on the path; the name is omitted.
		}

		// The parent marshaled, so a field of it marshals too. Marshaling the
		// field alone cannot reach a pointer-receiver marshaler, which
		// encoding/json does reach for a field promoted through a pointer
		// embed. No component type in the population has one; adding a type
		// with a pointer-only MarshalJSON or MarshalText needs a skip here.
		want, err := json.Marshal(fv.Interface())
		require.NoError(t, err)

		assert.JSONEq(t, string(want), string(raw),
			"name %q carries a value the winning field does not marshal", name)
	}
}

// hasMarshaler reports whether encoding/json/v2 would route a value of t
// through a marshaler rather than emitting an object of its fields.
func hasMarshaler(t reflect.Type) bool {
	for _, probe := range []reflect.Type{t, reflect.PointerTo(t)} {
		if probe.Implements(typeJSONMarshaler) || probe.Implements(typeJSONMarshalerTo) ||
			probe.Implements(typeTextMarshaler) || probe.Implements(typeTextAppender) {
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

// fallbackMembers returns the members the winning fallback contributes to the
// marshaled object for one filled value: the map's entries marshaled one by
// one, or the members of the jsontext.Value object. A nil map, a nil pointer
// (on the field or its embed path), and a jsontext.Value holding no object
// contribute nothing; the last of those refuses to marshal, so the caller's
// marshalObject already skipped the blob.
func fallbackMembers(t *testing.T, rv reflect.Value, fb *Fallback) map[string]jsontext.Value {
	t.Helper()

	if fb == nil {
		return nil
	}

	fv, err := rv.FieldByIndexErr(fb.StructField.Index)
	if err != nil {
		return nil
	}

	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return nil
		}

		fv = fv.Elem()
	}

	if fv.Type() == reflectkind.TypeJSONTextValue {
		// A jsontext.Value marshals as itself, so marshalObject decodes its
		// members directly; both of its no-verdict reasons collapse to "no
		// members" here.
		obj, _ := marshalObject(fv.Interface())

		return obj
	}

	out := map[string]jsontext.Value{}

	iter := fv.MapRange()
	for iter.Next() {
		data, err := json.Marshal(iter.Value().Interface())
		require.NoError(t, err)

		out[iter.Key().String()] = data
	}

	return out
}

// checkKeySet asserts the oracle for one type under one predicate, over every
// blob: every marshaled key is a resolved name or a fallback member
// (soundness), and every resolved name the options do not excuse appears in
// the filled value's keys, as does every fallback member (completeness). A
// blob whose value carries no verdict contributes its reason instead, and
// checkKeySet skips only when no blob reached an assertion.
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

		// A fallback key equal to a resolved name is a marshal-time
		// duplicate-name error, so a blob that marshaled has the two sets
		// disjoint and each key attributes to exactly one of them.
		fbMembers := fallbackMembers(t, rv, rn.fallback)

		for key, raw := range fullKeys {
			want, spliced := fbMembers[key]
			if spliced && !rn.names[key] {
				assert.JSONEq(t, string(want), string(raw),
					"fallback member %q carries a value the fallback field does not hold", key)

				continue
			}

			assert.True(t, rn.names[key],
				"filled value marshals key %q that no phase resolved", key)
		}

		for key := range fbMembers {
			_, present := fullKeys[key]
			assert.True(t, present,
				"fallback member %q is absent from the marshaled object", key)
		}

		for name := range rn.names {
			// An omitempty struct-typed field is one encoding/json never
			// omits, so excusing that name only weakens the assertion.
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
		for i, blob := range blobs {
			t.Run(fmt.Sprintf("%s/shape %d", setName, i), func(t *testing.T) {
				t.Parallel()

				// Fill each shape from the valueBlobs blobs that follow it, so
				// shape and value entropy stay independent without the
				// population thinning out toward the end of the list.
				values := make([][]byte, 0, valueBlobs)
				for n := 1; n <= valueBlobs; n++ {
					values = append(values, blobs[(i+n)%len(blobs)])
				}

				checkKeySet(t, fuzzshape.Type(blob), composedIn(set), values)
			})
		}
	}
}

// TestCompositionInvariance asserts that composing an embed rather than
// promoting it moves a name between the emitted properties and the ghost-won
// list without changing the set. It is the unguarded half of the oracle, so it
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

			base := resolve(t, typ, composedIn(nil))

			for setName, set := range predicateSets {
				got := resolve(t, typ, composedIn(set))
				assert.Equal(t, base.names, got.names,
					"composing %s changed the resolved name set", setName)

				// The fallback resolution is composition-independent: a
				// fallback sighted in a ghost subtree competes like any
				// other. The population holds no self- or mutually composed
				// root, the one shape whose in-flight skip legitimately drops
				// a fallback; TestCycleSkipDropsSubtreeFallback pins it.
				assert.Equal(t, base.fallback, got.fallback,
					"composing %s changed the resolved fallback", setName)
			}
		})
	}
}

// wantField is one expected row of [Result.Fields].
//
//nolint:unused // Read via struct equality in the comparison below.
type wantField struct {
	name          string
	index         []int
	compose       bool
	optional      bool
	shadowed      bool
	shadowPartial bool
}

// wantFallback pins one [Result.Fallback]: the field's index path, its
// unwrapped type, and the depth the walk sighted it at.
type wantFallback struct {
	typ   reflect.Type
	index []int
	depth int
}

// TestClassificationPins pins what the key-set oracle cannot see. The oracle
// compares name sets and values, so it is blind to the order Classify emits
// fields and ghost-won names in, and to the shadow marks entirely. Gutting the
// shadow marking leaves every other test in this package green. Both feed the
// generated schema. The field order becomes the object's property order, and
// the marks decide whether a composed branch is conditional.
func TestClassificationPins(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ      reflect.Type
		composed []string
		want     []wantField
		ghostWon []string
		// The fallback field, when set, pins [Result.Fallback]'s index path,
		// unwrapped type, and depth; unset pins a nil fallback.
		fallback *wantFallback
		// The wantKey field, when set, is a composition key Collect must have
		// produced. It ties a row that names the synthetic key in a struct tag
		// to allOfName, which mints it.
		wantKey string
		// The err field is the fault encoding/json/v2 reports for the type;
		// the recovered classification is still pinned beside it.
		err string
	}{
		"map fallback": {
			typ: reflect.TypeFor[fallbackMap](),
			want: []wantField{
				{name: "name", index: []int{0}},
			},
			fallback: &wantFallback{
				index: []int{1}, typ: reflect.TypeFor[map[string]int](),
			},
		},
		"value fallback": {
			typ: reflect.TypeFor[fallbackValue](),
			want: []wantField{
				{name: "name", index: []int{0}},
			},
			fallback: &wantFallback{
				index: []int{1}, typ: reflect.TypeFor[jsontext.Value](),
			},
		},
		"pointer fallback unwraps one level": {
			typ: reflect.TypeFor[fallbackPtrMap](),
			want: []wantField{
				{name: "name", index: []int{0}},
			},
			fallback: &wantFallback{
				index: []int{1}, typ: reflect.TypeFor[map[string]int](),
			},
		},
		"named-key fallback": {
			typ: reflect.TypeFor[fallbackNamedKey](),
			want: []wantField{
				{name: "name", index: []int{0}},
			},
			fallback: &wantFallback{
				index: []int{1}, typ: reflect.TypeFor[map[FallbackKey]int](),
			},
		},
		"promoted fallback": {
			typ: reflect.TypeFor[fallbackPromoted](),
			want: []wantField{
				{name: "q", index: []int{0, 1}},
				{name: "r", index: []int{1}},
			},
			fallback: &wantFallback{
				index: []int{0, 0}, typ: reflect.TypeFor[map[string]int](), depth: 1,
			},
		},
		"depth-0 fallback beats a promoted one": {
			typ: reflect.TypeFor[fallbackShallowWins](),
			want: []wantField{
				{name: "q", index: []int{0, 1}},
				{name: "r", index: []int{2}},
			},
			fallback: &wantFallback{
				index: []int{1}, typ: reflect.TypeFor[map[string]string](),
			},
		},
		"same-depth fallback tie drops both": {
			typ: reflect.TypeFor[fallbackSameDepthTie](),
			want: []wantField{
				{name: "q", index: []int{0, 1}},
				{name: "s", index: []int{1, 1}},
				{name: "r", index: []int{2}},
			},
		},
		"repeated embed annihilates its fallback": {
			typ:  fallbackRepeated,
			want: []wantField{},
		},
		"two fallbacks in one declaration": {
			typ:  reflect.TypeFor[twoFallbacks](),
			want: []wantField{},
			err:  "embedded Go struct fields A and B cannot both be a Go map or jsontext.Value",
		},
		"fallback key carrying marshal methods": {
			typ: reflect.TypeFor[fallbackBadKey](),
			want: []wantField{
				{name: "Extra", index: []int{0}},
			},
			err: "embedded map field Extra of type map[json.Number]int must have a string key that does not implement marshal or unmarshal methods",
		},
		"named type with jsontext.Value underlying": {
			typ: reflect.TypeFor[fallbackNamedValueType](),
			want: []wantField{
				{name: "Extra", index: []int{0}},
			},
			err: "embedded Go struct field Extra of type fieldset.namedRawValue must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"marshaler-bearing map is dropped": {
			typ: reflect.TypeFor[fallbackMarshalerMap](),
			want: []wantField{
				{name: "y", index: []int{1}},
			},
			err: "embedded Go struct field Extra of type fieldset.marshalerMap must not implement marshal or unmarshal methods",
		},
		"anonymous fallback form": {
			typ: reflect.TypeFor[fallbackAnonymous](),
			want: []wantField{
				{name: "Bag", index: []int{0}},
			},
			err: "embedded Go struct field Bag of non-struct type must be explicitly given a JSON name",
		},
		"promoted embed": {
			typ: reflect.TypeFor[valueEmbed](),
			want: []wantField{
				{name: "alpha", index: []int{0, 0}},
				{name: "beta", index: []int{0, 1}},
				{name: "gamma", index: []int{1}},
			},
		},
		"promoted through two levels": {
			typ: reflect.TypeFor[deepChain](),
			want: []wantField{
				{name: "alpha", index: []int{0, 0, 0}},
				{name: "beta", index: []int{0, 0, 1}},
				{name: "epsilon", index: []int{0, 1}},
				{name: "zeta", index: []int{1}},
			},
		},
		"composed embed": {
			typ:      reflect.TypeFor[valueEmbed](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true},
				{name: "gamma", index: []int{1}},
			},
			ghostWon: []string{"alpha", "beta"},
		},
		"composed embed partly shadowed": {
			typ:      reflect.TypeFor[shadowOuter](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true, shadowed: true, shadowPartial: true},
				{name: "alpha", index: []int{1}},
			},
			ghostWon: []string{"beta"},
		},
		"composed pointer embed": {
			typ:      reflect.TypeFor[pointerEmbed](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true, optional: true},
				{name: "gamma", index: []int{1}},
			},
			ghostWon: []string{"alpha", "beta"},
		},
		"composed embed promoting two levels": {
			typ:      reflect.TypeFor[deepChain](),
			composed: []string{"Deep"},
			want: []wantField{
				{index: []int{0}, compose: true},
				{name: "zeta", index: []int{1}},
			},
			// GhostWon keeps walk order, whereas Fields is sorted into
			// declaration order, so the embed's own name comes before the ones
			// it promotes. Both orders reach the object's property order, so
			// the asymmetry is deliberate.
			ghostWon: []string{"epsilon", "alpha", "beta"},
		},
		"annihilated name shadows a composed embed": {
			typ:      ambiguousEmbeds,
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true, shadowed: true, shadowPartial: true},
				{name: "delta", index: []int{1, 1}},
			},
			// Base and Other both claim "alpha" at one depth and both are
			// tagged, so the name annihilates and Base's branch cannot be
			// unconditional. Only "beta" survives for Base's ghost to win.
			ghostWon: []string{"beta"},
		},
		"composition nested in a composed embed": {
			typ:      reflect.TypeFor[nestedComposition](),
			composed: []string{"Deep", "Base"},
			want: []wantField{
				{index: []int{0}, compose: true, shadowed: true, shadowPartial: true},
				{name: "epsilon", index: []int{1}},
			},
			// The outer Epsilon shadows the one Deep promotes, so Deep's only
			// unshadowed contribution is the composition of Base nested inside
			// it, whose names the shadow marking treats as opaque.
			ghostWon: []string{"alpha", "beta"},
		},
		"field named like a composition key": {
			typ:      reflect.TypeFor[allOfNameClash](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true},
				{name: "__allof__Base__0", index: []int{1}},
			},
			// The synthetic key lives in its own namespace, so the field does
			// not shadow the composition in the same-depth tie-break.
			ghostWon: []string{"alpha", "beta"},
			wantKey:  "__allof__Base__0",
		},
		"composed embed promoting a non-struct": {
			typ:      reflect.TypeFor[composedNonStruct](),
			composed: []string{"NonStructOwner"},
			want: []wantField{
				{index: []int{0}, compose: true},
				{name: "r", index: []int{1}},
			},
			// An embedded non-struct without an explicit name is a v2 error,
			// recovered as a leaf field keyed by the field name, so the
			// embed's ghost wins it like any other.
			ghostWon: []string{"Leafish", "q"},
			err:      "embedded Go struct field Leafish of non-struct type must be explicitly given a JSON name",
		},
		"shadowed composed pointer embed": {
			typ:      reflect.TypeFor[shadowedPointerEmbed](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0}, compose: true, optional: true, shadowed: true, shadowPartial: true},
				{name: "alpha", index: []int{1}},
			},
			// The shadow marking is keyed by the element type, so it
			// dereferences *Base to reach the embed's fields.
			ghostWon: []string{"beta"},
		},
		"composition nested in a promoted embed": {
			typ:      reflect.TypeFor[deepChain](),
			composed: []string{"Base"},
			want: []wantField{
				{index: []int{0, 0}, compose: true},
				{name: "epsilon", index: []int{0, 1}},
				{name: "zeta", index: []int{1}},
			},
			ghostWon: []string{"alpha", "beta"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := NewCollector(composedIn(tc.composed))

			out, err := c.Of(tc.typ)
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.err)
			}

			if tc.wantKey != "" {
				// The error is asserted against Of above; Collect repeats it.
				col, _ := c.Collect(tc.typ) //nolint:errcheck // See above.
				assert.Contains(t, col.Order, Key{Name: tc.wantKey, ComposeAllOf: true})
			}

			got := make([]wantField, 0, len(out.Fields))
			for i := range out.Fields {
				f := &out.Fields[i]
				got = append(got, wantField{
					name:          f.JSONName,
					index:         f.StructField.Index,
					compose:       f.ComposeViaAllOf,
					optional:      f.Optional,
					shadowed:      f.Shadowed,
					shadowPartial: f.ShadowPartial,
				})
			}

			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ghostWon, out.GhostWon)

			if tc.fallback == nil {
				assert.Nil(t, out.Fallback)
			} else if assert.NotNil(t, out.Fallback) {
				assert.Equal(t, tc.fallback.index, out.Fallback.StructField.Index)
				assert.Equal(t, tc.fallback.typ, out.Fallback.Type)
				assert.Equal(t, tc.fallback.depth, out.Fallback.Depth)
			}
		})
	}
}

// TestPhasesComposeIntoOf drives the three phases separately and asserts they
// reproduce Of, so the split is part of the package's contract rather than a
// detail Of happens to use.
func TestPhasesComposeIntoOf(t *testing.T) {
	t.Parallel()

	for name, typ := range roster {
		for setName, set := range predicateSets {
			t.Run(name+"/"+setName, func(t *testing.T) {
				t.Parallel()

				composed := composedIn(set)

				// A self- or mutually composed root needs Of's in-flight guard,
				// which only Of can take; no roster type is one.
				require.False(t, composed(typ), "the root is not composed")

				c := NewCollector(composed)

				col, colErr := c.Collect(typ)

				// A caller builds the shadow marking's input from
				// Collection.Scanned, so build it the same way here.
				promoted := map[reflect.Type][]Field{}
				for _, ft := range col.Scanned {
					// A scanned embed that v2 refuses still classifies through
					// its recovered output.
					fields, _ := c.Of(ft) //nolint:errcheck // See the comment above.
					promoted[ft] = fields.Fields
				}

				whole, wholeErr := c.Of(typ)
				assert.Equal(t, whole, Classify(Resolve(col), promoted))
				assert.Equal(t, colErr == nil, wholeErr == nil,
					"the split phases and Of agree on whether the type is refused")
			})
		}
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
// cycB's branch stays unconditional. Change this pin deliberately.
func TestCycleSkipKeepsBranchUnconditional(t *testing.T) {
	t.Parallel()

	composed := func(typ reflect.Type) bool {
		return typ == reflect.TypeFor[cycA]() || typ == reflect.TypeFor[cycB]()
	}

	out, err := NewCollector(composed).Of(reflect.TypeFor[cycA]())
	require.NoError(t, err)

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

// cycFbA embeds the composed cycFbB, whose subtree carries a fallback.
type cycFbA struct {
	*cycFbB //nolint:unused // The mutual embed is the shape under test.

	X int `json:"x"`
}

// cycFbB re-enters cycFbA and carries the fallback the skip drops.
type cycFbB struct {
	*cycFbA //nolint:unused // The mutual embed is the shape under test.

	Extra map[string]int `json:",embed"`
	Y     int            `json:"y"`
}

// TestCycleSkipDropsSubtreeFallback pins the conservative drop: a fallback
// inside a composed subtree the in-flight guard skips resolves to nil, like
// the subtree's names. The direct Of sees cycFbB's fallback through the ghost
// walk; only a resolution entered while cycFbA is already in flight (the
// inner resolution [Collector.promoted] runs for the shadow marking) skips
// the re-entry and drops it. The test seeds the in-flight set the way that
// inner resolution finds it.
func TestCycleSkipDropsSubtreeFallback(t *testing.T) {
	t.Parallel()

	composed := func(typ reflect.Type) bool {
		return typ == reflect.TypeFor[cycFbA]() || typ == reflect.TypeFor[cycFbB]()
	}

	direct, err := NewCollector(composed).Of(reflect.TypeFor[cycFbA]())
	require.NoError(t, err)
	require.NotNil(t, direct.Fallback,
		"the ghost walk sights the composed subtree's fallback")
	assert.Equal(t, 1, direct.Fallback.Depth)

	c := NewCollector(composed)
	c.inFlight[reflect.TypeFor[cycFbA]()] = true

	inner, err := c.Of(reflect.TypeFor[cycFbB]())
	require.NoError(t, err)
	assert.NotNil(t, inner.Fallback, "cycFbB's own fallback is sighted directly")

	// A carrier whose only fallback sits beyond the skipped re-entry resolves
	// none at all.
	innerA, err := c.Of(reflect.TypeFor[cycFbA]())
	require.NoError(t, err)
	assert.NotNil(t, innerA.Fallback)

	c2 := NewCollector(composed)
	c2.inFlight[reflect.TypeFor[cycFbB]()] = true

	skipped, err := c2.Of(reflect.TypeFor[cycFbA]())
	require.NoError(t, err)
	assert.Nil(t, skipped.Fallback,
		"the in-flight skip of cycFbB drops the fallback its subtree carries")
}

// TestPromotedMarshalerHasNoKeySet pins reasonPromotedMarshaler: a promoted
// MarshalJSON replaces the whole object, so the resolved fields describe
// nothing the marshaled output carries.
func TestPromotedMarshalerHasNoKeySet(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[promotedMarshaler]()
	require.True(t, hasMarshaler(typ), reasonPromotedMarshaler)

	data, err := json.Marshal(promotedMarshaler{RawMessage: jsonv1.RawMessage(`[1]`)})
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
