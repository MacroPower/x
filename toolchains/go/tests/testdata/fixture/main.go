// Command fixture is a synthetic build and test target for the go toolchain
// tests. It has no external dependencies so the toolchain can be exercised
// fully offline.
package main

import "os"

func main() {
	if Greeting() == "" {
		os.Exit(1)
	}
}
