package schemaclone_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	upstream "go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// corpusRoots are the vendored schema corpora: the official test suite (its
// keyword files, their optional/ subdirectories, and the remotes served to $ref
// tests) and the official metaschemas. Between them they exercise every keyword
// and every sub-schema shape the package supports, which is what makes them the
// fidelity oracle a hand-written table cannot be.
var corpusRoots = []string{
	filepath.Join("..", "..", "testdata", "suite"),
	filepath.Join("..", "..", "testdata", "metaschemas"),
}

// TestCloneCorpusFidelity asserts the load-bearing property over every vendored
// schema. The copy is value-equal to its source and shares no container with
// it. Marshaled-byte equality follows from value equality and rides along as a
// cheap cross-check.
func TestCloneCorpusFidelity(t *testing.T) {
	t.Parallel()

	files := corpusFiles(t)
	require.NotEmpty(t, files, "the vendored corpora must be reachable from this package")

	for _, path := range files {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			t.Parallel()

			for i, src := range schemasIn(t, path) {
				cp := schemaclone.Clone(src)

				assert.True(t, reflect.DeepEqual(src, cp), "schema %d: the copy is value-equal to its source", i)
				assertDisjoint(t, i, src, cp)

				before, err := json.Marshal(src)
				require.NoError(t, err)

				after, err := json.Marshal(cp)
				require.NoError(t, err)
				assert.True(t, bytes.Equal(before, after), "schema %d: the copy marshals to the same bytes", i)
			}
		})
	}
}

// TestHasCycleOverCorpus asserts that no vendored schema reads as cyclic. Every
// schema in the corpora arrives through json.Unmarshal, and parsed JSON holds
// no pointer alias or cycle, so no vendored schema can hold a genuine cycle.
// The test guards the reverse direction. [schemaclone.HasCycle] never
// over-reports on a real document, so the check turns away nothing the
// validation walk or the inliner is asked to accept.
func TestHasCycleOverCorpus(t *testing.T) {
	t.Parallel()

	files := corpusFiles(t)
	require.NotEmpty(t, files, "the vendored corpora must be reachable from this package")

	for _, path := range files {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			t.Parallel()

			for i, src := range schemasIn(t, path) {
				assert.False(t, schemaclone.HasCycle(src), "schema %d must read as acyclic", i)
			}
		})
	}
}

// corpusFiles lists every JSON file under the vendored corpora.
func corpusFiles(t *testing.T) []string {
	t.Helper()

	var files []string

	for _, root := range corpusRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if !d.IsDir() && filepath.Ext(path) == ".json" {
				files = append(files, path)
			}

			return nil
		})
		require.NoError(t, err)
	}

	return files
}

// schemasIn reads the schemas out of one corpus file. A suite keyword file is an
// array of test groups, each carrying one schema; a remote fixture and a
// metaschema are each a bare schema document.
func schemasIn(t *testing.T, path string) []*jsonschema.Schema {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		var groups []struct {
			Schema *jsonschema.Schema `json:"schema"`
		}

		require.NoError(t, json.Unmarshal(data, &groups), "reading %s", path)

		schemas := make([]*jsonschema.Schema, 0, len(groups))
		for _, group := range groups {
			if group.Schema != nil {
				schemas = append(schemas, group.Schema)
			}
		}

		return schemas
	}

	var schema *jsonschema.Schema

	require.NoError(t, json.Unmarshal(data, &schema), "reading %s", path)

	if schema == nil {
		return nil
	}

	return []*jsonschema.Schema{schema}
}

// assertDisjoint asserts that the copy reuses none of the source's nodes or
// containers. Node identity comes from the package's own Schemas iterator, which
// dedups pointers and so terminates on any graph; container identity comes from
// the backing storage of each node's non-empty slice and map fields. It leaves
// empty containers out, because Go serves every zero-size allocation from one
// address, so two distinct empty slices would read as shared.
func assertDisjoint(t *testing.T, index int, src, cp *jsonschema.Schema) {
	t.Helper()

	nodes := map[*jsonschema.Schema]bool{}
	containers := map[uintptr]bool{}

	for _, node := range upstream.Schemas(src) {
		nodes[node] = true

		for _, ptr := range containerPointers(node) {
			containers[ptr] = true
		}
	}

	for _, node := range upstream.Schemas(cp) {
		assert.False(t, nodes[node], "schema %d: the copy reuses a source node", index)

		for _, ptr := range containerPointers(node) {
			assert.False(t, containers[ptr], "schema %d: the copy reuses a source container", index)
		}
	}
}

// containerPointers returns the backing storage of every non-empty slice and
// map one node owns, descending through the any-typed value fields so a
// container nested inside Extra, Enum, Examples, or Const is covered too. The
// numeric bound pointers stay out deliberately, since they address immutable
// scalars and stay shared by design.
func containerPointers(s *jsonschema.Schema) []uintptr {
	var ptrs []uintptr

	for _, field := range reflect.ValueOf(s).Elem().Fields() {
		switch field.Kind() {
		case reflect.Slice, reflect.Map:
			if field.Len() > 0 {
				ptrs = append(ptrs, field.Pointer())
			}

		default:
		}
	}

	for _, value := range []any{s.Extra, s.Enum, s.Examples} {
		ptrs = valuePointers(value, ptrs)
	}

	if s.Const != nil {
		ptrs = valuePointers(*s.Const, ptrs)
	}

	return ptrs
}

// valuePointers appends the backing storage of every non-empty JSON container
// reachable from v. A schema nested in a value field is left to the node walk,
// which reaches it through the same iterator.
func valuePointers(v any, ptrs []uintptr) []uintptr {
	switch value := v.(type) {
	case map[string]any:
		if len(value) > 0 {
			ptrs = append(ptrs, reflect.ValueOf(value).Pointer())
		}

		for _, elem := range value {
			ptrs = valuePointers(elem, ptrs)
		}

	case []any:
		if len(value) > 0 {
			ptrs = append(ptrs, reflect.ValueOf(value).Pointer())
		}

		for _, elem := range value {
			ptrs = valuePointers(elem, ptrs)
		}

	default:
	}

	return ptrs
}
