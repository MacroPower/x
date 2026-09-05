package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// suiteGroup is one test group from the JSON Schema Test Suite.
type suiteGroup struct {
	Description string         `json:"description"`
	Schema      jsontext.Value `json:"schema"`
	Tests       []suiteCase    `json:"tests"`
}

// suiteCase is a single test case within a group.
type suiteCase struct {
	Description string         `json:"description"`
	Data        jsontext.Value `json:"data"`
	Valid       bool           `json:"valid"`
}

// skipReason documents why a test is skipped.
type skipReason string

// Skip reasons. Each names the package constraint that makes a specific suite
// case diverge. Skips stay as narrow as possible: only the cases that genuinely
// cannot pass carry a reason, so the surrounding cases in the same file and
// group still run.
const (
	reasonCrossDraft       skipReason = "the referenced schema is Draft 2019-09, which this package does not support (Draft-07 and 2020-12 only); the draft is taken from the root schema's $schema and there is no per-ref switch to an unsupported draft's keyword semantics"
	reasonRE2Whitespace    skipReason = `patterns use Go RE2, not ECMA 262: RE2 \s matches only [\t\n\f\r ], so this character's class membership differs`
	reasonRE2ControlEscape skipReason = `patterns use Go RE2, not ECMA 262: RE2 has no \cX control escape, so the pattern does not compile, fails closed at validation, and rejects the matching instance`
	reasonAnnexBIdentity   skipReason = `the regex format applies ECMA 262 Annex B, where SourceCharacterIdentityEscape[~N] is "SourceCharacter but not c", so "\a" is a valid identity escape; the case pins the main grammar's narrower rule, which only applies under the u flag and which no engine applies to a bare pattern (V8 compiles /\a/ and rejects only /\a/u)`
)

func buildSuiteSkips() map[string]skipReason {
	skips := map[string]skipReason{
		// Cross-draft refs do not reprocess a referenced schema under a different
		// draft, so keywords that only have meaning in the referenced draft are
		// ignored. Only the cases that depend on that draft's keyword semantics
		// diverge; for draft7 the case relying on the future-draft keyword still
		// passes when the keyword is absent.
		"draft7/optional/cross-draft.json/refs to future drafts are processed as future drafts/missing bar is invalid":                     reasonCrossDraft,
		"draft2020-12/optional/cross-draft.json/refs to historic drafts are processed as historic drafts/first item not a string is valid": reasonCrossDraft,

		// The only optional/format skip. The suite reads an undefined ASCII
		// letter escape under the ECMA 262 main grammar, which excludes
		// UnicodeIDContinue from IdentityEscape; the format validator reads it
		// under Annex B, which every JS engine implements for a pattern without
		// the u flag. The surrounding regex format cases still run.
		`draft2020-12/optional/format/ecmascript-regex.json/\a is not an ECMA 262 control escape`: reasonAnnexBIdentity,
	}

	addECMARegexSkips(skips)

	return skips
}

// addECMARegexSkips records the ECMA-262 regex cases that diverge because
// patterns use Go's RE2. RE2's \s matches only [\t\n\f\r ]; it does not match
// vertical tab, non-breaking space, or the Unicode separators. Because \S is
// its inverse, the membership of those characters flips. RE2 also rejects the
// \cX control escape. The divergence is identical for both drafts, and every
// other case in these files (\d, \w, ASCII/Unicode semantics) still runs.
func addECMARegexSkips(skips map[string]skipReason) {
	// Characters that ECMA 262 treats as whitespace but RE2 does not. In the
	// \s group the "matches" case for each expects a match RE2 won't make; in
	// the \S group the "does not match" case expects a non-match RE2 won't make.
	whitespace := []struct{ sMatch, capSNoMatch string }{
		{"Line tabulation matches", "Line tabulation does not match"},
		{"latin-1 non-breaking-space matches", "latin-1 non-breaking-space does not match"},
		{"zero-width whitespace matches", "zero-width whitespace does not match"},
		{"paragraph separator matches (line terminator)", "paragraph separator does not match (line terminator)"},
		{"EM SPACE matches (Space_Separator)", "EM SPACE does not match (Space_Separator)"},
	}

	for _, draft := range []string{"draft7", "draft2020-12"} {
		file := draft + "/optional/ecmascript-regex.json"

		for _, c := range whitespace {
			skips[file+`/ECMA 262 \s matches whitespace/`+c.sMatch] = reasonRE2Whitespace
			skips[file+`/ECMA 262 \S matches everything but whitespace/`+c.capSNoMatch] = reasonRE2Whitespace
		}

		// The \cX escape doesn't compile, so the pattern fails closed at
		// validation and the matching instance can't validate. The "does not
		// match" instance still runs: the fail-closed pattern makes it
		// invalid, as expected.
		skips[file+`/ECMA 262 regex escapes control codes with \c and upper letter/matches`] = reasonRE2ControlEscape
		skips[file+`/ECMA 262 regex escapes control codes with \c and lower letter/matches`] = reasonRE2ControlEscape
	}
}

