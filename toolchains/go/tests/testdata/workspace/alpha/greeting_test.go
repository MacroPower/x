package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting(); got != "hello" {
		t.Fatalf("Greeting() = %q, want %q", got, "hello")
	}
}

// TestIntegrationGreeting is named so the toolchain's -run/-skip "Integration"
// selection (TestIntegration vs TestUnit) is exercised in workspace mode.
func TestIntegrationGreeting(t *testing.T) {
	if Greeting() == "" {
		t.Fatal("Greeting() is empty")
	}
}
