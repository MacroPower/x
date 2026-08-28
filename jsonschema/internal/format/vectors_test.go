package format_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rig 3, layer 3 -- curated / vendored acceptance vectors. For formats with an
// authoritative corpus or crisp RFC examples but no clean stdlib oracle, these
// table-driven tests lock the accept/reject boundary against fixed vectors drawn
// from the RFCs' own example text. They complement the robustness fuzz (layer 1)
// and the stdlib differentials (layer 2), and sit alongside the official JSON
// Schema Test Suite's optional/format cases, which already run via
// TestSuiteFormat. Each vector file names the suite coverage it complements and
// carries what that coverage leaves open, restating a suite case only where a
// boundary reads better whole in one place. So no vector file re-transcribes
// RFC 3339's leap second or the RFC 6901 §5 pointer examples. Those §5 examples
// are all valid, which leaves the json-pointer escape boundary open, so
// json-pointer.tsv carries it.
//
// One accepted coverage gap. One-way containment from uri and uri-reference
// (containment_test.go), the ucschar cases in format_iri_ucschar_test.go, and
// the RFC 3987 example rows in testdata/vectors pin the iri and iri-reference
// formats. Containment cannot catch over-acceptance on the ucschar side, and
// there is no public IRI corpus and no stdlib IRI parser to differential
// against, so that side stays uncovered.

// formatVector is one acceptance vector: an instance and whether the format
// validator must accept it.
type formatVector struct {
	instance string
	valid    bool
}

// runFormatVectors drives a validity table against the named validator.
func runFormatVectors(t *testing.T, name string, cases map[string]formatVector) {
	t.Helper()

	validate := validator(t, name)

	for caseName, tc := range cases {
		t.Run(caseName, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err, "%q should be a valid %s", tc.instance, name)
			} else {
				require.Error(t, err, "%q should be an invalid %s", tc.instance, name)
			}
		})
	}
}

var (
	// The RFC 3986 §5.4.1 normal and §5.4.2 abnormal reference-resolution
	// inputs, verbatim and in order. Every one is a well-formed
	// URI-reference; the section exists to pin merge and dot-segment removal,
	// so it is the densest set of relative-reference shapes the RFC publishes
	// and the suite carries none of it.
	rfc3986References = []string{
		// §5.4.1 Normal Examples.
		"g:h", "g", "./g", "g/", "/g", "//g", "?y", "g?y", "#s", "g#s",
		"g?y#s", ";x", "g;x", "g;x?y#s", "", ".", "./", "..", "../", "../g",
		"../..", "../../", "../../g",

		// §5.4.2 Abnormal Examples.
		"../../../g", "../../../../g", "/./g", "/../g", "g.", ".g", "g..",
		"..g", "./../g", "./g/.", "g/./h", "g/../h", "g;x=1/./y", "g;x=1/../y",
		"g?y/./x", "g?y/../x", "g#s/./x", "g#s/../x", "http:g",
	}

	// The distinct §5.4 resolution targets. Each is an absolute URI, so each
	// must satisfy the uri format as well as uri-reference: a reference that
	// resolves against "http://a/b/c/d;p?q" and produces something the uri
	// format rejects would make the two formats disagree about the same string.
	rfc3986ResolvedTargets = []string{
		"g:h", "http://a/b/c/g", "http://a/b/c/g/", "http://a/g", "http://g",
		"http://a/b/c/d;p?y", "http://a/b/c/g?y", "http://a/b/c/d;p?q#s",
		"http://a/b/c/g#s", "http://a/b/c/g?y#s", "http://a/b/c/;x",
		"http://a/b/c/g;x", "http://a/b/c/g;x?y#s", "http://a/b/c/d;p?q",
		"http://a/b/c/", "http://a/b/", "http://a/b/g", "http://a/",
		"http://a/b/c/g.", "http://a/b/c/.g", "http://a/b/c/g..",
		"http://a/b/c/..g", "http://a/b/c/g/h", "http://a/b/c/h",
		"http://a/b/c/g;x=1/y", "http://a/b/c/y", "http://a/b/c/g?y/./x",
		"http://a/b/c/g?y/../x", "http://a/b/c/g#s/./x", "http://a/b/c/g#s/../x",
		"http:g",
	}
)

