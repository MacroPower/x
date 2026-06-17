// Package alpha provides a named type used by the jsonschema package's
// cross-package name-disambiguation and comment-extraction tests. It exists as
// a real (non-test) source package so that go doc-comment extraction can load
// it via go/packages.
package alpha

import (
	"context"

	"go.jacobcolvin.com/x/jsonschema"
)

// Box is a documented generic type, exercising doc-comment extraction for an
// instantiated generic whose reflect name carries a type-argument list.
type Box[T any] struct {
	// Item documents the boxed value.
	Item T `json:"item"`
}

// ProviderSingleton documents a provider whose JSONSchema returns a shared,
// package-level schema pointer. It lives in this real source package so doc
// comments are loadable, letting a test confirm comment extraction does not
// mutate the singleton in place.
type ProviderSingleton struct{}

// SharedProviderSchema is the package-level singleton returned by every
// ProviderSingleton.JSONSchema call. It carries no Description so a test can
// observe whether comment extraction mutates it.
var SharedProviderSchema = jsonschema.Schema{Type: "string"}

// JSONSchema returns the shared singleton schema.
func (ProviderSingleton) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &SharedProviderSchema, nil
}

// Stamp is a documented named scalar embedded by [Envelope].
type Stamp string

// Envelope embeds a named type under an explicit JSON name, so the embedded
// field becomes a single named property instead of being promoted. Its doc
// comment must still be extracted for that property even though the field has
// no name identifier in the source.
type Envelope struct {
	// Stamp documents the envelope stamp.
	Stamp `json:"stamp"`
}

// Widget is a test type with documented fields.
type Widget struct {
	// Label documents the widget label. A jsonschema tag also sets a
	// description, which must override this comment.
	Label string `json:"label" jsonschema:"description=tag wins over comment"`

	// Size documents the widget size and carries no jsonschema description,
	// so the extracted comment is used.
	Size int `json:"size"`
}
