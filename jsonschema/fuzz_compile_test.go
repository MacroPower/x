package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/format"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// suiteDocument is one schema from the vendored suite, with the draft its
// file targets.
type suiteDocument struct {
	draft     jsonschema.Draft
	schemaURI string
	raw       jsontext.Value
}

// metaschemaTolerance names a document the metaschema rejects and Compile
// accepts on purpose, with the reason.
type metaschemaTolerance struct {
	reason  string
	catches func(doc map[string]any) bool
}

var (
	// SuiteDraftOf maps a suite directory name to the draft its schemas
	// target.
	suiteDraftOf = map[string]jsonschema.Draft{
		"draft7":       jsonschema.Draft7,
		"draft2020-12": jsonschema.Draft2020,
	}

	// SubschemaKeywords are the keywords whose value is a subschema in both
	// drafts. A null in one of them unmarshals as an absent keyword, which
	// is the one shape Compile cannot see and the metaschema rejects.
	subschemaKeywords = []string{
		"items", "additionalItems", "additionalProperties", "propertyNames", "contains",
		"if", "then", "else", "not", "unevaluatedItems", "unevaluatedProperties", "contentSchema",
	}

	// MemberKeywords are the keywords whose value is a map of subschemas. A
	// null member unmarshals as an absent one, as a null subschema does.
	memberKeywords = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}

	// VetBeyondMetaschema names the refusals Compile reports on a document
	// the metaschema accepts, each a check the metaschema cannot express,
	// with the sentinel that carries it. A refusal carrying no listed
	// sentinel on a metaschema-valid document is a finding.
	vetBeyondMetaschema = []struct {
		reason   string
		sentinel error
	}{
		{"a $ref, $dynamicRef, or definitions cycle the metaschema cannot follow", jsonschema.ErrSchemaCycle},
		{"a $ref that resolves to nothing in the document or the resolver", jsonschema.ErrNotResolved},
		{"a reference the resolver cannot fetch", jsonschema.ErrRefResolve},
		{"a $vocabulary outside a metaschema root", jsonschema.ErrMisplacedVocabulary},
		{"a $vocabulary naming a vocabulary this package does not implement", jsonschema.ErrUnknownVocabulary},
		{"a $schema naming a draft this package does not implement", jsonschema.ErrUnsupportedDraft},
		{"an empty $ref, which names the current document and must be spelled #", jsonschema.ErrEmptyRef},
		{
			"an $id that is not an absolute URI without a fragment, or an anchor-only fragment the draft refuses",
			jsonschema.ErrInvalidID,
		},
		{"two subschemas declaring one $id", jsonschema.ErrIDCollision},
		{"a schema field the same keyword names twice through an extension", jsonschema.ErrConflictingSchemaFields},
		{"a format name no draft this package implements defines", jsonschema.ErrUnknownFormat},
		{
			"a pattern or patternProperties key outside the ECMA-262 grammar, which the metaschema's regex format judges only when formats are asserted",
			jsonschema.ErrInvalidPattern,
		},
	}

	// MalformedKeywords are the keywords a mutation may add to an object,
	// each one the metaschema judges.
	malformedKeywords = []map[string]any{
		{"type": []any{}},
		{"type": "bogus"},
		{"type": []any{"string", "string"}},
		{"required": []any{"a", "a"}},
		{"required": []any{1}},
		{"enum": []any{}},
		{"enum": []any{1, 1}},
		{"multipleOf": 0},
		{"multipleOf": -1},
		{"maxLength": -1},
		{"minItems": 1.5},
		{"$ref": "#/$defs/missing"},
		{"$ref": ""},
		{"items": nil},
		{"properties": map[string]any{"a": nil}},
		{"pattern": "("},
		{"format": "bogus"},
		{"$id": "urn:example:fuzz"},
		{"$id": "#anchor"},
		{"$anchor": "not valid"},
		{"$schema": "https://example.com/unknown"},
		{"$vocabulary": map[string]any{"https://example.com/vocab": true}},
		{"const": jsontext.Value(`1e400`)},
		{"minimum": jsontext.Value(`1e400`)},
		{"default": jsontext.Value(`9007199254740993`)},
		{"dependentRequired": map[string]any{"a": []any{"b", "b"}}},
		{"if": true, "then": false},
		{"additionalProperties": "no"},
		{"uniqueItems": "yes"},
		{"contentEncoding": 7},
		{"$comment": 7},
		{"deprecated": "yes"},
		{"prefixItems": []any{}},
		{"examples": "one"},
	}
)

