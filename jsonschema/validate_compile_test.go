package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileRejectsUnknownFormatUnderFormatAssertionVocabulary locks in the
// 2020-12 requirement (validation section 7.2.3) that an implementation fails
// on unknown formats when the format-assertion vocabulary is specified: with
// that vocabulary driving assertion, whether the vocabulary or WithFormats(true)
// turned assertion on, Compile refuses a format name with no registered
// checker with ErrUnknownFormat, in the root and in every document a reference
// reaches. The refusal was once per string instance, which read the section's
// schema-level MUST as an instance verdict. The WithFormats(true) opt-in
// without the vocabulary and Draft-07's default assertion are the package's
// own contracts and stay lenient for unknown names. WithFormats(true) beside
// the vocabulary once dropped the failure, so that case pins that an option
// documented as opting in to assertion does not relax the spec's MUST.
func TestCompileRejectsUnknownFormatUnderFormatAssertionVocabulary(t *testing.T) {
	t.Parallel()

	assertionVocabs := jsonschema.WithVocabularies(
		jsonschema.VocabCore2020,
		jsonschema.VocabValidation2020,
		jsonschema.VocabApplicator2020,
		jsonschema.VocabFormatAssertion2020,
	)

	const metaID = "https://example.test/meta/format-assertion"

	meta := &jsonschema.Schema{
		ID: metaID,
		Vocabulary: map[string]bool{
			jsonschema.VocabCore2020:            true,
			jsonschema.VocabFormatAssertion2020: true,
		},
	}

	rejectAll := jsonschema.FormatValidatorFunc(func(context.Context, string, string) error {
		return errors.New("rejected")
	})

	tests := map[string]struct {
		schema   string
		opts     []jsonschema.ValidateOption
		err      error
		contains []string
		// The instance is validated after a successful compile when non-empty;
		// keyword names the keyword it fails on, and empty means it is valid.
		instance string
		keyword  string
	}{
		"unknown format under the vocabulary": {
			schema:   `{"properties": {"a": {"format": "no-such-format"}}}`,
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			err:      jsonschema.ErrUnknownFormat,
			contains: []string{`"no-such-format"`, "/properties/a/format"},
		},
		"known format still asserted under the vocabulary": {
			schema:   `{"format": "ipv4"}`,
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: `"not-an-ip"`,
			keyword:  jsonschema.KeywordFormat,
		},
		"registered format compiles and its checker runs": {
			schema: `{"format": "no-such-format"}`,
			opts: []jsonschema.ValidateOption{
				assertionVocabs, jsonschema.WithFormatValidator("no-such-format", rejectAll),
			},
			instance: `"x"`,
			keyword:  jsonschema.KeywordFormat,
		},
		"WithFormats force keeps the vocabulary-driven refusal": {
			schema: `{"format": "no-such-format"}`,
			opts:   []jsonschema.ValidateOption{assertionVocabs, jsonschema.WithFormats(true)},
			err:    jsonschema.ErrUnknownFormat,
		},
		"WithFormats off checks no name under the vocabulary": {
			schema:   `{"format": "no-such-format"}`,
			opts:     []jsonschema.ValidateOption{assertionVocabs, jsonschema.WithFormats(false)},
			instance: `"x"`,
		},
		"unknown format stays lenient under WithFormats force alone": {
			schema:   `{"format": "no-such-format"}`,
			opts:     []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
			instance: `"x"`,
		},
		"unknown format stays lenient under draft-07 default assertion": {
			schema:   `{"$schema": "http://json-schema.org/draft-07/schema#", "format": "no-such-format"}`,
			instance: `"x"`,
		},
		"unknown format compiles under the default 2020-12 vocabularies": {
			schema:   `{"format": "no-such-format"}`,
			instance: `"x"`,
		},
		"metaschema-driven vocabulary refuses an unknown format": {
			schema: `{"$schema": "` + metaID + `", "format": "no-such-format"}`,
			opts: []jsonschema.ValidateOption{
				jsonschema.WithMetaSchemaResolver(jsonschema.SchemaMap{metaID: meta}),
			},
			err: jsonschema.ErrUnknownFormat,
		},
		"unknown format in a fetched document": {
			schema: `{"$ref": "https://example.test/doc.json"}`,
			opts: []jsonschema.ValidateOption{
				assertionVocabs,
				jsonschema.WithRefResolver(jsonschema.SchemaMap{
					"https://example.test/doc.json": {Format: "no-such-format"},
				}),
			},
			err:      jsonschema.ErrUnknownFormat,
			contains: []string{"https://example.test/doc.json", "/format"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema, tc.opts...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				for _, want := range tc.contains {
					assert.Contains(t, err.Error(), want)
				}

				return
			}

			require.NoError(t, err)

			if tc.instance == "" {
				return
			}

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.keyword == "" {
				require.NoError(t, err)

				return
			}

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword)
		})
	}
}

