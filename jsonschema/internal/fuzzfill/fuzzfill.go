// Package fuzzfill turns a fuzzing entropy blob into a typed Go value.
//
// Native Go fuzzing cannot pass structs to an f.Fuzz callback; its arguments
// must be []byte, string, or scalars. The differential and property rigs in
// this module need typed values, so they fuzz a []byte blob and hand it to
// [Fill], which walks a [reflect.Value] and populates it from the blob via a
// deterministic byte cursor. A given blob always yields the same value, a
// fuzzing requirement: the cursor zero-extends once the blob is exhausted, so
// filling never depends on entropy that isn't there.
//
// Generic reflection recursion cannot populate types with unexported fields
// ([time.Time], [big.Int]): [reflect.Value.Set] panics on them. Nor can it produce
// a valid [json.RawMessage], since arbitrary bytes make [json.Marshal] fail. Such
// types are populated by a constructor registry consulted before the kind
// switch; the defaults cover [time.Time], [big.Int], and [json.RawMessage], and
// callers register more with [WithConstructor]. A rig type that has unexported
// fields without a registered constructor fills as its zero value, so its
// coverage is near-nil.
//
// The package is test-only infrastructure; it is not part of the fast gate.
package fuzzfill

import (
	"encoding/json"
	"maps"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	// These runes probe string-handling edges in the reflection and format
	// code: the JSON string delimiter and structural characters, the tag and
	// list separators the validator interpreter splits on, whitespace the
	// format parsers trim, an escape, non-ASCII runes that exercise UTF-8 and
	// IDNA, and a NUL.
	spicyRunes = []rune{
		'"',
		',',
		'|',
		' ',
		'\t',
		'\n',
		'\\',
		'{',
		'}',
		':',
		'\u00e9',
		'\U0001f4a5',
		'\u00a0',
		'\u200b',
		0,
	}

	// Registry entries for the standard-library types generic reflection cannot
	// fill: [time.Time] and [big.Int] have unexported fields, and
	// [json.RawMessage] must hold valid JSON. It is never mutated after init;
	// [WithConstructor] copies it before adding an override.
	defaultConstructors = map[reflect.Type]Constructor{
		reflect.TypeFor[time.Time]():       fillTime,
		reflect.TypeFor[big.Int]():         fillBigInt,
		reflect.TypeFor[json.RawMessage](): fillRawMessage,
	}
)

// Cursor is a deterministic reader over a fuzzing entropy blob. It never
// fails: once the blob is exhausted every read returns zero, so a value filled
// from a blob is a pure function of that blob. Constructor functions registered
// with [WithConstructor] receive a *Cursor to draw their own entropy.
type Cursor struct {
	data []byte
	pos  int
}

// NewCursor returns a Cursor reading from data. [Fill] builds one internally;
// it is exported so a rig can drive the same zero-extending reader over a blob
// of its own, drawing something other than a value from it.
func NewCursor(data []byte) *Cursor { return &Cursor{data: data} }

// Byte consumes and returns the next byte, or zero once the blob is exhausted.
func (c *Cursor) Byte() byte {
	if c.pos >= len(c.data) {
		c.pos++
		return 0
	}

	b := c.data[c.pos]
	c.pos++

	return b
}

// Uint64 consumes the next eight bytes (zero-extended) as a big-endian uint64.
func (c *Cursor) Uint64() uint64 {
	var u uint64

	for range 8 {
		u = u<<8 | uint64(c.Byte())
	}

	return u
}

// Bool consumes one byte and returns its low bit.
func (c *Cursor) Bool() bool { return c.Byte()&1 == 1 }

// Intn returns a value in [0, n) drawn from the blob, or 0 when n <= 0.
func (c *Cursor) Intn(n int) int {
	if n <= 0 {
		return 0
	}

	// The modulo result is in [0, n), so it always fits in int.
	return int(c.Uint64() % uint64(n)) //nolint:gosec // bounded by n above.
}

// Float64 returns a finite float64 drawn from the blob. NaN and infinities are
// remapped to a finite value so [json.Marshal] of the filled value cannot fail.
func (c *Cursor) Float64() float64 {
	f := math.Float64frombits(c.Uint64())
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return float64(int64(math.Float64bits(f))) / 1e6 //nolint:gosec // deterministic finite remap of NaN/Inf bits.
	}

	return f
}

// String returns a string of up to maxLen runes mixing printable ASCII with
// the spicy runes. The result is always valid UTF-8, so it round-trips through
// [json.Marshal] without loss.
func (c *Cursor) String(maxLen int) string {
	n := c.Intn(maxLen + 1)

	var b strings.Builder

	for range n {
		if c.Bool() {
			b.WriteRune(spicyRunes[c.Intn(len(spicyRunes))])
		} else {
			//nolint:gosec // 0x20+[0,0x5f) is in [32,126], a valid byte.
			b.WriteByte(byte(0x20 + c.Intn(0x5f))) // printable ASCII
		}
	}

	return b.String()
}

// Constructor builds a value of a fixed type from cursor entropy. It returns
// the concrete value (not a pointer) assignable to the registered type.
type Constructor func(c *Cursor) any

