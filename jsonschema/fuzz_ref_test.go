package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// The reference differential rig. TestRefEnginesAgreeOnPastFixes asserts the
// agreement property over the graphs behind past fixes, so it only catches
// drift in shapes someone already wrote down, and TestSuiteInlineAgrees runs
// only the graphs the official suite happens to contain. This rig fuzzes the
// graph itself: the document count, each document's $id, the anchor form, the
// reference spelling, and the draft, so the combinations nobody enumerated get
// exercised too.
//
// The generated $id pool deliberately collides with the root's base and with
// the other documents' retrieval URIs, since a fetched document's $id
// overwriting a loaded registry entry is one of the two bug classes this rig
// exists to catch. The unknown-keyword position is drawn for the other: a
// target reachable only through the JSON-pointer fallback, which is the site
// past fixes kept forgetting to vet.

const (
	// The root document's $id, and so the base every relative reference in it
	// absolutizes against.
	refGraphRootURI = "https://example.test/root.json"

	// The one anchor name every document declares, spelled $anchor under Draft
	// 2020-12 and as a fragment-only $id under Draft 7.
	refGraphAnchor = "anc"

	// A URI no document serves, so a reference to it fails to resolve. It is
	// what drives the substitute pipeline and, without one, what makes a graph
	// incomparable under reasonDeferredRefMiss.
	refGraphMissingURI = "https://nowhere.test/missing.json"

	// How many instances one blob draws. Each is checked against every
	// pipeline.
	refGraphInstanceCount = 6

	draft7SchemaURI    = "http://json-schema.org/draft-07/schema#"
	draft2020SchemaURI = "https://json-schema.org/draft/2020-12/schema"
)

var (
	// The retrieval URIs the synthesized remote documents are served under.
	refGraphDocURIs = []string{
		"https://example.test/a.json",
		"https://example.test/b.json",
	}

	// The $id values a synthesized document can declare. The pool overlaps the
	// root's base and both retrieval URIs on purpose: a document whose $id
	// resolves to an already-loaded URI is the shape behind da61121 and
	// 52b5110, where the fetched document silently overwrote the loaded one.
	refGraphIDs = []string{
		"",
		refGraphRootURI,
		"https://example.test/a.json",
		"https://example.test/b.json",
		"https://example.test/other.json",
	}

	// The constraint shapes a drawn target resolves to. They are chosen so the
	// drawn instances tell them apart: a target that resolved to the wrong one
	// changes a verdict rather than passing unnoticed.
	refGraphLeaves = []string{
		`{"type": "string"}`,
		`{"type": "integer"}`,
		`{"type": "integer", "minimum": 3}`,
		`{"type": "string", "minLength": 2}`,
		`{"type": "array", "minItems": 1}`,
		`{"type": "object", "required": ["k"]}`,
		`{"type": "boolean"}`,
		`{"not": {"type": "null"}}`,
		`{"enum": [1, "ab"]}`,
		`{}`,
	}

	// Keywords drawn beside a $ref. Draft 2020-12 keeps them and conjoins the
	// target, Draft 7 ignores them, so they are one of the places the two
	// engines must apply the same draft rule.
	refGraphSiblings = []string{
		"",
		`"type": "string"`,
		`"minLength": 2`,
		`"maxItems": 1`,
	}

	// The values drawn against a generated graph, spread across the seven JSON
	// type names and shaped to straddle the leaf pool's boundaries.
	refGraphInstances = []string{
		`null`, `true`, `false`,
		`0`, `3`, `2`, `4.5`, `-1`,
		`""`, `"a"`, `"ab"`, `"k"`,
		`[]`, `[1]`, `["a", "b"]`,
		`{}`, `{"k": 1}`,
		`{"p0": "ab"}`, `{"p0": 1, "p1": "ab"}`, `{"p0": null}`, `{"p0": [1]}`,
	}
)

// refGraphDoc is one synthesized document.
type refGraphDoc struct {
	// The document text.
	json string

	// True when the document carries a reference of its own, which
	// disqualifies it from being withheld and substituted.
	hasRef bool

	// True when some reference reaches the document by anchor, which also
	// disqualifies it. See reasonSubstituteBaseURI.
	anchorTargeted bool
}

