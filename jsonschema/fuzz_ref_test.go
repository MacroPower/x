package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// Rig 3 -- Compile vs Inline vs the substitute path, over a synthesized
// reference graph. The property:
//
//	Compile, Inline, and the substitute path must reach the same verdict on
//	every instance of one reference graph.
//
// TestRefEnginesAgreeOnPastFixes asserts that property over the graphs behind
// past fixes, so it only catches drift in graphs someone already wrote down,
// and TestSuiteInlineAgrees runs only the graphs the official suite happens to
// contain. This rig fuzzes the graph itself: the document count, each
// document's $id, the anchor form, the reference spelling, and the draft, so
// the combinations nobody enumerated get exercised too.
//
// The generated $id pool deliberately collides with the root's base and with
// the other documents' retrieval URIs, since a fetched document claiming a URI
// another document already holds is one of the two bug classes this rig exists
// to catch. It also holds each document's own retrieval URI, the near-universal
// remote that claims nothing, so the draw covers both sides of that rule. The
// unknown-keyword position is drawn for the other bug class, a target reachable
// only through the JSON-pointer fallback, which is the site past fixes kept
// forgetting to vet.

const (
	// The root document's $id, and so the base every relative reference in it
	// absolutizes against.
	refGraphRootURI = "https://example.test/root.json"

	// The one anchor name every document declares, spelled $anchor under Draft
	// 2020-12 and as a fragment-only $id under Draft 7.
	refGraphAnchor = "anc"

	// A URI no document serves. A reference to it fails to resolve, so Inline
	// refuses the graph and the rig discards the iteration under
	// reasonDeferredRefMiss.
	refGraphMissingURI = "https://nowhere.test/missing.json"

	// How many instances one blob draws. Each is checked against every
	// pipeline.
	refGraphInstanceCount = 6

	// How many leaves one document renders: two definitions, the anchored
	// definition, and the unknown-keyword target.
	refGraphLeavesPerDoc = 4

	draft7SchemaURI    = "http://json-schema.org/draft-07/schema#"
	draft2020SchemaURI = "https://json-schema.org/draft/2020-12/schema"
)

var (
	// The retrieval URIs the synthesized remote documents are served under.
	refGraphDocURIs = []string{
		"https://example.test/a.json",
		"https://example.test/b.json",
	}

	// The $id values a synthesized document can declare. A document declaring a
	// canonical $id that differs from the URI it was fetched from is the graph
	// behind dfa3d6b and 9ee414c, where an anchor registered under the
	// canonical base had to be found through the retrieval URI.
	//
	// The pool includes the root's URI and both retrieval URIs, so a draw can
	// make one document claim a URI another already holds. Both engines refuse
	// that graph with ErrIDCollision, which refBuildSentinels makes comparable
	// by cause, so the draw searches colliding graphs for a disagreement.
	refGraphIDs = append([]string{
		"",
		"https://example.test/other.json",
		"https://elsewhere.test/doc.json",
		refGraphRootURI,
	}, refGraphDocURIs...)

	// The constraint shapes a drawn target resolves to. The pool keeps every
	// shape distinguishable by the drawn instances, so a target that resolved
	// to the wrong one changes a verdict rather than passing unnoticed.
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

	// Constraint shapes the structural vet refuses. The misspelled type names
	// are deliberate. Reaching these is the only way a synthesized graph
	// produces the build-error outcome, so they keep refBuildSentinels and the
	// build-error comparison exercised rather than dead.
	refGraphMalformedLeaves = []string{
		`{"type": "strnig"}`,
		`{"type": "nteger"}`,
		`{"minLength": -1}`,
		`{"maxItems": -3}`,
		`{"multipleOf": 0}`,
	}

	// Keywords drawn beside a $ref. Draft 2020-12 keeps them and conjoins the
	// target while Draft 7 ignores them, so they are one of the places the two
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
		`{"p0": "ab", "p1": 3, "p2": "k"}`, `{"p2": [1]}`,
	}

	// The refBuildSentinels map holds the sentinels a materialization site can
	// report when it refuses a schema outright. Two build failures agree when
	// they match the same subset, which tells a negative-bound rejection from an
	// invalid-type one without depending on message text. The structural-vetting
	// sentinels carry that comparison; the rest are the other ways Compile
	// refuses a document.
	//
	// The table also decides what isRefMiss counts as a miss, since a miss is an
	// error wrapping ErrRefResolve that matches nothing here. A sentinel that can
	// travel with ErrRefResolve reclassifies a genuine miss as a build failure, so
	// only a sentinel both engines report on the same graph belongs here.
	// ErrIDCollision qualifies, because the registration refuses the document at
	// both engines and no later fetch changes the answer, so neither defers it.
	refBuildSentinels = map[string]error{
		"ErrInvalidType":              jsonschema.ErrInvalidType,
		"ErrNegativeBound":            jsonschema.ErrNegativeBound,
		"ErrNonPositiveMultipleOf":    jsonschema.ErrNonPositiveMultipleOf,
		"ErrNilSubschema":             jsonschema.ErrNilSubschema,
		"ErrConflictingSchemaFields":  jsonschema.ErrConflictingSchemaFields,
		"ErrDuplicatePropertyOrder":   jsonschema.ErrDuplicatePropertyOrder,
		"ErrInvalidID":                jsonschema.ErrInvalidID,
		"ErrMisplacedVocabulary":      jsonschema.ErrMisplacedVocabulary,
		"ErrItemsArrayUnderDraft2020": jsonschema.ErrItemsArrayUnderDraft2020,
		"ErrSchemaCycle":              jsonschema.ErrSchemaCycle,
		"ErrIDCollision":              jsonschema.ErrIDCollision,
		"ErrUnsupportedDraft":         jsonschema.ErrUnsupportedDraft,
		"ErrUnknownVocabulary":        jsonschema.ErrUnknownVocabulary,
		"ErrInvalidSchemaDocument":    jsonschema.ErrInvalidSchemaDocument,
		"ErrNilSchema":                jsonschema.ErrNilSchema,
	}
)

