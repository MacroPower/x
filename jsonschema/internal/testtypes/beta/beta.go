// Package beta provides a type whose name collides with alpha.Widget, used by
// the jsonschema package's cross-package name-disambiguation test.
package beta

// Widget is a test type whose name intentionally collides with alpha.Widget.
type Widget struct {
	// Color is an arbitrary field so the colliding Widget has content.
	Color string `json:"color"`
}

// Knot is a test type whose name intentionally collides with alpha.Knot, for
// the reachability test that pins provisional ref-token resolution under a
// base-name collision.
type Knot struct {
	// N is an arbitrary field so the colliding Knot has content.
	N int `json:"n"`
}