// suiteDocuments collects every object schema of the vendored suite, the
// population the compile differential mutates. A boolean schema is left out,
// since a mutation has nothing to reach in it.
func suiteDocuments(tb testing.TB) []suiteDocument {
	tb.Helper()

	var docs []suiteDocument

	for _, file := range suiteFiles(tb) {
		for _, group := range loadSuiteGroups(tb, file.path) {
			if len(group.Schema) == 0 || group.Schema[0] != '{' {
				continue
			}

			docs = append(docs, suiteDocument{
				draft:     suiteDraftOf[file.draft],
				schemaURI: file.schemaURI,
				raw:       group.Schema,
			})
		}
	}

	require.NotEmpty(tb, docs)

	return docs
}

// metaschemaTolerances records every way the metaschema rejects a document
// Compile accepts, each with a reason. A document caught by no entry that
// Compile accepts and the metaschema rejects is a finding.
func metaschemaTolerances() []metaschemaTolerance {
	return []metaschemaTolerance{
		{
			reason: "an empty type array compiles and rejects every instance, as TestValidateEmptyTypeArrayRejectsEverything pins, where the metaschema requires one member",
			catches: func(doc map[string]any) bool {
				return anyValue(doc, func(key string, val any) bool {
					arr, ok := val.([]any)

					return ok && key == "type" && len(arr) == 0
				})
			},
		},
		{
			reason: "a $schema outside the uri format keeps the default draft, as every unrecognized $schema does, and the empty string is the Go zero value of an absent one; the metaschema refuses both under format assertion",
			catches: func(doc map[string]any) bool {
				uri, ok := doc["$schema"].(string)

				return ok && format.Validators()["uri"](uri) != nil
			},
		},
		{
			reason: "a null in a subschema-valued keyword unmarshals as the keyword's absence, so Compile never sees it",
			catches: func(doc map[string]any) bool {
				return anyValue(doc, func(key string, val any) bool {
					return val == nil && slices.Contains(subschemaKeywords, key)
				})
			},
		},
		{
			reason: "a null-valued member of a subschema map unmarshals as an absent member",
			catches: func(doc map[string]any) bool {
				return anyValue(doc, func(key string, val any) bool {
					members, ok := val.(map[string]any)
					if !ok || !slices.Contains(memberKeywords, key) {
						return false
					}

					for _, member := range members {
						if member == nil {
							return true
						}
					}

					return false
				})
			},
		},
	}
}

// FuzzCompileAgreesWithMetaschema asserts that what Compile accepts and what
// the draft's metaschema accepts differ only where a table says so. The
// input is a schema of the vendored suite, mutated one way the blob draws:
// a keyword's value retyped, an array emptied, a number negated or pushed
// past the float64 range, a member duplicated, a member dropped, or a
// malformed keyword added. If the metaschema rejects the document, Compile
// returns an error, unless a metaschemaTolerances entry names the document.
// If the metaschema accepts the document and Compile refuses it, the refusal
// carries a vetBeyondMetaschema sentinel. A document the Schema struct cannot
// unmarshal counts as a Compile refusal, since the document never reaches
// it.
func FuzzCompileAgreesWithMetaschema(f *testing.F) {
	ctx := context.Background()
	metas := compileMetaSchemas(f)
	docs := suiteDocuments(f)

	for i := range 48 {
		f.Add(i*97, make([]byte, 8))
		f.Add(i*97, []byte{byte(i), 0x5a, 0xa5, 0x3c, byte(i * 7)})
	}

	f.Fuzz(func(t *testing.T, index int, mutation []byte) {
		if index < 0 {
			index = -index
		}

		doc := docs[index%len(docs)]

		var value map[string]any

		require.NoError(t, json.Unmarshal(doc.raw, &value), "decode a suite schema")

		if _, present := value["$schema"]; !present {
			value["$schema"] = doc.schemaURI
		}

		mutateDocument(value, fuzzfill.NewCursor(mutation))

		data, err := json.Marshal(value)
		require.NoError(t, err, "marshal the mutated document")

		metaErr := metas[doc.draft].ValidateJSON(ctx, data)
		compileErr := compileDocument(ctx, data)

		switch {
		case metaErr != nil && compileErr == nil:
			for _, tolerance := range metaschemaTolerances() {
				if tolerance.catches(value) {
					return
				}
			}

			t.Fatalf("the metaschema rejects a document Compile accepts\ndocument: %s\nmetaschema: %v", data, metaErr)

		case metaErr == nil && compileErr != nil:
			for _, vet := range vetBeyondMetaschema {
				if errors.Is(compileErr, vet.sentinel) {
					return
				}
			}

			t.Fatalf("Compile refuses a document the metaschema accepts\ndocument: %s\ncompile: %v", data, compileErr)
		}
	})
}