// refGraphRef is one reference drawn into the root, with everything about its
// placement decided before any leaf is drawn.
type refGraphRef struct {
	// The reference value, one of the spellings drawRefSpelling returns.
	spelling string

	// A keyword rendered beside the $ref, empty for a bare reference. The two
	// drafts treat siblings differently, so this is one of the places the
	// engines must apply the same draft rule.
	sibling string

	// True to place the reference in the root's allOf, false for properties.
	inAllOf bool

	// The property name the reference takes when it lands in properties.
	name string
}

// refGraphDoc is one synthesized document.
type refGraphDoc struct {
	// The document text.
	json string

	// True when the document carries a reference of its own, which
	// disqualifies it from being withheld and substituted.
	hasRef bool

	// True when some reference reaches the document by anchor, which also
	// disqualifies it. See reasonSubstituteNoAnchors.
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

	// True when some reference names refGraphMissingURI. Withholding a
	// document then leaves two unresolvable references, and the substitute
	// covers only one, so the substitute pipeline legitimately declines.
	unresolvable bool

	// True when the malformed leaf sits in a document no reference reaches
	// directly, in which case chooseWithheld withholds nothing and the
	// substitute pipeline never runs on the graph.
	malformedTransitive bool
}

// synthRefGraph turns an entropy blob into a reference graph. It never fails:
// every blob, including an empty or truncated one, yields documents ParseSchema
// accepts, since the cursor zero-extends once the blob runs out.
func synthRefGraph(blob []byte) refGraphSpec {
	cursor := fuzzfill.NewCursor(blob)
	gen := &refGraphGen{cursor: cursor}

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

	// Every structural choice is drawn before any leaf: the document count,
	// each $id, each inner reference, every reference spelling and its
	// placement, and where the malformed leaf goes. Leaves pad the documents
	// and consume whatever entropy is left. The cursor zero-extends past the
	// end of the blob, so a choice drawn after the padding would collapse to
	// its first option on every short blob.
	docCount := cursor.Intn(len(refGraphDocURIs) + 1)
	served := refGraphDocURIs[:docCount]

	ids := make([]string, docCount)
	innerRefs := make([]string, docCount)

	for i := range docCount {
		ids[i] = refGraphIDs[cursor.Intn(len(refGraphIDs))]

		// A document referencing another exercises cross-document base URI
		// resolution, and rules itself out of the substitute pipeline.
		if docCount > 1 && cursor.Bool() {
			innerRefs[i] = refGraphDocURIs[cursor.Intn(len(refGraphDocURIs))]
		}
	}

	refCount := 1 + cursor.Intn(3)
	refs := make([]refGraphRef, 0, refCount)
	anchorTargeted := map[string]bool{}
	directlyReferenced := map[string]bool{}
	unresolvable := false

	for i := range refCount {
		spelling, target, byAnchor := drawRefSpelling(cursor, defsKey, served)

		refs = append(refs, refGraphRef{
			spelling: spelling,
			sibling:  refGraphSiblings[cursor.Intn(len(refGraphSiblings))],
			inAllOf:  cursor.Bool(),
			name:     "p" + strconv.Itoa(i),
		})

		if spelling == refGraphMissingURI {
			unresolvable = true
		}

		if target != "" {
			directlyReferenced[target] = true

			if byAnchor {
				anchorTargeted[target] = true
			}
		}
	}

	gen.planMalformed(served)

	docs := make(map[string]*refGraphDoc, docCount)

	for i, uri := range served {
		docs[uri] = &refGraphDoc{
			json:           buildRefDoc(gen, uri, draft2020, defsKey, ids[i], innerRefs[i]),
			hasRef:         innerRefs[i] != "",
			anchorTargeted: anchorTargeted[uri],
		}
	}

	spec := refGraphSpec{
		root:         buildRefRoot(gen, draft2020, defsKey, schemaURI, refs),
		documents:    make(map[string]string, len(docs)),
		unresolvable: unresolvable,
	}

	for uri, doc := range docs {
		spec.documents[uri] = doc.json
	}

	spec.malformedTransitive = gen.malformedDoc != "" &&
		gen.malformedDoc != refGraphNoMalformed &&
		!directlyReferenced[gen.malformedDoc]

	spec.withheld = chooseWithheld(
		served, docs, ids, gen.malformedDoc, spec.malformedTransitive, directlyReferenced,
	)

	return spec
}