// refGraphSpec is one synthesized multi-document reference graph.
type refGraphSpec struct {
	// The root document text, the schema handed to every engine.
	root string

	// Each retrieval URI mapped to its document text.
	documents map[string]string

	// The URI of a document eligible to be served through a WithRefFallback
	// substitute instead of the resolver, or empty when none qualifies.
	withheld string
}

// synthRefGraph turns an entropy blob into a reference graph. It is total:
// every blob, including an empty or truncated one, yields documents
// ParseSchema accepts, since the cursor zero-extends once the blob runs out.
func synthRefGraph(blob []byte) refGraphSpec {
	cursor := fuzzfill.NewCursor(blob)

	draft2020 := cursor.Bool()

	// Draft 7 spells the reserved definition map "definitions" and has no
	// $anchor; its anchor form is a fragment-only $id on a sub-schema, which
	// is why the two drafts cannot share one document shape.
	defsKey := "definitions"
	schemaURI := draft7SchemaURI

	if draft2020 {
		defsKey = "$defs"
		schemaURI = draft2020SchemaURI
	}

	docCount := cursor.Intn(len(refGraphDocURIs) + 1)
	docs := make(map[string]*refGraphDoc, docCount)
	served := make([]string, 0, docCount)

	for i := range docCount {
		uri := refGraphDocURIs[i]
		id := refGraphIDs[cursor.Intn(len(refGraphIDs))]

		// A document referencing another exercises cross-document base URI
		// resolution, and rules itself out of the substitute pipeline.
		var innerRef string

		if cursor.Bool() && docCount > 1 {
			innerRef = refGraphDocURIs[cursor.Intn(len(refGraphDocURIs))]
		}

		docs[uri] = &refGraphDoc{
			json:   buildRefDoc(cursor, draft2020, defsKey, id, innerRef),
			hasRef: innerRef != "",
		}
		served = append(served, uri)
	}

	refCount := 1 + cursor.Intn(3)
	refs := make([]string, 0, refCount)

	for range refCount {
		ref, target, byAnchor := drawRefSpelling(cursor, defsKey, served)
		refs = append(refs, ref)

		if doc, ok := docs[target]; ok && byAnchor {
			doc.anchorTargeted = true
		}
	}

	spec := refGraphSpec{
		root:      buildRefRoot(cursor, draft2020, defsKey, schemaURI, refs),
		documents: make(map[string]string, len(docs)),
	}

	for uri, doc := range docs {
		spec.documents[uri] = doc.json
	}

	// Only a reference-free document that nothing reaches by anchor can be
	// withheld: reasonSubstituteBaseURI. The first qualifying URI in retrieval
	// order is chosen, so the choice stays a function of the blob.
	for _, uri := range served {
		doc := docs[uri]
		if !doc.hasRef && !doc.anchorTargeted && strings.Contains(spec.root, uri) {
			spec.withheld = uri

			break
		}
	}

	return spec
}

// buildRefDoc renders one remote document: an optional $id, a definition map of
// two leaves, an anchored leaf, an unknown keyword holding a leaf only the
// JSON-pointer fallback reaches, and an optional reference to another document.
func buildRefDoc(cursor *fuzzfill.Cursor, draft2020 bool, defsKey, id, innerRef string) string {
	fields := make([]string, 0, 6)

	if id != "" {
		fields = append(fields, jsonField("$id", quoteJSON(id)))
	}

	if draft2020 {
		fields = append(fields, jsonField("$anchor", quoteJSON(refGraphAnchor)))
	}

	anchored := drawLeaf(cursor)
	if !draft2020 {
		// Draft 7's anchor is a fragment-only $id, which must sit on a
		// sub-schema rather than the document root, where $id is the base.
		anchored = mergeIntoObject(anchored, jsonField("$id", quoteJSON("#"+refGraphAnchor)))
	}

	defs := "{" + strings.Join([]string{
		jsonField("d0", drawLeaf(cursor)),
		jsonField("d1", drawLeaf(cursor)),
		jsonField(refGraphAnchor, anchored),
	}, ",") + "}"

	fields = append(fields,
		jsonField(defsKey, defs),
		jsonField("x-custom", "{"+jsonField("sub", drawLeaf(cursor))+"}"),
	)

	if innerRef != "" {
		fields = append(fields, jsonField("allOf", "[{"+jsonField("$ref", quoteJSON(innerRef))+"}]"))
	}

	return "{" + strings.Join(fields, ",") + "}"
}

