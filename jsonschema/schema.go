package jsonschema

import "github.com/google/jsonschema-go/jsonschema"

// Schema is an alias for the upstream [jsonschema.Schema] type, so callers can
// reference it without importing google/jsonschema-go directly.
type Schema = jsonschema.Schema

// Ptr returns a pointer to a new variable whose value is x.
func Ptr[T any](x T) *T { return &x }