// chooseWithheld names the document the substitute pipeline may serve through a
// WithRefFallback substitute instead of the resolver, or returns empty when no
// document qualifies. Four conditions disqualify a document, and each rules out
// a false divergence rather than a real one:
//
//   - It carries a reference of its own (reasonSubstituteBaseURI).
//   - Some reference reaches it by anchor (reasonSubstituteNoAnchors).
//   - It holds the malformed leaf. The resolver serves a whole document and
//     both engines vet all of it, while a substitute stands in for one
//     reference and materializes only that target, so a violation elsewhere in
//     the document reaches one path and not the other.
//   - It takes part in an $id collision, by claiming a URI another document
//     already holds (the root's, or another served document's retrieval URI),
//     by having its own claimed, or by sharing an $id with another served
//     document. Withholding removes it from the resolver, so the collision the
//     other two pipelines refuse never happens on this one.
//
// No document qualifies at all when the malformed leaf sits in a document no
// reference reaches directly (reasonSubstituteTransitiveMalformed). The
// substitute pipeline configures a fallback, which suspends the walk's
// refusals for a structural violation, so the graph would compare a Compile
// refusal against an Inline that accepts.
//
// The document must also be referenced from the root, or withholding it changes
// nothing. The first qualifying URI in retrieval order wins, so the choice stays
// a function of the blob.
func chooseWithheld(
	served []string,
	docs map[string]*refGraphDoc,
	ids []string,
	malformedDoc string,
	malformedTransitive bool,
	directlyReferenced map[string]bool,
) string {
	// The substitute pipeline runs with a fallback configured, under which
	// Inline reports nothing for a document only the closure walk reaches. See
	// reasonSubstituteTransitiveMalformed.
	if malformedTransitive {
		return ""
	}

	claims := map[string]int{}
	for _, id := range ids {
		claims[id]++
	}

	for i, uri := range served {
		doc := docs[uri]

		switch {
		case doc.hasRef, doc.anchorTargeted:
			continue
		case uri == malformedDoc:
			continue
		case !directlyReferenced[uri]:
			continue
		case inCollision(claims, served, ids, i):
			// Another document claims this one's retrieval URI as its $id, or
			// this one claims a URI another document already holds.
			continue
		case ids[i] != "" && claims[ids[i]] > 1:
			// Two served documents claim this $id, and only one wins.
			continue
		}

		return uri
	}

	return ""
}

// buildRefDoc renders one remote document: an optional $id, a definition map
// holding two leaves and an anchored third, an unknown keyword holding a leaf
// only the JSON-pointer fallback reaches, and an optional reference to another
// document.
func buildRefDoc(gen *refGraphGen, uri string, draft2020 bool, defsKey, id, innerRef string) string {
	gen.enter(uri)

	fields := make([]string, 0, 6)

	if id != "" {
		fields = append(fields, jsonField("$id", quoteJSON(id)))
	}

	anchored := anchoredLeaf(gen, draft2020)

	defs := "{" + strings.Join([]string{
		jsonField("d0", gen.drawLeaf()),
		jsonField("d1", gen.drawLeaf()),
		jsonField(refGraphAnchor, anchored),
	}, ",") + "}"

	fields = append(fields,
		jsonField(defsKey, defs),
		jsonField("x-custom", "{"+jsonField("sub", gen.drawLeaf())+"}"),
	)

	if innerRef != "" {
		fields = append(fields, jsonField("allOf", "[{"+jsonField("$ref", quoteJSON(innerRef))+"}]"))
	}

	return "{" + strings.Join(fields, ",") + "}"
}

