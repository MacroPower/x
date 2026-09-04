// Package reflectkind holds Go type and method-set introspection used to decide
// how a type serializes to JSON. The predicates classify marshaler
// implementations (either direct or promoted from an embedded field), stringable
// kinds, valid map keys, and self-recursive container kinds. They are pure
// functions of [reflect.Type]/[reflect.Kind] and carry no schema or generator
// state.
package reflectkind

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"runtime"
	"strings"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
)

var (
	// TypeTextMarshaler is the [reflect.Type] of [encoding.TextMarshaler].
	TypeTextMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	// TypeTextAppender is the [reflect.Type] of [encoding.TextAppender].
	TypeTextAppender = reflect.TypeFor[encoding.TextAppender]()
	// TypeJSONMarshaler is the [reflect.Type] of [encoding/json/v2.Marshaler]
	// (which [encoding/json.Marshaler] aliases).
	TypeJSONMarshaler = reflect.TypeFor[json.Marshaler]()
	// TypeJSONMarshalerTo is the [reflect.Type] of [encoding/json/v2.MarshalerTo].
	TypeJSONMarshalerTo = reflect.TypeFor[json.MarshalerTo]()
	// TypeJSONTextValue is the [reflect.Type] of [jsontext.Value] (which
	// [encoding/json.RawMessage] aliases).
	TypeJSONTextValue = reflect.TypeFor[jsontext.Value]()
	// The typeJSONNumber value is the [reflect.Type] of [encoding/json.Number].
	typeJSONNumber = reflect.TypeFor[jsonv1.Number]()
	// The typeIsZeroer value is the interface encoding/json/v2 consults
	// under the omitzero option.
	typeIsZeroer = reflect.TypeFor[interface{ IsZero() bool }]()

	// The unmarshal-side interfaces join the marshal-side ones in
	// [ImplementsAnyMarshalMethod], which mirrors encoding/json/v2's
	// allMethodTypes set for its embedded-field refusal.
	typeTextUnmarshaler     = reflect.TypeFor[encoding.TextUnmarshaler]()
	typeJSONUnmarshaler     = reflect.TypeFor[json.Unmarshaler]()
	typeJSONUnmarshalerFrom = reflect.TypeFor[json.UnmarshalerFrom]()

	// The allMethodTypes list mirrors encoding/json/v2's list of every
	// custom serialization interface, in its order.
	allMethodTypes = []reflect.Type{
		TypeJSONMarshalerTo, TypeJSONMarshaler, TypeTextAppender, TypeTextMarshaler,
		typeJSONUnmarshalerFrom, typeJSONUnmarshaler, typeTextUnmarshaler,
	}

	// The marshalerTypes list is the marshal-side subset, for the
	// json:",string" exemption: encoding/json/v2 routes a marshaler-bearing
	// value through its method, which ignores the flag (json.Number, which
	// honors StringifyNumbers, is special-cased by [IsJSONNumber] instead).
	marshalerTypes = []reflect.Type{
		TypeJSONMarshalerTo, TypeJSONMarshaler, TypeTextAppender, TypeTextMarshaler,
	}
)

// implementsAny reports whether t or its pointer type implements any of the
// given interfaces, mirroring encoding/json/v2's implementsAny: v2 resolves a
// pointer-receiver method wherever the value came from an addressable place,
// and the generator models the addressable case.
func implementsAny(t reflect.Type, ifaces ...reflect.Type) bool {
	for _, iface := range ifaces {
		if t.Implements(iface) || reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}

	return false
}

// ImplementsAnyMarshalMethod reports whether t or its pointer type implements
// any of encoding/json/v2's seven custom serialization interfaces.
// Encoding/json/v2 refuses to promote the fields of an embedded type for
// which this holds (jsontext.Value excepted), since the methods make the
// promoted shape unknowable.
func ImplementsAnyMarshalMethod(t reflect.Type) bool {
	return implementsAny(t, allMethodTypes...)
}

// ImplementsIsZeroer reports whether t or its pointer type carries the IsZero
// method encoding/json/v2 consults under the omitzero option, the other
// method v2 refuses to call through an unexported named field.
func ImplementsIsZeroer(t reflect.Type) bool {
	return implementsAny(t, typeIsZeroer)
}

// ImplementsAnyMarshaler reports whether t or its pointer type implements any
// of the four marshal-side interfaces. A json:",string" flag on such a type
// is ignored rather than an error: the method output replaces the default
// encoding the flag would have applied to.
func ImplementsAnyMarshaler(t reflect.Type) bool {
	return implementsAny(t, marshalerTypes...)
}