// buildRefRoot renders the root document, placing the drawn references in
// properties and allOf so both a keyword-scoped and a conjoined position are
// covered.
func buildRefRoot(cursor *fuzzfill.Cursor, draft2020 bool, defsKey, schemaURI string, refs []string) string {
	anchored := drawLeaf(cursor)
	if !draft2020 {
		anchored = mergeIntoObject(anchored, jsonField("$id", quoteJSON("#"+refGraphAnchor)))
	}

	defs := "{" + strings.Join([]string{
		jsonField("d0", drawLeaf(cursor)),
		jsonField("d1", drawLeaf(cursor)),
		jsonField(refGraphAnchor, anchored),
	}, ",") + "}"

	fields := []string{
		jsonField("$schema", quoteJSON(schemaURI)),
		jsonField("$id", quoteJSON(refGraphRootURI)),
		jsonField(defsKey, defs),
		jsonField("x-custom", "{"+jsonField("sub", drawLeaf(cursor))+"}"),
	}

	if draft2020 {
		fields = append(fields, jsonField("$anchor", quoteJSON(refGraphAnchor)))
	}

	properties := make([]string, 0, len(refs))
	allOf := make([]string, 0, len(refs))

	for i, ref := range refs {
		node := "{" + jsonField("$ref", quoteJSON(ref))

		if sibling := refGraphSiblings[cursor.Intn(len(refGraphSiblings))]; sibling != "" {
			node += "," + sibling
		}

		node += "}"

		if cursor.Bool() {
			allOf = append(allOf, node)

			continue
		}

		properties = append(properties, jsonField("p"+string(rune('0'+i)), node))
	}

	if len(properties) > 0 {
		fields = append(fields, jsonField("properties", "{"+strings.Join(properties, ",")+"}"))
	}

	if len(allOf) > 0 {
		fields = append(fields, jsonField("allOf", "["+strings.Join(allOf, ",")+"]"))
	}

	return "{" + strings.Join(fields, ",") + "}"
}

// drawRefSpelling picks one reference spelling. The forms cover a
// same-document pointer and anchor, a whole remote document, a remote pointer
// and anchor, the two unknown-keyword positions only the JSON-pointer fallback
// reaches, and a URI no document serves. It returns the reference, the
// retrieval URI it targets (empty for a same-document or unresolvable form),
// and whether it targets by anchor.
func drawRefSpelling(cursor *fuzzfill.Cursor, defsKey string, served []string) (string, string, bool) {
	local := []string{
		"#/" + defsKey + "/d0",
		"#/" + defsKey + "/d1",
		"#" + refGraphAnchor,
		"#/x-custom/sub",
	}

	if len(served) == 0 {
		return local[cursor.Intn(len(local))], "", false
	}

	uri := served[cursor.Intn(len(served))]

	switch cursor.Intn(9) {
	case 0, 1, 2, 3:
		return local[cursor.Intn(len(local))], "", false
	case 4:
		return uri, uri, false
	case 5:
		return uri + "#/" + defsKey + "/d0", uri, false
	case 6:
		return uri + "#" + refGraphAnchor, uri, true
	case 7:
		return uri + "#/x-custom/sub", uri, false
	default:
		return refGraphMissingURI, "", false
	}
}

// drawLeaf picks one constraint shape from the pool.
func drawLeaf(cursor *fuzzfill.Cursor) string {
	return refGraphLeaves[cursor.Intn(len(refGraphLeaves))]
}

// jsonField renders one object member from a name and rendered value.
func jsonField(name, value string) string {
	return quoteJSON(name) + ":" + value
}

// quoteJSON renders s as a JSON string.
func quoteJSON(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		// A Go string always marshals; the branch keeps the error checked.
		return `""`
	}

	return string(encoded)
}

