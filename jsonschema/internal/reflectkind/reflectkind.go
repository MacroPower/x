// Package reflectkind holds Go type and method-set introspection used to decide
// how a type serializes to JSON. The predicates classify marshaler
// implementations (either direct or promoted from an embedded field), stringable
// kinds, valid map keys, and self-recursive container kinds. They are pure
// functions of [reflect.Type]/[reflect.Kind] and carry no schema or generator
// state.
package reflectkind

import (
	"encoding"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
)

var (
	// TypeTextMarshaler is the [reflect.Type] of [encoding.TextMarshaler].
	TypeTextMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	// TypeJSONMarshaler is the [reflect.Type] of [encoding/json.Marshaler].
	TypeJSONMarshaler = reflect.TypeFor[json.Marshaler]()
	// The typeJSONNumber value is the [reflect.Type] of [encoding/json.Number].
	typeJSONNumber = reflect.TypeFor[json.Number]()
)

// IsJSONNumber reports whether t is [encoding/json.Number], following the
// pointer chain the way the json tag's quoting does.
//
// The type is the one string kind encoding/json writes as a number: under
// json:",string" it emits the literal once-quoted ("5"), where every other
// string kind emits the already-encoded string a second time ("\"5\""). It is
// a type identity rather than a predicate over the kind because the encoder
// special-cases exactly this type, and it writes the literal verbatim, so 5.0
// stays 5.0 rather than being canonicalized.
func IsJSONNumber(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t == typeJSONNumber
}

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

// IsValidMapKey checks if a type is a valid map key for JSON serialization.
func IsValidMapKey(t reflect.Type) bool {
	if t.Kind() == reflect.String || numkind.IsInteger(t.Kind()) {
		return true
	}

	// Map keys are not addressable, so encoding/json requires the key type
	// itself to implement TextMarshaler; a method set satisfied only via a
	// pointer receiver does not count and json.Marshal rejects such a map. This
	// deliberately differs from implementsTextMarshaler, which serves addressable
	// struct fields where the pointer-receiver form is usable.
	if t.Implements(TypeTextMarshaler) {
		return true
	}

	return false
}

// IsStringableType reports whether json:",string" applies to the given type.
// Encoding/json dereferences exactly one pointer level, and only when the
// pointer type is unnamed, before checking the quotable kinds; a named pointer
// type or a multi-level pointer is not quoted and marshals as a bare value.
func IsStringableType(t reflect.Type) bool {
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if numkind.IsInteger(t.Kind()) {
		return true
	}

	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// IsDirectTextMarshaler reports whether a type directly implements
// [encoding.TextMarshaler] (not via a promoted embedded field method).
func IsDirectTextMarshaler(t reflect.Type) bool {
	if !implementsTextMarshaler(t) {
		return false
	}

	return HasDirectMethod(t, "MarshalText")
}

// IsBase64ByteSlice reports whether a slice type marshals as one base64
// string under [encoding/json]: its element kind is uint8 and the element
// carries no [encoding/json.Marshaler] or [encoding.TextMarshaler] of its
// own. Encoding/json encodes a marshaler-bearing element through its method,
// which makes such a slice a real JSON array rather than a base64 string.
func IsBase64ByteSlice(t reflect.Type) bool {
	if t.Kind() != reflect.Slice || t.Elem().Kind() != reflect.Uint8 {
		return false
	}

	pt := reflect.PointerTo(t.Elem())

	return !pt.Implements(TypeJSONMarshaler) && !pt.Implements(TypeTextMarshaler)
}

// ImplementsJSONMarshaler reports whether the type or its pointer type
// implements [encoding/json.Marshaler], directly or via promotion.
func ImplementsJSONMarshaler(t reflect.Type) bool {
	return t.Implements(TypeJSONMarshaler) || reflect.PointerTo(t).Implements(TypeJSONMarshaler)
}

// implementsTextMarshaler reports whether the type or its pointer type
// implements [encoding.TextMarshaler], directly or via promotion.
func implementsTextMarshaler(t reflect.Type) bool {
	return t.Implements(TypeTextMarshaler) || reflect.PointerTo(t).Implements(TypeTextMarshaler)
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

// IsPromotedTextMarshaler reports whether a type's method set includes
// MarshalText solely via promotion from an embedded field. See
// [IsPromotedJSONMarshaler].
func IsPromotedTextMarshaler(t reflect.Type) bool {
	if !implementsTextMarshaler(t) {
		return false
	}

	return !HasDirectMethod(t, "MarshalText")
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