// buildRefRoot renders the root document, placing the drawn references in
// properties and allOf so both a keyword-scoped and a conjoined position are
// covered.
func buildRefRoot(gen *refGraphGen, draft2020 bool, defsKey, schemaURI string, refs []refGraphRef) string {
	gen.enter("")

	anchored := anchoredLeaf(gen, draft2020)

	defs := "{" + strings.Join([]string{
		jsonField("d0", gen.drawLeaf()),
		jsonField("d1", gen.drawLeaf()),
		jsonField(refGraphAnchor, anchored),
	}, ",") + "}"

	fields := []string{
		jsonField("$schema", quoteJSON(schemaURI)),
		jsonField("$id", quoteJSON(refGraphRootURI)),
		jsonField(defsKey, defs),
		jsonField("x-custom", "{"+jsonField("sub", gen.drawLeaf())+"}"),
	}

	properties := make([]string, 0, len(refs))
	allOf := make([]string, 0, len(refs))

	for _, ref := range refs {
		node := "{" + jsonField("$ref", quoteJSON(ref.spelling))

		if ref.sibling != "" {
			node += "," + ref.sibling
		}

		node += "}"

		if ref.inAllOf {
			allOf = append(allOf, node)

			continue
		}

		properties = append(properties, jsonField(ref.name, node))
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
		"#/" + defsKey + "/" + refGraphAnchor,
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

// refGraphGen draws one graph. It carries the malformed-leaf plan alongside the
// cursor, since a document renders its leaves through two helpers and the plan
// has to name one leaf across all of them.
type refGraphGen struct {
	cursor *fuzzfill.Cursor

	// The document that holds the malformed leaf: empty for the root, a
	// retrieval URI for a remote, and refGraphNoMalformed when the graph draws
	// none at all.
	malformedDoc string

	// Which of the document's leaves is the malformed one, indexed in draw
	// order.
	malformedSlot int

	// The document being rendered and how many leaves it has drawn.
	current string
	slot    int
}

// refGraphNoMalformed marks a graph that draws no malformed leaf. The empty
// string cannot serve, since it names the root.
const refGraphNoMalformed = "\x00none"

// planMalformed decides whether the graph carries a malformed leaf and, if so,
// which leaf of which document. It is drawn once, before any leaf, so the
// fuzzer can flip it as a single structural bit.
//
// One leaf in twenty graphs, because a malformed leaf refuses the whole schema:
// every instance then compares a refusal and no reference resolves, so a high
// rate trades the rig's validation coverage for its build-error coverage.
//
// The candidates are the root and every served document, reached directly or
// only through another document's own reference. Both engines walk the whole
// reference closure and vet every document in it, so both refuse the same
// graph for the same cause wherever the leaf lands.
//
// Every slot is drawable on every graph, colliding $ids included. Each engine
// vets a JSON-pointer fallback target where its own session materializes it, so
// both meet the unknown-keyword slot at the same point in the shared closure
// walk. In a graph pairing a colliding document with a malformed target, both
// engines name the fault the walk reaches first, which is what makes the slot
// comparable. TestRefEnginesAgreeOnCollisionBesideMalformedTarget pins that
// pairing deterministically.
func (g *refGraphGen) planMalformed(served []string) {
	g.malformedDoc = refGraphNoMalformed

	if g.cursor.Intn(20) != 0 {
		return
	}

	candidates := append([]string{""}, served...)
	doc := candidates[g.cursor.Intn(len(candidates))]

	g.malformedDoc, g.malformedSlot = doc, g.cursor.Intn(refGraphLeavesPerDoc)
}

// inCollision reports whether the document served at index i takes part in an
// $id collision, in either direction: another document claims its retrieval
// URI, or it claims a URI another document already holds. Its own $id repeating
// its retrieval URI claims nothing, so inCollision excludes that case in both
// directions.
func inCollision(claims map[string]int, served, ids []string, i int) bool {
	uri, id := served[i], ids[i]

	self := 0
	if id == uri {
		self = 1
	}

	if claims[uri]-self > 0 {
		return true
	}

	if id == "" || id == uri {
		return false
	}

	return id == refGraphRootURI || slices.Contains(served, id)
}

// enter starts rendering one document, empty for the root.
func (g *refGraphGen) enter(document string) {
	g.current = document
	g.slot = 0
}

// drawLeaf picks the next constraint shape for the document being rendered.
// Exactly one leaf of one document is malformed when planMalformed chose it,
// and that leaf is what reaches the build-error outcome.
//
// A graph carries at most one violation because each engine reports the first
// one its own walk reaches, and Inline restructures the document, so a graph
// carrying two lets the two engines name different violations while both
// correctly refuse the schema.
//
// Both engines vet a root, so a malformed one fails Inline before any
// reference resolves and reaches the rig as a build error carrying the
// sentinel Compile reports. The rig compares that error by its sentinel, the
// way it compares a rejected remote. A malformed remote reaches the same
// outcome through the closure walk, whether a reference names it directly or
// only another document's reference does.
func (g *refGraphGen) drawLeaf() string {
	slot := g.slot
	g.slot++

	if g.current == g.malformedDoc && slot == g.malformedSlot {
		return refGraphMalformedLeaves[g.cursor.Intn(len(refGraphMalformedLeaves))]
	}

	return refGraphLeaves[g.cursor.Intn(len(refGraphLeaves))]
}

// anchoredLeaf renders the leaf every document anchors, spelled $anchor under
// Draft 2020-12 and as a fragment-only $id under Draft 7. The anchor sits on a
// definition entry rather than the document root under both drafts. On the root
// it would make the same-document reference "#anc" name the root itself, which
// Inline reports as a cycle, discarding the graph before anything is compared.
func anchoredLeaf(gen *refGraphGen, draft2020 bool) string {
	name := jsonField("$id", quoteJSON("#"+refGraphAnchor))
	if draft2020 {
		name = jsonField("$anchor", quoteJSON(refGraphAnchor))
	}

	return mergeIntoObject(gen.drawLeaf(), name)
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

// mergeIntoObject adds a member to a rendered JSON object. A value that is not
// an object, or an empty one, yields a fresh object holding only the member.
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
	require.NoErrorf(t, err, "the pointer target %s is not a schema", encoded)

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

	// The substitute pipeline configures a fallback, which suspends the walk's
	// refusals for a structural violation, so a graph whose violation only that
	// walk reaches would compare a Compile refusal against an Inline that
	// accepts. On such a graph chooseWithheld therefore withholds nothing. See
	// reasonSubstituteTransitiveMalformed.
	require.Falsef(t, spec.malformedTransitive,
		"withheld a document on a transitively malformed graph: %s",
		reasonSubstituteTransitiveMalformed)

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
				require.NoErrorf(t, err, "parse the withheld document %s", spec.withheld)

				return jsonschema.SubstituteRef(parsed)
			}

			// An anchor fragment is unreachable here, since the generator
			// never withholds an anchor-targeted document. See
			// reasonSubstituteNoAnchors.
			require.Truef(t, strings.HasPrefix(fragment, "/"),
				"withheld an anchor-targeted document: %s", reasonSubstituteNoAnchors)

			target, ok := pointerInto(t, withheldText, fragment)
			require.Truef(t, ok, "the pointer %q names nothing in the withheld document %s",
				fragment, spec.withheld)

			return jsonschema.SubstituteRef(target)
		},
	)

	inlineOpts := []jsonschema.InlineOption{
		jsonschema.WithRefResolver(partial),
		jsonschema.WithRefFallback(fallback),
	}

	substituted, reason := inlinePipeline(ctx, t, "Inline+Substitute", schema, compileOpts, inlineOpts)

	if reason != "" {
		// One decline is legitimate. The graph also draws refGraphMissingURI,
		// so Inline meets a reference no substitute stands in for. Every
		// reference to the withheld document is covered, since the closure
		// above substitutes each one or fails outright, so a decline can only
		// come from a reference pointing elsewhere. Anything else means the
		// fallback failed to stand in for a document the resolver serves
		// without complaint, and declining quietly would let a broken
		// substitute path leave the fuzz target green.
		require.Truef(t, reason == reasonDeferredRefMiss && spec.unresolvable,
			"the fallback did not stand in for the withheld document %s: %s",
			spec.withheld, reason)

		return refPipeline{}, false
	}

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
		blob := make([]byte, 48+int(next()%208))
		for i := range blob {
			blob[i] = byte(next())
		}

		blobs = append(blobs, blob)
	}

	return blobs
}

