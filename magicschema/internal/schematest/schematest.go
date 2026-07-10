// Package schematest holds the golden-file assertion the package's test
// suites share, so golden semantics -- indentation, the trailing newline,
// semantic JSON comparison, and the -update flag -- cannot drift between the
// core package and the four helm dialect suites.
package schematest

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// update rewrites golden files instead of comparing against them. Each test
// binary that imports this package registers the flag exactly once, so
// "go test ./... -update" regenerates every suite's goldens.
var update = flag.Bool("update", false, "update golden files")

// AssertGolden compares the JSON-marshaled schema against a golden file.
// When -update is set, it writes the golden file instead. Comparison is
// semantic (JSON equality) to tolerate formatter differences.
func AssertGolden(t *testing.T, goldenPath string, schema *jsonschema.Schema) {
	t.Helper()

	got, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(t, err)

	got = append(got, '\n')

	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644)) //nolint:gosec // committed test data, not a secret

		return
	}

	//nolint:gosec // G304: reading caller-provided golden paths is the helper's purpose.
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "golden file %s not found; run with -update to create", goldenPath)

	assert.JSONEq(t, string(want), string(got))
}