var (
	// Skip reasons keyed by test path, checked at file, group, and test
	// granularity. The path format is "draft/file.json",
	// "draft/file.json/group", or "draft/file.json/group/test".
	//
	// Every entry is a deliberate, minimal divergence required by this package's
	// design (Draft-07/2020-12 only, Go RE2 patterns, ECMA 262 Annex B identity
	// escapes): only the specific cases that cannot pass are skipped, so the
	// other cases in the same file and group still run. The required suite runs
	// with no skips, and the optional/format suite carries exactly one.
	// TestSuiteSkipsAreLive guards the map against typos and stale keys by
	// asserting each key names a real suite file, group, and test.
	suiteSkips = buildSuiteSkips()

	// Remote schemas keyed by http://localhost:1234/<path>, loaded from
	// testdata/suite/remotes/.
	remoteSchemas map[string]*jsonschema.Schema

	// Remote metaschemas: schemas from the remotes directory that have
	// $vocabulary set and should be registered as metaschemas.
	remoteMetaSchemas []*jsonschema.Schema

	// The two drafts the vendored suite covers. Each row carries the $schema
	// URI to inject into a group schema that declares none.
	suiteDrafts = []struct {
		name      string
		schemaURI string
	}{
		{"draft7", draft7SchemaURI},
		{"draft2020-12", draft2020SchemaURI},
	}

	// The three directory levels the suite is split across, each globbed
	// non-recursively. The tier partitions the file set: TestSuite runs the
	// required files, TestSuiteOptional the implementation-defined ones, and
	// TestSuiteFormat the format assertions, so a file belongs to exactly one
	// of them. The empty tier names the draft directory itself.
	suiteTiers = []string{"", "optional", "optional/format"}
)

func init() {
	remoteSchemas = map[string]*jsonschema.Schema{}
	remotesDir := "testdata/suite/remotes"

	err := filepath.Walk(remotesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		var s jsonschema.Schema

		err = json.Unmarshal(data, &s)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		// Compute the URL: http://localhost:1234/<relative path>.
		rel, err := filepath.Rel(remotesDir, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}

		uri := "http://localhost:1234/" + filepath.ToSlash(rel)

		remoteSchemas[uri] = &s

		// Collect metaschemas (schemas with $vocabulary).
		if len(s.Vocabulary) > 0 {
			remoteMetaSchemas = append(remoteMetaSchemas, &s)
		}

		return nil
	})
	if err != nil {
		panic("loading remote schemas: " + err.Error())
	}

	// Load vendored metaschemas by $id so they are resolvable when test
	// schemas use $ref to the official metaschema URIs.
	err = filepath.Walk("testdata/metaschemas", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		var s jsonschema.Schema

		err = json.Unmarshal(data, &s)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		if s.ID != "" {
			remoteSchemas[s.ID] = &s
		}

		if len(s.Vocabulary) > 0 {
			remoteMetaSchemas = append(remoteMetaSchemas, &s)
		}

		return nil
	})
	if err != nil {
		panic("loading metaschemas: " + err.Error())
	}
}

// suiteRemoteResolver resolves URIs from the remoteSchemas map.
type suiteRemoteResolver struct{}

func (suiteRemoteResolver) ResolveRef(_ context.Context, uri string) (*jsonschema.Schema, error) {
	if s, ok := remoteSchemas[uri]; ok {
		return s, nil
	}

	// Try with trailing "#" for schemas whose $id includes an empty
	// fragment (e.g. Draft 7 metaschema "http://json-schema.org/draft-07/schema#").
	if s, ok := remoteSchemas[uri+"#"]; ok {
		return s, nil
	}

	return nil, jsonschema.ErrNotResolved
}