// TestCompileChecksIDDomain locks in the compile-time $id domain checks: a
// fragment in $id is rejected under Draft 2020-12 (core section 8.2.1) and
// tolerated under Draft-07 (the anchor spelling), an $id beside a $ref is
// ignored under Draft-07, an unparsable $id is rejected, and a checked $id
// must resolve to an absolute URI against its enclosing base. The base chain
// is the parent $id hierarchy, or [jsonschema.WithBaseURI] for the root: the
// same relative root $id that fails bare compiles once a base supplies the
// absolute prefix.
func TestCompileChecksIDDomain(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
		path   string
	}{
		"2020-12 fragment-only id rejected": {
			schema: `{"$defs": {"a": {"$id": "#frag", "type": "integer"}}}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$defs/a/$id",
		},
		"2020-12 fragment-carrying id rejected": {
			schema: `{"$id": "http://example.com/root.json#frag", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
		"2020-12 empty fragment id accepted": {
			// Core section 8.2.1 forbids a non-empty fragment and tolerates
			// the empty one. The bare "#" used to be refused while the
			// trailing-"#" spelling below compiled, although both carry the
			// same empty fragment.
			schema: `{"$defs": {"a": {"$id": "#", "type": "integer"}}, "allOf": [{"$ref": "#/$defs/a"}]}`,
		},
		"2020-12 trailing empty fragment id accepted": {
			schema: `{
				"$id": "http://example.com/root.json#",
				"$defs": {"a": {"$id": "http://example.com/a.json#", "type": "integer"}},
				"allOf": [{"$ref": "http://example.com/a.json"}]
			}`,
		},
		"draft-07 fragment-only id compiles": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"a": {"$id": "#frag", "type": "integer"}},
				"allOf": [{"$ref": "#frag"}]
			}`,
		},
		"draft-07 escaped fragment-only id resolves by its decoded name": {
			// The freeze used to register the raw fragment text while a
			// reference resolves by the decoded text, so an escaped $id was
			// reachable by neither spelling. Both spellings name one anchor.
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"a": {"$id": "#a%2Db", "type": "integer"}},
				"allOf": [{"$ref": "#a-b"}, {"$ref": "#a%2Db"}]
			}`,
		},
		"draft-07 unparsable fragment-only id rejected": {
			// The freeze registers nothing for a $id it cannot parse, so the
			// check refuses it rather than let every reference to it miss.
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"a": {"$id": "#a%zz", "type": "integer"}}
			}`,
			err:  jsonschema.ErrInvalidID,
			path: "/definitions/a/$id",
		},
		"draft-07 fragment on the root URI id resolves as an anchor": {
			// The anchor spelling on a URI: the fragment names an anchor
			// within the document the URI part names, here the root, so the
			// fragment-only reference reaches it. The freeze used to drop the
			// fragment and register only the URI part, which the root already
			// held, so the $id named nothing.
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "http://example.com/root.json",
				"definitions": {"a": {"$id": "http://example.com/root.json#foo", "type": "integer"}},
				"allOf": [{"$ref": "#foo"}]
			}`,
		},
		"draft-07 fragment on a relative URI id resolves as an anchor": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "http://example.com/root.json",
				"definitions": {"a": {"$id": "other.json#foo", "type": "integer"}},
				"allOf": [{"$ref": "other.json#foo"}]
			}`,
		},
		"draft-07 id beside ref is ignored": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"t": {"type": "integer"}},
				"allOf": [{"$ref": "#/definitions/t", "$id": "relative.json"}]
			}`,
		},
		"draft-07 unparsable id beside ref is ignored": {
			// The draft ignores the $id outright, so the check reads nothing
			// of it, not even whether it parses. The parse step used to run
			// ahead of the sibling exemption and refused a document the
			// registry would have resolved without consulting the $id.
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"t": {"type": "integer"}},
				"allOf": [{"$ref": "#/definitions/t", "$id": "http://[::1"}]
			}`,
		},
		"draft-07 id beside ref rebases no child": {
			// The ignored $id changes no base, so the child's relative $id
			// resolves against the root's absent base and is refused where
			// the same child directly under the root is. The check used to
			// thread the ignored $id's resolution to the children while the
			// registry kept the parent scope, so the child compiled and
			// every ref naming it missed at validation.
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {
					"wrap": {
						"$id": "http://example.com/wrap.json",
						"$ref": "#/definitions/wrap/definitions/leaf",
						"definitions": {"leaf": {"$id": "leaf.json", "type": "integer"}}
					}
				}
			}`,
			err:  jsonschema.ErrInvalidID,
			path: "/definitions/wrap/definitions/leaf/$id",
		},
		"relative root id without base rejected": {
			schema: `{"$id": "sub/schema.json", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
		"relative root id under draft-07 without base rejected": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "sub/schema.json",
				"type": "string"
			}`,
			err:  jsonschema.ErrInvalidID,
			path: "/$id",
		},
		"relative root id with base compiles": {
			// The compile-accept side of the same schema: WithBaseURI supplies
			// the absolute prefix the relative $id resolves against.
			schema: `{"$id": "sub/schema.json", "type": "string"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithBaseURI("http://example.com/root.json")},
		},
		"nested relative id against absolute parent compiles": {
			schema: `{
				"$id": "http://example.com/root.json",
				"$defs": {"a": {"$id": "item.json", "type": "string"}}
			}`,
		},
		"nested relative id without any base rejected": {
			schema: `{"$defs": {"a": {"$id": "item.json", "type": "string"}}}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$defs/a/$id",
		},
		"unparsable id rejected": {
			schema: `{"$id": "http://example.com/a%zz", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the offending $id's schema path")
		})
	}
}

// TestCompileRejectsInvalidBaseURI pins that an unparsable
// [jsonschema.WithBaseURI] value fails Compile with
// [jsonschema.ErrInvalidBaseURI] instead of silently corrupting every
// registry key derived from the base.
func TestCompileRejectsInvalidBaseURI(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"type": "string"}`))
	require.NoError(t, err)

	_, err = jsonschema.Compile(t.Context(), schema, jsonschema.WithBaseURI("://bad"))
	require.ErrorIs(t, err, jsonschema.ErrInvalidBaseURI)
}

