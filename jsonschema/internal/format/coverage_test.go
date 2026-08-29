package format_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/format"
)

// Rig 3, the coverage guard. Layers 1 through 3 pin the built-in formats from
// three directions, and this test states which direction covers which format so
// a newly registered one cannot arrive with none. Every name in
// [format.Validators] must carry a differential fuzz target, a vendored corpus,
// or a vector file, or else an allowlist entry saying why it carries none.
//
// A coverage claim names the Go function itself rather than its name as a
// string, so deleting or renaming a target breaks the build here instead of
// leaving a claim that reads true and means nothing. Two guards close what a
// function value cannot carry. TestFormatCoverageSourcesNameTheirFormat
// requires each claimed source to name its row's format through one of the
// helpers formatHelper lists, so a row cannot claim a target that names another
// format. It also refuses a differential claim on a target of another shape.
// TestFormatCoverageClaimsEveryDifferential demands a row for every target
// shaped like a differential, wherever the package declares it, so a
// differential that names its format the way the others do cannot stay
// invisible.
//
// A differential's shape is a Fuzz target that names one format and does not
// reach its validator through fuzzFormatRobust. A robustness target names one
// format too but reaches it through that helper, so the second guard demands no
// row for it. Both guards read source text rather than a run, and neither reads
// what a target asserts about the format it names.
//
// Two limits follow from reading shape. A target that passes no string literal
// to a helper formatHelper lists names nothing the index can read, so the
// second guard passes over it, while the first fails any row claiming it and
// points at the helper list rather than at the row. The second guard would also
// demand a row for a single-format target that pins no accept/reject boundary.
// None exists. If one arrives, give it a helper the shape rule knows, the way
// fuzzFormatRobust marks the robustness targets.
//
// Containment targets (containment_test.go) are deliberately not a coverage
// source. A containment oracle is another format in this package rather than an
// independent one, so a containment pair that drifts together stays green, and
// a format covered by containment alone is not covered. One-wayness is not the
// distinction, since six of the thirteen differentials this table accepts
// assert one direction only. Because a containment target covers none of the
// formats it names, the second guard passes over any target naming more than
// one, rather than asking for rows the table deliberately does not carry.

// coverageSource is a differential target or the test that drives a vendored
// corpus, held as the function value so the compiler enforces that the
// identifier exists.
type coverageSource any

// coverage states how one format's accept/reject boundary is pinned. A row
// carries sources or a reason, never both. A reason is the admission that the
// format has no mechanical coverage, which a source contradicts.
type coverage struct {
	// Differential fuzz targets asserting this format against an oracle.
	differentials []coverageSource

	// Tests driving a vendored conformance corpus through this format.
	corpus []coverageSource

	// Whether testdata/vectors carries a file for this format.
	vectors bool

	// Why this format has no coverage at all. Empty on a covered row.
	reason string
}

// sources returns the row's function values. TestFormatCoverage checks the
// vector claim separately, by loading the file.
func (c coverage) sources() []coverageSource {
	return slices.Concat(c.differentials, c.corpus)
}

// formatCoverage maps every built-in format to the tests pinning it. The
// allowlist is empty. Every registered format carries at least one source.
var formatCoverage = map[string]coverage{
	"date-time": {differentials: []coverageSource{FuzzFormatDateTimeVsRFC3339}, vectors: true},
	"date":      {differentials: []coverageSource{FuzzFormatDateVsTimeParse}},
	"time":      {differentials: []coverageSource{FuzzFormatTimeVsTimeParse}},
	"duration":  {differentials: []coverageSource{FuzzFormatDurationVsABNF}, vectors: true},
	"email": {
		differentials: []coverageSource{FuzzFormatEmailVsNetMail},
		corpus:        []coverageSource{TestEmailCorpus},
		vectors:       true,
	},
	"idn-email":             {corpus: []coverageSource{TestIDNEmailCorpus}, vectors: true},
	"hostname":              {differentials: []coverageSource{FuzzFormatHostnameVsIDNA}, vectors: true},
	"idn-hostname":          {differentials: []coverageSource{FuzzFormatIDNHostnameVsIDNA}, vectors: true},
	"uri":                   {differentials: []coverageSource{FuzzFormatURIVsNetURL}, vectors: true},
	"uri-reference":         {differentials: []coverageSource{FuzzFormatURIReferenceVsNetURL}, vectors: true},
	"uri-template":          {corpus: []coverageSource{TestURITemplateCorpus}},
	"iri":                   {vectors: true},
	"iri-reference":         {vectors: true},
	"uuid":                  {differentials: []coverageSource{FuzzFormatUUIDVsGrammar}, vectors: true},
	"ipv4":                  {differentials: []coverageSource{FuzzFormatIPv4VsNetip}},
	"ipv6":                  {differentials: []coverageSource{FuzzFormatIPv6VsNetip}},
	"json-pointer":          {vectors: true},
	"relative-json-pointer": {vectors: true},
	"regex":                 {differentials: []coverageSource{FuzzFormatRegexVsRE2}, vectors: true},
}