// TestURIReferenceResolutionVectors runs every RFC 3986 §5.4 reference-
// resolution input through the uri-reference format. §5.4 is the RFC's own
// inventory of relative-reference shapes -- dot segments, empty references,
// semicolon parameters, a query or fragment carrying what looks like a path --
// and none of it appears in the vendored suite. The two inputs that carry a
// scheme are rows in testdata/vectors/uri.tsv, since a relative-ref cannot
// begin with one.
func TestURIReferenceResolutionVectors(t *testing.T) {
	t.Parallel()

	cases := make(map[string]formatVector, len(rfc3986References))
	for _, ref := range rfc3986References {
		cases["reference "+ref] = formatVector{instance: ref, valid: true}
	}

	runFormatVectors(t, "uri-reference", cases)
}

// TestURIResolutionTargetVectors runs every RFC 3986 §5.4 resolution target
// through the uri format. These are the strings a caller holds after resolving
// a reference against a base, so the format must accept them if the resolution
// step is to be usable.
func TestURIResolutionTargetVectors(t *testing.T) {
	t.Parallel()

	cases := make(map[string]formatVector, len(rfc3986ResolvedTargets))
	for _, target := range rfc3986ResolvedTargets {
		cases["target "+target] = formatVector{instance: target, valid: true}
	}

	runFormatVectors(t, "uri", cases)
}

// vectorDir holds one acceptance-vector file per format, each file named for
// the format it covers.
const vectorDir = "testdata/vectors"

// TestFormatVectors runs every vector file under testdata/vectors through the
// validator its basename names. Adding a file adds a test with no Go edit. A
// basename that names no registered format fails inside runFormatVectors, so a
// renamed format cannot leave an orphaned file sitting unexercised.
func TestFormatVectors(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(vectorDir)
	require.NoError(t, err, "read vector directory %s", vectorDir)

	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			require.False(t, entry.IsDir(), "%s holds vector files, not directories", vectorDir)

			name, ok := strings.CutSuffix(entry.Name(), ".tsv")
			require.True(t, ok, "vector file %s must carry the .tsv extension", entry.Name())

			runFormatVectors(t, name, loadVectorFile(t, filepath.Join(vectorDir, entry.Name())))
		})
	}
}

// loadVectorFile reads an acceptance-vector file: three tab-separated fields per
// row, "<input>", <valid>, <note>, with '#' comment lines and blank lines
// skipped. The map is keyed by the raw quoted field, which is printable by
// construction and unique once a duplicate fails the load, so it names the
// subtest without mangling an invisible character.
//
// The input is a Go quoted string literal, which is what lets a row carry the
// empty string, a tab, an invisible joiner, or a lone invalid UTF-8 byte. Write
// it in the spelling strconv.Quote produces. Printable non-ASCII stays literal,
// so "münchen.de" is the intended form rather than "m\u00fcnchen.de".
//
// Every field is mandatory. A note that rots to empty, a validity column that
// reads neither "true" nor "false", and an input that repeats an earlier row all
// fail the load, because each of the three would otherwise degrade a row into a
// weaker test that still passes. The note documents the row for whoever reads
// the file; the failure messages identify a row by its input.
func loadVectorFile(t *testing.T, path string) map[string]formatVector {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read vector file %s", path)

	cases := make(map[string]formatVector)
	seen := make(map[string]int)

	scanner := bufio.NewScanner(strings.NewReader(string(data)))

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		fields := strings.SplitN(text, "\t", 3)
		require.Len(t, fields, 3, "%s:%d: want three tab-separated fields in %q", path, line, text)

		quoted, validText, note := fields[0], fields[1], fields[2]
		require.NotEmpty(t, strings.TrimSpace(note), "%s:%d: the note field is mandatory", path, line)

		input, err := strconv.Unquote(quoted)
		require.NoError(t, err, "%s:%d: input %s is not a Go quoted string", path, line, quoted)

		require.Contains(t, []string{"true", "false"}, validText,
			"%s:%d: validity must be true or false", path, line)

		prior, duplicate := seen[input]
		require.False(t, duplicate, "%s:%d: input %s repeats the row on line %d", path, line, quoted, prior)

		seen[input] = line

		cases[quoted] = formatVector{instance: input, valid: validText == "true"}
	}

	require.NoError(t, scanner.Err(), "scan vector file %s", path)
	require.NotEmpty(t, cases, "vector file %s carries no rows", path)

	return cases
}