// TestCompileDrivePathBase pins that Compile reads a Windows drive path base
// as the file URI of its drive, so a relative ref in the root resolves under
// the drive and StripPrefix over that base serves it from an fs.
func TestCompileDrivePathBase(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "sub.json"}`))
	require.NoError(t, err)

	resolver := jsonschema.StripPrefix("file:///C:/schemas/",
		jsonschema.NewFileResolver(mapFS(map[string]string{"sub.json": `{"type": "integer"}`})))

	opts := []jsonschema.ValidateOption{
		jsonschema.WithRefResolver(resolver),
		jsonschema.WithBaseURI(`C:\schemas\main.json`),
	}

	require.NoError(t, validateValue(t.Context(), schema, 1, opts...))
	require.Error(t, validateValue(t.Context(), schema, "s", opts...))
}

// TestCompileChecksVocabularyPlacement locks in the $vocabulary placement
// check: a node carrying $vocabulary needs a $schema that establishes the
// Draft 2020-12 dialect. The match is exact, so the trailing-"#" spelling of
// the 2020-12 URI is rejected, and an empty $schema is rejected under
// Draft-07, which predates the vocabulary concept.
func TestCompileChecksVocabularyPlacement(t *testing.T) {
	t.Parallel()

	// A remote document carrying $vocabulary with no $schema of its own,
	// served for the fetched-document cases below.
	remote := jsonschema.SchemaMap{
		"http://example.com/vocab.json": {
			Vocabulary: map[string]bool{"https://json-schema.org/draft/2020-12/vocab/core": true},
			Type:       "object",
		},
	}

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
	}{
		"vocabulary without a schema under 2020-12 compiles": {
			// The empty $schema inherits the run's dialect (the reading
			// upstream applies to loaded documents).
			schema: `{
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
		},
		"fetched document vocabulary with empty schema compiles under 2020-12": {
			schema: `{"$ref": "http://example.com/vocab.json"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
		},
		"fetched document vocabulary with empty schema rejected under draft-07": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$ref": "http://example.com/vocab.json"
			}`,
			opts: []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
			err:  jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary under the exact 2020-12 schema URI compiles": {
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
		},
		"vocabulary under the trailing-hash 2020-12 spelling rejected": {
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema#",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			err: jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary under the draft-07 schema URI rejected": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			err: jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary without a schema under draft-07 rejected": {
			schema: `{
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			opts: []jsonschema.ValidateOption{jsonschema.WithDraft(jsonschema.Draft7)},
			err:  jsonschema.ErrMisplacedVocabulary,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
		})
	}
}

// TestCompileRefWalkRejectsUnresolvableRefs locks in the strict side of the
// compile-time reference walk: a reference that resolves to nothing while its
// document is present can never resolve later, so Compile reports it with
// [jsonschema.ErrNotResolved], naming the bearing node's schema path. The
// walk covers every node of the root document, referenced or not, and
// $dynamicRef resolves statically under the same policy.
func TestCompileRefWalkRejectsUnresolvableRefs(t *testing.T) {
	t.Parallel()

	remote := jsonschema.SchemaMap{
		"http://example.com/present.json": {Type: "string"},
	}

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
		want   string
	}{
		"unresolvable local pointer ref": {
			schema: `{"$ref": "#/nope"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "#/nope"`,
		},
		"unresolvable anchor ref": {
			schema: `{"properties": {"a": {"$ref": "#missing"}}}`,
			err:    jsonschema.ErrNotResolved,
			want:   "/properties/a",
		},
		"broken ref in unreferenced defs": {
			schema: `{"$defs": {"unused": {"$ref": "#/also/nope"}}}`,
			err:    jsonschema.ErrNotResolved,
			want:   "/$defs/unused",
		},
		"unresolvable dynamic ref": {
			schema: `{"$dynamicRef": "#nothing"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $dynamicRef "#nothing"`,
		},
		"unparsable ref": {
			schema: `{"$ref": "://bad"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "://bad"`,
		},
		"broken fragment into a fetched document": {
			schema: `{"$ref": "http://example.com/present.json#/missing"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "http://example.com/present.json#/missing"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.want,
				"the error must name the bearing node and the reference")
		})
	}
}

