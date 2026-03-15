// Package beta provides a type whose name collides with alpha.Widget, used by
// the jsonschema package's cross-package name-disambiguation test.
package beta

// Widget is a test type whose name intentionally collides with alpha.Widget.
type Widget struct {
	Color string `json:"color"`
}
