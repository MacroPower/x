package tagmodel

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// base64Encoding is the contentEncoding value the generator emits for a byte
// slice, and the third way a schema can declare that its instance is a string.
const base64Encoding = "base64"

// Form is the JSON shape an instance actually takes: the dispatch column of the
// constraint matrix. It is deliberately not the Go kind. Classifying once, at
// construction, is what turns coercion from a gate every operation must remember
// into a column no applier has to ask about.
type Form uint8

const (
	// FormUnset is the zero value and never names a shape.
	FormUnset Form = iota
	// FormString is a string instance from a string Go kind.
	FormString
	// FormNumber is a number instance.
	FormNumber
	// FormBool is a boolean instance.
	FormBool
	// FormArray is an array instance with per-element schemas.
	FormArray
	// FormObject is an object instance with a value schema, the shape a Go map
	// takes.
	FormObject
	// FormCoercedNumber is a numeric Go kind whose schema is a string: a
	// json:",string" field, or a numeric type marshaling itself as text. Its
	// scalars compare against the text the field emits, never against the number.
	FormCoercedNumber
	// FormCoercedBool is a bool Go kind whose schema is a string, the boolean
	// half of the same coercion.
	FormCoercedBool
	// FormTextString is a string-typed schema over a Go kind that is not a
	// scalar at all: [time.Time], big.Rat, a struct or map marshaling itself as
	// text. A string-only keyword such as format applies; a scalar comparison
	// does not, because the tag has no way to spell the value.
	FormTextString
	// FormByteString is a byte slice whose schema permits a string, so it
	// encodes as one base64 string with no per-element schema. Its length
	// keywords measure that string.
	FormByteString
	// FormRawBytes is a byte slice whose schema does not permit a string:
	// [json.RawMessage], whose unconstrained schema admits any JSON value at all,
	// so it has neither an array to size nor a string to measure.
	FormRawBytes
	// FormOpaque is every remaining shape -- a struct, interface, channel, or
	// function -- whose instance the tag vocabulary cannot describe.
	FormOpaque
	// The formCount constant bounds the matrix columns. Adding a form above grows every row,
	// which the init-time totality walk then reports as unfilled.
	formCount
)

// The formNames table labels each form for the matrix dump and the rejection text.
var formNames = [formCount]string{
	FormUnset:         "unset",
	FormString:        "string",
	FormNumber:        "number",
	FormBool:          "boolean",
	FormArray:         "array",
	FormObject:        "object",
	FormCoercedNumber: "string-coerced number",
	FormCoercedBool:   "string-coerced boolean",
	FormTextString:    "text-marshaled string",
	FormByteString:    "base64 byte string",
	FormRawBytes:      "raw byte slice",
	FormOpaque:        "opaque value",
}

// String returns the form's label.
func (f Form) String() string {
	if f >= formCount {
		return fmt.Sprintf("Form(%d)", uint8(f))
	}

	return formNames[f]
}

// isSized reports whether the form's instance carries a size rather than a
// single value, which is what a dialect spelling both with one key (eq on a
// slice) resolves against.
func (f Form) isSized() bool {
	return f == FormArray || f == FormObject
}

// Shape is everything an operation needs to know about what it is constraining:
// the declared Go type, its dereferenced form, the kind a scalar parses at, the
// JSON shape the instance takes, and whether the occurrence admits null.
type Shape struct {
	// Type is the type as declared, pointer-preserving, so a *int element still
	// knows it can hold null.
	Type reflect.Type
	// Elem is Type with its pointer chain followed: the type a scalar converts
	// back to when a coerced shape re-serializes.
	Elem reflect.Type
	// Kind is Elem's kind, the width a scalar parses at.
	Kind reflect.Kind
	// Form is the JSON shape of the instance.
	Form Form
	// Nullable reports a pointer occurrence. Exactly one operation consults it:
	// the non-zero assertion, because go-playground's required on a pointer
	// means non-nil and says nothing about the pointed-to value. Doubling the
	// matrix for one operation would cost more than this documented branch.
	Nullable bool
}

// ShapeOf classifies a field or element from its Go type and the type-derived
// base schema. It is the single home of the string-coercion test, the
// schema-permits-a-string test, the byte-slice test, and every kind predicate
// the dialects used to each keep their own copy of.
func ShapeOf(t reflect.Type, base *jsonschema.Schema) Shape {
	elem := numkind.DerefType(t)

	return Shape{
		Type:     t,
		Elem:     elem,
		Kind:     elem.Kind(),
		Form:     classifyForm(elem, base),
		Nullable: t.Kind() == reflect.Pointer,
	}
}

// classifyForm picks the Form from the JSON shape the instance actually takes.
//
// The type-derived base is the authority whenever it declares a type: a verbatim
// type schema, a type= override, or a provider can make a string-kinded field's
// instance a number, and a bound written against that instance is a numeric one
// no matter what the Go kind says. The Go kind fills in what the base leaves
// open, and is the only thing that can distinguish a coerced shape from a native
// one -- a string-typed schema over a numeric kind is the json:",string" (or
// MarshalText) shape, whose scalars compare against the serialized text.
func classifyForm(t reflect.Type, base *jsonschema.Schema) Form {
	str := schemaPermitsString(base)

	// A byte slice never has per-element schemas: it is one base64 string when
	// its schema says so, and otherwise a raw JSON value.
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		if str {
			return FormByteString
		}

		return FormRawBytes
	}

	// The two scalar kinds that can be coerced decide on the base's string-ness
	// alone; nothing else about the base can override what the Go value is.
	switch {
	case numkind.IsInteger(t.Kind()) || numkind.IsFloat(t.Kind()):
		if str {
			return FormCoercedNumber
		}

		return FormNumber

	case t.Kind() == reflect.Bool:
		if str {
			return FormCoercedBool
		}

		return FormBool
	}

	switch t.Kind() {
	case reflect.String:
		// A string kind under a number-typed schema is a verbatim or overridden
		// payload: the instance is a number, so numeric keywords are the ones
		// that constrain it.
		if schemaDeclares(base, typename.Integer, typename.Number) {
			return FormNumber
		}

		return FormString

	case reflect.Slice, reflect.Array:
		if str {
			return FormTextString
		}

		return FormArray

	case reflect.Map:
		if str {
			return FormTextString
		}

		return FormObject

	default:
		switch {
		case str:
			return FormTextString
		case schemaDeclares(base, typename.Integer, typename.Number):
			return FormNumber
		case schemaDeclares(base, typename.Boolean):
			return FormBool
		default:
			return FormOpaque
		}
	}
}

// schemaPermitsString reports whether the type-derived schema can hold a string
// instance. It accepts the single Type form, the Types array a nullable field
// produces, and a base64 contentEncoding, so a byte slice -- whose kind is not a
// string but whose schema is one -- classifies as the string it emits. A nil
// schema permits no string.
func schemaPermitsString(s *jsonschema.Schema) bool {
	return schemaDeclares(s, typename.String) ||
		(s != nil && s.ContentEncoding == base64Encoding)
}

// schemaDeclares reports whether the schema names any of the given JSON types,
// in either the single Type form or the Types array a nullable field produces.
func schemaDeclares(s *jsonschema.Schema, names ...string) bool {
	if s == nil {
		return false
	}

	for _, name := range names {
		if s.Type == name || slices.Contains(s.Types, name) {
			return true
		}
	}

	return false
}
