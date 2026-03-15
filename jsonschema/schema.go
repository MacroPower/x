package jsonschema

import "github.com/google/jsonschema-go/jsonschema"

// Schema is an alias for the upstream [jsonschema.Schema] type.
// Users need only import this package to access the Schema type and all
// generation functions.
type Schema = jsonschema.Schema

// Ptr returns a pointer to a new variable whose value is x.
func Ptr[T any](x T) *T { return &x }