// TestRefGraphSynthesisReachesEveryForm pins that the generator still draws
// every form the rig depends on. A generator edit that silently stops emitting
// a reference spelling, a draft, or a substitutable document would leave
// FuzzRefEnginesAgree green while covering less.
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

		first, second := refGraphDocURIs[0], refGraphDocURIs[1]

		for form, text := range map[string]string{
			"a same-document $defs reference":        `"$ref":"#/$defs/d0"`,
			"a same-document definitions reference":  `"$ref":"#/definitions/d0"`,
			"a same-document anchor reference":       `"$ref":"#` + refGraphAnchor + `"`,
			"a same-document unknown-keyword target": `"$ref":"#/x-custom/sub"`,
			"a pointer to the anchored definition":   `/` + refGraphAnchor + `"`,
			"an unresolvable reference":              `"$ref":"` + refGraphMissingURI + `"`,
			"a whole-document reference":             `"$ref":"` + first + `"`,
			"a remote pointer reference":             `"$ref":"` + first + `#/`,
			"a remote anchor reference":              `#` + refGraphAnchor + `"`,
			"a remote unknown-keyword target":        `#/x-custom/sub"`,
			"a reference to the second document":     `"$ref":"` + second,
			"a reference placed in allOf":            `"allOf":[{"$ref"`,
			"a reference placed in properties":       `"properties":{"p`,
		} {
			if strings.Contains(spec.root, text) {
				seen[form]++
			}
		}

		for _, sibling := range refGraphSiblings {
			if sibling != "" && strings.Contains(spec.root, ","+sibling+"}") {
				seen["a reference carrying a sibling"]++

				break
			}
		}

		for _, document := range spec.documents {
			if strings.Contains(document, `"$id":"http`) {
				seen["a document declaring $id"]++
			}

			if strings.Contains(document, `"$ref"`) {
				seen["a document carrying its own reference"]++
			}
		}

		// The build-error outcome is only reachable through a malformed leaf
		// in a document both engines vet, so the pools must keep drawing one.
		schema, resolver := parseRefGraph(t, spec.root, spec.documents)

		pipelines, reason := refEngines(t.Context(), t, schema,
			[]jsonschema.ValidateOption{jsonschema.WithRefResolver(resolver)},
			[]jsonschema.InlineOption{jsonschema.WithRefResolver(resolver)})
		if reason != "" {
			// FuzzRefEnginesAgree checks reasonDeferredRefMiss only in the
			// negative direction, that it arrives on no graph the generator
			// built fully resolvable. Counting it here states the other
			// direction, that a drawn missing reference reaches the
			// classification at all. The form check above cannot see that,
			// since it reads the root text rather than what the engines make
			// of it.
			if reason == reasonDeferredRefMiss {
				seen["a deferred reference miss"]++
			}

			continue
		}

		refused := 0

		for _, pipeline := range pipelines {
			if pipeline.outcome(t.Context(), []byte(`null`)).kind == refBuildErr {
				refused++
			}
		}

		if refused == len(pipelines) {
			seen["a schema both engines refuse"]++
		}
	}

	for _, form := range []string{
		"draft 7",
		"draft 2020-12",
		"a substitutable document",
		"a same-document $defs reference",
		"a same-document definitions reference",
		"a same-document anchor reference",
		"a same-document unknown-keyword target",
		"a pointer to the anchored definition",
		"an unresolvable reference",
		"a whole-document reference",
		"a remote pointer reference",
		"a remote anchor reference",
		"a remote unknown-keyword target",
		"a reference to the second document",
		"a reference carrying a sibling",
		"a reference placed in allOf",
		"a reference placed in properties",
		"a document declaring $id",
		"a document carrying its own reference",
		"a schema both engines refuse",
	} {
		assert.NotZerof(t, seen[form], "the generator draws %s in none of %d blobs", form, draws)
	}

	// A deferred reference miss carries its own message, since the one above
	// names the generator rather than the classification.
	assert.NotZerof(t, seen["a deferred reference miss"],
		"no graph in %d blobs reaches a deferred reference miss, so nothing exercises that classification",
		draws)
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
// document it stands in for, this test would fail and the generator could withhold
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
			},
		)))

	require.ErrorIs(t, err, jsonschema.ErrRefResolve, reasonSubstituteBaseURI)
}

