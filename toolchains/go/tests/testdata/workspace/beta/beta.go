// Package beta is the second workspace module so the "./..." expansion
// resolves more than one use directive.
package beta

// B returns a constant. It exists so the beta module has linted, tidy code.
func B() int {
	return 2
}
