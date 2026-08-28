package jsonschema_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// The differential rig runs one schema through every site that materializes a
// $ref target and asserts the sites agree on every instance. Three sites exist:
// Compile's reference fixpoint, Inline's own registry and index, and the
// JSON-pointer fallback both engines reach through internal/refresolve. The
// reasons below name the cases where a comparison is not meaningful. Each is
// classified from the error Inline returns rather than from a test name, so a
// reason cannot go stale against a renamed suite case.

const (
	// Why a recursive reference graph is not compared. A cyclic graph has no
	// finite static expansion, so Inline returns ErrRefCycle by design while
	// Compile resolves the cycle lazily per instance.
	reasonInlineCycle skipReason = "the reference graph is cyclic, which has no finite static expansion, so Inline returns ErrRefCycle by design while Compile resolves the cycle lazily at validation time"

	// Why a $dynamicRef graph is not compared. The target depends on the
	// dynamic scope at validation time, so no single replacement preserves it
	// and Inline returns ErrRefInline by design.
	reasonInlineDynamicRef skipReason = "$dynamicRef resolves through the dynamic scope at validation time, so no single static replacement preserves its semantics and Inline returns ErrRefInline by design"

	// Why an unresolvable reference makes the two engines incomparable. The
	// compile-time reference walk tolerates a document miss, so Compile
	// succeeds and reports the failure only if a validation walk reaches the
	// bearing reference, while Inline fails outright. An instance that never
	// reaches the reference is accepted by one engine and refused by the
	// other, and both answers are correct.
	reasonDeferredRefMiss skipReason = "the reference does not resolve: Compile tolerates a missing remote document and defers the failure to the validation walk that reaches the reference, while Inline fails at inline time, so the two verdicts are not comparable"

	// Why the substitute pipeline withholds only a reference-free document. A
	// substitute's own references resolve in the context of the document
	// holding the failing reference, while a fetched document's resolve
	// against its own base URI, so a substituted document carrying references
	// is not the schema the resolver would have served.
	reasonSubstituteBaseURI skipReason = "a WithRefFallback substitute's own references resolve against the document holding the failing reference, while a fetched document's resolve against its own base URI, so only a reference-free document can be withheld and substituted"
)

// refVetSentinels are the nine structural-vetting sentinels a materialization
// site can report. Two build failures agree when they match the same subset,
// which distinguishes a negative-bound rejection from an invalid-type one
// without depending on message text.
var refVetSentinels = map[string]error{
	"ErrInvalidType":              jsonschema.ErrInvalidType,
	"ErrNegativeBound":            jsonschema.ErrNegativeBound,
	"ErrNonPositiveMultipleOf":    jsonschema.ErrNonPositiveMultipleOf,
	"ErrNilSubschema":             jsonschema.ErrNilSubschema,
	"ErrConflictingSchemaFields":  jsonschema.ErrConflictingSchemaFields,
	"ErrDuplicatePropertyOrder":   jsonschema.ErrDuplicatePropertyOrder,
	"ErrInvalidID":                jsonschema.ErrInvalidID,
	"ErrMisplacedVocabulary":      jsonschema.ErrMisplacedVocabulary,
	"ErrItemsArrayUnderDraft2020": jsonschema.ErrItemsArrayUnderDraft2020,
}