// mergeIntoObject adds a member to a rendered JSON object, returning a fresh
// object when the value is not one (the pool's `{}` included).
func mergeIntoObject(object, field string) string {
	trimmed := strings.TrimSpace(object)
	if !strings.HasPrefix(trimmed, "{") {
		return "{" + field + "}"
	}

	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return "{" + field + "}"
	}

	return "{" + field + "," + inner + "}"
}

// synthRefInstances draws the instances one graph is checked against.
func synthRefInstances(blob []byte) [][]byte {
	cursor := fuzzfill.NewCursor(blob)
	instances := make([][]byte, 0, refGraphInstanceCount)

	for range refGraphInstanceCount {
		instances = append(instances, []byte(refGraphInstances[cursor.Intn(len(refGraphInstances))]))
	}

	return instances
}

// pointerInto resolves a JSON Pointer within a raw document and returns the
// schema at that position. It walks the decoded JSON by hand rather than
// through the package's own pointer code, so the substitute the third pipeline
// splices is built independently of the resolution it checks.
func pointerInto(t *testing.T, document, pointer string) (*jsonschema.Schema, bool) {
	t.Helper()

	var decoded any

	require.NoError(t, json.Unmarshal([]byte(document), &decoded), "decode a synthesized document")

	for token := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := decoded.(map[string]any)
		if !ok {
			return nil, false
		}

		// RFC 6901 unescaping, ~1 before ~0 so an encoded tilde survives.
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")

		decoded, ok = object[token]
		if !ok {
			return nil, false
		}
	}

	encoded, err := json.Marshal(decoded)
	require.NoError(t, err, "re-encode a pointer target")

	schema, err := jsonschema.ParseSchema(encoded)
	if err != nil {
		return nil, false
	}

	return schema, true
}

// substitutePipeline builds the third pipeline, which withholds one document
// from the resolver and serves it through a WithRefFallback substitute instead.
// Serving a document either way must reach the same verdicts, which is what
// exercises the substitute registration path.
//
// The substitute is a function of the failing reference, not a fixed schema:
// one withheld document can be targeted by several references with different
// fragments, and a fragment reference must expand to the fragment's target
// rather than to the whole document.
func substitutePipeline(
	ctx context.Context,
	t *testing.T,
	spec refGraphSpec,
	schema *jsonschema.Schema,
	compileOpts []jsonschema.ValidateOption,
) (refPipeline, bool) {
	t.Helper()

	if spec.withheld == "" {
		return refPipeline{}, false
	}

	partial := mapResolver{}

	for uri, document := range spec.documents {
		if uri == spec.withheld {
			continue
		}

		parsed, err := jsonschema.ParseSchema([]byte(document))
		require.NoErrorf(t, err, "parse the remote document %s", uri)

		partial[uri] = parsed
	}

	withheldText := spec.documents[spec.withheld]

	fallback := jsonschema.RefFallbackFunc(
		func(_ context.Context, failure jsonschema.RefFailure) jsonschema.RefAction {
			base, fragment, hasFragment := strings.Cut(failure.Ref, "#")
			if base != spec.withheld {
				return jsonschema.PropagateRef()
			}

			if !hasFragment || fragment == "" {
				parsed, err := jsonschema.ParseSchema([]byte(withheldText))
				if err != nil {
					return jsonschema.PropagateRef()
				}

				return jsonschema.SubstituteRef(parsed)
			}

			if !strings.HasPrefix(fragment, "/") {
				// An anchor fragment needs the withheld document's anchor
				// registry, which the substitute path does not build. See
				// reasonSubstituteBaseURI: the generator never withholds an
				// anchor-targeted document, so this is unreachable.
				return jsonschema.PropagateRef()
			}

			target, ok := pointerInto(t, withheldText, fragment)
			if !ok {
				return jsonschema.PropagateRef()
			}

			return jsonschema.SubstituteRef(target)
		})

	inlineOpts := []jsonschema.InlineOption{
		jsonschema.WithRefResolver(partial),
		jsonschema.WithRefFallback(fallback),
	}

	pipelines, reason := refEngines(ctx, t, schema, compileOpts, inlineOpts)
	if reason != "" {
		return refPipeline{}, false
	}

	substituted := pipelines[len(pipelines)-1]
	substituted.name = "Inline+Substitute"

	return substituted, true
}

