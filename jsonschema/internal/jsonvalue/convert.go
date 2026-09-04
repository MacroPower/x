package jsonvalue

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"unicode/utf8"

	jsonv1 "encoding/json"
)

// walk carries the state of one Go-to-Value conversion: the cycle guard
// every walk needs, and the render flag [FromDocument] reads.
type walk struct {
	// The onPath set holds the containers between the root and the current
	// node, keyed by {pointer, len}: a reslice such as c[:1] shares c's data
	// pointer but is a distinct, acyclic value, so keying on the pointer
	// alone would mistake it for a back-edge.
	onPath map[[2]uintptr]bool

	// The document flag turns on the UTF-8 scan of every string leaf and
	// member name, and only FromDocument sets it. The validation funnel
	// leaves it off, since nothing there reads render and the scan would tax
	// each string of every instance.
	document bool

	// The render flag reports that the input holds a leaf [encoding/json] v1
	// renders to a different shape than the converted value: a nil []any or
	// map[string]any (null, where the empty instance writes [] or {}), a
	// float32 (v1 formats the 32-bit shortest decimal, where the walk widens
	// the bits to float64), an empty [jsonv1.Number] (v1 writes 0), and, with
	// document set, a string or member name holding invalid UTF-8 (v1 writes
	// U+FFFD).
	render bool
}

// FromGo converts a Go value of the shapes the validation entry points accept
// into a Value, reporting false where a leaf has no accepted shape or a
// container contains itself:
//
//   - Signed and unsigned integer values of every width become exact
//     literals (a float64 conversion would round above 2^53).
//   - float32 widens to float64 first, so float32(0.1) is the literal
//     0.10000000149011612 rather than 0.1, and a float64 takes its shortest
//     decimal.
//   - nil, bool, string, float64 (including NaN and the infinities, which
//     JSON cannot represent), and [jsonv1.Number] convert directly.
//   - map[string]any and []any convert recursively.
//
// Anything else (a struct, a named numeric type, map[any]any) is not
// accepted. The input is never read past the first refusal and never
// mutated.
func FromGo(instance any) (Value, bool) {
	return (&walk{onPath: map[[2]uintptr]bool{}}).value(instance)
}

// FromDocument returns the Value the emitted schema document denotes for a
// schema-authored v. The upstream Schema.MarshalJSON renders const, enum, and
// examples with [encoding/json] v1, where a nil map, slice, or pointer writes
// null, so FromDocument marshals v the same way and decodes the bytes
// exactly. A value [FromGo] accepts skips the round trip, since v1 renders it
// to the same shape, so every decoded document and every tag-authored value
// costs no marshal. Four accepted shapes v1 renders differently, and a value
// holding one at any depth takes the round trip: a nil []any or
// map[string]any, which v1 writes as null where the empty instance writes []
// or {}; a float32, which v1 formats as the 32-bit shortest decimal where
// FromGo widens the bits to float64 (0.1 against 0.10000000149011612); an
// empty [jsonv1.Number], which v1 writes as 0; and a string or member name
// holding invalid UTF-8, which v1 writes with U+FFFD in place of each bad
// byte. FromDocument reports false where v has no JSON form (a func, a
// channel, a cyclic value, a map v1 refuses) or its own marshaler panics,
// and recovers the panic so the caller sees only the flag.
func FromDocument(v any) (Value, bool) {
	w := &walk{onPath: map[[2]uintptr]bool{}, document: true}
	if out, ok := w.value(v); ok && !w.render {
		return out, true
	}

	data, ok := marshalV1(v)
	if !ok {
		return Value{}, false
	}

	out, err := Decode(data)
	if err != nil {
		return Value{}, false
	}

	return out, true
}

// marshalV1 marshals v with [encoding/json] v1 and reports ok=false where the
// marshal returns an error or v's own marshaler panics. A recovered panic
// leaves the unnamed results at their zero values, nil and false, which is
// the refusal the caller expects.
func marshalV1(v any) ([]byte, bool) {
	defer func() {
		if recover() != nil {
			return
		}
	}()

	data, err := jsonv1.Marshal(v)

	return data, err == nil
}