// TestCompileToleratesResolverErrorAtCompile pins the Remote References
// contract's tolerant side: a resolver failure at compile time is a document
// miss, so Compile succeeds, and the validation walk reports the ref with the
// resolver's error wrapping [jsonschema.ErrRefResolve].
func TestCompileToleratesResolverErrorAtCompile(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "http://example.com/broken.json"}`))
	require.NoError(t, err)

	resolver := jsonschema.RefResolverFunc(func(context.Context, string) (*jsonschema.Schema, error) {
		return nil, errors.New("backend unavailable")
	})

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "a resolver failure at compile time defers to the validation walk")

	err = v.Validate(t.Context(), "anything")
	require.ErrorIs(t, err, jsonschema.ErrRefResolve,
		"the validation walk must report the deferred resolver failure")
}

// TestCompileFetchCountsOncePerURI pins the once-per-URI resolver contract
// across the pipeline: the compile-time reference walk fetches the document
// into the shared registry, so two later validation runs resolve it from
// cache without another resolver call.
func TestCompileFetchCountsOncePerURI(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	resolver := jsonschema.RefResolverFunc(func(context.Context, string) (*jsonschema.Schema, error) {
		calls.Add(1)

		return &jsonschema.Schema{Type: "string"}, nil
	})

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "http://example.com/s.json"}`))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	require.NoError(t, v.Validate(t.Context(), "hello"))
	require.NoError(t, v.Validate(t.Context(), "world"))

	assert.Equal(t, int64(1), calls.Load(),
		"the resolver must be consulted once per URI across Compile and every Validate")
}

// TestCompileRejectsConflictingSchemaFields locks in the compile-time
// rejection of a schema that sets both Go fields spelling one JSON keyword.
// Each pair marshals to a single keyword, so such a schema cannot round-trip
// through JSON and the walk would silently prefer one form. These shapes are
// only expressible on a hand-built Schema, so the cases build the values
// directly rather than parsing JSON.
func TestCompileRejectsConflictingSchemaFields(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		err    error
		path   string
	}{
		"type and types": {
			schema: &jsonschema.Schema{Type: "string", Types: []string{"string"}},
			err:    jsonschema.ErrConflictingSchemaFields,
			path:   "/type",
		},
		"defs and definitions": {
			schema: &jsonschema.Schema{
				Defs:        map[string]*jsonschema.Schema{"a": {}},
				Definitions: map[string]*jsonschema.Schema{"b": {}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/$defs",
		},
		"items and items array": {
			schema: &jsonschema.Schema{
				Items:      &jsonschema.Schema{},
				ItemsArray: []*jsonschema.Schema{{}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/items",
		},
		"dependencies key in both maps": {
			schema: &jsonschema.Schema{
				DependencySchemas: map[string]*jsonschema.Schema{"k": {}},
				DependencyStrings: map[string][]string{"k": {"x"}},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/dependencies/k",
		},
		"conflict nested under properties": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "string", Types: []string{"string"}},
				},
			},
			err:  jsonschema.ErrConflictingSchemaFields,
			path: "/properties/a/type",
		},
		"duplicate property order entry": {
			schema: &jsonschema.Schema{
				Properties:    map[string]*jsonschema.Schema{"a": {}, "b": {}},
				PropertyOrder: []string{"a", "b", "a"},
			},
			err:  jsonschema.ErrDuplicatePropertyOrder,
			path: "/propertyOrder",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the offending keyword's schema path")
		})
	}
}

// TestCompileRejectsNilSubschemaEntries locks in the compile-time rejection of
// a nil *Schema element inside a sub-schema slice or map. The walk skips a nil
// element silently, so the branch the author listed would assert nothing. A
// nil direct field stays an absent keyword and compiles.
func TestCompileRejectsNilSubschemaEntries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		err    error
		path   string
	}{
		"nil element in allOf": {
			schema: &jsonschema.Schema{AllOf: []*jsonschema.Schema{nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/allOf/0",
		},
		"nil element beside a real branch": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "string"}, nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/anyOf/1",
		},
		"nil member in properties": {
			schema: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"a": nil}},
			err:    jsonschema.ErrNilSubschema,
			path:   "/properties/a",
		},
		"nil member nested in defs": {
			schema: &jsonschema.Schema{
				Defs: map[string]*jsonschema.Schema{
					"inner": {OneOf: []*jsonschema.Schema{nil}},
				},
			},
			err:  jsonschema.ErrNilSubschema,
			path: "/$defs/inner/oneOf/0",
		},
		"nil direct field is an absent keyword": {
			schema: &jsonschema.Schema{Not: nil, Items: nil, Type: "string"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the nil element's schema path")
		})
	}
}

// TestCompileFreezesRootGraph locks in the root-document graph rule. A
// pointer cycle fails Compile with [jsonschema.ErrSchemaCycle] naming the
// graph the freeze walked and the pointer where the loop closes, whether the
// loop runs through a sub-schema keyword or a value field. A *Schema value
// reachable through two paths compiles, since the freeze copies it once per
// path, as do two distinct pointers with identical content.
func TestCompileFreezesRootGraph(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string"}

	cyclic := &jsonschema.Schema{Defs: map[string]*jsonschema.Schema{}}
	cyclic.Defs["loop"] = cyclic

	valueCyclic := &jsonschema.Schema{Type: "object"}
	valueCyclic.Extra = map[string]any{"x-self": valueCyclic}

	tests := map[string]struct {
		schema  *jsonschema.Schema
		err     error
		paths   []string
		subject string
	}{
		"aliased subschema": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
			},
		},
		"pointer cycle": {
			schema:  cyclic,
			err:     jsonschema.ErrSchemaCycle,
			paths:   []string{"/$defs/loop"},
			subject: "the root document",
		},
		"cycle through a value field": {
			schema:  valueCyclic,
			err:     jsonschema.ErrSchemaCycle,
			paths:   []string{"/x-self"},
			subject: "the root document",
		},
		"distinct pointers with identical content": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "string"},
					"b": {Type: "string"},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Compile(t.Context(), tc.schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)

			for _, path := range tc.paths {
				assert.Contains(t, err.Error(), path,
					"the violation must name every pointer the check reports")
			}

			if tc.subject != "" {
				assert.Contains(t, err.Error(), tc.subject,
					"a loop must name the graph the freeze found it in")
			}
		})
	}
}