// TestMetaschemaTablesAreReasoned pins that every tolerance and every vet
// entry carries a reason, so a row cannot enter either table as a bare
// exception.
func TestMetaschemaTablesAreReasoned(t *testing.T) {
	t.Parallel()

	for _, tolerance := range metaschemaTolerances() {
		require.NotEmpty(t, tolerance.reason)
		require.NotNil(t, tolerance.catches)
	}

	for _, vet := range vetBeyondMetaschema {
		require.NotEmpty(t, vet.reason)
		require.Error(t, vet.sentinel)
	}
}

// compileDocument unmarshals and compiles a document under the suite's
// resolvers, reporting the first refusal.
func compileDocument(ctx context.Context, data []byte) error {
	var schema jsonschema.Schema

	err := json.Unmarshal(data, &schema)
	if err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	_, err = jsonschema.Compile(ctx, &schema, suiteBaseOpts()...)

	return err //nolint:wrapcheck // The sentinel is what the caller classifies.
}

// anyValue reports whether pred holds for some member of the document tree,
// walking every object and array.
func anyValue(doc any, pred func(key string, val any) bool) bool {
	switch v := doc.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if pred(key, v[key]) || anyValue(v[key], pred) {
				return true
			}
		}

	case []any:
		for _, elem := range v {
			if anyValue(elem, pred) {
				return true
			}
		}

	default:
	}

	return false
}

// site is one mutable position in a document: a member of an object or an
// element of an array, reached through its container.
type site struct {
	object map[string]any
	key    string
	array  []any
	index  int
}

// get returns the value at the site.
func (s site) get() any {
	if s.object != nil {
		return s.object[s.key]
	}

	return s.array[s.index]
}

// set replaces the value at the site.
func (s site) set(v any) {
	if s.object != nil {
		s.object[s.key] = v

		return
	}

	s.array[s.index] = v
}

// sites lists every position of the document in a deterministic order,
// object members by sorted key.
func sites(doc any, out []site) []site {
	switch v := doc.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			out = append(out, site{object: v, key: key})
			out = sites(v[key], out)
		}

	case []any:
		for i := range v {
			out = append(out, site{array: v, index: i})
			out = sites(v[i], out)
		}

	default:
	}

	return out
}

// mutateDocument applies one mutation the cursor draws to one site of the
// document. A cursor with no entropy leaves the document as it is, so the
// unmutated suite schema is in the population too.
func mutateDocument(doc map[string]any, c *fuzzfill.Cursor) {
	kind := c.Intn(10)
	if kind == 0 {
		return
	}

	positions := sites(doc, nil)
	if len(positions) == 0 {
		return
	}

	target := positions[c.Intn(len(positions))]

	switch kind {
	case 1:
		retyped := []any{"x", 1, true, nil, []any{}, map[string]any{}}
		target.set(retyped[c.Intn(len(retyped))])

	case 2:
		if arr, ok := target.get().([]any); ok {
			target.set(arr[:0:0])
		} else {
			target.set([]any{})
		}

	case 3:
		if n, ok := target.get().(float64); ok {
			target.set(-n)
		} else {
			target.set(-1)
		}

	case 4:
		if arr, ok := target.get().([]any); ok && len(arr) > 0 {
			target.set(append(slices.Clone(arr), arr[0]))
		}

	case 5:
		if target.object != nil {
			delete(target.object, target.key)
		}

	case 6:
		target.set(jsontext.Value(`1e400`))

	case 7:
		target.set(jsontext.Value(`9007199254740993`))

	case 8:
		if _, ok := target.get().(string); ok {
			target.set("")
		} else {
			target.set(strings.Repeat("x", 3))
		}

	default:
		object := doc
		if nested, ok := target.get().(map[string]any); ok && c.Bool() {
			object = nested
		}

		maps.Copy(object, malformedKeywords[c.Intn(len(malformedKeywords))])
	}
}
