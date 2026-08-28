package jsonschema_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
)

// The reason constants name the field classes the probe cannot observe, on the
// convention suite_test.go uses for its skips. Each is a property of the probe,
// not of the package, so none of them appears in doc.go or README.md.
const (
	// A field carrying no json tag never reaches a tag interpreter, since the
	// probe is registered under the json tag key.
	reasonUntaggedField = "a field with no json tag is never handed to a tag interpreter"
	// A json:"-" field is dropped before field processing, so it has neither a
	// property name nor a classified shape.
	reasonExcludedField = "a json:\"-\" field is excluded from the schema"
	// An unexported field is ignored by encoding/json, and the generator builds
	// no node for one.
	reasonUnexportedField = "an unexported field has no property to classify"
	// An embed the generator composes with allOf returns before the pending
	// field list, so its own node never reaches applyFieldInterpreters. Its
	// promoted fields are observed under the embedded type as their owner.
	reasonComposedEmbed = "an allOf-composed embed is not a property of the parent object"
	// Two fields resolving to one JSON name at the same depth are both dropped
	// by encoding/json's dominance rule, so neither is a property to classify.
	reasonShadowedName = "a JSON name claimed by more than one field at one depth is dropped"
)

// The raw byte and opaque columns admit any JSON value, so no token follows
// from either, and the form does not influence what encoding/json writes. No
// assertion over the marshaled member could distinguish a right classification
// from a wrong one there. Their totality is pinned in internal/tagmodel
// instead, by the matrix golden and TestFormClassificationTotal.
const reasonFormPredictsNoToken = "the raw byte and opaque columns admit any JSON value, so no token follows from them"

// unprobedReasons lists every reason the probe could legitimately not observe
// f. An unobserved field matching none of them is a hole in the oracle rather
// than a property of the probe, so the synthesized leg fails on one.
func unprobedReasons(f reflect.StructField, shared map[string]int) []string {
	var out []string

	if !f.IsExported() {
		out = append(out, reasonUnexportedField)
	}

	tag, tagged := f.Tag.Lookup("json")
	if !tagged {
		out = append(out, reasonUntaggedField)
	}

	if name, _, _ := strings.Cut(tag, ","); name == "-" {
		out = append(out, reasonExcludedField)
	}

	if f.Anonymous {
		out = append(out, reasonComposedEmbed)
	}

	if shared[jsonName(f)] > 1 {
		out = append(out, reasonShadowedName)
	}

	return out
}

// jsonName returns the JSON property name a field resolves to, which is the
// tag's name when it sets one and the Go name otherwise.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}

	return name
}

// jsonToken is the kind of JSON value a marshaled field turned out to be. It is
// the oracle's observation: encoding/json wrote one of these six, and the form
// tagmodel assigned the field has to agree.
type jsonToken uint8

const (
	tokenNull jsonToken = iota
	tokenString
	tokenNumber
	tokenBool
	tokenArray
	tokenObject
)

// String names the token for a failure message.
func (tk jsonToken) String() string {
	return [...]string{"null", "string", "number", "boolean", "array", "object"}[tk]
}

// tokenOf classifies a marshaled JSON value by its first structural byte, which
// is all encoding/json's grammar needs to distinguish the six.
func tokenOf(t *testing.T, raw json.RawMessage) jsonToken {
	t.Helper()

	require.NotEmpty(t, raw, "marshaled value is empty")

	switch raw[0] {
	case '{':
		return tokenObject
	case '[':
		return tokenArray
	case '"':
		return tokenString
	case 't', 'f':
		return tokenBool
	case 'n':
		return tokenNull
	default:
		return tokenNumber
	}
}

// shapeObservation is one field the probe saw during a generation run: the type
// that declares it, its property name, and the classification the generator
// performed. Owner is recorded because the probe fires for fields of nested and
// $def'd struct types too, whose names are not members of the root object; the
// assertion marshals a value of the owner rather than of the root.
type shapeObservation struct {
	owner reflect.Type
	field reflect.StructField
	name  string
	typ   reflect.Type
	base  *jsonschema.Schema
	shape jsonschema.Shape
	elems []jsonschema.Shape
}