// suiteBaseOpts returns the standard ValidateOption set for suite tests,
// including the remote ref resolver and the metaschema lookup.
func suiteBaseOpts() []jsonschema.ValidateOption {
	metaSchemas := jsonschema.SchemaMap{}
	for _, ms := range remoteMetaSchemas {
		metaSchemas[ms.ID] = ms
	}

	return []jsonschema.ValidateOption{
		jsonschema.WithRefResolver(suiteRemoteResolver{}),
		jsonschema.WithMetaSchemaResolver(metaSchemas),
	}
}

// suiteFile is one vendored suite file with everything needed to run it.
type suiteFile struct {
	// The draft directory name, "draft7" or "draft2020-12".
	draft string

	// The sub-directory below the draft, one of suiteTiers.
	tier string

	// The base name, which is also the subtest name.
	fileName string

	// The file's path on disk.
	path string

	// The suiteSkips key for the file, the path relative to
	// testdata/suite with forward slashes. The shouldSkip helper appends the
	// group and test descriptions to it, and TestSuiteSkipsAreLive turns it
	// back into a path, so its spelling is load-bearing.
	pathKey string

	// The $schema injected into a group schema that declares none.
	schemaURI string

	// The validate options the file runs under, the base set plus the
	// per-file format and content gates.
	opts []jsonschema.ValidateOption
}

// suiteFiles enumerates every vendored suite file across both drafts and all
// three tiers. It is the single enumeration of the suite. The three conformance
// tests and the inline differential all draw their files from it, so the
// differential cannot drift from what the conformance tests run.
//
// Each row carries its own options slice, since suiteBaseOpts builds a fresh
// metaschema map per call. Every draft and tier directory must hold at least
// one file, so an emptied directory fails whichever test calls this, not only
// the one that owns that tier.
func suiteFiles(t *testing.T) []suiteFile {
	t.Helper()

	var files []suiteFile

	for _, draft := range suiteDrafts {
		for _, tier := range suiteTiers {
			dir := filepath.Join("testdata", "suite", draft.name)
			if tier != "" {
				dir = filepath.Join(dir, filepath.FromSlash(tier))
			}

			matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
			require.NoError(t, err)
			require.NotEmpty(t, matches, "no suite files in %s", dir)

			for _, path := range matches {
				fileName := filepath.Base(path)

				// The key joins its parts with slashes rather than going
				// through filepath.Rel, which would yield host separators.
				pathKey := draft.name + "/" + fileName
				if tier != "" {
					pathKey = draft.name + "/" + tier + "/" + fileName
				}

				files = append(files, suiteFile{
					draft:     draft.name,
					tier:      tier,
					fileName:  fileName,
					path:      path,
					pathKey:   pathKey,
					schemaURI: draft.schemaURI,
					opts:      suiteFileOpts(tier, fileName),
				})
			}
		}
	}

	return files
}

// suiteFileOpts returns the validate options one suite file runs under.
func suiteFileOpts(tier, fileName string) []jsonschema.ValidateOption {
	opts := suiteBaseOpts()

	switch {
	case tier == "optional/format":
		// The optional format suite tests format as an assertion. Under Draft
		// 2020-12 format is annotation-only by default, so opt in explicitly;
		// Draft-07 asserts regardless.
		opts = append(opts, jsonschema.WithFormats(true))
	case tier == "" && fileName == "format.json":
		// Format.json tests annotation-only behavior. Format must not cause
		// validation failures.
		opts = append(opts, jsonschema.WithFormats(false))
	case tier == "optional" && fileName == "content.json":
		// The optional content suite asserts contentEncoding and
		// contentMediaType, which are annotation-only by default.
		opts = append(opts, jsonschema.WithContent(true))
	}

	return opts
}

