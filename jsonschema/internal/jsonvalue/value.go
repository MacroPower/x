// Package jsonvalue is the one JSON value model the validator walks. A
// [Value] is a tree of the seven JSON kinds, with every number carried as its
// exact decimal literal and the canonical decomposition [numrat] gives it, so
// type checks, bound comparisons, const and enum equality, and the uniqueItems
// hash all read one representation instead of dispatching over the Go
// shapes a caller may hand in.
//
// The constructors are the only entry points: [Decode] and [DecodeReader]
// walk a JSON token stream, [FromGo] converts the Go shapes the validation
// entry points accept, [FromDocument] converts a schema-authored value into
// what the emitted document renders for it, and [Exact] and [Normalize] keep
// the legacy any-typed shape for the callers that still consume it. A Value
// is always a finite tree, so the equality and hash walks need no cycle
// guard, and a Value holds no reference to the Go value it was built from.
//
// A number is one of three states. An exactly comparable literal, at most
// [numrat.MaxNumberLen] significant digits and a decimal exponent within that
// cap, yields a rational on request. An over-cap literal keeps its canonical
// decomposition and compares by magnitude class, so an adversarial literal is
// never expanded. A literal with no numeric value at all, a NaN or infinity
// from a Go float or a [encoding/json.Number] whose text does not parse, keeps
// its text and fails every bound closed.
package jsonvalue

import (
	"math"
	"math/big"
	"strconv"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// Kind is the JSON kind a [Value] holds.
type Kind uint8

// The JSON kinds. Invalid marks a value with no JSON form at all (a func, a
// cyclic container, a map v1 refuses to marshal), which equals nothing and
// matches no type.
const (
	Invalid Kind = iota
	Null
	Bool
	Number
	String
	Array
	Object
)

// numState classifies what a Number carries.
type numState uint8

// The number states. A Number with no numeric value (a non-finite float or a
// literal that does not parse) fails every bound closed; an over-cap literal
// compares by magnitude class and exact canonical exponent only; a literal
// within the expansion cap yields an exact rational on request.
const (
	numNone numState = iota
	numParsed
	numExact
)

// Value is one JSON value. It is a small struct passed by value: the
// containers and the number decomposition sit behind references so the struct
// stays within 64 bytes and the validator's per-node context stays cheap to
// copy.
//
// Field order is tuned for struct packing (govet fieldalignment); the
// grouping is not semantic.
type Value struct {
	obj map[string]Value
	dec *numrat.DecNumber
	// The str field holds the String text, or for a Number its literal: the
	// decoded text, the shortest decimal of a Go float, or the %v text of a
	// non-finite float.
	str   string
	arr   []Value
	kind  Kind
	b     bool
	num   numState
	float bool
}

// NewNull returns the JSON null.
func NewNull() Value { return Value{kind: Null} }

// NewBool returns a JSON boolean.
func NewBool(b bool) Value { return Value{kind: Bool, b: b} }

// NewString returns a JSON string.
func NewString(s string) Value { return Value{kind: String, str: s} }

// NewNumber returns a JSON number from its literal. A literal outside the
// JSON number grammar keeps its text with no numeric value, so it fails every
// bound closed and equals only another number with the same text; -0 parses
// to canonical zero.
func NewNumber(literal string) Value {
	v := Value{kind: Number, str: literal}

	d, ok := numrat.ParseDecNumber(literal)
	if !ok {
		return v
	}

	v.dec = &d
	v.num = numParsed

	if d.ExactlyComparable() {
		v.num = numExact
	}

	return v
}

// NewFloat returns a JSON number from a Go float64, interpreted through its
// shortest decimal so float64(0.1) is the literal 0.1 rather than its binary
// expansion. A NaN or infinity carries no numeric value; it keeps its %v text
// for messages and is unequal to everything, itself included.
func NewFloat(f float64) Value {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Value{kind: Number, str: strconv.FormatFloat(f, 'g', -1, 64), float: true}
	}

	v := NewNumber(strconv.FormatFloat(f, 'g', -1, 64))
	v.float = true

	return v
}

// NewArray returns a JSON array over elems, which the Value takes ownership
// of.
func NewArray(elems []Value) Value { return Value{kind: Array, arr: elems} }

// NewObject returns a JSON object over members, which the Value takes
// ownership of.
func NewObject(members map[string]Value) Value { return Value{kind: Object, obj: members} }

