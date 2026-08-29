package tagmodel

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

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
	// FormCoercedString is a string Go kind under json:",string": encoding/json
	// encodes the already-encoded string a second time, so the instance is the
	// JSON-quoted text (value abc marshals as "\"abc\""). Its scalars compare
	// against that quoted text; keywords that would measure or match the
	// unquoted value (bounds, len, the string keywords) have no faithful
	// mapping and report. Unlike the numeric and bool coercions, this form is
	// invisible to the type-and-base classification (a plain string field has
	// the same string kind under the same string-typed schema), so it exists
	// only where the caller supplies the quoted flag ([ShapeOfQuoted]).
	FormCoercedString
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
	// FormRef is a payload that is a bare reference to a definition, over a Go
	// kind that reveals nothing either. The instance shape is whatever the
	// definition declares, which is not readable here, so a keyword a dialect
	// names outright is emitted as a $ref sibling and the definition decides
	// whether it means anything. It is the one deliberately permissive column,
	// and it is permissive only for the rules that name a keyword: a rule that
	// would have to infer one, or read a value at a Go kind, still reports.
	FormRef
	// FormDeclaredObject is a payload declaring an object outright over a Go
	// kind that is not a map: an inline (anonymous) struct field is the common
	// case, and a verbatim or overridden object schema on an opaque kind is
	// the other. The instance is an object, so a property-count keyword a
	// dialect names outright applies exactly as it does on the $defs-backed
	// named spelling. What the object holds is not classifiable from here, so
	// element- and value-wise rules still report, and a rule-shaped bound has
	// no family to take: go-playground's min means an entry count only on a
	// map, and this is not one.
	FormDeclaredObject
	// FormOpaque is every remaining shape -- a struct, interface, channel, or
	// function -- whose instance the tag vocabulary cannot describe.
	FormOpaque
	// The formCount constant bounds the matrix columns. Adding a form above grows every row,
	// which the init-time totality walk then reports as unfilled.
	formCount
)

// The formNames table labels each form for the matrix dump and the rejection text.
var formNames = [formCount]string{
	FormUnset:          "unset",
	FormString:         "string",
	FormNumber:         "number",
	FormBool:           "boolean",
	FormArray:          "array",
	FormObject:         "object",
	FormCoercedNumber:  "string-coerced number",
	FormCoercedBool:    "string-coerced boolean",
	FormCoercedString:  "string-coerced string",
	FormTextString:     "text-marshaled string",
	FormByteString:     "base64 byte string",
	FormRawBytes:       "raw byte slice",
	FormRef:            "referenced definition",
	FormDeclaredObject: "declared object",
	FormOpaque:         "opaque value",
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
	// Nullable reports whether the occurrence admits null. Two producers derive
	// it differently. [ShapeOf] reads the Go type alone, so it reports the
	// pointer occurrences and nothing else; internal/tagparse classifies through
	// it and sees only that pointer answer. The parent package's
	// FieldContext.Shape reads the generator's own null decision off the field's
	// node, so Nullable is true there for a nilable slice, map, or byte slice,
	// though its Go type is not a pointer.
	//
	// Two operations consult it. The non-zero assertion forbids the null a nil
	// occurrence marshals as, and the scalar constructor admits the literal null
	// only where the occurrence can hold it. Doubling the matrix for them would
	// cost more than these documented branches.
	Nullable bool
}

// isPointer reports whether the occurrence is a pointer, a narrower question
// than [Shape.Nullable] answers. The non-zero assertion asks it because
// go-playground's required on a pointer means non-nil and says nothing about
// the pointed-to value. A nilable container's emptiness is still a size, so
// the floor keeps applying there. [ShapeForTypeName] builds a shape with no
// Type, so the method guards on that before reading the kind.
func (sh Shape) isPointer() bool {
	return sh.Type != nil && sh.Type.Kind() == reflect.Pointer
}

// FormForTypeName returns the form an instance of the named JSON type takes. It
// is the classification for a dialect that names the JSON type outright rather
// than describing a Go value -- the jsonschema tag's type= pair -- and it is
// where a JSON type name becomes a form, so a name written in a tag and a name
// [classifyForm] reads off a type-derived schema mean the same thing.
//
// The null type names no value a constraint can describe, so it maps to
// [FormOpaque], whose every operation reports. A name outside the seven JSON
// Schema types maps to [FormUnset].
func FormForTypeName(name string) Form {
	switch name {
	case typename.String:
		return FormString
	case typename.Integer, typename.Number:
		return FormNumber
	case typename.Boolean:
		return FormBool
	case typename.Array:
		return FormArray
	case typename.Object:
		return FormObject
	case typename.Null:
		return FormOpaque
	default:
		return FormUnset
	}
}