// runSuiteTier runs every suite file in one tier, grouped by draft. The two
// levels of subtest, the draft then the file, keep every subtest name byte
// identical to what the three tests produced before they shared this helper.
// The suiteSkips key is file.pathKey, which carries the tier segment the
// subtest name omits.
func runSuiteTier(t *testing.T, tier string) {
	t.Helper()

	files := suiteFiles(t)

	for _, draft := range suiteDrafts {
		t.Run(draft.name, func(t *testing.T) {
			t.Parallel()

			var selected []suiteFile

			for _, file := range files {
				if file.draft == draft.name && file.tier == tier {
					selected = append(selected, file)
				}
			}

			require.NotEmpty(t, selected)

			for _, file := range selected {
				t.Run(file.fileName, func(t *testing.T) {
					t.Parallel()

					runSuiteFile(t, file.path, file.pathKey, file.schemaURI, file.opts...)
				})
			}
		})
	}
}

// TestSuite runs the JSON Schema Test Suite for draft7 and draft2020-12.
//
// Test suite commit: 60755c1097769e313fae3ec4d63bcc9d49b5d2d5.
func TestSuite(t *testing.T) {
	t.Parallel()

	runSuiteTier(t, "")
}

// TestSuiteFormat runs optional format tests from the JSON Schema Test Suite.
func TestSuiteFormat(t *testing.T) {
	t.Parallel()

	runSuiteTier(t, "optional/format")
}

// TestSuiteOptional runs the optional (non-format) files from the JSON Schema
// Test Suite. These exercise behavior that the spec leaves implementation
// defined (bignum precision, content assertion, ECMA-262 regex, unknown
// keywords, $id/$anchor/$dynamicRef edge cases). Cases that depend on behavior
// this package documents as a deviation (e.g. Go RE2 instead of ECMA 262, or
// content keywords as annotation only) are skipped via suiteSkips with a
// documented reason.
func TestSuiteOptional(t *testing.T) {
	t.Parallel()

	runSuiteTier(t, "optional")
}

// loadSuiteGroups reads one suite file and decodes its test groups.
func loadSuiteGroups(t *testing.T, path string) []suiteGroup {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var groups []suiteGroup

	require.NoError(t, json.Unmarshal(data, &groups))

	return groups
}

// runSuiteFile runs every test group in one suite file.
func runSuiteFile(t *testing.T, path, pathKey, schemaURI string, opts ...jsonschema.ValidateOption) {
	t.Helper()

	if reason, ok := shouldSkip(pathKey, "", ""); ok {
		t.Skip(reason)
	}

	for _, group := range loadSuiteGroups(t, path) {
		t.Run(group.Description, func(t *testing.T) {
			t.Parallel()

			if reason, ok := shouldSkip(pathKey, group.Description, ""); ok {
				t.Skip(reason)
			}

			schema := unmarshalTestSchema(t, group.Schema, schemaURI)

			for _, tc := range group.Tests {
				t.Run(tc.Description, func(t *testing.T) {
					t.Parallel()

					if reason, ok := shouldSkip(pathKey, group.Description, tc.Description); ok {
						t.Skip(reason)
					}

					err := validateJSON(t.Context(), schema, tc.Data, opts...)
					if tc.Valid {
						assert.NoError(t, err, "expected valid, got: %v", err)
					} else {
						assert.Error(t, err, "expected invalid, but validation passed")
					}
				})
			}
		})
	}
}

// unmarshalTestSchema deserializes a test schema from JSON.
// It injects $schema if not present, based on the draft.
func unmarshalTestSchema(t *testing.T, raw jsontext.Value, schemaURI string) *jsonschema.Schema {
	t.Helper()

	// Handle boolean schemas: true -> {}, false -> {"not":{}}.
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "true" {
		return &jsonschema.Schema{Schema: schemaURI}
	}

	if trimmed == "false" {
		return &jsonschema.Schema{Schema: schemaURI, Not: &jsonschema.Schema{}}
	}

	var s jsonschema.Schema

	require.NoError(t, json.Unmarshal(raw, &s))

	// Inject $schema if not present.
	if s.Schema == "" {
		s.Schema = schemaURI
	}

	return &s
}

// shouldSkip checks whether a test should be skipped at file, group, or test level.
func shouldSkip(fileKey, group, test string) (skipReason, bool) {
	// Test level (most specific).
	if test != "" {
		if reason, ok := suiteSkips[fileKey+"/"+group+"/"+test]; ok {
			return reason, true
		}
	}

	// Group level.
	if group != "" {
		if reason, ok := suiteSkips[fileKey+"/"+group]; ok {
			return reason, true
		}
	}

	// File level (least specific).
	if reason, ok := suiteSkips[fileKey]; ok {
		return reason, true
	}

	return "", false
}