// IsJSONNumber reports whether t is [encoding/json.Number]. Callers pass an
// already-dereferenced type; the predicate does not follow a pointer chain.
//
// The type is the one Go string kind encoding/json/v2 writes as a number: its
// MarshalJSONTo emits the literal bare, and it honors StringifyNumbers, so
// json:",string" quotes it once ("5") where every other string kind is a
// SemanticError under the flag. The predicate compares type identity rather
// than reading the kind because the special casing rides on the type's
// methods.
func IsJSONNumber(t reflect.Type) bool { return t == typeJSONNumber }

// IsRecursiveContainerKind reports whether a kind can hold a value of its own
// type and thus form a cycle through schema generation: slices, arrays, and
// maps recurse on the element (or value) type. Other non-struct kinds cannot
// embed themselves, so they need no cycle guard.
func IsRecursiveContainerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

// IndirectType strips one level of unnamed-pointer indirection, mirroring
// encoding/json/v2's indirectType. A named pointer type carries its own
// method set and stays a leaf, so only an unnamed pointer unwraps, and only
// one level deep.
func IndirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		return t.Elem()
	}

	return t
}

// IsEmbeddedFallback reports whether t qualifies as encoding/json/v2's
// embedded fallback under json:",embed": exactly [jsontext.Value], or a map
// whose key kind is string and whose key type implements no marshal or
// unmarshal method. It unwraps the one unnamed-pointer level v2's
// indirectType removes (see [IndirectType]) before classifying.
func IsEmbeddedFallback(t reflect.Type) bool {
	t = IndirectType(t)

	if t == TypeJSONTextValue {
		return true
	}

	return t.Kind() == reflect.Map && t.Key().Kind() == reflect.String &&
		!ImplementsAnyMarshalMethod(t.Key())
}

// IsValidMapKey reports whether a map key type can encode as a JSON object
// member name. Encoding/json/v2 accepts any key that encodes as a JSON
// string: the string kinds, the integer and float kinds (their numbers are
// quoted in the name position), and any marshaler-bearing key, pointer
// receivers included (v2 boxes the key, so addressability never matters). A
// marshaler whose output is not a JSON string still fails, but only when the
// value marshals, which a static predicate cannot see.
func IsValidMapKey(t reflect.Type) bool {
	if t.Kind() == reflect.String || numkind.IsInteger(t.Kind()) || numkind.IsFloat(t.Kind()) {
		return true
	}

	return ImplementsAnyMarshaler(t)
}

// IsStringifiableNumber reports whether json:",string" stringifies the given
// type. Encoding/json/v2 applies the flag to number-encoding values only --
// the integer and float kinds, plus [encoding/json.Number] -- and the flag
// survives every pointer level (a nil pointer still marshals null). Any other
// non-marshaler-bearing type makes marshaling return a SemanticError; see
// [ImplementsAnyMarshaler] for the exemption.
func IsStringifiableNumber(t reflect.Type) bool {
	t = numkind.DerefType(t)

	return numkind.IsInteger(t.Kind()) || numkind.IsFloat(t.Kind()) || IsJSONNumber(t)
}

// IsBase64ByteSlice reports whether a slice type marshals as one base64
// string under [encoding/json/v2]: its element is the unnamed builtin byte.
// A named byte element (which, unlike the builtin, could also carry methods)
// makes the slice a JSON array of numbers.
func IsBase64ByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && isUnnamedByte(t.Elem())
}

// IsBase64ByteArray reports whether an array type marshals as one base64
// string under [encoding/json/v2]: a [N]byte with the unnamed builtin
// element, on the same condition as [IsBase64ByteSlice]. Unmarshaling
// requires the string to decode to exactly N bytes.
func IsBase64ByteArray(t reflect.Type) bool {
	return t.Kind() == reflect.Array && isUnnamedByte(t.Elem())
}

// isUnnamedByte reports whether t is the builtin byte type.
func isUnnamedByte(t reflect.Type) bool {
	return t.Kind() == reflect.Uint8 && t.PkgPath() == ""
}

// ImplementsJSONMarshaler reports whether the type or its pointer type
// implements [encoding/json.Marshaler], directly or via promotion.
func ImplementsJSONMarshaler(t reflect.Type) bool {
	return implementsAny(t, TypeJSONMarshaler)
}

