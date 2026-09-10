package format_test

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rig 3, layer 2, the V8 oracle. The regex format asserts ECMA-262 syntax,
// and no Go parser reads that grammar: RE2 lacks backreferences and
// lookaround and reads flag groups and named groups by its own rules. The
// oracle is V8 itself, reached through the node runtime devbox provides:
// testdata/oracle/ecma_regex.mjs compiles each pattern with `new RegExp` and
// no flags, which is the bare Annex B pattern the format judges. Agreement is
// two-way. The format accepts a pattern exactly when V8 compiles it, so a
// false rejection and a false acceptance both fail here, with one exception
// regexV8Quirk names.
//
// A missing node is a failure rather than a skip. The gate runs inside the
// devbox environment, and a skip would let the format drift from the engine
// on any host that lacks the runtime.

// v8Oracle is one node process answering compile queries over a pipe pair.
type v8Oracle struct {
	mu  sync.Mutex
	in  io.WriteCloser
	out *bufio.Scanner
	err error
}

// v8Answer is one line the oracle process writes.
type v8Answer struct {
	OK  bool   `json:"ok"`
	Err string `json:"err"`
}

var (
	v8Once sync.Once
	v8     *v8Oracle

	// The namedGroup regexp finds a named group's name, in the "(?<name>"
	// spelling alone, so the lookbehind introducers do not match.
	namedGroup = regexp.MustCompile(`\(\?<([^>=!][^>]*)>`)
)

// The reasonV8DuplicateName constant is the one divergence the format keeps
// from V8. ECMA-262 22.2.1.1 refuses a capture name defined twice wherever
// both groups can take part in one match, and admits it only across the
// alternatives of one disjunction. V8 (node 24) misjudges some patterns of
// that shape: it compiles "(?<a>(?<a>x)|y)" and "(?<a>)(?:|(?<a>))",
// where the two groups take part in one match, while refusing
// "(?<a>y|(?<a>x))" and "(?<a>)(?:(?<a>)|)". The format reads the
// specification and refuses all four, so where a pattern with a '|' defines
// a name twice, a V8 acceptance the format refuses is not compared. The rows
// of regex.tsv still pin the cross-alternative patterns every engine admits.
const reasonV8DuplicateName = "V8 admits some capture names defined twice across a '|' where both groups take part in one match"

// regexV8Quirk reports whether s has the shape V8 misjudges, with the reason:
// a '|' and a capture name defined twice. That over-approximates the quirk,
// and an over-approximation is safe here, since the caller skips only a V8
// acceptance the format refuses.
func regexV8Quirk(s string) (string, bool) {
	if !strings.Contains(s, "|") {
		return "", false
	}

	seen := map[string]bool{}

	for _, m := range namedGroup.FindAllStringSubmatch(s, -1) {
		if seen[m[1]] {
			return reasonV8DuplicateName, true
		}

		seen[m[1]] = true
	}

	return "", false
}

// ecmaOracle returns the package's oracle process, started on first use and
// left to exit with the test binary, which closes its stdin.
func ecmaOracle(tb testing.TB) *v8Oracle {
	tb.Helper()

	v8Once.Do(func() { v8 = startV8Oracle() })

	require.NoError(tb, v8.err,
		"the ECMA-262 regex oracle needs node on PATH; devbox provides it (devbox shell, or devbox run -- task check)")

	return v8
}

// startV8Oracle launches the oracle script and wires its pipes.
func startV8Oracle() *v8Oracle {
	cmd := exec.CommandContext(context.Background(), "node", filepath.Join("testdata", "oracle", "ecma_regex.mjs"))
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()
	if err != nil {
		return &v8Oracle{err: fmt.Errorf("open the oracle's stdin: %w", err)}
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return &v8Oracle{err: fmt.Errorf("open the oracle's stdout: %w", err)}
	}

	err = cmd.Start()
	if err != nil {
		return &v8Oracle{err: fmt.Errorf("start the oracle: %w", err)}
	}

	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	return &v8Oracle{in: in, out: scanner}
}

// compiles reports whether V8 compiles s as a bare pattern, with the
// SyntaxError message when it does not. The pattern crosses the pipe as hex,
// so a line terminator inside it stays inside the line.
func (o *v8Oracle) compiles(tb testing.TB, s string) (bool, string) {
	tb.Helper()

	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.in.Write([]byte(hex.EncodeToString([]byte(s)) + "\n"))
	require.NoError(tb, err, "write the pattern to the oracle")

	require.True(tb, o.out.Scan(), "the oracle closed its output: %v", o.out.Err())

	var answer v8Answer

	require.NoError(tb, json.Unmarshal(o.out.Bytes(), &answer), "decode the oracle's answer %q", o.out.Bytes())

	return answer.OK, answer.Err
}

