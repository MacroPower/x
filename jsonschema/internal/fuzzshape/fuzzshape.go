// Package fuzzshape synthesizes a Go struct type from a fuzzing entropy blob.
//
// The differential rigs assert that a schema generated for a type accepts
// whatever [encoding/json/v2] marshals from a value of that type. Rig 1 asserts it
// over a hand-written roster, so it covers only shapes someone thought to write
// down. This package makes the shape itself fuzzable: [Type] turns a blob into
// a [reflect.Type] built with [reflect.StructOf], which the jsonschema
// package's non-generic Generate entry point accepts as-is.
//
// [Type] is total over construction. Every blob, including an empty or
// truncated one, yields a type [reflect.StructOf] accepts: every pool and
// every draw is constrained so no reachable combination trips a StructOf
// panic, and each constraint is recorded at the pool it shapes. Marshaling is
// deliberately not total. Some draws yield declarations [encoding/json/v2]
// refuses -- a malformed tag name, a JSON name two fields of one struct claim
// -- and the rigs assert that generation refuses exactly those types.
//
// [Type] is deterministic: it draws through a [fuzzfill.Cursor], so a given
// blob always yields the same type. Go's fuzzing engine re-runs a failing
// input to reproduce and minimize it; a generator that consulted map iteration
// order would silently defeat minimization.
//
// Memory grows with run length, and the pools set that rate rather than
// capping it. Each distinct [reflect.StructOf] shape allocates a runtime type
// descriptor that is never freed, and the pools cross to far more shapes than
// any run can exhaust, so the descriptor set keeps growing instead of
// plateauing. Growth is sublinear, since draws collide: measured over
// uniformly random blobs, two million draws retain roughly 250 MB, and the
// default 30s FUZZTIME costs on the order of 150 MB for one target. That is
// why maxFields and every pool stay small, and why a run of many hours wants
// its memory budget checked first.
//
// The package is test-only infrastructure; it is not part of the fast gate.
package fuzzshape