// TestSuiteSkipsAreLive asserts every suiteSkips key names a real suite file
// and, for group- or test-scoped keys, a real group and test within it. It
// fails on typos and on keys left stale when the upstream suite renames or
// drops a case, so a skip can never silently hide a case that should run.
func TestSuiteSkipsAreLive(t *testing.T) {
	t.Parallel()

	const ext = ".json"

	for key := range suiteSkips {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			idx := strings.Index(key, ext)
			require.NotEqual(t, -1, idx, "skip key must reference a %s file", ext)

			fileKey := key[:idx+len(ext)]
			remainder := strings.TrimPrefix(key[idx+len(ext):], "/")

			path := filepath.Join("testdata", "suite", filepath.FromSlash(fileKey))

			data, err := os.ReadFile(path)
			require.NoError(t, err, "skip key references a missing file: %s", path)

			// File-level skip: the file existing is all there is to check.
			if remainder == "" {
				return
			}

			var groups []suiteGroup

			require.NoError(t, json.Unmarshal(data, &groups))

			// A remainder is valid if it matches a group description or a
			// "group/test" pair. Building the set tolerates "/" inside a
			// description, which splitting the key would not.
			valid := map[string]struct{}{}
			for _, group := range groups {
				valid[group.Description] = struct{}{}
				for _, tc := range group.Tests {
					valid[group.Description+"/"+tc.Description] = struct{}{}
				}
			}

			_, ok := valid[remainder]
			assert.True(t, ok, "skip key names no group or test in %s: %q", fileKey, remainder)
		})
	}
}

// suiteInlineOpts returns the inline options matching suiteBaseOpts. Only the
// ref resolver carries over. WithRefResolver returns a RefOption serving both
// engines, while the metaschema resolver and the format and content gates are
// validation concerns the inliner has no use for.
func suiteInlineOpts() []jsonschema.InlineOption {
	return []jsonschema.InlineOption{jsonschema.WithRefResolver(suiteRemoteResolver{})}
}

// TestSuiteInlineAgrees runs every group schema in the vendored suite through
// the validator and through the inliner, and asserts the two agree on every
// case. It covers the three sites that materialize a $ref target. A fix at one
// site that misses another fails here rather than reaching a release.
//
// It deliberately ignores suiteSkips. Those entries record where this package's
// answer differs from the suite's expected answer. That is unrelated to whether
// the two engines answer alike, since both share the Go RE2 matcher and the
// draft support those skips are about. Running the skipped cases is extra
// coverage the conformance tests cannot give.
func TestSuiteInlineAgrees(t *testing.T) {
	t.Parallel()

	for _, file := range suiteFiles(t) {
		t.Run(file.pathKey, func(t *testing.T) {
			t.Parallel()

			for _, group := range loadSuiteGroups(t, file.path) {
				t.Run(group.Description, func(t *testing.T) {
					t.Parallel()

					schema := unmarshalTestSchema(t, group.Schema, file.schemaURI)

					pipelines, reason := refEngines(
						t.Context(), t, schema, file.opts, suiteInlineOpts(),
					)
					// The suite serves every remote it references, so a miss
					// here is an Inline resolution bug, never a deferred fetch.
					require.NotEqual(t, reasonDeferredRefMiss, reason,
						"Inline failed to resolve a reference the suite serves")

					if reason != "" {
						t.Skip(string(reason))
					}

					for _, tc := range group.Tests {
						t.Run(tc.Description, func(t *testing.T) {
							t.Parallel()

							assertRefEnginesAgree(t.Context(), t, tc.Data, pipelines...)
						})
					}
				})
			}
		})
	}
}