// shapeProbe returns a generate option that records the classification of every
// field carrying a json tag. It registers under the json key rather than a key
// of its own so no roster type and no synthesized shape needs retagging: the
// generator looks an interpreter's key up with StructField.Tag.Lookup and
// special-cases nothing. The probe writes nothing to the canvas, so the schema
// it observes is the schema generation would have produced without it.
func shapeProbe(seen *[]shapeObservation) jsonschema.GenerateOption {
	return jsonschema.WithTagInterpreter("json", jsonschema.TagInterpreterFunc(
		func(_ context.Context, fc jsonschema.FieldContext, _ jsonschema.Tag) error {
			children := fc.ElementContexts()
			elems := make([]jsonschema.Shape, len(children))

			for i := range children {
				elems[i] = children[i].Shape()
			}

			*seen = append(*seen, shapeObservation{
				owner: fc.Owner,
				field: fc.StructField,
				name:  fc.Name,
				typ:   fc.Type,
				base:  fc.Base,
				shape: fc.Shape(),
				elems: elems,
			})

			return nil
		}))
}

// rawMessageType and numberType are the two encoding/json types whose contents
// are a JSON document rather than an ordinary Go value, so the filler writes
// valid JSON into them instead of a generic non-zero value.
var (
	rawMessageType = reflect.TypeFor[json.RawMessage]()
	numberType     = reflect.TypeFor[json.Number]()

	// The formToken table gives the token each classifying form predicts.
	// FormRef is absent because its token comes from the definition it names,
	// and FormRawBytes and FormOpaque are absent because they predict none; see
	// reasonFormPredictsNoToken.
	formToken = map[jsonschema.Form]jsonToken{
		jsonschema.FormString:         tokenString,
		jsonschema.FormTextString:     tokenString,
		jsonschema.FormNumber:         tokenNumber,
		jsonschema.FormBool:           tokenBool,
		jsonschema.FormArray:          tokenArray,
		jsonschema.FormObject:         tokenObject,
		jsonschema.FormDeclaredObject: tokenObject,
		jsonschema.FormCoercedNumber:  tokenString,
		jsonschema.FormCoercedBool:    tokenString,
		jsonschema.FormCoercedString:  tokenString,
		jsonschema.FormByteString:     tokenString,
	}
)

// fillNonZero populates v so every pointer is non-nil, every slice and map has
// one entry, and every scalar is away from its zero. Determinism is the point:
// the oracle asserts that a null token appears only where the Go value is nil,
// which a random fill would leave to chance.
func fillNonZero(v reflect.Value) {
	switch v.Type() {
	case rawMessageType:
		v.SetBytes([]byte(`{"k":1}`))

		return

	case numberType:
		v.SetString("1")

		return

	default:
	}

	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}

		fillNonZero(v.Elem())

	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}

	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillNonZero(v.Index(0))

	case reflect.Array:
		for i := range v.Len() {
			fillNonZero(v.Index(i))
		}

	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))

		key := reflect.New(v.Type().Key()).Elem()
		fillNonZero(key)

		val := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(val)

		v.SetMapIndex(key, val)

	case reflect.Interface:
		// Only an empty interface can hold an arbitrary value; a method-bearing
		// one has no value the filler can invent.
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf("x"))
		}

	case reflect.String:
		v.SetString("x")

	case reflect.Bool:
		v.SetBool(true)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(1)

	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)

	default:
	}
}

// jsonStringOption reports whether the field carries json:",string", the one
// classification input neither the Go type nor the base schema states.
func jsonStringOption(f reflect.StructField) bool {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return false
	}

	_, opts, _ := strings.Cut(tag, ",")

	for opt := range strings.SplitSeq(opts, ",") {
		if opt == "string" {
			return true
		}
	}

	return false
}

// jsonOmitOption reports whether the field carries an option that drops it from
// the object at its zero value, which is the one reason a member may be missing
// without that being a divergence.
func jsonOmitOption(f reflect.StructField) bool {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return false
	}

	_, opts, _ := strings.Cut(tag, ",")

	for opt := range strings.SplitSeq(opts, ",") {
		if opt == "omitempty" || opt == "omitzero" {
			return true
		}
	}

	return false
}

// declaredToken returns the token a schema's own type names, skipping the null
// member a nullable occurrence adds. The second result is false when the schema
// names no type.
func declaredToken(s *jsonschema.Schema) (jsonToken, bool) {
	if s == nil {
		return tokenNull, false
	}

	names := s.Types
	if s.Type != "" {
		names = []string{s.Type}
	}

	for _, name := range names {
		switch name {
		case "string":
			return tokenString, true
		case "integer", "number":
			return tokenNumber, true
		case "boolean":
			return tokenBool, true
		case "array":
			return tokenArray, true
		case "object":
			return tokenObject, true
		}
	}

	return tokenNull, false
}