import (
	"encoding/json"
	"encoding/json/jsontext"
	"math/big"
	"reflect"
	"strconv"
	"time"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// Draw bounds. A struct gets at most maxFields fields; one field in embedOdds
// is an embed, one of the rest in unexportedOdds is unexported, and one tag in
// tagOdds is untagged with another one excluded outright.
const (
	maxFields      = 4
	embedOdds      = 4
	fallbackOdds   = 6
	unexportedOdds = 4
	tagOdds        = 8

	// Length cycle for [Blobs]: drawn lengths run from zero to one below it. A
	// four-field draw consumes about 170 cursor bytes, so this sits well above
	// that -- a population capped below it would zero-extend every blob and
	// starve the many-field shapes where JSON names collide.
	maxBlobLen = 256
)

// Key is a named string used as a map key, the form encoding/json renders as an
// object key without conversion.
type Key string

// BagKey is the key type of every drawn embedded-fallback map. Its
// constructor draws from a namespace disjoint from exportedNames, tagNames,
// and every name the embed pool promotes, so a spliced fallback member can
// never collide with a named field, which is a marshal-time duplicate-name
// error that would cost the rig the whole iteration.
type BagKey string

// Level is a named non-struct type. Embedded, encoding/json keeps it as a leaf
// field named for the type rather than flattening it.
type Level int8

// Opaque is a named interface embedded to exercise the embedded-interface leaf
// rule. It declares no methods, which two separate constraints require:
// [reflect.StructOf] synthesizes a promoted interface method as a stub that
// panics when called, and a method set carrying MarshalJSON or MarshalText
// would make the synthesized struct a marshaler whose output shape reflection
// cannot know.
type Opaque any

// Leaf is a nested named struct drawn as a field type, so a synthesized shape
// can nest one level without recursive synthesis. Its json:"-" field keeps an
// exclusion reachable below the root.
type Leaf struct {
	Depth **bool `json:"depth,omitempty"`
	Note  string `json:"-"`
	ID    uint32 `json:"id"`
}

// Alpha and Beta are embedded to promote their fields into the synthesized
// struct. Both carry an untagged Shared field, so drawing the two together
// reproduces encoding/json's same-depth ambiguity drop; drawing one alongside a
// root field named Shared reproduces outer-shadows-inner. The colliding fields
// stay untagged so go vet's structtag check does not flag the intended
// collision, mirroring the rig-1 roster.
type Alpha struct {
	Shared string
	A      int `json:"a,omitempty"`
}

// Beta is Alpha's counterpart; see Alpha.
type Beta struct {
	B      *string `json:"b"`
	Shared string
}

// Draw pools and the fixtures they need. Each pool records the constraint that
// keeps [Type] total.
var (
	// Types a plain field is drawn from. Every entry is marshalable by
	// [encoding/json] and provider-free: none implements JSONSchemaProvider
	// or TypeSchemaProvider, so no synthesized shape can reach the
	// generator's allOf composition path, and none is a direct
	// [encoding/json.Marshaler] the generator does not already override exactly
	// ([time.Time], [encoding/json.RawMessage], [big.Int],
	// [encoding/json.Number] are all built-in overrides). A direct marshaler
	// outside that set would reflect its Go struct shape rather than its
	// marshaled shape and report a false divergence.
	//
	// The multi-level pointer chains earn their place: encoding/json/v2
	// carries the ,string flag down the whole pointer chain, so crossing
	// **T with that option reaches the deepest drift case the rig exists to
	// catch.
	componentTypes = []reflect.Type{
		reflect.TypeFor[string](),
		reflect.TypeFor[bool](),
		reflect.TypeFor[int](),
		reflect.TypeFor[int8](),
		reflect.TypeFor[int64](),
		reflect.TypeFor[uint16](),
		reflect.TypeFor[uint64](),
		reflect.TypeFor[float32](),
		reflect.TypeFor[float64](),
		reflect.TypeFor[*string](),
		reflect.TypeFor[*float64](),
		reflect.TypeFor[**int](),
		reflect.TypeFor[**string](),
		reflect.TypeFor[[]string](),
		reflect.TypeFor[[]*int](),
		reflect.TypeFor[[2]bool](),
		reflect.TypeFor[[]byte](),
		reflect.TypeFor[map[string]int](),
		reflect.TypeFor[map[int]string](),
		reflect.TypeFor[map[Key]float64](),
		reflect.TypeFor[time.Time](),
		reflect.TypeFor[*time.Time](),
		reflect.TypeFor[jsontext.Value](),
		reflect.TypeFor[json.Number](),
		reflect.TypeFor[*big.Int](),
		reflect.TypeFor[any](),
		reflect.TypeFor[Leaf](),
		reflect.TypeFor[*Leaf](),
		reflect.TypeFor[[]Leaf](),
	}

	// Types an anonymous field is drawn from. None carries a method:
	// [reflect.StructOf] refuses to build an embed with methods unless it is
	// the sole field, and a method-bearing embed would promote a marshaler that
	// changes the whole struct's output shape. None is a generic instantiation
	// either, since Wrap[string] is not a valid Go field name. Both classes
	// stay in the rig-1 static roster, which declares its embeds in source.
	embedTypes = []reflect.Type{
		reflect.TypeFor[Alpha](),
		reflect.TypeFor[*Alpha](),
		reflect.TypeFor[Beta](),
		reflect.TypeFor[Level](),
		reflect.TypeFor[Opaque](),
	}

	// Go names a plain field is drawn from. Shared collides with the name Alpha
	// and Beta promote, so the shadowing rule is reachable.
	exportedNames = []string{"Shared", "Name", "Value", "Data", "Item", "Count"}

	// Go names drawn with PkgPath set, which [reflect.StructOf] accepts and which
	// encoding/json excludes from the output. A field drawing one never carries
	// a json tag, since [encoding/json/v2] refuses a tag on an unexported field
	// outright and the exclusion class is what the draw is for.
	unexportedNames = []string{"shared", "hidden"}

	// JSON tag name parts that name a property. They span encoding/json's name
	// grammar, which is not the obvious one: the quote is a reserved character,
	// so a"b keeps its leading identifier and names the property "a", while any
	// other rune run is a name, so "a b" names a property as written.
	// The "-," form names a property that is literally a hyphen, distinct from
	// the bare "-" exclusion drawn separately.
	tagNames = []string{
		"alt",
		"Shared",
		"-,",
		`a"b`,
		"a b",
		"été",
	}

	// JSON tag option parts, crossed with every name. The format option is a
	// declaration refusal on every field (the feature was cut from stable
	// encoding/json/v2, go.dev/issue/79071), so every draw of it asserts the
	// refusal agreement.
	tagOpts = []string{"", ",omitempty", ",omitzero", ",string", ",format:units"}

	// Types an embedded-fallback field is drawn from. The maps qualify (a
	// string-kind key with no marshal methods, one behind the unnamed-pointer
	// level v2 unwraps); the int-keyed map and the scalar are declaration
	// refusals both sides agree on. The pool deliberately omits
	// jsontext.Value, because its shared fill draws arbitrary JSON and a
	// non-object value is a value-level v2 refusal the accept rig treats as
	// failure, so the Value form lives in rig 1's roster and the static
	// tables instead.
	fallbackFieldTypes = []reflect.Type{
		reflect.TypeFor[map[BagKey]int](),
		reflect.TypeFor[*map[BagKey]int](),
		reflect.TypeFor[map[BagKey]*string](),
		reflect.TypeFor[map[int]string](),
		reflect.TypeFor[int](),
	}

	// Built once and shared: the override set is the same for every draw, and
	// [FillOptions] is called on every fuzzing iteration.
	fillOptions = []fuzzfill.Option{
		fuzzfill.WithConstructor(reflect.TypeFor[json.Number](), fillNumber),
		fuzzfill.WithConstructor(reflect.TypeFor[BagKey](), fillBagKey),
	}

	// This package's import path, required on an unexported field.
	pkgPath = reflect.TypeFor[Leaf]().PkgPath()
)

// FillOptions returns the [fuzzfill.Option] values a value of a synthesized type must
// be filled with. Generic reflection fills a [encoding/json.Number] with the
// cursor's spicy runes, and [encoding/json/v2.Marshal] then rejects the result as
// an invalid number literal, losing the whole iteration. Keeping the
// requirement here means a component type added to the pool has one place to
// declare what filling it needs.
func FillOptions() []fuzzfill.Option { return fillOptions }

// fillBagKey draws one of sixteen fallback member names, keeping the spliced
// keys inside BagKey's reserved namespace.
func fillBagKey(c *fuzzfill.Cursor) any {
	return BagKey("bag" + strconv.Itoa(c.Intn(16)))
}

// fillNumber draws a valid JSON number literal, spanning the integer, negative,
// fractional, and exponent forms.
func fillNumber(c *fuzzfill.Cursor) any {
	switch c.Intn(4) {
	case 0:
		return json.Number("0")
	case 1:
		//nolint:gosec // any 64-bit pattern is a valid signed integer literal.
		return json.Number(strconv.FormatInt(int64(c.Uint64()), 10))
	case 2:
		return json.Number(strconv.FormatFloat(c.Float64(), 'g', -1, 64))
	default:
		return json.Number(strconv.Itoa(c.Intn(1000)) + "e" + strconv.Itoa(c.Intn(20)-10))
	}
}

// Blobs returns n deterministic entropy blobs of varying length, the shared
// population for tests that sample the reachable shapes instead of fuzzing
// them. A fixed-seed xorshift keeps the population identical across runs and
// toolchains, which math/rand's globally seeded source does not, so a failure
// reproduces exactly. A field draw consumes several eight-byte reads, so
// maxBlobLen is set above what a full struct costs: the length range then spans
// both blobs that feed one and blobs the cursor has to zero-extend, and an
// exhausted cursor reads zero forever, which collapses the tail of a short blob
// to one repeatedly deduped draw.
func Blobs(n int) [][]byte {
	state := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17

		return state
	}

	out := make([][]byte, 0, n)

	for i := range n {
		b := make([]byte, i%maxBlobLen)
		for j := range b {
			b[j] = byte(next()) //nolint:gosec // any 64-bit pattern truncates to a valid entropy byte.
		}

		out = append(out, b)
	}

	return out
}