// TestRefEnginesAgreeOnTransitiveVetting pins the reference closure both
// engines compute. The root's anchor reference reaches b.json, whose own allOf
// reference reaches a.json, and a.json carries an invalid type name. Neither
// engine copies anything out of a.json, and Inline's output would be identical
// without it. A document inside the closure is still a document the run holds,
// so both engines fetch it, vet it, and refuse the graph for the same cause.
func TestRefEnginesAgreeOnTransitiveVetting(t *testing.T) {
	t.Parallel()

	root := `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`
	documents := map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
	}

	schema, resolver := parseRefGraph(t, root, documents)

	_, compileErr := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	_, inlineErr := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))

	for name, err := range map[string]error{"Compile": compileErr, "Inline": inlineErr} {
		require.ErrorIsf(t, err, jsonschema.ErrInvalidType,
			"%s reaches a.json through b.json's own reference and vets it", name)
		require.ErrorContainsf(t, err, "a.json",
			"%s names the transitively fetched document holding the violation", name)
	}
}

// TestInlineFallbackSuspendsTransitiveVetting pins what a fallback narrows. A
// [jsonschema.RefFallback] answers one failing reference at a time, and a
// document reachable only through another document's reference has no reference
// in the expansion for a policy to answer, so with a fallback configured the walk
// refuses nothing and every failure waits in the negative cache for the
// references walkPair does reach. TestRefEnginesAgreeOnTransitiveVetting pins
// that Inline refuses the same graph with no fallback.
func TestInlineFallbackSuspendsTransitiveVetting(t *testing.T) {
	t.Parallel()

	root := `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`
	documents := map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
	}

	schema, resolver := parseRefGraph(t, root, documents)

	var consulted []jsonschema.RefFailure

	fallback := jsonschema.RefFallbackFunc(
		func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
			consulted = append(consulted, f)

			return jsonschema.PropagateRef()
		},
	)

	out, err := jsonschema.Inline(t.Context(), schema,
		jsonschema.WithRefResolver(resolver), jsonschema.WithRefFallback(fallback))
	require.NoError(t, err, "a fallback suspends the walk's refusals, so the graph inlines")
	assert.Empty(t, consulted, "a document outside the expansion consults nobody")

	// The anchor still inlined, so the run did the work rather than stopping early.
	standalone, err := jsonschema.Compile(t.Context(), out)
	require.NoError(t, err)
	require.NoError(t, standalone.ValidateJSON(t.Context(), []byte(`"x"`)))
	require.Error(t, standalone.ValidateJSON(t.Context(), []byte(`1`)))
}