// refGraphBlobs returns n deterministic entropy blobs of varying length, the
// population the reachability guard samples. A fixed-seed xorshift keeps the
// population identical across runs and toolchains, which math/rand's globally
// seeded source does not, so a failure reproduces exactly.
func refGraphBlobs(n int) [][]byte {
	state := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17

		return state
	}

	blobs := make([][]byte, 0, n)

	for range n {
		blob := make([]byte, 8+int(next()%120))
		for i := range blob {
			blob[i] = byte(next())
		}

		blobs = append(blobs, blob)
	}

	return blobs
}

// TestRefGraphSynthesisReachesEveryForm pins that the generator still draws
// every shape the rig depends on. A generator edit that silently stops emitting
// a reference spelling, a draft, or a substitutable document would leave
// FuzzRefEnginesAgree green while covering less, which is the failure mode this
// guard exists to prevent.
func TestRefGraphSynthesisReachesEveryForm(t *testing.T) {
	t.Parallel()

	const draws = 4000

	seen := map[string]int{}

	for _, blob := range refGraphBlobs(draws) {
		spec := synthRefGraph(blob)

		if strings.Contains(spec.root, draft7SchemaURI) {
			seen["draft 7"]++
		} else {
			seen["draft 2020-12"]++
		}

		if spec.withheld != "" {
			seen["a substitutable document"]++
		}

		for form, text := range map[string]string{
			"a same-document pointer reference":  "#/$defs/d0",
			"a draft 7 definitions reference":    "#/definitions/d0",
			"an anchor reference":                "#" + refGraphAnchor,
			"an unknown-keyword reference":       "#/x-custom/sub",
			"an unresolvable reference":          refGraphMissingURI,
			"a reference to the first document":  refGraphDocURIs[0],
			"a reference to the second document": refGraphDocURIs[1],
		} {
			if strings.Contains(spec.root, text) {
				seen[form]++
			}
		}

		for _, document := range spec.documents {
			if strings.Contains(document, `"$id"`) {
				seen["a document declaring $id"]++
			}

			if strings.Contains(document, `"$ref"`) {
				seen["a document carrying its own reference"]++
			}
		}
	}

	for _, form := range []string{
		"draft 7",
		"draft 2020-12",
		"a substitutable document",
		"a same-document pointer reference",
		"a draft 7 definitions reference",
		"an anchor reference",
		"an unknown-keyword reference",
		"an unresolvable reference",
		"a reference to the first document",
		"a reference to the second document",
		"a document declaring $id",
		"a document carrying its own reference",
	} {
		assert.NotZerof(t, seen[form], "the generator draws %s in none of %d blobs", form, draws)
	}
}

// TestSubstituteDoesNotRebaseNestedRefs pins the carve-out behind
// reasonSubstituteBaseURI, which is why synthRefGraph withholds only a
// document carrying no reference of its own.
//
// A document served by the resolver resolves its own relative references
// against its retrieval URI. The same document handed back as a
// WithRefFallback substitute is spliced at the failing reference instead, so
// its relative references resolve against that document's base. Here the two
// bases differ by one path segment, so the substituted copy looks for a
// document nobody serves. Were Inline to start rebasing a substitute onto the
// document it stands in for, this test fails and the generator could withhold
// any document.
func TestSubstituteDoesNotRebaseNestedRefs(t *testing.T) {
	t.Parallel()

	const (
		rootURI = "https://example.test/root.json"
		docURI  = "https://example.test/sub/doc.json"
		leafURI = "https://example.test/sub/leaf.json"
	)

	root, resolver := parseRefGraph(t,
		`{"$schema": "`+draft2020SchemaURI+`", "$id": "`+rootURI+`", "$ref": "`+docURI+`"}`,
		map[string]string{
			// The relative reference resolves against the document's own
			// retrieval base, https://example.test/sub/.
			docURI:  `{"$ref": "leaf.json"}`,
			leafURI: `{"type": "integer"}`,
		})

	served, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "the resolver serves the document, so its relative reference resolves")

	standalone, err := jsonschema.Compile(t.Context(), served)
	require.NoError(t, err, "the served document inlines to a self-contained schema")
	require.NoError(t, standalone.ValidateJSON(t.Context(), []byte(`5`)),
		"the inlined leaf accepts an integer")
	require.Error(t, standalone.ValidateJSON(t.Context(), []byte(`"x"`)),
		"the inlined leaf refuses a string, so the relative reference really resolved")

	withheld, err := jsonschema.ParseSchema([]byte(`{"$ref": "leaf.json"}`))
	require.NoError(t, err)

	partial := mapResolver{leafURI: resolver[leafURI]}

	_, err = jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(partial),
		jsonschema.WithRefFallback(jsonschema.RefFallbackFunc(
			func(_ context.Context, failure jsonschema.RefFailure) jsonschema.RefAction {
				if failure.Ref != docURI {
					return jsonschema.PropagateRef()
				}

				return jsonschema.SubstituteRef(withheld)
			})))

	require.ErrorIs(t, err, jsonschema.ErrRefResolve, string(reasonSubstituteBaseURI))
}