// TestSubstitutePipelineBuilds pins that the third pipeline runs on every
// synthesized graph that qualifies for it. The builder declines only when a
// graph withholds nothing or draws an unresolvable reference, and the loop
// rules out both, so every remaining graph must build and this asserts equality
// rather than a non-zero count. Without it the rig could stop exercising the
// substitute path while FuzzRefEnginesAgree stayed green.
func TestSubstitutePipelineBuilds(t *testing.T) {
	t.Parallel()

	const draws = 300

	eligible, built := 0, 0

	for _, blob := range refGraphBlobs(draws) {
		spec := synthRefGraph(blob)
		if spec.withheld == "" || spec.unresolvable {
			continue
		}

		schema, resolver := parseRefGraph(t, spec.root, spec.documents)
		compileOpts := []jsonschema.ValidateOption{jsonschema.WithRefResolver(resolver)}
		inlineOpts := []jsonschema.InlineOption{jsonschema.WithRefResolver(resolver)}

		// The fuzz target reaches substitutePipeline only for a graph the full
		// resolver already handled, so the guard applies the same precondition.
		if _, reason := refEngines(t.Context(), t, schema, compileOpts, inlineOpts); reason != "" {
			continue
		}

		eligible++

		if _, ok := substitutePipeline(t.Context(), t, spec, schema, compileOpts); ok {
			built++
		}
	}

	assert.NotZerof(t, eligible, "no comparable graph withholds a document in %d blobs", draws)
	assert.Equalf(t, eligible, built,
		"the substitute pipeline built for %d of %d eligible graphs", built, eligible)
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
			// A documented Inline limitation, or an unresolvable reference the
			// generator drew on purpose; the pipelines are not comparable for
			// this graph. A miss on a graph whose every reference resolves is
			// an Inline registry bug, the class the rig exists to catch.
			if reason == reasonDeferredRefMiss {
				require.Truef(t, spec.unresolvable,
					"Inline reported a reference miss on a graph the generator built fully resolvable:\n%s", spec.root)
			}

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

// The reference differential. It runs one schema through every site that
// materializes a $ref target and asserts the sites agree on every instance.
// Three sites exist: Compile's reference fixpoint, Inline's own registry and
// index, and the JSON-pointer fallback both engines reach through
// internal/refresolve.
//
// Three reasons below name a graph the differential cannot compare, and two
// name a constraint the substitute pipeline works under. The rig classifies a
// graph from the error Inline returns rather than from a test name, so a reason
// cannot go stale against a renamed suite case.

// reasonInlineCycle is why a cyclic reference graph is not compared.
const reasonInlineCycle skipReason = "a cyclic reference graph has no finite static expansion, so Inline returns ErrRefCycle by design while Compile resolves the cycle lazily at validation time"

// reasonInlineDynamicRef is why a $dynamicRef graph is not compared.
const reasonInlineDynamicRef skipReason = "$dynamicRef resolves through the dynamic scope at validation time, so no single static replacement preserves its semantics and Inline returns ErrRefInline by design"

// reasonDeferredRefMiss is why an unresolvable reference leaves the engines
// incomparable.
const reasonDeferredRefMiss skipReason = "the reference does not resolve. The compile-time reference walk tolerates a missing remote document and defers to the validation walk that reaches the reference, while Inline fails at inline time, so one engine accepts an instance that never reaches the reference and the other refuses it, and both answers are correct"

// reasonSubstituteBaseURI is why the substitute pipeline withholds only a
// document carrying no reference of its own. It is not a skip reason. The
// generator applies it when choosing what to withhold, and
// TestSubstituteDoesNotRebaseNestedRefs pins the behavior it describes.
const reasonSubstituteBaseURI = "a WithRefFallback substitute's own references resolve against the document holding the failing reference, while a fetched document's resolve against its own base URI, so only a reference-free document can be withheld and substituted"

// reasonSubstituteTransitiveMalformed is why the substitute pipeline stands
// down on a graph whose malformed leaf sits in a document no reference reaches
// directly. It is not a skip reason; the generator applies it when choosing
// which document to withhold.
const reasonSubstituteTransitiveMalformed = "a WithRefFallback substitute answers one failing reference at a time, so Inline suspends the walk's structural refusals whenever a fallback is configured, and Compile refuses the graph regardless; the substitute pipeline therefore compares nothing on a graph whose violation only the closure walk reaches"

// reasonSubstituteNoAnchors is why the substitute pipeline withholds only a
// document nothing reaches by anchor. It is not a skip reason; the generator
// applies it when choosing what to withhold.
const reasonSubstituteNoAnchors = "resolving an anchor fragment needs the withheld document's anchor registry, which the substitute path never builds, and a spliced copy carries no $anchor of its own"

// refUnclassified is the signature of an error matching no build sentinel.
const refUnclassified = "unclassified"

// refErrSignature names the build sentinels err matches, sorted and joined so
// two errors carrying the same causes compare equal regardless of wording.
func refErrSignature(err error) string {
	var names []string

	for name, sentinel := range refBuildSentinels {
		if errors.Is(err, sentinel) {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return refUnclassified
	}

	slices.Sort(names)

	return strings.Join(names, "+")
}

// refOutcomeKind is a pipeline's answer for one instance, one of accept,
// reject, and build error.
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

// refOutcome pairs one pipeline's answer with the error behind a rejection or a
// build failure.
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
// instance. Two build failures agree when they carry the same build sentinels.
// A pair carrying none on either side fails too. By construction that is a
// refusal cause refBuildSentinels does not model, and calling two unmodeled
// causes equal would let a real disagreement pass.
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

		if want.kind != refBuildErr {
			continue
		}

		wantSig, gotSig := refErrSignature(want.err), refErrSignature(got.err)

		if !assert.Equalf(t, wantSig, gotSig,
			"%s and %s refuse the schema for different causes\n  %s: %v\n  %s: %v",
			pipelines[0].name, pipeline.name,
			pipelines[0].name, want.err,
			pipeline.name, got.err,
		) {
			continue
		}

		assert.NotEqualf(
			t,
			refUnclassified,
			wantSig,
			"%s and %s both refuse the schema for a cause refBuildSentinels does not model, so the match proves nothing; add the sentinel\n  %s: %v\n  %s: %v",
			pipelines[0].name,
			pipeline.name,
			pipelines[0].name,
			want.err,
			pipeline.name,
			got.err,
		)
	}
}