// TestDraft7AnchorKeywordsRegisterNothing pins that $anchor and $dynamicAnchor
// are Draft 2020-12 keywords in the shared ref-resolution registry: under
// Draft-07 they are unknown annotations that register no resolution targets, so
// a plain-name fragment $ref naming one stays unresolvable instead of asserting
// the target, matching what a conforming Draft-07 processor produces. The
// Draft-07 spelling of an anchor (a fragment-only $id) and the 2020-12 $anchor
// keyword under its own draft both still resolve.
func TestDraft7AnchorKeywordsRegisterNothing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  string
		compile bool // whether Compile succeeds
		valid   bool // for a compiled schema: whether "not an int" passes
	}{
		"draft-07 $anchor is inert": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$anchor": "a", "type": "integer"}}
				}
			`),
			compile: false,
		},
		"draft-07 $dynamicAnchor is inert": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$dynamicAnchor": "a", "type": "integer"}}
				}
			`),
			compile: false,
		},
		"draft-07 fragment-only $id still names an anchor": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$id": "#a", "type": "integer"}}
				}
			`),
			compile: true,
			valid:   false,
		},
		"draft 2020-12 $anchor still resolves": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"allOf": [{"$ref": "#a"}],
					"$defs": {"x": {"$anchor": "a", "type": "integer"}}
				}
			`),
			compile: true,
			valid:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			if !tc.compile {
				require.Error(t, err,
					"a plain-name fragment with no registered anchor must not compile")

				return
			}

			require.NoError(t, err)

			err = v.Validate(t.Context(), "not an int")
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err,
					"the anchor ref must resolve and assert its target")
			}
		})
	}
}

