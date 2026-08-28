package format_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

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
// leaving a claim that reads true and means nothing. Two limits of that
// mechanism are worth stating outright. A function reference proves the
// identifier exists, not that the target it names actually exercises the format
// the row claims it for; and a new differential that nobody adds to the table
// stays invisible to this test, since nothing enumerates the package's fuzz
// targets. The table is a declaration, and it is only as honest as its author.
//
// Containment targets (containment_test.go) are deliberately not a coverage
// source. Each asserts one direction of a subset relation, which cannot catch
// over-acceptance, so a format covered by containment alone is not covered.

// coverageSource is a differential target or a corpus loader, held as the
// function value so the compiler enforces that the identifier exists.
type coverageSource any

// coverage states how one format's accept/reject boundary is pinned. A row
// carries sources or a reason, never both: a reason is the admission that the
// format has no mechanical coverage, and a source contradicts it.
type coverage struct {
	// Differential fuzz targets asserting this format against an oracle.
	differentials []coverageSource

	// Loaders for a vendored conformance corpus this format runs.
	corpus []coverageSource

	// Whether testdata/vectors carries a file for this format.
	vectors bool

	// Why this format has no coverage at all. Empty on a covered row.
	reason string
}

// formatCoverage maps every built-in format to the tests pinning it. The
// allowlist is empty: every one of the nineteen carries at least one source.
var formatCoverage = map[string]coverage{
	"date-time": {differentials: []coverageSource{FuzzFormatDateTimeVsRFC3339}, vectors: true},
	"date":      {differentials: []coverageSource{FuzzFormatDateVsTimeParse}},
	"time":      {differentials: []coverageSource{FuzzFormatTimeVsTimeParse}},
	"duration":  {differentials: []coverageSource{FuzzFormatDurationVsABNF}, vectors: true},
	"email": {
		differentials: []coverageSource{FuzzFormatEmailVsNetMail},
		corpus:        []coverageSource{loadISEmailCorpus},
		vectors:       true,
	},
	"idn-email":             {corpus: []coverageSource{loadISEmailCorpus}, vectors: true},
	"hostname":              {differentials: []coverageSource{FuzzFormatHostnameVsIDNA}, vectors: true},
	"idn-hostname":          {differentials: []coverageSource{FuzzFormatIDNHostnameVsIDNA}, vectors: true},
	"uri":                   {differentials: []coverageSource{FuzzFormatURIVsNetURL}, vectors: true},
	"uri-reference":         {differentials: []coverageSource{FuzzFormatURIReferenceVsNetURL}, vectors: true},
	"uri-template":          {corpus: []coverageSource{loadURITemplateCorpus}},
	"iri":                   {vectors: true},
	"iri-reference":         {vectors: true},
	"uuid":                  {differentials: []coverageSource{FuzzFormatUUIDVsGrammar}, vectors: true},
	"ipv4":                  {differentials: []coverageSource{FuzzFormatIPv4VsNetip}},
	"ipv6":                  {differentials: []coverageSource{FuzzFormatIPv6VsNetip}},
	"json-pointer":          {vectors: true},
	"relative-json-pointer": {vectors: true},
	"regex":                 {differentials: []coverageSource{FuzzFormatRegexVsRE2}, vectors: true},
}

// TestFormatCoverage asserts that the table and the registry describe the same
// nineteen formats, that each row claims coverage that exists, and that a row
// claiming a vector file has one carrying rows.
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

			sources := len(row.differentials) + len(row.corpus)
			if row.vectors {
				sources++
			}

			if row.reason != "" {
				require.Zero(t, sources,
					"format %q carries both coverage and a reason for having none: %s", name, row.reason)

				return
			}

			require.NotZero(t, sources, "format %q has an empty coverage row", name)

			for _, source := range append(append([]coverageSource(nil), row.differentials...), row.corpus...) {
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
// does not, and that every vector file is claimed by a row. Both directions
// matter: a row for a format that no longer exists sits unexercised, and a
// vector file no row claims would make the coverage table read as complete
// while the guard skipped a file.
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
