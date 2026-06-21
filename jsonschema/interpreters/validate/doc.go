// Package validate provides a [jsonschema.TagInterpreter] that maps
// go-playground/validator/v10 struct tag syntax to JSON Schema constraints.
//
// It does not import or depend on the validator library. It is a pure
// tag-syntax-to-schema mapper that adopts the validator tag naming convention
// for ecosystem consistency, so users who already annotate structs with
// validate tags get schema generation for free.
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
//   - required: adds the field to the parent's "required" array. For non-pointer
//     fields it also adds a type-specific non-zero constraint: minLength: 1 for
//     strings, minItems: 1 for slices/arrays, minProperties: 1 for maps,
//     const: true for bools, and a not forbidding 0 for numbers. Pointer fields
//     only get the required constraint, not the type-specific non-zero check.
//     In go-playground/validator, required on a pointer means "must be
//     non-nil", so the pointed-to value may be zero. The required tag adds the
//     field to the parent's required array even when json:",omitempty" or
//     json:",omitzero" would normally exclude it.
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
// Numeric bounds intersect with the bounds derived from the field's Go type:
// a tag bound wider than the type's range clamps to the type limit (int8 with
// max=200 emits maximum: 127), matching the jsonschema tag's bound handling.
// Scalar values (eq, ne, oneof, and len on a numeric field) are instead
// range-checked against the field's Go type, and a value the type cannot hold
// is an error, mirroring the jsonschema tag's const/enum behavior.
//
// A json:",string" numeric or bool field serializes its value as a quoted
// string, so the generated schema has type string. Scalar value rules (eq, ne,
// oneof, len, and required's non-zero check) compare against that serialized
// form. Numeric bounds (min, max, gt, lt, gte, lte) have no faithful mapping
// onto the serialized string -- minimum and friends constrain JSON numbers, not
// the quoted instance -- so they are rejected with an error rather than silently
// dropped as an inert numeric keyword on a string schema.
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
//   - unique: uniqueItems: true
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
package validate