// TestInlineDraft7AnchorKeywordsRegisterNothing pins the same draft gate
// through the inliner, which shares the refresolve registry walk: a Draft-07
// document whose only route to the ref target is a 2020-12 $anchor keyword
// reports the ref as unresolvable.
func TestInlineDraft7AnchorKeywordsRegisterNothing(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
		{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"properties": {"x": {"$ref": "#a"}},
			"definitions": {"t": {"$anchor": "a", "type": "integer"}}
		}
	`)))
	require.NoError(t, err)

	_, err = jsonschema.Inline(t.Context(), schema)
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
}

// A hand-built const or enum value can carry container shapes the schema
// parser never produces, most commonly the map[any]any gopkg.in/yaml.v2
// decodes documents into. The validator compares such a value the way its
// document renders it, so a string-keyed map[any]any equals the object
// encoding/json v1 writes for it. A map v1 refuses to marshal (one with a
// bool key) has no document form, and comparing it against a decoded object
// must report the values unequal instead of panicking inside the upstream
// reflect-based comparison (reflect.Value.MapIndex rejects an interface{}
// key against a string-keyed map).
func TestValidateHandBuiltMapAnyConst(t *testing.T) {
	t.Parallel()

	stringKeyed := any(map[any]any{"a": 1})
	boolKeyed := any(map[any]any{true: 1})

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"const matches its document": {
			schema:   &jsonschema.Schema{Const: &stringKeyed},
			instance: map[string]any{"a": 1.0},
		},
		"const rejects another object": {
			schema:   &jsonschema.Schema{Const: &stringKeyed},
			instance: map[string]any{"a": 2.0},
			err:      "const",
		},
		"const without a document form": {
			schema:   &jsonschema.Schema{Const: &boolKeyed},
			instance: map[string]any{"a": 1.0},
			err:      "const",
		},
		"enum matches its document": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{"a": 1}}},
			instance: map[string]any{"a": 1.0},
		},
		"enum rejects another object": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{"a": 1}}},
			instance: map[string]any{"a": 2.0},
			err:      "enum",
		},
		"enum without a document form": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{true: 1}}},
			instance: map[string]any{"a": 1.0},
			err:      "enum",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tc.schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), tc.instance)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// TestCompileMetaschemaResolverContextNormalized locks in that the metaschema
// resolver receives a normalized context: a nil context passed to Compile (a
// misuse Go tolerates and runContext exists to defend against) must reach the
// resolver as a usable non-nil context, matching every other resolver
// invocation, so a well-behaved resolver that reads the context does not
// panic.
func TestCompileMetaschemaResolverContextNormalized(t *testing.T) {
	t.Parallel()

	var (
		called    bool
		ctxWasNil bool
	)

	resolver := jsonschema.RefResolverFunc(func(ctx context.Context, _ string) (*jsonschema.Schema, error) {
		called = true
		ctxWasNil = ctx == nil
		// A nil context panics here the way any deadline-aware resolver would.
		_, _ = ctx.Deadline()

		return nil, jsonschema.ErrNotResolved
	})

	var nilCtx context.Context

	schema := &jsonschema.Schema{Schema: "https://example.test/meta"}

	_, err := jsonschema.Compile(nilCtx, schema, jsonschema.WithMetaSchemaResolver(resolver))
	require.NoError(t, err, "a metaschema miss falls through to the default vocabularies")
	require.True(t, called, "the resolver must have been consulted")
	require.False(t, ctxWasNil,
		"a nil compile context must be normalized before reaching the metaschema resolver")
}

// TestCompileMultipleOfUnderflow locks in the float64-precision contract for a
// multipleOf literal below the smallest positive float64: the authored value
// is spec-valid (strictly greater than zero) but underflows to zero when the
// document is decoded, so the parse drops the keyword instead of letting the
// domain vet report the underflowed zero as an authored one. An authored zero
// and a negative underflowing literal stay rejected with
// [jsonschema.ErrNonPositiveMultipleOf].
// TestCompileRejectsInvalidPattern pins that a pattern or a patternProperties
// key outside the ECMA-262 grammar is a compile error, since no engine could
// run it, while a pattern the grammar admits and RE2 cannot compile still
// compiles and fails closed at validation time. Compile used to accept "["
// and fail every string it judged.
func TestCompileRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		err    error
	}{
		"unterminated class": {
			schema: `{"pattern":"["}`,
			err:    jsonschema.ErrInvalidPattern,
		},
		"unbalanced group in patternProperties": {
			schema: `{"patternProperties":{"(":{}}}`,
			err:    jsonschema.ErrInvalidPattern,
		},
		"nested under properties": {
			schema: `{"properties":{"a":{"pattern":"a{2,1}"}}}`,
			err:    jsonschema.ErrInvalidPattern,
		},
		"backreference compiles": {
			schema: `{"pattern":"(a)\\1"}`,
		},
		"lookahead compiles": {
			schema: `{"pattern":"^(?=a)"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var s jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &s))

			_, err := jsonschema.Compile(t.Context(), &s)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestCompileMultipleOfUnderflow(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  string
		valid   []string
		invalid []string
		err     error
	}{
		"positive underflowing literal compiles and asserts nothing": {
			schema: `{"multipleOf": 1e-400}`,
			valid:  []string{`7`, `0.3`, `"s"`},
		},
		"nested positive underflowing literal compiles": {
			schema: `{"properties": {"p": {"multipleOf": 1e-400}}}`,
			valid:  []string{`{"p": 0.3}`},
		},
		"authored zero stays rejected": {
			schema: `{"multipleOf": 0}`,
			err:    jsonschema.ErrNonPositiveMultipleOf,
		},
		"negative underflowing literal stays rejected": {
			schema: `{"multipleOf": -1e-400}`,
			err:    jsonschema.ErrNonPositiveMultipleOf,
		},
		"representable literal keeps asserting": {
			schema:  `{"multipleOf": 0.5}`,
			valid:   []string{`1.5`},
			invalid: []string{`0.3`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err, "a spec-valid multipleOf literal must compile")

			for _, instance := range tc.valid {
				require.NoError(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must validate", instance)
			}

			for _, instance := range tc.invalid {
				require.Error(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must not validate", instance)
			}
		})
	}
}

