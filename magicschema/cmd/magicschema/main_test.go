package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
)

// TestGenerateRejectsRepeatedStdin verifies that passing "-" more than once
// is rejected before any stdin read, rather than silently reading the
// drained stream as an empty, permit-everything input.
func TestGenerateRejectsRepeatedStdin(t *testing.T) {
	t.Parallel()

	_, err := generate(magicschema.NewGenerator(), []string{"-", "-"})
	require.ErrorIs(t, err, magicschema.ErrReadInput)
}