// Kind returns the JSON kind.
func (v Value) Kind() Kind { return v.kind }

// Bool returns the boolean; false for any other kind.
func (v Value) Bool() bool { return v.b }

// Str returns the string text; "" for any other kind.
func (v Value) Str() string {
	if v.kind != String {
		return ""
	}

	return v.str
}

// Literal returns a Number's text: the decoded literal, the shortest decimal
// of a Go float, or the %v text of a non-finite float. It is "" for any other
// kind.
func (v Value) Literal() string {
	if v.kind != Number {
		return ""
	}

	return v.str
}

// FromFloat reports whether a Number was built from a Go float64, so its
// legacy form is a float64 and a non-finite one equals nothing.
func (v Value) FromFloat() bool { return v.float }

// Elements returns an Array's elements; nil for any other kind. The slice is
// the Value's own and must not be mutated.
func (v Value) Elements() []Value { return v.arr }

// Members returns an Object's members; nil for any other kind. The map is
// the Value's own and must not be mutated.
func (v Value) Members() map[string]Value { return v.obj }

// Dec returns a Number's canonical decomposition, reporting false for a
// Number with no numeric value or any other kind.
func (v Value) Dec() (numrat.DecNumber, bool) {
	if v.dec == nil {
		return numrat.DecNumber{}, false
	}

	return *v.dec, true
}

// Comparable reports whether a Number carries a numeric value at all: an
// exactly comparable or over-cap literal, but not a non-finite float or an
// unparseable literal.
func (v Value) Comparable() bool { return v.num != numNone }

// Exact reports whether a Number can be expanded into a rational at bounded
// cost (see [numrat.DecNumber.ExactlyComparable]).
func (v Value) Exact() bool { return v.num == numExact }

// Rat expands an exactly comparable Number into its rational, reporting false
// for every other value. The cost is bounded by [numrat.MaxNumberLen]; an
// over-cap literal is never expanded.
func (v Value) Rat() (*big.Rat, bool) {
	if v.num != numExact {
		return nil, false
	}

	return v.dec.Rat(), true
}

// IsIntegral reports whether a Number denotes a mathematical integer, at any
// magnitude. A Number with no numeric value is not integral.
func (v Value) IsIntegral() bool {
	return v.num != numNone && v.dec.IsIntegral()
}

// TypeName returns the JSON Schema type name of the value, or "" for an
// Invalid value.
func (v Value) TypeName() string {
	switch v.kind {
	case Null:
		return typename.Null
	case Bool:
		return typename.Boolean
	case String:
		return typename.String
	case Number:
		if v.IsIntegral() {
			return typename.Integer
		}

		return typename.Number

	case Array:
		return typename.Array
	case Object:
		return typename.Object
	case Invalid:
		return ""
	}

	return ""
}

// MatchesType reports whether the value matches the JSON Schema type name
// typ. Every Number is a number; only an integral one is an integer.
func (v Value) MatchesType(typ string) bool {
	switch typ {
	case typename.Null:
		return v.kind == Null
	case typename.Boolean:
		return v.kind == Bool
	case typename.String:
		return v.kind == String
	case typename.Integer:
		return v.IsIntegral()
	case typename.Number:
		return v.kind == Number
	case typename.Object:
		return v.kind == Object
	case typename.Array:
		return v.kind == Array
	}

	return false
}

// Interface returns the legacy any-typed form of the value: nil, bool,
// string, a [encoding/json.Number] literal (or a float64 for a Number built
// from one), []any, or map[string]any, rebuilt so the result shares nothing
// with the Value. An Invalid value has no form and returns nil.
func (v Value) Interface() any {
	switch v.kind {
	case Null, Invalid:
		return nil
	case Bool:
		return v.b
	case String:
		return v.str
	case Number:
		if v.float {
			// The literal is the shortest decimal of the float, or the %v
			// text of a non-finite one; both parse back to the same bits, so
			// the parse cannot fail.
			f, _ := strconv.ParseFloat(v.str, 64) //nolint:errcheck // See above.

			return f
		}

		return jsonv1.Number(v.str)

	case Array:
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = e.Interface()
		}

		return out

	case Object:
		out := make(map[string]any, len(v.obj))
		for k, m := range v.obj {
			out[k] = m.Interface()
		}

		return out
	}

	return nil
}