// IsPromotedJSONMarshaler reports whether a type's method set includes
// MarshalJSON solely via promotion from an embedded field. Encoding/json
// resolves marshalers through the method set, so a promoted MarshalJSON
// serializes the whole outer value. Non-struct types cannot have promoted
// methods, so this is always false for them.
func IsPromotedJSONMarshaler(t reflect.Type) bool {
	if !ImplementsJSONMarshaler(t) {
		return false
	}

	return !HasDirectMethod(t, "MarshalJSON")
}

// ImplementsJSONMarshalerTo reports whether the type or its pointer type
// implements [encoding/json/v2.MarshalerTo], directly or via promotion.
func ImplementsJSONMarshalerTo(t reflect.Type) bool {
	return implementsAny(t, TypeJSONMarshalerTo)
}

// IsPromotedJSONMarshalerTo reports whether a type's method set includes
// MarshalJSONTo solely via promotion from an embedded field. See
// [IsPromotedJSONMarshaler]; MarshalJSONTo outranks every other custom
// serialization method in encoding/json/v2's precedence.
func IsPromotedJSONMarshalerTo(t reflect.Type) bool {
	if !ImplementsJSONMarshalerTo(t) {
		return false
	}

	return !HasDirectMethod(t, "MarshalJSONTo")
}

// ImplementsAnyJSONMarshaler reports whether the type or its pointer type
// implements [encoding/json/v2.MarshalerTo] or [encoding/json/v2.Marshaler],
// directly or via promotion. It is the guard that keeps a text-marshaling
// type's string schema from applying where a json marshaler outranks the
// text method.
func ImplementsAnyJSONMarshaler(t reflect.Type) bool {
	return ImplementsJSONMarshalerTo(t) || ImplementsJSONMarshaler(t)
}

// ImplementsAnyTextMarshaler reports whether the type or its pointer type
// implements [encoding.TextAppender] or [encoding.TextMarshaler], directly or
// via promotion. Either method always emits a string.
func ImplementsAnyTextMarshaler(t reflect.Type) bool {
	return implementsAny(t, TypeTextAppender, TypeTextMarshaler)
}

// HasDirectMethod reports whether a method is defined directly on the type
// (not solely promoted from an embedded field). A method declared directly on
// the outer type shadows an embedded one at runtime, so detection must honor
// that: it cannot simply assume the method is promoted whenever an embedded
// field also provides it.
//
// Go offers no reflect API distinguishing a shadowing direct method from a
// promoted one, so this inspects the method's implementation: the compiler
// emits promotion wrappers with a synthetic "<autogenerated>" source location,
// whereas a directly declared method points to its real source file. It checks
// the value receiver first, then the pointer receiver, mirroring how Go
// resolves the method set; a pointer-receiver method that shadows a promoted
// value method suppresses that promotion, so the value method set reports no
// method and the pointer set yields the direct one.
func HasDirectMethod(t reflect.Type, name string) bool {
	if t.Kind() != reflect.Struct {
		// Non-struct types can't have promoted methods, so a method they have is
		// necessarily direct. This short-circuits to true without checking the
		// method set: every caller already establishes that the method exists
		// (via an Implements guard) before asking, so the question is only
		// "direct or promoted", and for a non-struct the answer is always
		// direct.
		return true
	}

	if m, ok := t.MethodByName(name); ok {
		return !isPromotedMethod(m)
	}

	if m, ok := reflect.PointerTo(t).MethodByName(name); ok {
		return !isPromotedMethod(m)
	}

	// The method is not in the type's method set at all; treat as not direct.
	return false
}

// isPromotedMethod reports whether a method is a compiler-generated promotion
// wrapper rather than a directly declared method. Promotion wrappers report a
// synthetic "<autogenerated>" source location.
func isPromotedMethod(m reflect.Method) bool {
	fn := runtime.FuncForPC(m.Func.Pointer())
	if fn == nil {
		return false
	}

	file, _ := fn.FileLine(m.Func.Pointer())

	return strings.Contains(file, "<autogenerated>")
}

// DeclaringType returns the struct type that actually declares field f. For a
// field promoted from an embedded struct this is the embedded type, not the
// outer struct, and the field's doc comment lives in that type's source. The
// field's index path is absolute from outer, so walking all but its last element
// (dereferencing embedded pointers) reaches the declaring type.
func DeclaringType(outer reflect.Type, f reflect.StructField) reflect.Type {
	t := outer

	for _, i := range f.Index[:max(len(f.Index)-1, 0)] {
		t = numkind.DerefType(t)
		t = t.Field(i).Type
	}

	t = numkind.DerefType(t)

	return t
}
