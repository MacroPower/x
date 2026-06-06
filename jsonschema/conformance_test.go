package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// metaSchemaResolver resolves the official meta-schema and its vocabulary
// sub-schemas by their $id, so that the meta-schema's internal $ref and
// $dynamicRef targets resolve during validation.
type metaSchemaResolver map[string]*jsonschema.Schema

func (m metaSchemaResolver) ResolveRef(uri string) (*jsonschema.Schema, error) {
	if s, ok := m[uri]; ok {
		return s, nil
	}

	// Some $id values carry an empty fragment (e.g. the Draft-07 meta-schema id
	// ends in "#").
	if s, ok := m[uri+"#"]; ok {
		return s, nil
	}

	//nolint:nilnil // A miss returns (nil, nil): the validator treats it as "unresolved".
	return nil, nil
}

// loadMetaSchemas reads every vendored meta-schema under testdata/metaschemas,
// indexes them by $id, and returns the index plus the validate options needed to
// validate a document against them (a resolver for the sub-schema refs and the
// $vocabulary registrations).
func loadMetaSchemas(t *testing.T) (map[string]*jsonschema.Schema, []jsonschema.ValidateOption) {
	t.Helper()

	byID := metaSchemaResolver{}

	var opts []jsonschema.ValidateOption

	walk := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}

		var s jsonschema.Schema

		unmarshalErr := json.Unmarshal(data, &s)
		if unmarshalErr != nil {
			return fmt.Errorf("parse %s: %w", path, unmarshalErr)
		}

		if s.ID != "" {
			byID[s.ID] = &s
		}

		if len(s.Vocabulary) > 0 {
			opts = append(opts, jsonschema.WithMetaSchema(&s))
		}

		return nil
	}

	require.NoError(t, filepath.Walk("testdata/metaschemas", walk))

	opts = append(opts, jsonschema.WithRefResolver(byID))

	return byID, opts
}

// The following package-level types form a corpus exercising the distinct
// shapes the generator emits: primitives, bounded integers, nullable pointers
// (anyOf with null), slices, arrays, maps, []byte (contentEncoding), well-known
// overrides (time.Time, url.URL, big.Int), $defs/$ref extraction, recursion,
// generics, and interface fields.

type conformanceAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
	Zip    string `json:"zip"    jsonschema:"pattern=^[0-9]{5}$"`
}

type conformancePerson struct {
	Name     string              `json:"name"     jsonschema:"description=Full name,minLength=1"`
	Age      int8                `json:"age"`
	ID       uint64              `json:"id"`
	Score    float64             `json:"score"    jsonschema:"minimum=0,maximum=100"`
	Active   bool                `json:"active"`
	Email    string              `json:"email"    jsonschema:"format=email"`
	Website  url.URL             `json:"website"`
	Born     time.Time           `json:"born"`
	Balance  big.Int             `json:"balance"`
	Tags     []string            `json:"tags"`
	Triplet  [3]int              `json:"triplet"`
	Labels   map[string]string   `json:"labels"`
	Address  *conformanceAddress `json:"address"`
	Blob     []byte              `json:"blob"`
	Extra    map[string]any      `json:"extra"`
	Metadata conformanceMetadata `json:"metadata"`
}

type conformanceMetadata struct {
	Kind    string `json:"kind"    jsonschema:"enum=a|b|c"`
	Version int    `json:"version" jsonschema:"const=2"`
}

// conformanceTree is a recursive type, exercising $ref into $defs.
type conformanceTree struct {
	Value    string             `json:"value"`
	Children []*conformanceTree `json:"children"`
}

// conformancePair is a generic type, exercising bracket/comma name escaping.
type conformancePair[A any, B any] struct {
	First  A `json:"first"`
	Second B `json:"second"`
}

// TestGeneratedSchemaConformsToMetaschema generates a schema for a broad type
// corpus under both supported drafts and validates each generated document
// against the official meta-schema, confirming every keyword the generator
// emits is well-formed (correct type and shape). Meta-schemas leave
// additionalProperties open, so a separate guard asserts the output uses the
// draft-appropriate definitions keyword.
func TestGeneratedSchemaConformsToMetaschema(t *testing.T) {
	t.Parallel()

	metas, opts := loadMetaSchemas(t)

	drafts := []struct {
		name    string
		opt     jsonschema.Option
		metaURI string
	}{
		{"draft2020-12", jsonschema.WithDraft(jsonschema.Draft2020), "https://json-schema.org/draft/2020-12/schema"},
		{"draft7", jsonschema.WithDraft(jsonschema.Draft7), "http://json-schema.org/draft-07/schema#"},
	}

	types := map[string]reflect.Type{
		"string":    reflect.TypeFor[string](),
		"bool":      reflect.TypeFor[bool](),
		"int":       reflect.TypeFor[int](),
		"int8":      reflect.TypeFor[int8](),
		"uint":      reflect.TypeFor[uint](),
		"float64":   reflect.TypeFor[float64](),
		"pointer":   reflect.TypeFor[*string](),
		"slice":     reflect.TypeFor[[]int](),
		"array":     reflect.TypeFor[[3]int](),
		"map":       reflect.TypeFor[map[string]int](),
		"bytes":     reflect.TypeFor[[]byte](),
		"interface": reflect.TypeFor[any](),
		"time":      reflect.TypeFor[time.Time](),
		"url":       reflect.TypeFor[url.URL](),
		"bigint":    reflect.TypeFor[big.Int](),
		"address":   reflect.TypeFor[conformanceAddress](),
		"person":    reflect.TypeFor[conformancePerson](),
		"tree":      reflect.TypeFor[conformanceTree](),
		"generic":   reflect.TypeFor[conformancePair[int, string]](),
		"metadata":  reflect.TypeFor[conformanceMetadata](),
	}

	for _, draft := range drafts {
		t.Run(draft.name, func(t *testing.T) {
			t.Parallel()

			meta := metas[draft.metaURI]
			require.NotNil(t, meta, "vendored meta-schema %s must be loaded", draft.metaURI)

			// Guard: confirm the meta-schema validation is live (not a no-op) by
			// confirming it rejects a structurally invalid schema. Otherwise a
			// broken setup could make every conformance check pass vacuously.
			t.Run("rejects-invalid-schema", func(t *testing.T) {
				t.Parallel()

				bad := []byte(`{"type":42,"minLength":-1}`)
				err := jsonschema.ValidateJSON(meta, bad, opts...)
				require.Error(t, err, "meta-schema must reject a structurally invalid schema")
			})

			for name, typ := range types {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					schema, err := jsonschema.Generate(typ, draft.opt)
					require.NoError(t, err)

					raw, err := json.Marshal(schema)
					require.NoError(t, err)

					// Validate the generated schema document as an instance of the
					// official meta-schema, using the package's own validator.
					err = jsonschema.ValidateJSON(meta, raw, opts...)
					require.NoErrorf(t, err, "generated %s schema must conform to %s:\n%s", name, draft.metaURI, raw)

					// Meta-schema conformance alone cannot catch a wrong-draft
					// definitions keyword (additionalProperties is open), so guard
					// it explicitly: $defs is 2020-12, definitions is Draft-07.
					var doc map[string]any

					require.NoError(t, json.Unmarshal(raw, &doc))

					if draft.name == "draft7" {
						assert.NotContains(t, doc, "$defs", "Draft-07 output must use definitions, not $defs")
					} else {
						assert.NotContains(t, doc, "definitions", "2020-12 output must use $defs, not definitions")
					}
				})
			}
		})
	}
}