// assertShapeMatchesToken is the oracle. It asserts that the form the generator
// assigned the field agrees with the JSON encoding/json wrote for it,
// and reports whether it had an assertion to make. A column that predicts no
// token has none; see reasonFormPredictsNoToken.
func assertShapeMatchesToken(
	t *testing.T,
	obs *shapeObservation,
	raw json.RawMessage,
	isNil bool,
	root *jsonschema.Schema,
) bool {
	t.Helper()

	got := tokenOf(t, raw)
	where := fmt.Sprintf("field %s.%s (%s, form %s)", obs.owner, obs.field.Name, obs.typ, obs.shape.Form)

	// A null token is the marshaling of a nil Go value and nothing else. Tying
	// it to the reflected value rather than waving it through is what keeps a
	// nil slice or map from excusing a wrong form.
	if got == tokenNull {
		// A null token says the Go value was nil and nothing about the form, so
		// the claim is worth making but does not count toward the assertion
		// tally the vacuity guard reads.
		assert.True(t, isNil, "%s marshaled null from a non-nil value", where)

		return false
	}

	require.False(t, isNil, "%s marshaled %s from a nil value", where, got)

	if want, ok := formToken[obs.shape.Form]; ok {
		require.Equal(t, want, got, "%s: %s predicts a %s instance", where, obs.shape.Form, want)
		assertCoercedContent(t, obs, raw, where)
		assertElementTokens(t, obs, raw, where)

		return true
	}

	// The forms with a predicted token returned above, and the default fails on
	// anything this switch does not name.
	//nolint:exhaustive // The predicted-token forms are handled by formToken above.
	switch obs.shape.Form {
	case jsonschema.FormRef:
		// The definition states the instance shape the Go kind withheld, so the
		// oracle reads it there rather than restating the base.Ref guard
		// classifyForm already applied.
		name, ok := strings.CutPrefix(obs.base.Ref, "#/$defs/")
		require.True(t, ok, "%s: expected a $defs pointer, got %q", where, obs.base.Ref)

		def, ok := root.Defs[name]
		require.True(t, ok, "%s: $defs has no entry %q", where, name)

		want, ok := declaredToken(def)
		if !ok {
			return false
		}

		assert.Equal(t, want, got, "%s: $defs/%s declares a %s instance", where, name, want)

		return true

	case jsonschema.FormRawBytes, jsonschema.FormOpaque:
		return false

	default:
		t.Fatalf("%s: unclassified form", where)

		return false
	}
}

// assertElementTokens asserts the element classifications the field's own
// contexts carry against the members encoding/json wrote inside it. It is the
// same property one level down, and it is what makes the element shapes the
// probe records load-bearing rather than merely observed.
//
// A null member is a nil element, which every form admits, and a member whose
// element form predicts no token is left to the field-level assertion.
func assertElementTokens(t *testing.T, obs *shapeObservation, raw json.RawMessage, where string) {
	t.Helper()

	if len(obs.elems) == 0 {
		return
	}

	var members []json.RawMessage

	switch obs.shape.Form {
	case jsonschema.FormArray:
		require.NoError(t, json.Unmarshal(raw, &members), "%s: decode the array", where)

	case jsonschema.FormObject:
		var values map[string]json.RawMessage

		require.NoError(t, json.Unmarshal(raw, &values), "%s: decode the object", where)
		require.Len(t, obs.elems, 1,
			"%s: an object carries one value context, so its members need no ordering", where)

		for _, v := range values {
			members = append(members, v)
		}

	default:
		return
	}

	for i, member := range members {
		// A tuple carries one context per position; a list and a map carry one
		// context for every member.
		elem := obs.elems[0]
		if len(obs.elems) > 1 {
			if i >= len(obs.elems) {
				continue
			}

			elem = obs.elems[i]
		}

		tok := tokenOf(t, member)
		if tok == tokenNull {
			continue
		}

		if want, ok := formToken[elem.Form]; ok {
			assert.Equal(t, want, tok,
				"%s: element %d classified %s predicts a %s instance", where, i, elem.Form, want)
		}
	}
}

