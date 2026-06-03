// Package alpha provides a named type used by the jsonschema package's
// cross-package name-disambiguation and comment-extraction tests. It exists as
// a real (non-test) source package so that go doc-comment extraction can load
// it via go/packages.
package alpha

// Box is a documented generic type, exercising doc-comment extraction for an
// instantiated generic whose reflect name carries a type-argument list.
type Box[T any] struct {
	// Item documents the boxed value.
	Item T `json:"item"`
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
