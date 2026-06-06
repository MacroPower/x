package beta

import "testing"

func TestB(t *testing.T) {
	if B() != 2 {
		t.Fatalf("B() = %d, want 2", B())
	}
}