type config struct {
	constructors  map[reflect.Type]Constructor
	candidates    map[string][]string
	nilContainers bool
}

// Recursion depth and collection/string size caps Fill applies.
const (
	maxDepth     = 5
	maxStringLen = 8
	maxCollLen   = 4
)

// Option configures [Fill].
type Option func(*config)

// WithConstructor registers ctor as the builder for values of type t, taking
// precedence over kind-based reflection. Use it for any type with unexported
// fields the rig exercises. Registering a type already covered by the defaults
// overrides the default. It copies the registry, so the shared package-level
// defaults are never mutated.
func WithConstructor(t reflect.Type, ctor Constructor) Option {
	return func(cfg *config) {
		cloned := make(map[reflect.Type]Constructor, len(cfg.constructors)+1)
		maps.Copy(cloned, cfg.constructors)

		cloned[t] = ctor
		cfg.constructors = cloned
	}
}

// WithCandidates biases named fields toward a set of candidate values. For a
// field whose name is a key in m, Fill picks one candidate about half the time
// instead of drawing a random value, letting a rig steer a field toward its
// constraint's accept/reject boundary (for example an oneof member). Candidates
// are parsed into the field's kind: strings verbatim, numbers and bools via
// strconv; an unparseable candidate is skipped for that draw.
func WithCandidates(m map[string][]string) Option {
	return func(cfg *config) { cfg.candidates = m }
}

// WithNilContainers makes Fill draw a nil slice or map about half the time,
// the way it draws a nil pointer. A []byte is one of the slices it draws for.
// Use it in a rig that compares what a nil container marshals to; every nil
// container marshals to null.
//
// It is off by default because the fuzzer minimized the committed corpora
// against a draw that allocates. Fill reads the bit only when a rig sets the
// option, so a rig that leaves it off consumes the entropy the default draw
// consumes and decodes every corpus entry to the same value.
func WithNilContainers() Option {
	return func(cfg *config) { cfg.nilContainers = true }
}

// Fill populates the value rv points at from data. Rv must be a non-nil
// pointer to a settable value. Unexported struct fields without a registered
// constructor and kinds json cannot encode (chan, func, [unsafe.Pointer]) are
// left at their zero value.
func Fill(rv reflect.Value, data []byte, opts ...Option) {
	cfg := &config{constructors: defaultConstructors}
	for _, opt := range opts {
		opt(cfg)
	}

	f := &filler{cfg: cfg, cur: NewCursor(data)}
	f.fill(rv.Elem(), 0)
}

type filler struct {
	cfg *config
	cur *Cursor
}