// TestParseSchemaOutOfRangeNumberLiteral pins that a JSON number outside
// float64 range survives in the members the decode reads as data (const,
// enum, examples, default, and an unknown keyword, at the root and inside a
// sub-schema) and stays refused in the float64 and int bound keywords, whose
// fields cannot hold it. ParseSchemaValue round-trips the document through
// the upstream UnmarshalJSON, which decoded those data members as float64
// and so refused the whole document before restoreExactValues could re-copy
// the authored literal, although the validator's instance side accepts the
// same literal as an ordinary number.
func TestParseSchemaOutOfRangeNumberLiteral(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc     string
		want    string // the parsed schema's JSON form, in the upstream marshal order
		valid   []string
		invalid []string
		refused bool
		err     error // the sentinel a refusal carries, when it carries one
	}{
		"const": {
			doc:     `{"const": 1e400}`,
			want:    `{"const":1e400}`,
			valid:   []string{`1e400`},
			invalid: []string{`1e401`, `1.7976931348623157e308`},
		},
		"const nested": {
			doc:   `{"const": {"a": [1e400]}}`,
			want:  `{"const":{"a":[1e400]}}`,
			valid: []string{`{"a": [1e400]}`},
		},
		"const negative": {
			doc:     `{"const": -1e400}`,
			want:    `{"const":-1e400}`,
			valid:   []string{`-1e400`},
			invalid: []string{`1e400`},
		},
		"enum": {
			doc:     `{"enum": [1e400, "s"]}`,
			want:    `{"enum":[1e400,"s"]}`,
			valid:   []string{`1e400`, `"s"`},
			invalid: []string{`1e401`},
		},
		"examples": {
			doc:   `{"examples": [1e400]}`,
			want:  `{"examples":[1e400]}`,
			valid: []string{`1`},
		},
		"default": {
			doc:   `{"default": 1e400}`,
			want:  `{"default":1e400}`,
			valid: []string{`1`},
		},
		"unknown keyword": {
			doc:   `{"x-ext": {"const": 1e400}}`,
			want:  `{"x-ext":{"const":1e400}}`,
			valid: []string{`1`},
		},
		"inside a property": {
			doc:     `{"properties": {"p": {"const": 1e400}}}`,
			want:    `{"properties":{"p":{"const":1e400}}}`,
			valid:   []string{`{"p": 1e400}`},
			invalid: []string{`{"p": 1e401}`},
		},
		"inside a draft-07 items array": {
			doc:     `{"$schema": "http://json-schema.org/draft-07/schema#", "items": [{"const": 1e400}]}`,
			want:    `{"items":[{"const":1e400}],"$schema":"http://json-schema.org/draft-07/schema#"}`,
			valid:   []string{`[1e400]`},
			invalid: []string{`[1e401]`},
		},
		"minimum stays refused": {
			doc:     `{"minimum": 1e400}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		"minLength stays refused": {
			doc:     `{"minLength": 1e400}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		// The contains counts are sizes with no bound row, and the
		// placeholder walk used to exempt the bound keywords alone, so an
		// out-of-range count was rewritten to 0 and never restored:
		// maxContains 1e400 refused every match and minContains -1e400
		// slipped past the negative-bound vet.
		"maxContains stays refused": {
			doc:     `{"contains": {"type": "string"}, "maxContains": 1e400}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		"negative minContains stays refused": {
			doc:     `{"contains": {"type": "string"}, "minContains": -1e400}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		"nested multipleOf stays refused": {
			doc:     `{"properties": {"p": {"multipleOf": 1e400}}}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		// The upstream decode holds a count in an int32 and reads the plain
		// integer spelling alone, where the metaschema admits any
		// integer-valued number, so the parse spells an in-range count
		// plainly and refuses a larger one by name; a fraction stays the
		// decode's own refusal, since the metaschema refuses it too.
		"count spelled with an exponent": {
			doc:     `{"maxLength": 1E2}`,
			want:    `{"maxLength":100}`,
			valid:   []string{`"ab"`},
			invalid: []string{`"` + strings.Repeat("a", 101) + `"`},
		},
		"count spelled with a point and an exponent": {
			doc:   `{"minItems": 1.0e1}`,
			want:  `{"minItems":10}`,
			valid: []string{`[1,2,3,4,5,6,7,8,9,10]`},
		},
		"count at the int32 ceiling": {
			doc:   `{"maxItems": 2147483647}`,
			want:  `{"maxItems":2147483647}`,
			valid: []string{`[]`},
		},
		"count past the int32 ceiling": {
			doc:     `{"maxItems": 2147483648}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		"count past the int64 range": {
			doc:     `{"maxProperties": 1e30}`,
			refused: true,
			err:     jsonschema.ErrKeywordOutOfRange,
		},
		"fractional count": {
			doc:     `{"maxLength": 1.5}`,
			refused: true,
			err:     jsonschema.ErrKeywordType,
		},
		"count as a property name is data": {
			doc:   `{"properties": {"maxLength": {"const": 1E2}}}`,
			want:  `{"properties":{"maxLength":{"const":1E2}}}`,
			valid: []string{`{"maxLength": 100}`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.ParseSchema([]byte(tc.doc))
			if tc.refused {
				require.Error(t, err, "a typed keyword cannot hold the literal")

				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				} else {
					require.NotErrorIs(t, err, jsonschema.ErrKeywordOutOfRange)
				}

				return
			}

			require.NoError(t, err, "a valid JSON Schema document parses")

			got, err := s.MarshalJSON()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got), "the authored literal survives")

			v, err := jsonschema.Compile(t.Context(), s)
			require.NoError(t, err)

			for _, instance := range tc.valid {
				require.NoError(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must validate", instance)
			}

			for _, instance := range tc.invalid {
				require.Error(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must not validate", instance)
			}
		})
	}
}

// TestParseSchemaNullKeywordReadsAsAbsent pins that a null under any keyword
// but const, default, and the sub-schema keywords reads as the keyword's
// absence, as unmarshaling into a Schema reads it, where the metaschema
// types the keyword and refuses the null; const and default hold the null
// as the value it is, and a null under a sub-schema keyword is refused,
// since unmarshaling once read it as the false schema under items and
// silently rejected every element.
func TestParseSchemaNullKeywordReadsAsAbsent(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  string
		valid   string
		invalid string
		err     error
	}{
		"$schema": {
			schema:  `{"$schema": null, "type": "boolean"}`,
			valid:   `true`,
			invalid: `1`,
		},
		"minLength": {
			schema:  `{"minLength": null, "type": "string"}`,
			valid:   `""`,
			invalid: `1`,
		},
		"enum": {
			schema:  `{"enum": null, "type": "integer"}`,
			valid:   `7`,
			invalid: `"s"`,
		},
		"items": {
			schema: `{"items": null, "type": "array"}`,
			err:    jsonschema.ErrNilSubschema,
		},
		"dependentSchemas": {
			schema:  `{"dependentSchemas": null, "type": "object"}`,
			valid:   `{"a": 1}`,
			invalid: `1`,
		},
		"allOf": {
			schema:  `{"allOf": null, "type": "object"}`,
			valid:   `{}`,
			invalid: `1`,
		},
		"dependentRequired member": {
			schema:  `{"dependentRequired": {"a": null}, "type": "object"}`,
			valid:   `{"a": 1}`,
			invalid: `1`,
		},
		"contains inside a property": {
			schema: `{"properties": {"a": {"contains": null}}}`,
			err:    jsonschema.ErrNilSubschema,
		},
		"property member": {
			schema: `{"properties": {"a": null}, "required": ["a"]}`,
			err:    jsonschema.ErrNilSubschema,
		},
		"const holds the null": {
			schema:  `{"const": null}`,
			valid:   `null`,
			invalid: `1`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err, "a null keyword compiles")

			require.NoError(t, v.ValidateJSON(t.Context(), []byte(tc.valid)))
			require.Error(t, v.ValidateJSON(t.Context(), []byte(tc.invalid)))
		})
	}
}

