package jsonschema

import (
	"context"
	"reflect"
)

// JSONSchemaProvider allows a type to provide its own schema, bypassing
// automatic generation entirely. When a type implements JSONSchemaProvider,
// the returned schema replaces reflection-based generation for that type.
// If JSONSchema returns nil, the type is treated as unrestricted ({}).
type JSONSchemaProvider interface {
	JSONSchema() *Schema
}

// JSONSchemaExtender allows a type to modify its auto-generated schema.
// The method is called after the schema has been generated via reflection,
// allowing the type to add, remove, or modify any fields.
type JSONSchemaExtender interface {
	JSONSchemaExtend(schema *Schema)
}

// TagInterpreter translates struct field tags into JSON Schema constraints.
type TagInterpreter interface {
	// TagKey returns the struct tag key this interpreter reads (e.g., "validate").
	TagKey() string

	// Interpret reads the tag value and the field context, then modifies
	// the schema in place. It is called during field-level processing, after
	// the type schema, comments, and jsonschema struct tag have been applied.
	// The FieldContext provides access to both the field's own schema and the
	// parent object schema, enabling constraints like "required" that modify
	// the parent.
	Interpret(tag string, field FieldContext) error
}

// RefResolver resolves remote schema URIs during validation. The resolver
// is called only when local resolution fails to find a target. Successfully
// resolved schemas are cached within the validation run, so the resolver is
// invoked at most once per URI that resolves; a URI for which the resolver
// returns nil or an error is not cached and may be queried again for each ref
// that targets it. Implementations must be safe for concurrent use if passed
// to multiple Validate calls.
type RefResolver interface {
	ResolveRef(uri string) (*Schema, error)
}

// RefResolverContext is an optional extension of [RefResolver]. When the
// resolver passed to [WithRefResolver] also implements RefResolverContext,
// resolution calls ResolveRefContext with the context from [CompileContext]
// or [Validator.ValidateContext]; context-less entry points pass
// [context.Background]. The caching and concurrency contract of [RefResolver]
// applies unchanged.
type RefResolverContext interface {
	RefResolver

	// ResolveRefContext resolves a remote schema URI under the caller's
	// context, so a resolver that fetches over the network can honor
	// cancellation and deadlines.
	ResolveRefContext(ctx context.Context, uri string) (*Schema, error)
}

// FieldContext provides context about a struct field to tag interpreters.
type FieldContext struct {
	// Type is the Go reflect.Type of the field.
	Type reflect.Type
	// Schema is the field's own generated schema, modified in place by the interpreter.
	Schema *Schema
	// Parent is the enclosing object schema, so an interpreter can append to its Required list.
	Parent *Schema
	// Name is the JSON property name for the field.
	Name string
}
