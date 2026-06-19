package jsonschema

import "go.jacobcolvin.com/x/jsonschema/internal/normalize"

// Normalize converts a Go value into the JSON-compatible shape the validator
// works with, so instances decoded from non-encoding/json sources (YAML, TOML,
// a hand-built map[string]any) validate without a manual conversion pass:
//
//   - Signed and unsigned integer values become [encoding/json.Number],
//     preserving the exact value at any magnitude (a float64 conversion would
//     round above 2^53).
//   - float32 widens to float64.
//   - map[string]any and []any are normalized recursively.
//   - nil, bool, string, float64 (including NaN and the infinities, which JSON
//     marshaling would reject), and [encoding/json.Number] pass through
//     unchanged.
//
// Anything else (structs, named numeric types, map[any]any) also passes
// through unchanged and is rejected by [Validate]'s instance-type check.
//
// Containers are copied only when normalization changes something inside them;
// an already JSON-shaped value is returned as-is, and the input is never
// mutated. [Validator.Validate] and [Validate] normalize their instance
// automatically; Normalize is exported so a caller can pre-normalize a value
// once and reuse it across many validations.
//
// A self-referential instance (a map or slice that contains itself) is not
// descended past the cycle, so Normalize terminates instead of overflowing the
// stack. Validating such an instance against a recursive schema may still
// recurse without bound, so callers building cyclic instances by hand should
// avoid recursive schemas.
func Normalize(instance any) any { return normalize.Value(instance) }