// Type synthesizes a struct type from a fuzzing entropy blob. It is total:
// every blob yields a type [reflect.StructOf] accepts and [encoding/json] can
// marshal. A given blob always yields the same type.
func Type(data []byte) reflect.Type {
	c := fuzzfill.NewCursor(data)

	n := c.Intn(maxFields + 1)
	fields := make([]reflect.StructField, 0, n)

	// Go field names must be unique or reflect.StructOf panics on a duplicate.
	// JSON names deliberately are not deduped: two fields resolving to the same
	// property name is the ambiguity the rig is built to probe.
	used := make(map[string]bool, n)

	for range n {
		f, ok := drawField(c, used)
		if !ok {
			continue
		}

		used[f.Name] = true

		fields = append(fields, f)
	}

	return reflect.StructOf(fields)
}

// drawField draws one field, reporting false when its Go name is already taken
// and the draw must be dropped. Dropping rather than renaming keeps the name
// pool's collisions meaningful: a synthesized name would never match the name
// an embed promotes.
func drawField(c *fuzzfill.Cursor, used map[string]bool) (reflect.StructField, bool) {
	if c.Intn(embedOdds) == 0 {
		return drawEmbed(c, used)
	}

	if c.Intn(fallbackOdds) == 0 {
		return drawFallback(c, used)
	}

	if c.Intn(unexportedOdds) == 0 {
		name, ok := pickUnused(c, used, unexportedNames)
		if !ok {
			return reflect.StructField{}, false
		}

		// Never a json tag; see unexportedNames.
		return reflect.StructField{
			Name:    name,
			PkgPath: pkgPath,
			Type:    componentTypes[c.Intn(len(componentTypes))],
		}, true
	}

	name, ok := pickUnused(c, used, exportedNames)
	if !ok {
		return reflect.StructField{}, false
	}

	return reflect.StructField{
		Name: name,
		Type: componentTypes[c.Intn(len(componentTypes))],
		Tag:  drawTag(c),
	}, true
}