// assertParseValueMatchesRemarshal pins ParseSchemaValue's direct exact copy
// of value members to the marshal round trip it replaced: parsing doc
// directly must produce the same schema as marshaling doc and parsing the
// bytes, whose value members take the exact-decode path by construction.
func assertParseValueMatchesRemarshal(t *testing.T, doc any) {
	t.Helper()

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	want, wantErr := jsonschema.ParseSchema(data)
	got, gotErr := jsonschema.ParseSchemaValue(doc)

	if wantErr != nil {
		require.Error(t, gotErr)

		return
	}

	require.NoError(t, gotErr)
	assert.Equal(t, want, got)
}

// TestParseSchemaValueExactCopyMatchesRemarshal covers Go-typed leaves in
// every member restoreExactValues repairs (const, enum, examples, and unknown
// keywords), including the shapes that exercise the exact copy's marshal
// fallback.
func TestParseSchemaValueExactCopyMatchesRemarshal(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc any
	}{
		"const float64 fraction": {doc: map[string]any{"const": 0.1}},
		"const float32":          {doc: map[string]any{"const": float32(0.1)}},
		"const large uint64":     {doc: map[string]any{"const": uint64(1)<<60 + 1}},
		"const number literal":   {doc: map[string]any{"const": jsonv1.Number("9007199254740993")}},
		"enum mixed leaves": {doc: map[string]any{
			"enum": []any{1, 2.5, jsonv1.Number("9007199254740993"), "s", nil, true},
		}},
		"examples nested containers": {doc: map[string]any{
			"examples": []any{map[string]any{"n": uint64(1)<<60 + 1, "f": float32(2.5)}},
		}},
		"unknown keyword subschema": {doc: map[string]any{
			"myext": map[string]any{"const": jsonv1.Number("9007199254740993")},
		}},
		"unknown keyword struct fallback": {doc: map[string]any{
			"myext": struct {
				N int `json:"n"`
			}{N: 1},
		}},
		"unknown keyword raw message fallback": {doc: map[string]any{
			"myext": jsonv1.RawMessage(`{"n":9007199254740993}`),
		}},
		"unknown keyword raw value fallback": {doc: map[string]any{
			"myext": jsontext.Value(`[1,2]`),
		}},
		"multipleOf underflow literal": {doc: map[string]any{
			"multipleOf": jsonv1.Number("1e-320"),
		}},
		"boolean document": {doc: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assertParseValueMatchesRemarshal(t, tt.doc)
		})
	}
}

// TestCompileDraft7IDBesideRefIsNotATarget pins that under Draft-07 a $id
// placed beside a $ref names no resolution target, in either its anchor or
// its absolute form, since the draft ignores every sibling of $ref. The
// freeze used to register the identifier before it applied the sibling
// rule, so a $ref naming it resolved through a node the draft says has no
// identifier. A fragment miss is refused at Compile; an absolute miss with
// no resolver is the validation-time error every unresolvable remote ref is.
func TestCompileDraft7IDBesideRefIsNotATarget(t *testing.T) {
	t.Parallel()

	t.Run("anchor form", func(t *testing.T) {
		t.Parallel()

		schema, err := jsonschema.ParseSchema([]byte(`{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"definitions": {
				"a": {"$id": "#foo", "$ref": "#/definitions/b"},
				"b": {"type": "string"}
			},
			"$ref": "#foo"
		}`))
		require.NoError(t, err)

		_, err = jsonschema.Compile(t.Context(), schema)
		require.ErrorIs(t, err, jsonschema.ErrNotResolved)
	})

	t.Run("absolute form", func(t *testing.T) {
		t.Parallel()

		schema, err := jsonschema.ParseSchema([]byte(`{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"definitions": {
				"a": {"$id": "https://example.test/a.json", "$ref": "#/definitions/b"},
				"b": {"type": "string"}
			},
			"$ref": "https://example.test/a.json"
		}`))
		require.NoError(t, err)

		err = jsonschema.Validate(t.Context(), schema, 1)
		require.ErrorContains(t, err, `cannot resolve $ref "https://example.test/a.json"`)
	})
}