// assertCoercedContent checks what a coerced form claims about the text inside
// the string it predicted. The claim holds for a json:",string" field, whose
// text is the value re-encoded; a type that marshals itself as text writes
// whatever it likes, so only the string token is asserted there.
func assertCoercedContent(t *testing.T, obs *shapeObservation, raw json.RawMessage, where string) {
	t.Helper()

	var content string

	switch obs.shape.Form {
	case jsonschema.FormCoercedNumber, jsonschema.FormCoercedBool, jsonschema.FormCoercedString:
		if !jsonStringOption(obs.field) {
			return
		}

		require.NoError(t, json.Unmarshal(raw, &content), "%s: decode the coerced string", where)

	case jsonschema.FormByteString:
		if obs.base == nil || obs.base.ContentEncoding != "base64" {
			return
		}

		require.NoError(t, json.Unmarshal(raw, &content), "%s: decode the byte string", where)

		_, err := base64.StdEncoding.DecodeString(content)
		require.NoError(t, err, "%s: the byte string is base64", where)

		return

	default:
		return
	}

	//nolint:exhaustive // The switch above already narrowed the form to the three coerced ones.
	switch obs.shape.Form {
	case jsonschema.FormCoercedNumber:
		// Validating the bare text as JSON applies the JSON number grammar
		// itself, which strconv would have widened to NaN, Inf, and hex floats.
		assert.True(t, json.Valid([]byte(content)) && !strings.ContainsAny(content, `"{[tfn`),
			"%s: a quoted number field emits a JSON number literal, got %q", where, content)

	case jsonschema.FormCoercedBool:
		assert.Contains(t, []string{"true", "false"}, content,
			"%s: a quoted bool field emits a bool literal", where)

	case jsonschema.FormCoercedString:
		assert.True(t, json.Valid([]byte(content)) && strings.HasPrefix(content, `"`),
			"%s: a quoted string field double-encodes, got %q", where, content)
	}
}

// The roster's component types. Each carries the marshaling behavior one past
// fix turned on, which reflection alone cannot express.
type (
	// The oracleText type marshals itself as text over a numeric kind, so its
	// schema is a string while its Go value is a number.
	oracleText int
	// The oracleByte type is a uint8 carrying MarshalText. A slice of it marshals
	// as a real JSON array rather than one base64 string, the exemption 649a6f2
	// added.
	oracleByte uint8
	// The oracleBoolWord type is a string kind whose declared schema is a boolean, the
	// promotion 679bd8b added. Its MarshalJSON is what makes the declaration
	// true of the instance.
	oracleBoolWord string
	// The oracleMarshaler type is a struct carrying MarshalJSON. Reflection deliberately
	// does not consult a direct json.Marshaler, so the field keeps its reflected
	// object schema, which is the behavior 5c04089 pinned.
	oracleMarshaler struct {
		N int `json:"n"`
	}
	// The oracleInner type is an ordinary named struct, extracted to $defs by default.
	oracleInner struct {
		A int `json:"a"`
	}
)

// MarshalText writes the level as L<n>.
func (o oracleText) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "L%d", int(o)), nil }

// MarshalText writes the byte as B<n>.
func (o oracleByte) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "B%d", int(o)), nil }

// MarshalJSON writes a JSON boolean, matching the declared schema.
func (o oracleBoolWord) MarshalJSON() ([]byte, error) { return []byte("true"), nil }