// dynamicRefInlinePhrase distinguishes Inline's one documented ErrRefInline
// case from its three other producers, two internal-invariant violations and
// the substitute depth limit, none of which may skip the rig.
//
// The schema graph cannot be inspected instead. A $dynamicRef commonly lives in
// a fetched document that a walk from the root never reaches, which is the case
// for every suite group referencing the Draft 2020-12 metaschema.
// TestInlineDifferentialSkipsAreLive fails if this phrase stops matching.
const dynamicRefInlinePhrase = "has no static expansion"

// isDynamicRefInline reports whether err is Inline's refusal to statically
// expand a $dynamicRef.
func isDynamicRefInline(err error) bool {
	return errors.Is(err, jsonschema.ErrRefInline) &&
		strings.Contains(err.Error(), dynamicRefInlinePhrase)
}

// isRefMiss reports whether err is a reference that did not resolve, as opposed
// to one that resolved to a target some check refused. Both wrap ErrRefResolve
// or ErrNotResolved, and only a reference that did not resolve leaves the
// engines incomparable.
func isRefMiss(err error) bool {
	if !errors.Is(err, jsonschema.ErrRefResolve) && !errors.Is(err, jsonschema.ErrNotResolved) {
		return false
	}

	return refErrSignature(err) == refUnclassified
}

// inlinePipeline inlines schema under inlineOpts and compiles the result. The
// second return names the reason the graph is not comparable, and is empty when
// the graph is comparable. An Inline failure the rig does not classify fails
// the test outright,
// since ErrRefInline also covers two internal-invariant violations and the
// substitute depth limit.
func inlinePipeline(
	ctx context.Context,
	t *testing.T,
	name string,
	schema *jsonschema.Schema,
	compileOpts []jsonschema.ValidateOption,
	inlineOpts []jsonschema.InlineOption,
) (refPipeline, skipReason) {
	t.Helper()

	inlined, inlineErr := jsonschema.Inline(ctx, schema, inlineOpts...)

	switch {
	case inlineErr == nil:
	case errors.Is(inlineErr, jsonschema.ErrRefCycle):
		return refPipeline{}, reasonInlineCycle
	case isDynamicRefInline(inlineErr):
		return refPipeline{}, reasonInlineDynamicRef
	case isRefMiss(inlineErr):
		return refPipeline{}, reasonDeferredRefMiss
	case refErrSignature(inlineErr) != refUnclassified:
		// Any refusal refBuildSentinels names: a structural-vet violation in
		// the root, a fetched document, a substitute, or a fallback target; a
		// cyclic root; or an unsupported dialect.
		// Compile refuses the same schema for the same cause, so the two stay
		// comparable through the build-error outcome. The case sits after
		// isRefMiss, which matches only an unclassified signature, so the two
		// never contend for one error.
		return refPipeline{name: name, buildErr: inlineErr}, ""

	default:
		require.NoError(t, inlineErr, "Inline failed for a reason the differential does not classify")
	}

	// The inlined schema must be self-contained, so the rig disables the ref
	// resolver. ValidateOption is opaque and suiteBaseOpts bundles both
	// resolvers, so the slice cannot be filtered; a trailing nil resolver wins
	// instead, since the option assigns rather than merges, and a nil resolver
	// is documented as restoring local-only resolution. Every other option
	// survives. The metaschema resolver and the format and content gates decide
	// vocabulary and assertion behavior that has nothing to do with $ref, and
	// dropping them reports divergences the engines do not have: 309 over the
	// vendored suite for the format and content gates, 3 more for the
	// metaschema resolver.
	standaloneOpts := make([]jsonschema.ValidateOption, 0, len(compileOpts)+1)
	standaloneOpts = append(standaloneOpts, compileOpts...)
	standaloneOpts = append(standaloneOpts, jsonschema.WithRefResolver(nil))

	standalone, standaloneErr := jsonschema.Compile(ctx, inlined, standaloneOpts...)

	return refPipeline{name: name, validator: standalone, buildErr: standaloneErr}, ""
}

