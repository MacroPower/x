// Package nested is a second module in the fixture so multi-module
// discovery, lint, and tidy are exercised by the toolchain tests.
package nested

// N returns a constant. It exists so the nested module has linted, tidy code.
func N() int {
	return 1
}