// TestFormatCoverage asserts that every registered format carries a coverage
// row, that each row claims coverage that exists or a reason for having none
// but never both, and that a row claiming a vector file has one carrying rows.
// The other direction, that the table names nothing the registry does not, is
// TestFormatCoverageTableIsLive's.
func TestFormatCoverage(t *testing.T) {
	t.Parallel()

	for name := range format.Validators() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row, declared := formatCoverage[name]
			require.True(
				t,
				declared,
				"format %q is registered with no coverage row; give it a differential, a corpus, or a vector file",
				name,
			)

			claims := len(row.differentials) + len(row.corpus)
			if row.vectors {
				claims++
			}

			if row.reason != "" {
				require.Zero(t, claims,
					"format %q carries both coverage and a reason for having none: %s", name, row.reason)

				return
			}

			require.NotZero(t, claims, "format %q has an empty coverage row", name)

			for _, source := range row.sources() {
				require.NotEmpty(t, sourceName(source),
					"format %q names a coverage source the runtime cannot identify", name)
			}

			if row.vectors {
				require.NotEmpty(t, loadVectorFile(t, filepath.Join(vectorDir, name+".tsv")),
					"format %q claims a vector file that carries no rows", name)
			}
		})
	}
}

// TestFormatCoverageTableIsLive asserts the table names nothing the registry
// does not, that an allowlist entry's reason is written out rather than left as
// a TODO placeholder, and that a row claims every vector file. A row naming a
// format the registry dropped sits unexercised, and a vector file no row claims
// lets the table read as complete while the guard skips that file.
func TestFormatCoverageTableIsLive(t *testing.T) {
	t.Parallel()

	registered := format.Validators()

	for name, row := range formatCoverage {
		_, ok := registered[name]
		require.True(t, ok, "coverage row %q names no registered format; drop it", name)

		if row.reason == "" {
			continue
		}

		require.NotContains(t, row.reason, "TODO", "coverage row %q must carry a written reason", name)
	}

	entries, err := os.ReadDir(vectorDir)
	require.NoError(t, err, "read vector directory %s", vectorDir)

	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".tsv")
		require.True(t, formatCoverage[name].vectors,
			"vector file %s is not claimed by a coverage row", entry.Name())
	}
}

// TestFormatCoverageSourcesNameTheirFormat asserts that each source a row
// claims names that row's format in its own body. A function reference proves
// only that the identifier exists, so without this a row can claim a target
// that validates another format and read as covered.
func TestFormatCoverageSourcesNameTheirFormat(t *testing.T) {
	t.Parallel()

	index := indexTestSources(t)

	for name, row := range formatCoverage {
		for _, source := range row.sources() {
			claimed := declaredName(sourceName(source))

			fn, found := index[claimed]
			require.Truef(t, found,
				"coverage source %s for format %q is declared in no test file of this package", claimed, name)

			require.NotEmptyf(t, fn.formats,
				"coverage source %s for format %q names no format at all, either because formatHelper does "+
					"not list the helper it calls or because it passes the name through a variable; "+
					"the fix is there rather than in the row",
				claimed, name)

			assert.Truef(t, fn.formats[name],
				"format %q claims coverage source %s, which names %s",
				name, claimed, strings.Join(slices.Sorted(maps.Keys(fn.formats)), ", "))
		}

		for _, source := range row.differentials {
			claimed := declaredName(sourceName(source))
			fn := index[claimed]

			assert.Falsef(t, fn.robust,
				"format %q claims the robustness target %s as a differential, and a robustness target "+
					"asserts only that the validator neither panics nor contradicts itself",
				name, claimed)

			assert.LessOrEqualf(t, len(fn.formats), 1,
				"format %q claims %s as a differential, and it names %s, the shape of a containment target "+
					"rather than of an oracle",
				name, claimed, strings.Join(slices.Sorted(maps.Keys(fn.formats)), ", "))
		}
	}
}

