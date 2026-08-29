// Package validate provides a [jsonschema.TagInterpreter] that maps
// go-playground/validator/v10 struct tag syntax to JSON Schema constraints.
//
// It does not import or depend on the validator library. It is a pure
// tag-syntax-to-schema mapper that adopts the validator tag naming convention
// for ecosystem consistency, so users who already annotate structs with
// validate tags get schema generation for free.
//
// The interpreter declares each constraint as a fact on the field's authored
// canvas ([jsonschema.FieldContext.Canvas]) rather than mutating a merged
// schema, and generation composes those facts with the field's type-derived
// schema. On a nilable field that composition splits across the null encoding.
// A value constraint such as eq or oneof lands on the value branch of the
// anyOf[value, null] wrapper, so the permitted null stays valid. A forbidden
// value (ne) and length or numeric bounds move to the wrapper itself. A nilable
// slice, map, or []byte carrying no const or enum takes the ["null", base] type
// list instead, where every keyword is a plain sibling of that list. Element
// constraints (dive, and oneof on a sequence)
// reach the element schemas through the field's element contexts.
//
// # Usage
//
// Register the interpreter when generating a schema:
//
//	schema, err := jsonschema.GenerateFor[MyType](ctx,
//	    jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
//	)
//
// # Supported Tags
//
// Tags are type-aware: the same tag (e.g., min) maps to different JSON Schema
// keywords depending on the Go field type.
//
// Presence:
//
//   - required: adds the field to the parent's "required" array, even where
//     json:",omitempty" or json:",omitzero" would normally exclude it. A
//     property whose value is null satisfies that entry on its own, so wherever
//     the field's shape has a non-zero form the interpreter also forbids null:
//     a string, number, bool, slice, map, or []byte, as a pointer or bare. A
//     pointer is nilable, and so are a bare slice, map, and []byte, each of
//     which carries a null branch of its own. Encoding/json writes null for a
//     nil value, which go-playground's required rejects. Where the occurrence
//     admits no null, as under
//     WithNullable(false) or a type schema declaring NullForbidden, the
//     interpreter writes no forbidden null and the type rejects a null instance
//     on its own.
//
//     A non-pointer field also gets a type-specific non-zero constraint:
//     minLength: 1 for strings, minItems: 1 for slices/arrays,
//     minProperties: 1 for maps, const: true for bools, and a not forbidding 0
//     for numbers. On a bare container that constraint measures a size, which a
//     null instance does not carry, so the field needs the forbidden null
//     beside it. A pointer field gets the forbidden null and no such
//     constraint, since go-playground reads required on a pointer as "must be
//     non-nil" and says nothing about the pointed-to value, which may be zero.
//
//     A shape with no non-zero form the schema can express gets the required
//     entry and nothing else, not even the forbidden null: a struct, a
//     text-marshaling type, a referenced definition, an opaque value such as
//     an interface, and a raw JSON value. A byte slice falls on one side or
//     the other, depending on its schema. A []byte encodes as a base64 string
//     and gets minLength: 1 on that string, while a byte-slice type whose
//     schema is not a string (json.RawMessage) gets neither the floor nor the
//     forbidden null, since a RawMessage holding the literal null is a non-nil
//     value go-playground accepts.
//
//     A dive carries the whole rule onto the element schemas, so
//     dive,required on a [][]string forbids null and floors the size on each
//     inner slice.
//
// String constraints:
//
//   - min=N / gte=N: minLength
//   - max=N / lte=N: maxLength
//   - len=N: minLength and maxLength
//   - gt=N: minLength: N+1
//   - lt=N: maxLength: N-1
//   - oneof=a b c: enum (space-separated values; single-quoted runs group
//     multi-word values, e.g. oneof='New York' Boston)
//   - eq=val: const
//   - ne=val: forbids the value via not (not.const for a single value, composed
//     into not.enum or allOf when several values are forbidden, e.g. required+ne)
//
// Numeric constraints:
//
//   - min=N / gte=N: minimum
//   - max=N / lte=N: maximum
//   - gt=N: exclusiveMinimum
//   - lt=N: exclusiveMaximum
//   - oneof=1 2 3: enum (space-separated, parsed as numbers)
//   - eq=N: const
//   - ne=N: forbids the value via not (not.const for a single value, composed
//     into not.enum or allOf when several values are forbidden, e.g. required+ne)
//   - len=N: const (value equals N)
//
// Numeric, length, and count bounds are contributed through the field's shared
// Constraints facade, so this interpreter applies the one 2^53
// exact-representability policy and the one intersection the jsonschema tag and
// the Go kind also merge through, rather than a private bound path.
//
// Numeric bounds intersect with the bounds derived from the field's Go type:
// a tag bound wider than the type's range clamps to the type limit (int8 with
// max=200 emits maximum: 127), matching the jsonschema tag's bound handling.
// Scalar values (eq, ne, oneof, and len on a numeric field) are instead
// range-checked against the field's Go type, and a value the type cannot hold
// is an error, mirroring the jsonschema tag's const/enum behavior.
//
// Some fields serialize a scalar Go value as a quoted string, so the generated
// schema has type string: a json:",string" numeric or bool field, and equally a
// numeric or bool type that marshals itself as text. The rule is stated in terms
// of that *shape* rather than of the json tag, so both are covered by one
// behavior. Scalar value rules (eq, ne, oneof, len, and required's non-zero
// check) compare against the serialized form: each value is parsed against the
// field's Go type (keeping the range check above), converted back to that type,
// and marshaled, so a non-canonical spelling such as eq=5.0 or eq=1e2 constrains
// the canonical text ("5", "100") the field actually emits, and a
// string-marshaling type constrains whatever text it writes rather than the
// number's own spelling.
//
// Numeric bounds (min, max, gt, lt, gte, lte) have no faithful mapping onto that
// serialized string -- minimum and friends constrain JSON numbers, not the
// quoted instance -- so they are rejected with an error rather than silently
// dropped as an inert numeric keyword on a string schema.
//
// A json:",string" string field double-encodes (the value abc marshals as the
// JSON string "\"abc\""), so its scalar rules (eq, ne, oneof, and required's
// non-zero check) compare against that quoted text, while the rules that would
// measure or match the unquoted value -- the length bounds and the string
// keywords -- are rejected with an error for the same reason numeric bounds
// are above.
//
// An [encoding/json.Number] is the one Go string kind exempt from that rule.
// [encoding/json] writes it as the number it holds, so a quoted one emits its
// literal once-quoted and follows the coerced-numeric rules above instead.
//
// Length and size bounds (minLength/maxLength, minItems/maxItems,
// minProperties/maxProperties) from several rules in one tag intersect
// independently of order: a floor only rises and a ceiling only falls, and len=N
// pins both to N. A len incompatible with a min/max or required therefore yields
// an unsatisfiable range rather than overriding the other bound. A ceiling rule
// that resolves below zero (lt<=0, or a negative max/lte) likewise yields an
// unsatisfiable range, since go-playground rejects every value of such a field
// including the empty one, rather than clamping to a permissive maxLength: 0.
//
// Boolean constraints:
//
//   - eq=true / eq=false: const
//   - ne=true / ne=false: not (forbids the value)
//   - oneof=true false: enum
//
// Combining required with eq=false on a bool is an error
// ([ErrConflictingConstraints]): required on a bool pins the value to true,
// which contradicts a const of false.
//
// Array/slice constraints:
//
//   - min=N / gte=N: minItems
//   - max=N / lte=N: maxItems
//   - len=N: minItems and maxItems
//   - gt=N: minItems: N+1
//   - lt=N: maxItems: N-1
//   - eq=N: minItems and maxItems (length equals N)
//   - ne=N: not (forbids length N)
//   - unique: uniqueItems: true. The unique=<field> form asserts uniqueness of
//     one named field across struct elements, which uniqueItems (whole-element
//     comparison) cannot express, so it is an error rather than being silently
//     weakened. On a shape with no array to constrain -- a string, number,
//     bool, or struct -- unique is likewise an error; a map is the one
//     exception, documented under the map constraints below.
//   - oneof=a b c: enum on the item schemas, parsed against the element type
//     (each element must be one of the values; [][]T descends to the innermost
//     element schema). A []byte field has no item schema (it encodes as a
//     base64 string), so oneof on it is an error.
//
// Map constraints:
//
//   - min=N / gte=N: minProperties
//   - max=N / lte=N: maxProperties
//   - len=N: minProperties and maxProperties
//   - gt=N: minProperties: N+1
//   - lt=N: maxProperties: N-1
//   - eq=N: minProperties and maxProperties (entry count equals N)
//   - ne=N: not (forbids entry count N)
//   - unique: a documented no-op. Unlike the shapes where unique is rejected,
//     go-playground's unique-on-map does mean something -- the map's values must
//     be distinct -- but JSON Schema has no object-side counterpart to
//     uniqueItems, so there is nothing faithful to emit.
//
// Format tags (mapped to "format"):
//
//   - email, url (-> "uri"), uri (-> "uri-reference"), uuid, ipv4, ipv6, hostname
//
// Pattern tags (mapped to "pattern"):
//
//   - alpha: ^[a-zA-Z]+$
//   - alphanum: ^[a-zA-Z0-9]+$
//   - numeric: ^[-+]?[0-9]+(?:\.[0-9]+)?$
//   - number: ^[0-9]+$
//   - ascii: ^[\x00-\x7F]*$
//
// Content tags:
//
//   - json (-> contentMediaType: "application/json")
//   - base64 (-> contentEncoding: "base64")
//
// On a raw JSON field (json.RawMessage) both are documented no-ops, like
// unique on a map: the rules are real runtime checks over the raw bytes, but
// the content keywords describe a string carrying an encoded document, and a
// raw field's instance is whatever JSON value it holds, already decoded, so
// there is nothing faithful to emit.
//
// # Dive
//
// The dive tag descends into the element type of a slice, array, or map,
// applying subsequent constraints to the items or additionalProperties
// sub-schema. Multiple dive tags can be chained for nested containers:
//
//	Tags [][]string `validate:"min=1,dive,max=5,dive,min=3"`
//
// This produces minItems: 1 on the outer slice, maxItems: 5 on the inner slice
// schema (the outer slice's items), and minLength: 3 on the string element
// schema.
//
// For maps, dive descends into the additionalProperties sub-schema (the value
// type). When dive descends through a pointer element type (e.g., []*int),
// constraints after dive apply to the underlying type's schema.
//
// Descending is all dive does. A constraint written after it runs against the
// element through the same path a sequence-wide rule takes, so on a slice or
// array `oneof=a b` and `dive,oneof=a b` produce identical schemas, and each
// element's own shape (including a string-coerced or text-marshaling one)
// decides how the constraint applies. Two divergences from that are deliberate:
// on a map, dive descends to the values while a bare oneof is an error, because
// dive says "descend" explicitly and a bare oneof on a map has no go-playground
// element meaning; and on a []byte both forms are an error, because the field
// encodes as a single base64 string with no element schema either one could
// reach.
//
// # Skipped and Unrecognized Tags
//
// Some tags carry no JSON Schema representation and are skipped: cross-field and
// conditional validators (eqfield, required_if, skip_unless, ...), control tags
// that govern when validation runs (omitempty, structonly, ...), and the
// constraints inside a keys...endkeys block (map-key constraints are not
// modeled). The | OR operator is not modeled either: within a single comma
// group the pipe separates OR alternatives, of which only the first is
// interpreted, so later comma-separated constraints still apply.
//
// Any other key that is not a recognized constraint causes Interpret to return
// an error rather than being silently consumed, so a typo'd or unsupported
// validator surfaces at generation time instead of yielding a schema that
// quietly drops the intended constraint.
//
// # Implementation
//
// This package owns this dialect's grammar and nothing more: splitting the tag,
// the OR and escape handling, the dive and keys blocks, the skipped tags, and a
// table naming the operation each validator spells. What an operation does to a
// given field is the shared constraint model's, which the jsonschema struct tag
// runs through as well, so the two dialects cannot drift on a rule they both
// express. Every constraint is contributed through [jsonschema.Constraints];
// this package holds no scalar parser and writes no schema keyword directly,
// which is what keeps the two interpretations one.
package validate