// pickUnused draws a name from names, reporting false when it is already taken.
// It is the one place the drop-on-collision rule lives, so the three draw sites
// cannot disagree about it.
func pickUnused(c *fuzzfill.Cursor, used map[string]bool, names []string) (string, bool) {
	name := names[c.Intn(len(names))]

	return name, !used[name]
}

// drawFallback draws a non-anonymous json:",embed" field. Most draws are
// valid embedded fallbacks; the pool's refusal entries and the
// option-combined tag are declaration refusals encoding/json/v2 agrees on.
// Two fallback draws in one struct are allowed for the same reason: the
// same-declaration error is a refusal both sides report.
func drawFallback(c *fuzzfill.Cursor, used map[string]bool) (reflect.StructField, bool) {
	name, ok := pickUnused(c, used, exportedNames)
	if !ok {
		return reflect.StructField{}, false
	}

	opt := ",embed"
	if c.Intn(4) == 0 {
		opt = ",embed,omitempty"
	}

	return reflect.StructField{
		Name: name,
		Type: fallbackFieldTypes[c.Intn(len(fallbackFieldTypes))],
		Tag:  buildTag("", opt),
	}, true
}

// drawEmbed draws an anonymous field. Its Go name must be the embedded type's
// name, so a pointer embed reports the pointee's name.
func drawEmbed(c *fuzzfill.Cursor, used map[string]bool) (reflect.StructField, bool) {
	t := embedTypes[c.Intn(len(embedTypes))]

	elem := t
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}

	if used[elem.Name()] {
		return reflect.StructField{}, false
	}

	// An embed is left untagged half the time. A json name on an anonymous
	// field suppresses promotion and makes it one named property instead, and
	// both forms must stay well represented.
	tag := reflect.StructTag("")
	if c.Bool() {
		tag = drawTag(c)
	}

	return reflect.StructField{Name: elem.Name(), Type: t, Anonymous: true, Tag: tag}, true
}

// drawTag draws a struct tag, including the untagged and excluded forms.
func drawTag(c *fuzzfill.Cursor) reflect.StructTag {
	switch c.Intn(tagOdds) {
	case 0: // untagged
		return ""
	case 1: // excluded outright
		return `json:"-"`
	default:
		// The empty name part reaches the rule that an options-only tag leaves
		// the Go field name as the property name. Only this branch draws it, so
		// drawNamedTag can promise a name.
		if c.Bool() {
			return buildTag("", tagOpts[c.Intn(len(tagOpts))])
		}

		return drawNamedTag(c)
	}
}

// drawNamedTag draws a json tag whose name part is always present. The
// unexported draw path needs that promise: its regression class is a tag
// naming a property on a field encoding/json must exclude anyway, which an
// empty name part would not express.
func drawNamedTag(c *fuzzfill.Cursor) reflect.StructTag {
	return buildTag(tagNames[c.Intn(len(tagNames))], tagOpts[c.Intn(len(tagOpts))])
}

// buildTag assembles a json struct tag. Quoting rather than concatenating is
// what makes the invalid a"b name expressible: [reflect.StructTag.Get] unquotes
// the value, so the quote must reach it escaped.
func buildTag(name, opt string) reflect.StructTag {
	return reflect.StructTag("json:" + strconv.Quote(name+opt))
}