// MarshalJSON writes a JSON object, matching the reflected schema.
func (o oracleMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{"n":1}`), nil }

// formUnpinned is the model's zero form, which names no shape, so a roster row
// leaves a definition mode's expectation unstated by leaving it at the zero.
// The constant is not re-exported from the package, since no caller has a use
// for an unclassified form.
const formUnpinned = jsonschema.Form(0)

// oracleRow is one roster entry: a field type, the json tag it carries, and the
// form each definition mode must classify it as. An unset want leaves the form
// unpinned and asserts only the encoding/json agreement.
type oracleRow struct {
	typ        reflect.Type
	jsonTag    string
	opts       []jsonschema.GenerateOption
	wantDefs   jsonschema.Form
	wantNoDefs jsonschema.Form
	// The noToken flag marks a row whose form predicts no token, so the row
	// pins the classification and the oracle has nothing to check it against;
	// see reasonFormPredictsNoToken.
	noToken bool
}

// oracleRoster is the hand-written half of the oracle: every coercible kind
// crossed with pointer-ness and json:",string", the container kinds, and one
// row per past classification fix.
func oracleRoster() map[string]oracleRow {
	boolWordSchema := jsonschema.WithTypeSchema(
		reflect.TypeFor[oracleBoolWord](),
		jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "boolean"}})

	rows := map[string]oracleRow{
		"string":             {typ: reflect.TypeFor[string](), wantDefs: jsonschema.FormString},
		"pointer to string":  {typ: reflect.TypeFor[*string](), wantDefs: jsonschema.FormString},
		"int":                {typ: reflect.TypeFor[int](), wantDefs: jsonschema.FormNumber},
		"pointer to int":     {typ: reflect.TypeFor[*int](), wantDefs: jsonschema.FormNumber},
		"int8":               {typ: reflect.TypeFor[int8](), wantDefs: jsonschema.FormNumber},
		"uint16":             {typ: reflect.TypeFor[uint16](), wantDefs: jsonschema.FormNumber},
		"float64":            {typ: reflect.TypeFor[float64](), wantDefs: jsonschema.FormNumber},
		"pointer to float64": {typ: reflect.TypeFor[*float64](), wantDefs: jsonschema.FormNumber},
		"bool":               {typ: reflect.TypeFor[bool](), wantDefs: jsonschema.FormBool},
		"pointer to bool":    {typ: reflect.TypeFor[*bool](), wantDefs: jsonschema.FormBool},
		"slice of string":    {typ: reflect.TypeFor[[]string](), wantDefs: jsonschema.FormArray},
		"array of int":       {typ: reflect.TypeFor[[3]int](), wantDefs: jsonschema.FormArray},

		// The map and the interface are what reach the object and opaque
		// columns without relying on a synthesized shape drawing them.
		"map":       {typ: reflect.TypeFor[map[string]int](), wantDefs: jsonschema.FormObject},
		"interface": {typ: reflect.TypeFor[any](), wantDefs: jsonschema.FormOpaque, noToken: true},

		"byte slice": {typ: reflect.TypeFor[[]byte](), wantDefs: jsonschema.FormByteString},
		"raw message": {
			typ:      reflect.TypeFor[json.RawMessage](),
			wantDefs: jsonschema.FormRawBytes, noToken: true,
		},

		// A named struct and a time are $def'd by default and inline without
		// definitions, which is what makes both the referenced and the
		// text-marshaled columns reachable.
		"named struct": {
			typ:        reflect.TypeFor[oracleInner](),
			wantDefs:   jsonschema.FormRef,
			wantNoDefs: jsonschema.FormDeclaredObject,
		},
		"pointer to named struct": {
			typ:        reflect.TypeFor[*oracleInner](),
			wantDefs:   jsonschema.FormRef,
			wantNoDefs: jsonschema.FormDeclaredObject,
		},
		"time": {
			typ:        reflect.TypeFor[time.Time](),
			wantDefs:   jsonschema.FormRef,
			wantNoDefs: jsonschema.FormTextString,
		},

		// db3c7b5: a json:",string" field classifies at its real Go kind, so
		// its scalars compare against the text it emits.
		"quoted int": {
			typ: reflect.TypeFor[int](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedNumber,
		},
		"quoted pointer to int": {
			typ: reflect.TypeFor[*int](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedNumber,
		},
		"quoted bool": {
			typ: reflect.TypeFor[bool](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedBool,
		},
		// 77744d1: a json:",string" string field double-encodes.
		"quoted string": {
			typ: reflect.TypeFor[string](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedString,
		},
		"quoted pointer to string": {
			typ: reflect.TypeFor[*string](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedString,
		},

		// A text-marshaling numeric reaches the same coerced column without a
		// json tag option, which is the shape the coercion rule is stated for.
		"text-marshaling numeric": {
			typ: reflect.TypeFor[oracleText](), wantDefs: jsonschema.FormCoercedNumber,
		},
		"pointer to text-marshaling numeric": {
			typ: reflect.TypeFor[*oracleText](), wantDefs: jsonschema.FormCoercedNumber,
		},

		// A json.Number is the one string kind encoding/json writes as a
		// number, so a quoted one emits its literal once-quoted rather than
		// double-encoded and classifies in the numeric coercion column.
		"quoted json number": {
			typ: reflect.TypeFor[json.Number](), jsonTag: "v,string",
			wantDefs: jsonschema.FormCoercedNumber,
		},
		"json number": {
			typ: reflect.TypeFor[json.Number](), wantDefs: jsonschema.FormNumber,
		},

		// 649a6f2: a marshaler-bearing uint8 element is exempt from the byte
		// slice forms, since the slice marshals as a real JSON array.
		"slice of marshaling bytes": {
			typ: reflect.TypeFor[[]oracleByte](), wantDefs: jsonschema.FormArray,
		},

		// 679bd8b: a string Go kind under a boolean-declared schema is a boolean.
		"boolean-declared string kind": {
			typ:      reflect.TypeFor[oracleBoolWord](),
			opts:     []jsonschema.GenerateOption{boolWordSchema},
			wantDefs: jsonschema.FormBool,
		},

		// 5c04089: a json.Marshaler field keeps its reflected schema.
		"json marshaler": {
			typ:        reflect.TypeFor[oracleMarshaler](),
			wantDefs:   jsonschema.FormRef,
			wantNoDefs: jsonschema.FormDeclaredObject,
		},

		// 99ba651: an anonymous struct's inline payload declares an object over
		// a Go kind that is not a map.
		"anonymous struct": {
			typ: reflect.StructOf([]reflect.StructField{{
				Name: "B", Type: reflect.TypeFor[int](), Tag: `json:"b"`,
			}}),
			wantDefs: jsonschema.FormDeclaredObject,
		},
	}

	for name, row := range rows {
		if row.jsonTag == "" {
			row.jsonTag = "v"
			rows[name] = row
		}
	}

	return rows
}

// isNilValue reports whether v is a nil of a kind encoding/json writes as null.
func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// checkObservations marshals a value of each observation's owner and asserts
// the field's form against the token encoding/json wrote. It runs the owner
// twice, once filled so every nullable field carries a value and once at its
// zero so the null carve-out is exercised, and returns the number of fields it
// actually asserted.
func checkObservations(t *testing.T, seen []shapeObservation, root *jsonschema.Schema) int {
	t.Helper()

	checked := 0

	for i := range seen {
		obs := &seen[i]

		for _, filled := range []bool{true, false} {
			owner := reflect.New(obs.owner).Elem()
			if filled {
				fillNonZero(owner)
			}

			data, err := json.Marshal(owner.Interface())
			require.NoError(t, err, "marshal %s", obs.owner)

			var members map[string]json.RawMessage

			require.NoError(t, json.Unmarshal(data, &members), "decode %s as an object", obs.owner)

			raw, ok := members[obs.name]
			if !ok {
				// A member may be missing only because an omitempty or omitzero
				// field sat at its zero, which the filler cannot always prevent:
				// omitzero consults the type's own IsZero, and a struct whose
				// fields are all unexported (time.Time) stays zero however it is
				// filled. Missing for any other reason is a JSON name the
				// generator claimed and encoding/json did not write.
				require.True(t, jsonOmitOption(obs.field),
					"%s.%s: property %q is absent from %s", obs.owner, obs.field.Name, obs.name, data)

				continue
			}

			field := owner.FieldByName(obs.field.Name)
			require.True(t, field.IsValid(), "%s has no field %s", obs.owner, obs.field.Name)

			if assertShapeMatchesToken(t, obs, raw, isNilValue(field), root) {
				checked++
			}
		}
	}

	return checked
}

// TestTagShapeOracleRoster is the hand-written half: for every roster shape, the
// form the generator assigned the field must agree with the JSON encoding/json
// writes for a value of it.
//
// The oracle observes the classification through a probe interpreter rather
// than recomputing it from the generated schema, because four shapes do not
// survive that reconstruction: a json:",string" string field and its pointer
// (the quoted flag is invisible to ShapeOf), a pointer to a text-marshaling
// numeric, and a pointer to a $def'd type (the nullable wrapper hides the
// payload the string and $ref tests read).
func TestTagShapeOracleRoster(t *testing.T) {
	t.Parallel()

	for name, row := range oracleRoster() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for mode, defs := range map[string]bool{"with definitions": true, "without definitions": false} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()

					var seen []shapeObservation

					opts := append([]jsonschema.GenerateOption{
						shapeProbe(&seen), jsonschema.WithDefinitions(defs),
					}, row.opts...)

					doc := reflect.StructOf([]reflect.StructField{{
						Name: "V", Type: row.typ,
						Tag: reflect.StructTag(fmt.Sprintf("json:%q", row.jsonTag)),
					}})

					root, err := jsonschema.Generate(t.Context(), doc, opts...)
					require.NoError(t, err)

					// The vacuity guard: a roster row that loses its json tag
					// would otherwise go unprobed and assert nothing.
					var (
						field *shapeObservation
						count int
					)

					for i, obs := range seen {
						if obs.owner == doc {
							field = &seen[i]
							count++
						}
					}

					require.Equal(t, 1, count,
						"the roster field must be probed exactly once; an untagged row reaches no interpreter")

					want := row.wantDefs
					if !defs && row.wantNoDefs != formUnpinned {
						want = row.wantNoDefs
					}

					if want != formUnpinned {
						assert.Equal(t, want, field.shape.Form, "classified form")
					}

					checked := checkObservations(t, seen, root)
					if row.noToken {
						assert.Zero(t, checked,
							"the row declares no token, so the oracle must have nothing to assert")

						return
					}

					assert.Positive(t, checked, "the oracle asserted no field")
				})
			}
		})
	}
}

// assertEveryFieldAccountedFor requires each of typ's own fields to be either
// observed by the probe or covered by one of the named unprobed reasons, so a
// field class the probe silently misses fails instead of shrinking the oracle.
func assertEveryFieldAccountedFor(t *testing.T, typ reflect.Type, seen []shapeObservation) {
	t.Helper()

	observed := make(map[reflect.Type]map[string]bool, len(seen))
	owners := map[reflect.Type]bool{typ: true}

	for i := range seen {
		owner := seen[i].owner
		owners[owner] = true

		if observed[owner] == nil {
			observed[owner] = map[string]bool{}
		}

		observed[owner][seen[i].field.Name] = true
	}

	// Every owner the probe reached is checked, not only the root, so a field
	// class missed inside a nested or $def'd struct is caught too.
	for owner := range owners {
		if owner.Kind() != reflect.Struct {
			continue
		}

		shared := make(map[string]int, owner.NumField())
		for f := range owner.Fields() {
			shared[jsonName(f)]++
		}

		for f := range owner.Fields() {
			if observed[owner][f.Name] {
				continue
			}

			assert.NotEmpty(t, unprobedReasons(f, shared),
				"field %s.%s was neither observed nor covered by a named reason", owner, f.Name)
		}
	}
}

// TestTagShapeOracleSynthesized runs the same property over synthesized shapes,
// so the type is fuzzed as well as the value. It draws through
// [fuzzshape.Blobs] rather than a fuzz target because the property is
// exhaustive over a shape rather than a search for one counterexample; the
// roster keeps the classes reflect.StructOf cannot express.
//
// Values come from the same deterministic filler the roster uses rather than
// from fuzzfill, since the oracle's null carve-out asserts that a null token
// appears only where the Go value is nil, and that needs a pass where every
// nullable field is guaranteed to carry a value.
//
// Five field classes stay unobserved and are named rather than left implicit:
// reasonUntaggedField, reasonExcludedField, reasonUnexportedField,
// reasonComposedEmbed, and reasonShadowedName.
func TestTagShapeOracleSynthesized(t *testing.T) {
	t.Parallel()

	blobs := fuzzshape.Blobs(256)
	checked := 0

	for i, blob := range blobs {
		typ := fuzzshape.Type(blob)

		var seen []shapeObservation

		root, err := jsonschema.Generate(t.Context(), typ, shapeProbe(&seen))
		require.NoError(t, err, "generate shape %d", i)

		assertEveryFieldAccountedFor(t, typ, seen)

		checked += checkObservations(t, seen, root)
	}

	// The population must actually reach fields; a draw that stopped producing
	// them would leave the leg green and inert.
	assert.Positive(t, checked, "the synthesized leg asserted no field")
	t.Logf("checked %d field observations across %d synthesized shapes", checked, len(blobs))
}

// TestTagShapeOracleNoTokenColumns pins the split between the columns
// encoding/json can judge and the two it cannot, so a form cannot be both
// predicted by the token table and excused by the oracle's switch. The excuse
// is reasonFormPredictsNoToken; making it a guard rather than a comment is what
// keeps a later token prediction from being silently overridden.
func TestTagShapeOracleNoTokenColumns(t *testing.T) {
	t.Parallel()

	for _, form := range []jsonschema.Form{jsonschema.FormRawBytes, jsonschema.FormOpaque} {
		_, predicted := formToken[form]
		assert.False(t, predicted, "%s predicts a token, so %s no longer holds",
			form, reasonFormPredictsNoToken)
	}
}
