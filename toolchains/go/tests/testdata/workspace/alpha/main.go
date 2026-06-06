// Command alpha is a synthetic build and test target for the go toolchain's
// workspace tests. Together with go.work at the workspace root (which has no
// go.mod of its own), it exercises the "./..." expansion into per-module
// package patterns.
package main

import "os"

func main() {
	if Greeting() == "" {
		os.Exit(1)
	}
}
