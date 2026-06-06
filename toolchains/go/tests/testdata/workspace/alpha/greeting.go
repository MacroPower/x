package main

// Greeting returns a fixed greeting used to exercise the build and test
// stages of the go toolchain in workspace mode.
func Greeting() string {
	return "hello"
}
