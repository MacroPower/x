package fuzzgen

import (
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// tagClass is the shape a jsonschema tag pair is judged against: the JSON
// form the field emits, as far as the pair's admission depends on it, with
// the numeric kinds split by what literal they can spell.
type tagClass uint8

const (
	// The classOther class admits annotations alone: an opaque value, a raw JSON
	// value, a big integer, a JSON number literal, and a struct marshaling
	// itself through a promoted method.
	classOther tagClass = iota
	// The classString class is a string instance from a string kind, or one a hook
	// declares over another kind with no marshaler, whose literals are text.
	classString
	// The classCoercedString class is a string kind marshaling itself as text.
	classCoercedString
	// The classText class is a string a non-scalar kind marshals itself as,
	// the [time.Time] form.
	classText
	// The classBytes class is a byte slice, one base64 string.
	classBytes
	// The classCoercedInt, classCoercedUint, and classCoercedFloat classes
	// are numeric kinds under json:",string", whose literals spell the
	// number and whose bounds measure the quoted text.
	classCoercedInt
	classCoercedUint
	classCoercedFloat
	// The classInt, classUint, and classFloat classes are number instances
	// by kind.
	classInt
	classUint
	classFloat
	// The classBool class is a boolean instance.
	classBool
	// The classSequence class is an array instance.
	classSequence
	// The classMap class is an object instance a map emits, and classObject one a
	// struct or a declared object schema emits.
	classMap
	classObject
)

// The parameter spellings the classes admit, each a subset of the spellings
// the draw pairs with the key. A spelling outside a set is one the dialect
// refuses on that class, or one whose admission depends on a fact the class
// does not carry (a null on a nullable occurrence, an escaped value), which
// the oracle leaves unjudged.
var (
	admittedFlags     = set("true", "false")
	admittedCounts    = set("0", "1", "3")
	admittedPatterns  = set("^a", "[0-9]+")
	admittedFormats   = set("email", "date-time", "uuid")
	admittedStrings   = set("1", "x", "1.5", "true")
	admittedStrLists  = set("1|2", "a|b", "1|x", "true|false")
	admittedInts      = set("0", "1")
	admittedIntLists  = set("1|2")
	admittedFloats    = set("0", "1", "-2.5", "1e3")
	admittedFloatLits = set("1", "1.5")
	admittedDivisors  = set("1", "0.5")
	admittedBools     = set("true")
	admittedBoolLists = set("true|false")

	// AnnotationKeys are the keys every class admits, and the keys a type=
	// pair may follow.
	annotationKeys = set(keyword.Description, keyword.Title, keyword.Deprecated, keyword.ReadOnly, keyword.WriteOnly)

	// DeclaredClasses maps a type= parameter to the class the keys after it
	// are judged against. A null override is left unjudged, since its
	// admission depends on the occurrence's null decision.
	declaredClasses = map[string]tagClass{
		typename.String:  classString,
		typename.Integer: classInt,
		typename.Number:  classFloat,
		typename.Boolean: classBool,
		typename.Array:   classSequence,
		typename.Object:  classObject,
	}
)

// set returns a membership map over values.
func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}

	return m
}

// Admitted reports whether every jsonschema tag on rt's exported, unexcluded
// fields consists of pairs the field's shape admits, as the package
// documents: a bound, size, pattern, or format the emitted form carries, a
// literal the form can spell, an annotation, or a type= pair that follows
// annotations alone and whose later keys the declared type admits. A type
// the oracle answers true for must generate without a jsonschema tag
// refusal. A tag with a spelling the oracle does not judge, and a tag
// parsing to a bare description, leaves the answer false, so the rig asks
// nothing of it.
func Admitted(rt reflect.Type) bool {
	for f := range rt.Fields() {
		if f.Anonymous || f.PkgPath != "" || f.Tag.Get("json") == "-" {
			continue
		}

		tag := f.Tag.Get("jsonschema")
		if tag == "" {
			continue
		}

		if !admittedTag(classOf(f.Type, jsonQuoted(f)), tag) {
			return false
		}
	}

	return true
}

// admittedTag judges one tag against the class of the field carrying it.
func admittedTag(cls tagClass, tag string) bool {
	directives, desc, err := tagparse.Parse(tag)
	if err != nil || desc != "" {
		return false
	}

	seen := map[string]bool{}

	for i, d := range directives {
		if seen[d.Key] {
			return false
		}

		seen[d.Key] = true

		if d.Key == keyword.Type {
			declared, ok := declaredClasses[d.Value]
			if !ok {
				return false
			}

			for _, earlier := range directives[:i] {
				if !annotationKeys[earlier.Key] {
					return false
				}
			}

			cls = declared

			continue
		}

		if !admits(cls, d.Key, d.Value) {
			return false
		}
	}

	return true
}