// Exact deep-copies a JSON-shaped value with numbers as exact
// [jsonv1.Number] literals, reporting ok=false where the value has no JSON
// form. It is a [json.Marshal] plus [Decode] round trip: scalars keep the
// literal the v2 encoder renders (a float leaf takes the shortest-decimal
// form, including float32's 32-bit one), containers come back rebuilt so the
// copy shares no memory with the source, and the marshal supplies the
// refusals: invalid UTF-8 in strings and member names (RFC 7493), NaN and
// the infinities, a cyclic or absurdly deep value, anything else without a
// JSON form.
func Exact(v any) (any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	out, err := Decode(data)
	if err != nil {
		return nil, false
	}

	return out.Interface(), true
}

// value converts one node, reporting false at the first leaf outside the
// accepted shapes or at a back-edge into a container already on the path.
func (w *walk) value(instance any) (Value, bool) {
	switch v := instance.(type) {
	// JSON-shaped scalar leaves lead the dispatch: every node of an
	// already-JSON-shaped instance, the entirety of the validation funnel's
	// traffic, matches here, so the integer widths wait in the default
	// branch rather than taxing each node with a guaranteed-failing 11-case
	// dispatch first.
	case nil:
		return NewNull(), true
	case bool:
		return NewBool(v), true
	case float64:
		return NewFloat(v), true

	case string:
		if w.document && !utf8.ValidString(v) {
			w.render = true
		}

		return NewString(v), true

	case jsonv1.Number:
		if v == "" {
			w.render = true
		}

		return NewNumber(string(v)), true

	case float32:
		w.render = true

		return NewFloat(float64(v)), true

	case map[string]any:
		return w.object(v)
	case []any:
		return w.array(v)

	default:
		if lit, ok := intLiteral(instance); ok {
			return NewNumber(lit), true
		}

		return Value{}, false
	}
}

// object converts a map's members. A self-referential instance (a map that
// contains itself, directly or transitively) would otherwise recurse without
// bound and abort the process with a stack overflow that recover cannot
// catch, so a back-edge into a container already on the path refuses the
// value. For a map the pointer alone identifies the container (a map cannot
// be resliced into a distinct value sharing its pointer), so len never
// changes the decision and is carried only to share the key shape with
// array.
func (w *walk) object(m map[string]any) (Value, bool) {
	key := [2]uintptr{reflect.ValueOf(m).Pointer(), uintptr(len(m))}
	if w.onPath[key] {
		return Value{}, false
	}

	w.onPath[key] = true
	defer delete(w.onPath, key)

	if m == nil {
		w.render = true
	}

	out := make(map[string]Value, len(m))

	for k, val := range m {
		if w.document && !utf8.ValidString(k) {
			w.render = true
		}

		nv, ok := w.value(val)
		if !ok {
			return Value{}, false
		}

		out[k] = nv
	}

	return NewObject(out), true
}

// array converts a slice's elements, on the same terms as object.
func (w *walk) array(s []any) (Value, bool) {
	key := [2]uintptr{reflect.ValueOf(s).Pointer(), uintptr(len(s))}
	if w.onPath[key] {
		return Value{}, false
	}

	w.onPath[key] = true
	defer delete(w.onPath, key)

	if s == nil {
		w.render = true
	}

	out := make([]Value, len(s))

	for i, val := range s {
		nv, ok := w.value(val)
		if !ok {
			return Value{}, false
		}

		out[i] = nv
	}

	return NewArray(out), true
}

// intLiteral formats a Go integer of any width as its exact decimal literal,
// reporting ok=false for every other type.
func intLiteral(v any) (string, bool) {
	switch v := v.(type) {
	case int:
		return strconv.FormatInt(int64(v), 10), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(v), 10), true
	default:
		return "", false
	}
}