// ShapeForTypeName returns the shape an instance of the named JSON type takes.
// It is what a dialect installs when its tag restates the type outright: the
// named type displaces the Go type entirely, so the shape carries the named
// type's own form and the kind its scalar literals parse at, and it never
// admits null -- an overridden occurrence is the named type itself, so a null
// literal has nothing to assign to.
//
// The three types a tag cannot spell a scalar for -- array, object, and null --
// carry [reflect.Invalid] as their kind, which is how a front-end tells that a
// scalar key following the override has nothing to parse against.
func ShapeForTypeName(name string) Shape {
	var kind reflect.Kind

	switch name {
	case typename.String:
		kind = reflect.String
	case typename.Integer:
		// The widest integer, not a platform int, so a literal above 2^31-1
		// survives on a 32-bit build.
		kind = reflect.Int64
	case typename.Number:
		kind = reflect.Float64
	case typename.Boolean:
		kind = reflect.Bool
	}

	return Shape{Kind: kind, Form: FormForTypeName(name)}
}

// ShapeOf classifies a field or element from its Go type and the type-derived
// base schema. It is the single home of the string-coercion test, the
// schema-permits-a-string test, the byte-slice test, and every kind predicate
// the dialects used to each keep their own copy of.
//
// Type and base alone cannot see a json:",string" flag on a string Go kind
// (the numeric and bool coercions surface as a string-typed base over a
// non-string kind, but a quoted string field looks exactly like a plain one);
// a caller that knows the flag classifies through [ShapeOfQuoted] instead.
func ShapeOf(t reflect.Type, base *jsonschema.Schema) Shape {
	return ShapeOfQuoted(t, base, false)
}

// ShapeOfQuoted is [ShapeOf] carrying the field's json:",string" flag, the one
// input the type and base cannot express: with it, a string Go kind under a
// string-typed base classifies as [FormCoercedString] (its instance is the
// JSON-quoted text) rather than a plain string. The flag is redundant for
// every other kind, whose coercion the base already states.
func ShapeOfQuoted(t reflect.Type, base *jsonschema.Schema, quoted bool) Shape {
	elem := numkind.DerefType(t)

	return Shape{
		Type:     t,
		Elem:     elem,
		Kind:     elem.Kind(),
		Form:     classifyForm(elem, base, quoted),
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
func classifyForm(t reflect.Type, base *jsonschema.Schema, quoted bool) Form {
	str := schemaPermitsString(base)

	// A byte slice never has per-element schemas: it is one base64 string when
	// its schema says so, and otherwise a raw JSON value. A marshaler-bearing
	// uint8 element is exempt (the predicate mirrors encoding/json): such a
	// slice marshals as a real JSON array with per-element schemas, so it
	// classifies as any other slice below.
	if reflectkind.IsBase64ByteSlice(t) {
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
		// A string kind under a number- or boolean-typed schema is a verbatim
		// or overridden payload: the instance is a number or boolean, so that
		// form's keywords are the ones that constrain it.
		if f := declaredForm(base); f == FormNumber || f == FormBool {
			return f
		}

		// The quoted flag is the only thing that distinguishes a
		// double-encoding json:",string" string field from a plain one.
		if quoted && str {
			// A json.Number belongs in the numeric coercion column rather than
			// the double-encoding one; see [reflectkind.IsJSONNumber].
			if reflectkind.IsJSONNumber(t) {
				return FormCoercedNumber
			}

			return FormCoercedString
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
		if str {
			return FormTextString
		}

		// The scalar forms and the object form are read off the base here. A
		// declared object -- an anonymous struct's inline payload, or a
		// verbatim object override -- is judged by what it declares, so its
		// count keywords apply while its values stay unclassifiable
		// ([FormDeclaredObject] carries exactly that split). A declared array
		// stays opaque: nothing inlines one over these kinds without an
		// explicit override, and its elements are equally unclassifiable.
		f := declaredForm(base)
		if f == FormNumber || f == FormBool {
			return f
		}

		if f == FormObject {
			return FormDeclaredObject
		}

		if base != nil && base.Ref != "" {
			// The payload defers to a definition and the Go kind said nothing,
			// so neither source knows what the instance is.
			return FormRef
		}

		return FormOpaque
	}
}

// declaredForm returns the form the type-derived schema names outright, or
// [FormUnset] when it names none. It routes through [FormForTypeName] so a type
// name reflection wrote and a type name a tag wrote classify identically.
//
// The null member of a Types array is skipped: there it states that the
// occurrence admits null, not what the value looks like.
func declaredForm(s *jsonschema.Schema) Form {
	if s == nil {
		return FormUnset
	}

	if f := namedForm(s.Type); f != FormUnset {
		return f
	}

	for _, name := range s.Types {
		if f := namedForm(name); f != FormUnset {
			return f
		}
	}

	return FormUnset
}

// namedForm is [FormForTypeName] with the null marker skipped, for reading a
// schema's own type list.
func namedForm(name string) Form {
	if name == typename.Null {
		return FormUnset
	}

	return FormForTypeName(name)
}

// schemaPermitsString reports whether the type-derived schema can hold a string
// instance. It accepts the single Type form, the Types array a nullable field
// produces, and a base64 contentEncoding, so a byte slice -- whose kind is not a
// string but whose schema is one -- classifies as the string it emits. A nil
// schema permits no string.
func schemaPermitsString(s *jsonschema.Schema) bool {
	return schemaDeclares(s, typename.String) ||
		(s != nil && s.ContentEncoding == content.Base64)
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
