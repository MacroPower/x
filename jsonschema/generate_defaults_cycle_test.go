package jsonschema_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// cyclicDefaultsPtr is a single-step pointer cycle: its Elem is itself, so
// dereferencing never leaves the pointer kind.
type cyclicDefaultsPtr *cyclicDefaultsPtr

// mutualDefaultsA and mutualDefaultsB form a two-step pointer cycle.
type (
	mutualDefaultsA *mutualDefaultsB
	mutualDefaultsB *mutualDefaultsA
)

// cyclicDefaultsRoot is the generated root type; only the defaults instance
// is cyclic.
type cyclicDefaultsRoot struct {
	Name string `json:"name"`
}

func TestWithDefaultsFromCyclicPointerInstance(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		instance any
	}{
		"self-referential pointer":    {instance: cyclicDefaultsPtr(nil)},
		"mutually recursive pointers": {instance: mutualDefaultsA(nil)},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A typed nil makes the interface non-nil, so the defaults path
			// runs. The cyclic pointer chain can never dereference to the
			// root struct type, so the contract is an
			// ErrInvalidDefaultsInstance error; a regression here hangs
			// instead, hence the timeout guard.
			done := make(chan error, 1)

			go func() {
				_, err := jsonschema.GenerateFor[cyclicDefaultsRoot](
					t.Context(),
					jsonschema.WithDefaultsFrom(test.instance),
				)
				done <- err
			}()

			select {
			case err := <-done:
				require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance)
			case <-time.After(5 * time.Second):
				t.Fatal("Generate did not return within 5s; pointer dereference is cycling")
			}
		})
	}
}