// refEngines builds the Compile and Inline pipelines for one schema. The second
// return names the reason the graph is not comparable, and is empty when the
// graph is comparable.
func refEngines(
	ctx context.Context,
	t *testing.T,
	schema *jsonschema.Schema,
	compileOpts []jsonschema.ValidateOption,
	inlineOpts []jsonschema.InlineOption,
) ([]refPipeline, skipReason) {
	t.Helper()

	compiled, compileErr := jsonschema.Compile(ctx, schema, compileOpts...)

	inlined, reason := inlinePipeline(ctx, t, "Inline+Compile", schema, compileOpts, inlineOpts)
	if reason != "" {
		return nil, reason
	}

	return []refPipeline{
		{name: "Compile", validator: compiled, buildErr: compileErr},
		inlined,
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

// TestRefEnginesAgreeOnPastFixes runs one reference graph per $ref fix,
// fourteen rows in six classes: a fetched document claiming a URI another
// document holds, structural vetting of JSON-pointer fallback targets, the
// fallback cache key, fallback registry merge order, anchor resolution under a
// fetched document's canonical base, and vetting the whole closure, which
// covers a document only another document's reference reaches and the order
// the two engines report a violation in. One row carries two independent
// faults. A regression in one of those classes fails here with the graph in
// view rather than waiting for the fuzzer to rediscover it.
//
// Several rows misspell a type name, "strnig" and "nteger". Those are the
// invalid type names the structural vet rejects, and correcting them guts the
// row.
func TestRefEnginesAgreeOnPastFixes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root      string
		remotes   map[string]string
		instances []string
	}{
		"fetched document claims a loaded URI (da61121, 52b5110)": {
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
		"items array in a same-document fallback target (371092b)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$ref": "#/x-custom",
					"x-custom": {"type": "array", "items": [{"type": "string"}]}
				}
			`),
			instances: []string{`[]`, `["a"]`, `[1]`, `"text"`},
		},
		"collision beside a malformed fallback target": {
			root: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"$id": "https://ex.test/root.json",
					"x-custom": {"sub": {"type": "strnig"}},
					"properties": {
						"a": {"$ref": "#/x-custom/sub"},
						"b": {"$ref": "https://ex.test/b.json"}
					}
				}
			`),
			remotes: map[string]string{
				"https://ex.test/b.json": `{"$id": "https://ex.test/root.json", "type": "array"}`,
			},
			instances: []string{`{}`, `{"a": "text"}`, `{"b": []}`, `null`},
		},
		"violation in a transitively reached document": {
			root: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"$id": "https://ex.test/root.json",
					"$ref": "https://ex.test/b.json#anc"
				}
			`),
			remotes: map[string]string{
				"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
				"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
			},
			instances: []string{`"x"`, `1`, `null`},
		},
		"malformed document beside an unresolvable reference": {
			root: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"$id": "https://ex.test/root.json",
					"allOf": [
						{"$ref": "https://ex.test/a.json"},
						{"$ref": "https://ex.test/b.json"}
					]
				}
			`),
			remotes: map[string]string{
				"https://ex.test/a.json": `{"$id":"https://ex.test/a.json","definitions":{"bad":{"type":"strnig"}}}`,
				"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","allOf":[{"$ref":"#/definitions/nope"}]}`,
			},
			instances: []string{`"x"`, `1`},
		},
		"negative bound in a same-document fallback target (371092b)": {
			root: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"$ref": "#/x-custom",
					"x-custom": {"minLength": -5}
				}
			`),
			instances: []string{`"text"`, `""`, `42`},
		},
		"fallback target in a document fetched at validation time (e88e354)": {
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
		"fallback target two remote hops from the root (df730d4)": {
			root: `{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "https://example.test/a.json"}`,
			remotes: map[string]string{
				"https://example.test/a.json": `{"$ref": "https://example.test/b.json"}`,
				"https://example.test/b.json": `{"$ref": "#/examples/0", "examples": [{"type": "strnig"}]}`,
			},
			instances: []string{`"text"`, `42`},
		},
		"fallback target in a remote unknown keyword (575eeff)": {
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
			// Every row names each document its references reach, so a miss
			// is a resolution bug in one engine, not a deferred fetch.
			require.NotEqual(t, reasonDeferredRefMiss, reason,
				"Inline failed to resolve a reference the row serves")

			if reason != "" {
				t.Skip(string(reason))
			}

			for _, instance := range tc.instances {
				assertRefEnginesAgree(t.Context(), t, []byte(instance), pipelines...)
			}
		})
	}
}