// refErrSignature names the vetting sentinels err matches, sorted and joined so
// two errors carrying the same causes compare equal regardless of wording. An
// error matching none reports "unclassified", which compares equal to another
// unclassified error and so does not by itself fail a comparison.
func refErrSignature(err error) string {
	var names []string

	for name, sentinel := range refVetSentinels {
		if errors.Is(err, sentinel) {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return "unclassified"
	}

	slices.Sort(names)

	return strings.Join(names, "+")
}

// refOutcomeKind is one pipeline's answer for one instance.
type refOutcomeKind int

const (
	// The pipeline validated the instance.
	refAccept refOutcomeKind = iota

	// The pipeline refused the instance.
	refReject

	// The pipeline never produced a validator, because Compile or Inline
	// refused the schema itself.
	refBuildErr
)

// String names the kind for a failure message.
func (k refOutcomeKind) String() string {
	switch k {
	case refAccept:
		return "accept"
	case refReject:
		return "reject"
	case refBuildErr:
		return "build error"
	default:
		return "unknown"
	}
}

// refOutcome is one pipeline's answer for one instance, with the error behind a
// rejection or a build failure.
type refOutcome struct {
	kind refOutcomeKind
	err  error
}

// refPipeline is one materialization path, built once per schema and then asked
// for a verdict per instance. A pipeline that failed to build carries the
// failure in buildErr and answers refBuildErr for every instance, so a schema
// one engine refuses outright still compares against the other.
type refPipeline struct {
	name      string
	validator *jsonschema.Validator
	buildErr  error
}

// outcome returns the pipeline's answer for one JSON instance.
func (p refPipeline) outcome(ctx context.Context, instance []byte) refOutcome {
	if p.buildErr != nil {
		return refOutcome{kind: refBuildErr, err: p.buildErr}
	}

	err := p.validator.ValidateJSON(ctx, instance)
	if err != nil {
		return refOutcome{kind: refReject, err: err}
	}

	return refOutcome{kind: refAccept}
}

// assertRefEnginesAgree fails unless every pipeline answers identically for the
// instance. Two build failures agree when they carry the same vetting
// sentinels, so a schema both engines refuse for the same structural cause
// passes while a disagreement about the cause does not.
func assertRefEnginesAgree(ctx context.Context, t *testing.T, instance []byte, pipelines ...refPipeline) {
	t.Helper()

	require.GreaterOrEqual(t, len(pipelines), 2, "a differential needs at least two pipelines")

	want := pipelines[0].outcome(ctx, instance)

	for _, pipeline := range pipelines[1:] {
		got := pipeline.outcome(ctx, instance)

		if !assert.Equalf(t, want.kind, got.kind,
			"%s and %s disagree on instance %s\n  %s: %s (%v)\n  %s: %s (%v)",
			pipelines[0].name, pipeline.name, instance,
			pipelines[0].name, want.kind, want.err,
			pipeline.name, got.kind, got.err,
		) {
			continue
		}

		if want.kind == refBuildErr {
			assert.Equalf(t, refErrSignature(want.err), refErrSignature(got.err),
				"%s and %s refuse the schema for different causes\n  %s: %v\n  %s: %v",
				pipelines[0].name, pipeline.name,
				pipelines[0].name, want.err,
				pipeline.name, got.err,
			)
		}
	}
}

// dynamicRefInlinePhrase is the text distinguishing Inline's one documented
// ErrRefInline case from its three other producers, which report "schema not
// interned in the node index", "subschema child count diverged", and
// "substitution exceeded N nested levels". Only the $dynamicRef case carries
// this phrase, and the two invariant violations and the substitute depth limit
// must fail the rig rather than skip it.
//
// The schema graph cannot be inspected instead. A $dynamicRef commonly lives in
// a fetched document that a walk from the root never reaches, which is the case
// for every suite group referencing the Draft 2020-12 metaschema.
// TestInlineDifferentialSkipsAreLive fails if this phrase ever stops matching.
const dynamicRefInlinePhrase = "has no static expansion"

// isDynamicRefInline reports whether err is Inline's refusal to statically
// expand a $dynamicRef.
func isDynamicRefInline(err error) bool {
	return errors.Is(err, jsonschema.ErrRefInline) &&
		strings.Contains(err.Error(), dynamicRefInlinePhrase)
}

// isRefMiss reports whether err is a reference that did not resolve, as opposed
// to one that resolved to a target the structural vet refused. Both wrap
// ErrRefResolve, and only the former makes the engines incomparable.
func isRefMiss(err error) bool {
	if !errors.Is(err, jsonschema.ErrRefResolve) && !errors.Is(err, jsonschema.ErrNotResolved) {
		return false
	}

	return refErrSignature(err) == "unclassified"
}

// refEngines builds the Compile and Inline pipelines for one schema. The second
// return names the reason the schema is not comparable and is empty when it is;
// an Inline failure the rig does not classify fails the test outright, since
// ErrRefInline also covers two internal-invariant violations and the substitute
// depth limit.
func refEngines(
	ctx context.Context,
	t *testing.T,
	schema *jsonschema.Schema,
	compileOpts []jsonschema.ValidateOption,
	inlineOpts []jsonschema.InlineOption,
) ([]refPipeline, skipReason) {
	t.Helper()

	compiled, compileErr := jsonschema.Compile(ctx, schema, compileOpts...)
	compilePipeline := refPipeline{name: "Compile", validator: compiled, buildErr: compileErr}

	inlined, inlineErr := jsonschema.Inline(ctx, schema, inlineOpts...)

	switch {
	case inlineErr == nil:
	case errors.Is(inlineErr, jsonschema.ErrRefCycle):
		return nil, reasonInlineCycle
	case isDynamicRefInline(inlineErr):
		return nil, reasonInlineDynamicRef
	case isRefMiss(inlineErr):
		return nil, reasonDeferredRefMiss
	case errors.Is(inlineErr, jsonschema.ErrRefResolve):
		// A structural rejection of a fetched document or a fallback target.
		// Compile refuses the same schema for the same cause, so the two stay
		// comparable through the build-error outcome.
		return []refPipeline{
			compilePipeline,
			{name: "Inline+Compile", buildErr: inlineErr},
		}, ""

	default:
		require.NoError(t, inlineErr, "Inline failed for a reason the differential does not classify")
	}

	// The inlined schema must be self-contained, so the ref resolver is
	// disabled. ValidateOption is opaque and suiteBaseOpts bundles both
	// resolvers, so the slice cannot be filtered; a trailing nil resolver wins
	// instead, since the option assigns rather than merges, and a nil resolver
	// is documented as restoring local-only resolution. Every other option
	// survives: the metaschema resolver and the format and content gates
	// decide vocabulary and assertion behavior that has nothing to do with
	// $ref, and dropping them reports hundreds of divergences the engines do
	// not have.
	standaloneOpts := make([]jsonschema.ValidateOption, 0, len(compileOpts)+1)
	standaloneOpts = append(standaloneOpts, compileOpts...)
	standaloneOpts = append(standaloneOpts, jsonschema.WithRefResolver(nil))

	standalone, standaloneErr := jsonschema.Compile(ctx, inlined, standaloneOpts...)

	return []refPipeline{
		compilePipeline,
		{name: "Inline+Compile", validator: standalone, buildErr: standaloneErr},
	}, ""
}

// parseRefGraph builds the root schema and the resolver for one differential
// case from JSON text.
func parseRefGraph(t *testing.T, root string, remotes map[string]string) (*jsonschema.Schema, mapResolver) {
	t.Helper()

	schema, err := jsonschema.ParseSchema([]byte(root))
	require.NoError(t, err, "parse the root document")

	resolver := mapResolver{}

	for uri, doc := range remotes {
		parsed, err := jsonschema.ParseSchema([]byte(doc))
		require.NoErrorf(t, err, "parse the remote document %s", uri)

		resolver[uri] = parsed
	}

	return schema, resolver
}

// TestRefEnginesAgreeOnPastFixes runs the reference graph behind every past
// $ref fix through the differential. Each row names the commits whose bug it
// reproduces, so a regression in one of those classes fails here with the graph
// in view rather than waiting for the fuzzer to rediscover it.
func TestRefEnginesAgreeOnPastFixes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root      string
		remotes   map[string]string
		instances []string
	}{
		"fetched document $id clobbers the registry (da61121, 52b5110)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"allOf": [
						{"$ref": "https://example.com/a"},
						{"$ref": "https://example.com/b"},
						{"$ref": "https://example.com/a"}
					]
				}
			`),
			remotes: map[string]string{
				"https://example.com/a": `{"type": "string"}`,
				"https://example.com/b": `{"$id": "https://example.com/a", "type": "integer"}`,
			},
			instances: []string{`"text"`, `42`, `null`, `[]`},
		},
		"items array in a JSON-pointer fallback target (371092b)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$ref": "#/x-custom",
					"x-custom": {"type": "array", "items": [{"type": "string"}]}
				}
			`),
			instances: []string{`[]`, `["a"]`, `[1]`, `"text"`},
		},
		"negative bound in a JSON-pointer fallback target (371092b)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$ref": "#/x-custom",
					"x-custom": {"minLength": -5}
				}
			`),
			instances: []string{`"text"`, `""`, `42`},
		},
		"fallback target vetted during a run (e88e354)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$ref": "https://example.test/late.json#/x-custom/sub"
				}
			`),
			remotes: map[string]string{
				"https://example.test/late.json": `{"x-custom": {"sub": {"minItems": -1}}}`,
			},
			instances: []string{`[]`, `[1]`, `"text"`},
		},
		"fallback target vetted two remote hops out (df730d4)": {
			root: `{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "https://example.test/a.json"}`,
			remotes: map[string]string{
				"https://example.test/a.json": `{"$ref": "https://example.test/b.json"}`,
				"https://example.test/b.json": `{"$ref": "#/examples/0", "examples": [{"type": "strnig"}]}`,
			},
			instances: []string{`"text"`, `42`},
		},
		"fallback target vetted in the inliner's session (575eeff)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}
				}
			`),
			remotes: map[string]string{
				"https://example.test/doc.json": `{"x-shared": {"type": "nteger"}}`,
			},
			instances: []string{`{"p": 1}`, `{"p": "text"}`, `{}`},
		},
		"well-formed unknown-keyword target inlines (575eeff)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"x-shared": {"type": "integer"},
					"properties": {"p": {"$ref": "#/x-shared"}}
				}
			`),
			instances: []string{`{"p": 1}`, `{"p": "text"}`, `{}`, `{"p": null}`},
		},
		"injective JSON-pointer fallback cache key (47fa6df)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$id": "https://example.com/nul-pointer",
					"allOf": [{"$ref": "#/a%00b"}, {"$ref": "#/a/b"}],
					"a\u0000b": {"type": "string"},
					"a": {"b": {"type": "integer"}}
				}
			`),
			instances: []string{`"text"`, `42`, `null`},
		},
		"fallback registries merge first-write-wins (ef68c28)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"allOf": [
						{"$ref": "#/x-first/sub"},
						{"$ref": "#/x-second/sub"},
						{"$ref": "https://example.com/shared"}
					],
					"x-first": {"sub": {"$id": "https://example.com/shared", "type": "integer"}},
					"x-second": {"sub": {"$id": "https://example.com/shared", "type": "string"}}
				}
			`),
			instances: []string{`42`, `"text"`, `null`},
		},
		"anchor resolves under a fetched document's canonical base (dfa3d6b, 9ee414c)": {
			root: `{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "https://fetch.example/doc#myanchor"}`,
			remotes: map[string]string{
				"https://fetch.example/doc": `{"$id": "https://canonical.example/c", "$anchor": "myanchor", "type": "integer"}`,
			},
			instances: []string{`5`, `"text"`, `null`},
		},
		"draft 7 fragment-only $id acts as an anchor (dfa3d6b, 9ee414c)": {
			root: `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "https://fetch.example/d7#myanchor"}`,
			remotes: map[string]string{
				"https://fetch.example/d7": `{"$id": "https://canonical.example/d7", "definitions": {"t": {"$id": "#myanchor", "type": "integer"}}}`,
			},
			instances: []string{`5`, `"text"`, `null`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, resolver := parseRefGraph(t, tc.root, tc.remotes)

			pipelines, reason := refEngines(
				t.Context(), t, schema,
				[]jsonschema.ValidateOption{jsonschema.WithRefResolver(resolver)},
				[]jsonschema.InlineOption{jsonschema.WithRefResolver(resolver)},
			)
			if reason != "" {
				t.Skip(string(reason))
			}

			for _, instance := range tc.instances {
				assertRefEnginesAgree(t.Context(), t, []byte(instance), pipelines...)
			}
		})
	}
}
