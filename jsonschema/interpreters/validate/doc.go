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
//	schema, err := jsonschema.GenerateFor[MyType](
//	    jsonschema.WithTagInterpreter(validate.NewInterpreter()),
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
//     strings also sets minLength: 1; for slices/arrays, minItems: 1; for maps,
//     minProperties: 1. Pointer fields only get the required constraint (not the
//     type-specific minimum), since in go-playground/validator, required on a
//     pointer means "must be non-nil" — the pointed-to value may be zero.
//     The required tag adds the field to the parent's required array even when
//     json:",omitempty" or json:",omitzero" would normally exclude it.
//
// String constraints:
//
//   - min=N / gte=N: minLength
//   - max=N / lte=N: maxLength
//   - len=N: minLength and maxLength
//   - gt=N: minLength: N+1
//   - lt=N: maxLength: N-1
//   - oneof=a b c: enum (space-separated values)
//   - eq=val: const
//   - ne=val: not.const
//
// Numeric constraints:
//
//   - min=N / gte=N: minimum
//   - max=N / lte=N: maximum
//   - gt=N: exclusiveMinimum
//   - lt=N: exclusiveMaximum
//   - oneof=1 2 3: enum (space-separated, parsed as numbers)
//   - eq=N: const
//   - ne=N: not.const
//
// Array/slice constraints:
//
//   - min=N / gte=N: minItems
//   - max=N / lte=N: maxItems
//   - len=N: minItems and maxItems
//   - gt=N: minItems: N+1
//   - lt=N: maxItems: N-1
//   - unique: uniqueItems: true
//
// Map constraints:
//
//   - min=N / gte=N: minProperties
//   - max=N / lte=N: maxProperties
//   - len=N: minProperties and maxProperties
//   - gt=N: minProperties: N+1
//   - lt=N: maxProperties: N-1
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
// This produces minItems: 1 on the outer slice, maxItems: 5 on the inner slice's
// items schema, and minLength: 3 on the string element schema.
//
// For maps, dive descends into the additionalProperties sub-schema (the value
// type). When dive descends through a pointer element type (e.g., []*int),
// constraints after dive apply to the underlying type's schema.
//
// # Unsupported Tags
//
// Cross-field validators (eqfield, required_if, etc.), map key validators
// (keys, endkeys), and the | OR operator are silently ignored or discarded.
// Only the first group before | is used.
package validate