// TestSubstitutePipelineBuilds pins that the third pipeline actually runs on
// synthesized graphs. The builder declines silently when a graph has no
// withheld document, or when the fallback does not cover every failing
// reference, so without this guard the substitute path could stop being
// exercised while FuzzRefEnginesAgree stayed green.
func TestSubstitutePipelineBuilds(t *testing.T) {
	t.Parallel()

	const draws = 300

	built := 0

	for _, blob := range refGraphBlobs(draws) {
		spec := synthRefGraph(blob)
		if spec.withheld == "" {
			continue
		}

		schema, resolver := parseRefGraph(t, spec.root, spec.documents)
		compileOpts := []jsonschema.ValidateOption{jsonschema.WithRefResolver(resolver)}

		if _, ok := substitutePipeline(t.Context(), t, spec, schema, compileOpts); ok {
			built++
		}
	}

	assert.NotZerof(t, built, "the substitute pipeline builds for none of %d graphs", draws)
}

// FuzzRefEnginesAgree asserts that Compile, Inline, and the substitute path
// agree on every instance of a synthesized reference graph. A disagreement is a
// bug in one of the three sites that materialize a $ref target.
//
// The f.Fuzz callback cannot call t.Parallel (the fuzzing framework forbids
// it), the one documented exemption from the t.Parallel-everywhere convention.
func FuzzRefEnginesAgree(f *testing.F) {
	addRefGraphSeeds(f)

	f.Fuzz(func(t *testing.T, graph, instances []byte) {
		spec := synthRefGraph(graph)

		schema, resolver := parseRefGraph(t, spec.root, spec.documents)

		compileOpts := []jsonschema.ValidateOption{jsonschema.WithRefResolver(resolver)}
		inlineOpts := []jsonschema.InlineOption{jsonschema.WithRefResolver(resolver)}

		pipelines, reason := refEngines(t.Context(), t, schema, compileOpts, inlineOpts)
		if reason != "" {
			// A documented Inline limitation or an unresolvable reference; the
			// pipelines are not comparable for this graph.
			return
		}

		if substituted, ok := substitutePipeline(t.Context(), t, spec, schema, compileOpts); ok {
			pipelines = append(pipelines, substituted)
		}

		for _, instance := range synthRefInstances(instances) {
			assertRefEnginesAgree(t.Context(), t, instance, pipelines...)
		}
	})
}

// addRefGraphSeeds seeds the corpus with blobs spanning the draws: an exhausted
// cursor (every choice its zero), a saturated one, and mixed patterns between.
// The graphs behind past fixes are pinned by TestRefEnginesAgreeOnPastFixes
// instead, since a corpus entry for a []byte argument is entropy and no blob
// can be written by hand to decode to a chosen graph.
func addRefGraphSeeds(f *testing.F) {
	f.Helper()

	values := bytes.Repeat([]byte{0x1f, 0x03, 0x7a, 0xc1}, 16)

	f.Add([]byte{}, []byte{})
	f.Add(make([]byte, 96), make([]byte, 32))
	f.Add(bytes.Repeat([]byte{0xff}, 96), bytes.Repeat([]byte{0xff}, 32))
	f.Add(bytes.Repeat([]byte{0x01}, 96), values)
	f.Add(bytes.Repeat([]byte{0x02, 0x11}, 48), values)
	f.Add([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98}, values)
}