func (f *filler) fill(rv reflect.Value, depth int) {
	if !rv.CanSet() {
		return
	}

	if ctor, ok := f.cfg.constructors[rv.Type()]; ok {
		rv.Set(reflect.ValueOf(ctor(f.cur)))

		return
	}

	switch rv.Kind() {
	case reflect.Bool:
		rv.SetBool(f.cur.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv.SetInt(f.maskedInt(rv.Type().Bits()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		rv.SetUint(f.maskedUint(rv.Type().Bits()))
	case reflect.Float32, reflect.Float64:
		f.fillFloat(rv)
	case reflect.String:
		rv.SetString(f.cur.String(maxStringLen))
	case reflect.Pointer:
		f.fillPointer(rv, depth)
	case reflect.Slice:
		f.fillSlice(rv, depth)
	case reflect.Array:
		for i := range rv.Len() {
			f.fill(rv.Index(i), depth+1)
		}

	case reflect.Map:
		f.fillMap(rv, depth)
	case reflect.Struct:
		f.fillStruct(rv, depth)
	default:
		// Interface, Chan, Func, UnsafePointer: leave the zero value. The
		// rig rosters avoid types json cannot encode.
	}
}

func (f *filler) maskedInt(bits int) int64 {
	u := f.cur.Uint64()
	shift := 64 - bits

	return int64(u<<shift) >> shift //nolint:gosec // width-masked sign extension into the field's int type.
}

func (f *filler) maskedUint(bits int) uint64 {
	u := f.cur.Uint64()
	if bits < 64 {
		u &= (uint64(1) << bits) - 1
	}

	return u
}

func (f *filler) fillFloat(rv reflect.Value) {
	v := f.cur.Float64()
	if rv.Type().Bits() == 32 {
		v = float64(float32(v))
		if math.IsInf(v, 0) {
			v = 0
		}
	}

	rv.SetFloat(v)
}

// atDepthLimit reports whether depth has reached the recursion cap, zeroing rv
// when it has so the caller can stop descending.
func (f *filler) atDepthLimit(rv reflect.Value, depth int) bool {
	if depth >= maxDepth {
		rv.Set(reflect.Zero(rv.Type()))

		return true
	}

	return false
}

// drewNil reports whether the nil-container draw came up nil for rv. It zeroes
// rv when it did, and reads no entropy unless a rig sets [WithNilContainers],
// which keeps the default draw's cursor positions where the committed corpora
// expect them.
func (f *filler) drewNil(rv reflect.Value) bool {
	if !f.cfg.nilContainers || f.cur.Bool() {
		return false
	}

	rv.Set(reflect.Zero(rv.Type()))

	return true
}

func (f *filler) fillPointer(rv reflect.Value, depth int) {
	if depth >= maxDepth || !f.cur.Bool() {
		rv.Set(reflect.Zero(rv.Type()))

		return
	}

	rv.Set(reflect.New(rv.Type().Elem()))
	f.fill(rv.Elem(), depth+1)
}

func (f *filler) fillSlice(rv reflect.Value, depth int) {
	if f.atDepthLimit(rv, depth) || f.drewNil(rv) {
		return
	}

	n := f.cur.Intn(maxCollLen + 1)
	s := reflect.MakeSlice(rv.Type(), n, n)

	for i := range n {
		f.fill(s.Index(i), depth+1)
	}

	rv.Set(s)
}

func (f *filler) fillMap(rv reflect.Value, depth int) {
	if f.atDepthLimit(rv, depth) || f.drewNil(rv) {
		return
	}

	n := f.cur.Intn(maxCollLen + 1)
	m := reflect.MakeMap(rv.Type())
	kt, vt := rv.Type().Key(), rv.Type().Elem()

	for range n {
		k := reflect.New(kt).Elem()
		f.fill(k, depth+1)

		v := reflect.New(vt).Elem()
		f.fill(v, depth+1)
		m.SetMapIndex(k, v)
	}

	rv.Set(m)
}

func (f *filler) fillStruct(rv reflect.Value, depth int) {
	rt := rv.Type()
	for i := range rv.NumField() {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}

		fv := rv.Field(i)
		if f.applyCandidate(field.Name, fv) {
			continue
		}

		f.fill(fv, depth+1)
	}
}

// applyCandidate sets fv from a registered candidate for fieldName about half
// the time, reporting whether it did. It only fires for scalar fields whose
// kind a candidate string can be parsed into.
func (f *filler) applyCandidate(fieldName string, fv reflect.Value) bool {
	cands := f.cfg.candidates[fieldName]
	if len(cands) == 0 || !fv.CanSet() || !f.cur.Bool() {
		return false
	}

	cand := cands[f.cur.Intn(len(cands))]
	switch fv.Kind() { //nolint:exhaustive // only scalar kinds a candidate string can parse into are biased.
	case reflect.String:
		fv.SetString(cand)

		return true

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(cand, 10, 64)
		if err == nil && !fv.OverflowInt(n) {
			fv.SetInt(n)

			return true
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(cand, 10, 64)
		if err == nil && !fv.OverflowUint(n) {
			fv.SetUint(n)

			return true
		}

	case reflect.Float32, reflect.Float64:
		x, err := strconv.ParseFloat(cand, 64)
		if err == nil && !fv.OverflowFloat(x) {
			fv.SetFloat(x)

			return true
		}

	case reflect.Bool:
		b, err := strconv.ParseBool(cand)
		if err == nil {
			fv.SetBool(b)

			return true
		}
	}

	return false
}

// fillTime draws a [time.Time], favoring the RFC 3339 year boundaries that probe
// date-time formatting. Every result stays in [year 1, year 9999] so
// [time.Time.MarshalJSON] never errors on an out-of-range year.
func fillTime(c *Cursor) any {
	switch c.Intn(4) {
	case 0:
		return time.Unix(0, 0).UTC()
	case 1:
		return time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	case 2:
		return time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)
	default:
		return time.Date(
			1+c.Intn(9999),
			time.Month(1+c.Intn(12)),
			1+c.Intn(28),
			c.Intn(24), c.Intn(60), c.Intn(60), c.Intn(1_000_000_000),
			time.UTC,
		)
	}
}

// fillBigInt draws a [big.Int] spanning zero, machine-word, and multi-word
// magnitudes with either sign.
func fillBigInt(c *Cursor) any {
	var z big.Int

	switch c.Intn(4) {
	case 0:
		// Zero.
	case 1:
		z.SetInt64(int64(c.Uint64())) //nolint:gosec // any 64-bit pattern is a valid big.Int value.
	default:
		z.SetString(strconv.FormatUint(c.Uint64(), 10)+strconv.FormatUint(c.Uint64(), 10), 10)

		if c.Bool() {
			z.Neg(&z)
		}
	}

	return z
}

// fillRawMessage emits a small valid JSON document so the value marshals and
// validates as arbitrary JSON.
func fillRawMessage(c *Cursor) any {
	switch c.Intn(6) {
	case 0:
		return json.RawMessage(`null`)
	case 1:
		return json.RawMessage(`true`)
	case 2:
		//nolint:gosec // any 64-bit pattern is a valid signed integer.
		return json.RawMessage(strconv.FormatInt(int64(c.Uint64()), 10))
	case 3:
		// The generated string is valid UTF-8, so marshaling it never errors.
		b, err := json.Marshal(c.String(4))
		if err != nil {
			return json.RawMessage(`""`)
		}

		return json.RawMessage(b)

	case 4:
		return json.RawMessage(`[]`)
	default:
		return json.RawMessage(`{}`)
	}
}