// TestFormatCoverageClaimsEveryDifferential asserts that a coverage row claims
// every differential target. Nothing else enumerates the package's fuzz
// targets, so without this a new differential lands with the table still
// reading as complete.
func TestFormatCoverageClaimsEveryDifferential(t *testing.T) {
	t.Parallel()

	claimed := map[string]bool{}

	for _, row := range formatCoverage {
		for _, source := range row.differentials {
			claimed[declaredName(sourceName(source))] = true
		}
	}

	targets := 0

	for name, fn := range indexTestSources(t) {
		if !strings.HasPrefix(name, "Fuzz") || fn.robust || len(fn.formats) != 1 {
			continue
		}

		targets++

		assert.Truef(t, claimed[name],
			"no coverage row claims the fuzz target %s (%s), which names the format %s",
			name, fn.file, strings.Join(slices.Sorted(maps.Keys(fn.formats)), ", "))
	}

	require.NotZerof(t, targets, "the source index found no target shaped like a differential")
}

// testFunc is what the source index records about one top-level function: the
// file declaring it, the format names its body passes to a format helper, and
// whether it is a robustness target, the one single-format shape that covers
// nothing.
type testFunc struct {
	file    string
	formats map[string]bool
	robust  bool
}

// indexTestSources parses this package's test files and indexes every top-level
// function by name. The test binary runs in the package directory, so the read
// below resolves against the sources it indexes.
func indexTestSources(t *testing.T) map[string]testFunc {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err, "read the package directory")

	fset := token.NewFileSet()
	index := map[string]testFunc{}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}

		parsed, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoErrorf(t, err, "parse %s", name)

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}

			previous, duplicate := index[fn.Name.Name]
			require.Falsef(t, duplicate,
				"%s is declared in both %s and %s; the index keys by name alone, which holds only while "+
					"every test file in this directory shares one package",
				fn.Name.Name, previous.file, name)

			index[fn.Name.Name] = scanFunc(t, name, fn)
		}
	}

	require.NotEmpty(t, index, "the source index holds no function; the test files moved")

	return index
}

// formatHelper reports whether name is a helper the source index reads a format
// name from. The name is the argument after the testing.TB, and it counts only
// as a string literal, so a function that passes it through a variable names
// nothing the index can read.
func formatHelper(name string) bool {
	switch name {
	case "validator", "runISEmailCorpus", "fuzzFormatRobust":
		return true
	default:
		return false
	}
}

// scanFunc reads one declaration for the formats it names and the shape it
// carries. A call to fuzzFormatRobust names a format like the others and marks
// the target as a robustness one, the shape the second guard passes over.
func scanFunc(t *testing.T, file string, fn *ast.FuncDecl) testFunc {
	t.Helper()

	scanned := testFunc{file: file, formats: map[string]bool{}}

	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}

		callee, ok := call.Fun.(*ast.Ident)
		if !ok || !formatHelper(callee.Name) {
			return true
		}

		if callee.Name == "fuzzFormatRobust" {
			scanned.robust = true
		}

		literal, ok := call.Args[1].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}

		name, err := strconv.Unquote(literal.Value)
		require.NoErrorf(t, err, "unquote the format name %s passes to %s", fn.Name.Name, callee.Name)

		scanned.formats[name] = true

		return true
	})

	return scanned
}

// sourceName resolves a coverage source to the name of the function it holds,
// for a failure message. A top-level function value resolves to its qualified
// name; anything else resolves to the empty string, which the guard reports.
func sourceName(source coverageSource) string {
	value := reflect.ValueOf(source)
	if value.Kind() != reflect.Func || value.IsNil() {
		return ""
	}

	fn := runtime.FuncForPC(value.Pointer())
	if fn == nil {
		return ""
	}

	return fn.Name()
}

// declaredName reduces a qualified function name to the identifier the source
// declares, the key the index uses.
func declaredName(qualified string) string {
	dot := strings.LastIndex(qualified, ".")
	if dot < 0 {
		return qualified
	}

	return qualified[dot+1:]
}