// TestRefEnginesAgreeOnCollidingIDs pins that both engines refuse a graph in
// which a fetched document claims a URI another document already holds. Two
// documents under one URI leave every reference naming it ambiguous, so the
// registration reports ErrIDCollision at both engines instead of picking a
// winner.
//
// The collision has two spellings and both are refused. A document can claim
// the root's URI, or it can claim another document's retrieval URI. Each row
// holds exactly one collision, since the engines reach documents in different
// orders and a graph with two would let them name different pairs.
func TestRefEnginesAgreeOnCollidingIDs(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root      string
		documents map[string]string
		claimant  string
		holder    string
	}{
		"a fetched document claims the root's URI": {
			root: `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","properties":{"p0":{"$ref":"https://ex.test/b.json"}}}`,
			documents: map[string]string{
				"https://ex.test/b.json": `{"$id":"https://ex.test/root.json","type":"array"}`,
			},
			claimant: `document "https://ex.test/b.json"`,
			holder:   `document "https://ex.test/root.json"`,
		},
		"a fetched document claims another's retrieval URI": {
			root: `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","properties":{"p0":{"$ref":"https://ex.test/a.json"}}}`,
			documents: map[string]string{
				"https://ex.test/a.json": `{"type":"array","allOf":[{"$ref":"https://ex.test/b.json"}]}`,
				"https://ex.test/b.json": `{"$id":"https://ex.test/a.json","type":"string"}`,
			},
			claimant: `document "https://ex.test/b.json"`,
			holder:   `document "https://ex.test/a.json"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, resolver := parseRefGraph(t, tc.root, tc.documents)

			_, compileErr := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			_, inlineErr := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))

			for engine, err := range map[string]error{"Compile": compileErr, "Inline": inlineErr} {
				require.ErrorIsf(t, err, jsonschema.ErrIDCollision,
					"%s must refuse a document claiming a URI another document holds", engine)
				assert.Containsf(t, err.Error(), tc.claimant,
					"%s must name the document making the claim", engine)
				assert.Containsf(t, err.Error(), tc.holder,
					"%s must name the document already holding the URI", engine)
			}
		})
	}
}

// TestRefEnginesAgreeOnCollisionBesideMalformedTarget pins which of two
// independent faults both engines report when one graph carries both. One
// document claims the root's URI, and an unknown keyword holds a malformed
// JSON-pointer target. Each engine vets a fallback target where its session
// materializes it, and each drives the same closure walk over sorted map keys.
// The reference the walk reaches first therefore decides the sentinel, and
// both engines name the same one.
//
// The two rows differ only in which property key carries which reference, which
// is what makes the walk order the variable under test. Either fault alone
// makes both engines refuse the graph, so a row proves nothing on its own; the
// pair proves the order is the reference order and not a fixed precedence
// between the two checks.
func TestRefEnginesAgreeOnCollisionBesideMalformedTarget(t *testing.T) {
	t.Parallel()

	const (
		root = `{"$schema":"http://json-schema.org/draft-07/schema#",` +
			`"$id":"https://ex.test/root.json",` +
			`"x-custom":{"sub":{"type":"strnig"}},` +
			`"properties":{%q:{"$ref":"#/x-custom/sub"},%q:{"$ref":"https://ex.test/b.json"}}}`

		collidingDoc = `{"$id":"https://ex.test/root.json","type":"array"}`
	)

	tests := map[string]struct {
		targetKey string
		remoteKey string
		want      error
		other     error
	}{
		"target first": {
			targetKey: "a",
			remoteKey: "b",
			want:      jsonschema.ErrInvalidType,
			other:     jsonschema.ErrIDCollision,
		},
		"collision first": {
			targetKey: "b",
			remoteKey: "a",
			want:      jsonschema.ErrIDCollision,
			other:     jsonschema.ErrInvalidType,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			documents := map[string]string{"https://ex.test/b.json": collidingDoc}

			schema, resolver := parseRefGraph(t,
				fmt.Sprintf(root, tc.targetKey, tc.remoteKey), documents)

			_, compileErr := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			_, inlineErr := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))

			for engine, err := range map[string]error{"Compile": compileErr, "Inline": inlineErr} {
				require.ErrorIsf(t, err, tc.want,
					"%s must report the fault the walk reaches first", engine)
				require.NotErrorIsf(t, err, tc.other,
					"%s must stop at the first fault rather than report both", engine)
			}

			assert.Equal(t, refErrSignature(compileErr), refErrSignature(inlineErr),
				"both engines must refuse the two-fault graph for the same cause")
		})
	}
}

// TestInlineDifferentialSkipsAreLive asserts every reason the suite
// differential can reach still classifies at least one vendored group, so a
// constant cannot go dead unnoticed. Were Inline to gain a static expansion for
// $dynamicRef, or the upstream suite to drop its recursive schemas, the
// matching constant would stop earning its place and this test would fail.
//
// One reason is not checked here, reasonDeferredRefMiss. The suite serves every
// remote it references, so no case reaches it.
// TestRefGraphSynthesisReachesEveryForm keeps that reason live instead, since
// its generator draws unresolvable references on purpose and one of its blobs
// must reach the classification. Three reasons are not skips,
// reasonSubstituteBaseURI, reasonSubstituteNoAnchors, and
// reasonSubstituteTransitiveMalformed, which chooseWithheld applies when it
// picks a document to withhold. TestSubstituteDoesNotRebaseNestedRefs pins
// reasonSubstituteBaseURI.
func TestInlineDifferentialSkipsAreLive(t *testing.T) {
	t.Parallel()

	seen := map[skipReason]int{}
	compared := 0

	for _, file := range suiteFiles(t) {
		for _, group := range loadSuiteGroups(t, file.path) {
			schema := unmarshalTestSchema(t, group.Schema, file.schemaURI)

			_, reason := refEngines(t.Context(), t, schema, file.opts, suiteInlineOpts())
			if reason != "" {
				seen[reason]++

				continue
			}

			compared++
		}
	}

	assert.NotZero(t, compared, "the differential compares no suite group at all")

	for _, reason := range []skipReason{reasonInlineCycle, reasonInlineDynamicRef} {
		assert.NotZerof(t, seen[reason],
			"no vendored suite group reaches this reason any more, so it is dead: %s", reason)
	}
}