// TestRegexVectorsAgreeWithV8 holds every row of regex.tsv to V8's verdict,
// so the vector file cannot pin a boundary the engine does not have.
func TestRegexVectorsAgreeWithV8(t *testing.T) {
	t.Parallel()

	oracle := ecmaOracle(t)

	for name, row := range loadVectorFile(t, filepath.Join(vectorDir, "regex.tsv")) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ok, message := oracle.compiles(t, row.instance)

			if reason, quirk := regexV8Quirk(row.instance); quirk && ok && !row.valid {
				t.Skip(reason)
			}

			assert.Equalf(t, row.valid, ok,
				"regex.tsv says valid=%v for %s, V8 says compiles=%v (%s)", row.valid, name, ok, message)
		})
	}
}

// TestRegexV8QuirkIsLive asserts the quirk still holds in the node on PATH:
// some row of regex.tsv has the shape, and V8 compiles what that row refuses.
// Once a node release reads the specification here, this fails, and the fix
// is to delete regexV8Quirk and let the rows run.
func TestRegexV8QuirkIsLive(t *testing.T) {
	t.Parallel()

	oracle := ecmaOracle(t)
	live := false

	for _, row := range loadVectorFile(t, filepath.Join(vectorDir, "regex.tsv")) {
		if _, quirk := regexV8Quirk(row.instance); !quirk {
			continue
		}

		ok, _ := oracle.compiles(t, row.instance)
		if ok && !row.valid {
			live = true
		}
	}

	assert.True(t, live, "no regex.tsv row shows the V8 quirk regexV8Quirk names; delete the carve-out")
}

// FuzzFormatRegexVsV8 differentials the regex validator against V8 in both
// directions: the format accepts s exactly when `new RegExp(s)` succeeds.
// Seeds are every vector row, the suite's regex format cases under both
// drafts, and the patterns the retired RE2 differential carried. A string
// that is not UTF-8 is skipped, since node would decode its bytes to
// replacement characters and the two sides would judge different strings; the
// format refuses such a string on its own.
func FuzzFormatRegexVsV8(f *testing.F) {
	fn := validator(f, "regex")

	for _, row := range loadVectorFile(f, filepath.Join(vectorDir, "regex.tsv")) {
		f.Add(row.instance)
	}

	for _, seed := range suiteRegexFormatCases(f) {
		f.Add(seed)
	}

	for _, seed := range []string{
		"^[a-z]+$", `(foo)\1`, "foo(?=bar)", "[abc]", "a{2,3}", `\a`, `\_`, `\c`,
		`\Q[\E`, "[]", "[]Z(]", "(", "[", `\`, "a|b", `\p{L}`, "", "*a", "a**",
		"{1}", "a{2,1}", "a{,5}", "a+?", "(?i)a*", "(?P<n>a)+", "{00}", "a{}",
		"(?i:a)", "(?ims-i:a)", `\b+`, "^*", "$?", `\B{2}`, "(?<a>x)|(?<a>y)",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}

		sut := fn(s) == nil

		ok, message := ecmaOracle(t).compiles(t, s)
		if sut == ok {
			return
		}

		if _, quirk := regexV8Quirk(s); quirk && ok && !sut {
			return
		}

		t.Fatalf("the regex format and V8 disagree on %q: format accepts=%v, V8 compiles=%v (%s)",
			s, sut, ok, message)
	})
}

// suiteRegexFormatCases returns every string instance the official suite
// holds against the regex format, under both vendored drafts.
func suiteRegexFormatCases(tb testing.TB) []string {
	tb.Helper()

	type suiteGroup struct {
		Schema map[string]any `json:"schema"`
		Tests  []struct {
			Data any `json:"data"`
		} `json:"tests"`
	}

	var cases []string

	for _, draft := range []string{"draft7", "draft2020-12"} {
		for _, file := range []string{"ecmascript-regex.json", "regex.json"} {
			path := filepath.Join("..", "..", "testdata", "suite", draft, "optional", "format", file)

			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}

			require.NoError(tb, err, "read %s", path)

			var groups []suiteGroup

			require.NoError(tb, json.Unmarshal(data, &groups), "parse %s", path)

			for _, group := range groups {
				if group.Schema["format"] != "regex" {
					continue
				}

				for _, test := range group.Tests {
					if s, ok := test.Data.(string); ok {
						cases = append(cases, s)
					}
				}
			}
		}
	}

	require.NotEmpty(tb, cases, "the vendored suite holds no regex format case")

	return cases
}
