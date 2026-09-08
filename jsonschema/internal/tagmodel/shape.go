package tagmodel

import (
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/content"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
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
	// FormCoercedBool is a bool Go kind whose schema is a string: a bool type
	// marshaling itself as text. The boolean half of the numeric coercion.
	// (A json:",string" bool is a generation error under encoding/json/v2, so
	// the flag produces no boolean coercion.)
	FormCoercedBool
	// FormCoercedString is a string Go kind that marshals itself as text, so
	// the instance is the text MarshalText writes rather than the Go string.
	// Its scalars compare against that text, as the numeric coercion's do,
	// which is what keeps a const from pinning a value the field never emits.
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
	// FormUnresolvedRef is a payload that is a bare reference to a definition
	// the classifier cannot read: no resolver was supplied, or the definition
	// is unfilled or is itself a reference. Nothing about the instance is
	// known, so every rule reports. A reference whose definition is readable
	// never lands here: it classifies as the definition's own schema would,
	// which is what the generator supplies for every field and element.
	FormUnresolvedRef
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
	FormUnresolvedRef:  "unresolved reference",
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

// isSized reports whether the form's Go value carries a size rather than a
// single value, which is what a dialect spelling both with one key (eq on a
// slice) resolves against. A byte slice is sized here although its instance
// is one string: go-playground's eq on a []byte compares the length, so the
// rule-shaped dialect means a size there, and the size cells report that the
// base64 string has no array to measure.
func (f Form) isSized() bool {
	return f == FormArray || f == FormObject || f == FormByteString
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
	// Nullable reports whether the occurrence admits null. [ShapeOf] reads the
	// Go type alone, so it reports the pointer occurrences and nothing else.
	// The parent package reads the generator's own null decision off the
	// field's node and overrides that answer at the two places it classifies a
	// field: the input it hands internal/tagparse, and FieldContext.Shape.
	// The override is what makes Nullable true for an interface occurrence or
	// a NullAllowed stance, neither of which is a pointer type, and false for
	// a pointer to a NullForbidden type, whose schema the generator gives no
	// null branch. A bare slice, map, or byte slice is not nullable
	// under the default marshal options, where encoding/json/v2 marshals a
	// nil one as its empty instance; the generator's WithJSONOptions with
	// FormatNilSliceAsNull or FormatNilMapAsNull makes the parent's override
	// answer true for those occurrences too. An element occurrence keeps the
	// pointer answer, since an element is not the container that holds it.
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
// Type and base alone cannot see a json:",string" flag on
// [encoding/json.Number] (the numeric coercion otherwise surfaces as a
// string-typed base over a non-string kind, but Number's kind is string); a
// caller that knows the flag classifies through [ShapeOfQuoted] instead. Nor
// can they read the definition a bare $ref base names, so such a base
// classifies as [FormUnresolvedRef] here; a caller that can read it
// supplies the resolver to [ShapeOfQuoted].
func ShapeOf(t reflect.Type, base *jsonschema.Schema) Shape {
	return ShapeOfQuoted(t, base, false, nil)
}

// ShapeOfQuoted is [ShapeOf] carrying the two inputs the type and base
// cannot express. The json:",string" flag: with it, an [encoding/json.Number]
// under a string-typed base classifies as [FormCoercedNumber] (its instance
// is the once-quoted numeric literal) rather than a plain string. The flag is
// redundant for every other kind: the numeric kinds' coercion the base
// already states, and every other kind under the flag is a generation error
// upstream. And the definition a bare $ref base names: def returns its
// schema, or nil while it is unreadable, and a reference then classifies as
// that schema would over the same Go type, so a bound on a reference to an
// integer definition is a numeric one and a count on a reference to a struct
// definition applies as it does on the inline struct. A nil def, or one
// answering nil or another reference, leaves the reference
// [FormUnresolvedRef], the column every rule reports on.
//
// A nil type classifies as [FormOpaque], so every rule against it reports a
// shape error rather than dereferencing nothing. Only a caller-built context
// can reach here without a type.
func ShapeOfQuoted(t reflect.Type, base *jsonschema.Schema, quoted bool, def func() *jsonschema.Schema) Shape {
	if t == nil {
		return Shape{Form: FormOpaque}
	}

	elem := numkind.DerefType(t)

	return Shape{
		Type:     t,
		Elem:     elem,
		Kind:     elem.Kind(),
		Form:     classifyForm(elem, base, quoted, def),
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
//
// A base that is a bare $ref classifies as the definition it names, read
// once through def: the definition's schema takes the base's place over the
// same Go type, so the reference answers exactly as the inline schema would.
// A definition that cannot be read, or that is itself a reference, leaves
// the reference unresolved.
func classifyForm(t reflect.Type, base *jsonschema.Schema, quoted bool, def func() *jsonschema.Schema) Form {
	if base != nil && base.Ref != "" {
		if def == nil {
			return FormUnresolvedRef
		}

		body := def()
		if body == nil || body.Ref != "" {
			return FormUnresolvedRef
		}

		return classifyForm(t, body, quoted, nil)
	}

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

	// A byte array is the same base64 string with a pinned length; the
	// generator never renders it as a raw JSON value, so only the string
	// form applies.
	if reflectkind.IsBase64ByteArray(t) && str {
		return FormByteString
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
		// json:",string" [encoding/json.Number] field (string kind, numeric
		// instance) from a plain string field; see [reflectkind.IsJSONNumber].
		if quoted && str && reflectkind.IsJSONNumber(t) {
			return FormCoercedNumber
		}

		// A string kind that marshals itself as text writes MarshalText's
		// output, not the Go string, so its scalars take the coerced
		// round-trip. The predicate is reflection's own for the text step: a
		// JSON marshaler outranks the text methods under encoding/json/v2,
		// so a type carrying one writes whatever that marshaler emits.
		if str && reflectkind.ImplementsAnyTextMarshaler(t) && !reflectkind.ImplementsAnyJSONMarshaler(t) {
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
	return schemashape.DeclaresType(s, typename.String) ||
		(s != nil && s.ContentEncoding == content.Base64)
}