// admits reports whether the class admits the key with the parameter.
func admits(cls tagClass, key, param string) bool {
	switch key {
	case keyword.Description, keyword.Title:
		return true
	case keyword.Deprecated, keyword.ReadOnly, keyword.WriteOnly:
		return admittedFlags[param]
	}

	switch cls {
	case classString, classCoercedString:
		return admitsLength(key, param) || admitsLiteral(key, param, admittedStrings, admittedStrLists)
	case classText, classBytes:
		return admitsLength(key, param)
	case classCoercedInt, classCoercedUint:
		return admitsLength(key, param) || admitsLiteral(key, param, admittedInts, admittedIntLists)
	case classCoercedFloat:
		return admitsLength(key, param) || admitsLiteral(key, param, admittedFloatLits, admittedIntLists)
	case classInt, classUint:
		return admitsBound(key, param, admittedInts, set("1")) ||
			admitsLiteral(key, param, admittedInts, admittedIntLists)

	case classFloat:
		return admitsBound(key, param, admittedFloats, admittedDivisors) ||
			admitsLiteral(key, param, admittedFloatLits, admittedIntLists)

	case classBool:
		return admitsLiteral(key, param, admittedBools, admittedBoolLists)
	case classSequence:
		switch key {
		case keyword.MinItems, keyword.MaxItems:
			return admittedCounts[param]
		case keyword.UniqueItems:
			return admittedFlags[param]
		}

		return false

	case classMap, classObject:
		return (key == keyword.MinProperties || key == keyword.MaxProperties) && admittedCounts[param]
	case classOther:
		return false
	}

	return false
}

// admitsLength judges the string keys: a length count, a pattern, or a
// format.
func admitsLength(key, param string) bool {
	switch key {
	case keyword.MinLength, keyword.MaxLength:
		return admittedCounts[param]
	case keyword.Pattern:
		return admittedPatterns[param]
	case keyword.Format:
		return admittedFormats[param]
	}

	return false
}

// admitsBound judges the numeric keys against the bound and divisor
// spellings a numeric class admits.
func admitsBound(key, param string, bounds, divisors map[string]bool) bool {
	switch key {
	case keyword.Minimum, keyword.Maximum, keyword.ExclusiveMinimum, keyword.ExclusiveMaximum:
		return bounds[param]
	case keyword.MultipleOf:
		return divisors[param]
	}

	return false
}

// admitsLiteral judges the value keys against the scalar and list spellings
// a class admits.
func admitsLiteral(key, param string, scalars, lists map[string]bool) bool {
	switch key {
	case keyword.Const, keyword.Default:
		return scalars[param]
	case keyword.Enum, keyword.Examples:
		return lists[param]
	}

	return false
}

// jsonQuoted reports whether the field's json tag carries the ,string
// option.
func jsonQuoted(f reflect.StructField) bool {
	_, opts, _ := strings.Cut(f.Tag.Get("json"), ",")

	return slices.Contains(strings.Split(opts, ","), "string")
}

// classOf returns the class a field of ft is judged as, under the json
// tag's ,string option.
func classOf(ft reflect.Type, quoted bool) tagClass {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	switch ft {
	case reflect.TypeFor[Stamp]():
		return classCoercedString
	case reflect.TypeFor[Coded](), reflect.TypeFor[Labeled]():
		return classString
	case reflect.TypeFor[Pairs]():
		return classObject
	case reflect.TypeFor[time.Time]():
		return classText
	case reflect.TypeFor[[]byte]():
		return classBytes
	case reflect.TypeFor[Marked](), reflect.TypeFor[big.Int]():
		return classOther
	}

	if ft.Name() != "" && ft.Kind() != reflect.Struct && ft != reflect.TypeFor[fuzzshape.Level]() {
		// A named scalar or composite outside the pools above: json.Number,
		// jsontext.Value, and their kin, whose literal grammar the oracle
		// does not model.
		return classOther
	}

	kind := ft.Kind()

	switch {
	case kind == reflect.Struct:
		return classObject
	case kind == reflect.String:
		return classString
	case kind >= reflect.Int && kind <= reflect.Int64:
		if quoted {
			return classCoercedInt
		}

		return classInt

	case kind >= reflect.Uint && kind <= reflect.Uintptr:
		if quoted {
			return classCoercedUint
		}

		return classUint

	case kind == reflect.Float32 || kind == reflect.Float64:
		if quoted {
			return classCoercedFloat
		}

		return classFloat

	case kind == reflect.Bool:
		if quoted {
			return classOther
		}

		return classBool

	case kind == reflect.Slice || kind == reflect.Array:
		return classSequence
	case kind == reflect.Map:
		return classMap
	default:
		return classOther
	}
}

// InterpreterRuns returns how many times an interpreter registered under
// the validate tag key runs for each tagged field a run over rt reaches,
// keyed by [RunKey]: once for each of rt's own exported, unexcluded fields
// carrying the tag, and for each tagged field of [Tagged] once when
// definitions are extracted, since one definition serves every occurrence,
// and once per occurrence of the type among rt's fields otherwise, since
// each occurrence reflects the body again. An occurrence a type= pair
// replaced counts, since the replaced subtree is still reflected. The
// answer holds for a run that generates; a refused run stops wherever it
// stops.
func InterpreterRuns(rt reflect.Type, definitions bool) map[string]int {
	runs := map[string]int{}
	occurrences := 0

	for f := range rt.Fields() {
		if f.Anonymous || f.PkgPath != "" || f.Tag.Get("json") == "-" {
			continue
		}

		if f.Tag.Get("validate") != "" {
			runs[RunKey(rt, f.Name)]++
		}

		if namedIn(f.Type) == reflect.TypeFor[Tagged]() {
			occurrences++
		}
	}

	if occurrences == 0 {
		return runs
	}

	if definitions {
		occurrences = 1
	}

	for f := range reflect.TypeFor[Tagged]().Fields() {
		if f.Tag.Get("validate") != "" {
			runs[RunKey(reflect.TypeFor[Tagged](), f.Name)] = occurrences
		}
	}

	return runs
}

// RunKey names a field for [InterpreterRuns]: its declaring type and its Go
// name, the pair a [jsonschema.FieldContext] carries as Owner and
// StructField.Name.
func RunKey(owner reflect.Type, field string) string {
	return owner.String() + "." + field
}
