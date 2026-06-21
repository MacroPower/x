// Package main implements the jsonschemagen CLI tool, which generates JSON
// Schema files from Go types at build time.
//
// Usage:
//
//	//go:generate go run go.jacobcolvin.com/x/jsonschema/cmd/jsonschemagen -type Config -o config.schema.json
//
// The tool works by creating a temporary Go program that imports the target
// package, calls [jsonschema.Generate], and outputs the resulting JSON, so it
// reuses the library's generation pipeline rather than duplicating it.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

type config struct {
	TypeName             string
	Output               string
	Draft                string
	Indent               string
	Comments             bool
	AdditionalProperties bool
	Validate             bool
}

func main() {
	cfg := config{}

	flag.StringVar(&cfg.TypeName, "type", "", "Go type name to generate schema for (required)")
	flag.StringVar(&cfg.Output, "o", "", "output file path (default: stdout)")
	flag.StringVar(&cfg.Draft, "draft", "2020", `JSON Schema draft: "7" or "2020"`)
	flag.BoolVar(&cfg.Comments, "comments", false, "extract Go doc comments as descriptions")
	flag.BoolVar(&cfg.AdditionalProperties, "additional-properties", false, "allow additional properties")
	flag.StringVar(&cfg.Indent, "indent", "  ", "JSON indentation string")
	flag.BoolVar(&cfg.Validate, "validate", false, "add validate tag interpreter")
	flag.Parse()

	// Reject leftover positional arguments so a mistyped invocation (a stray
	// token in a //go:generate line, a value given without its flag) fails
	// loudly instead of generating against the default configuration.
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "jsonschemagen: unexpected arguments: %v\n", flag.Args())
		os.Exit(2)
	}

	err := run(cfg, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jsonschemagen: %v\n", err)
		os.Exit(1)
	}
}

func run(cfg config, stdout io.Writer) error {
	if cfg.TypeName == "" {
		return fmt.Errorf("-type flag is required")
	}

	if cfg.Draft != "7" && cfg.Draft != "2020" {
		return fmt.Errorf("unsupported draft %q: must be \"7\" or \"2020\"", cfg.Draft)
	}

	// The indent string is embedded verbatim into json.MarshalIndent, which does
	// not validate it. A non-whitespace indent is repeated between JSON tokens
	// and produces output that no longer parses, so reject it up front.
	if strings.TrimLeft(cfg.Indent, " \t\n\r") != "" {
		return fmt.Errorf("invalid -indent %q: must contain only whitespace", cfg.Indent)
	}

	importPath, err := resolveImportPath()
	if err != nil {
		return fmt.Errorf("resolve import path: %w", err)
	}

	modPath, modDir, err := resolveModuleInfo()
	if err != nil {
		return fmt.Errorf("resolve module info: %w", err)
	}

	jsonschemaDir, err := resolveJSONSchemaDir()
	if err != nil {
		return fmt.Errorf("resolve jsonschema dir: %w", err)
	}

	err = checkReplaceDir("module directory", modDir)
	if err != nil {
		return err
	}

	err = checkReplaceDir("jsonschema directory", jsonschemaDir)
	if err != nil {
		return err
	}

	tempDir, err := createTempDir(cfg, importPath, modPath, modDir, jsonschemaDir)
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	output, err := runGenerate(tempDir)
	if err != nil {
		return err
	}

	if cfg.Output != "" {
		return writeFileAtomic(cfg.Output, output, 0o644)
	}

	_, err = stdout.Write(output)

	return err
}

// writeFileAtomic writes data to path by writing a temp file in the same
// directory and renaming it into place, so a failed write never truncates or
// corrupts a file already at path (unlike os.WriteFile, which opens with
// O_TRUNC first). The rename is atomic within a filesystem; the temp file
// shares path's directory to keep it on the same one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpName := tmp.Name()

	// A close error can mean unflushed data, so it matters as much as a write
	// error; take whichever failed first.
	_, writeErr := tmp.Write(data)
	err = cmp.Or(writeErr, tmp.Close())

	// CreateTemp makes the file 0600, so set the requested mode explicitly.
	if err == nil {
		err = os.Chmod(tmpName, perm)
	}

	if err == nil {
		err = os.Rename(tmpName, path)
	}

	if err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}

// resolveImportPath returns the import path of the current package. It rejects a
// main package up front: jsonschemagen generates a program that imports the
// target package to reflect over it, and Go forbids importing a package main,
// which would otherwise fail late with the opaque "is a program, not an
// importable package" build error from `go run`. The package name comes from the
// same `go list` call, so the check costs no extra process.
func resolveImportPath() (string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-f", "{{.Name}} {{.ImportPath}}", ".")
	out, err := cmd.Output()
	if err != nil {
		return "", cmdError(err)
	}

	// A package name and an import path are each a single space-free token, so
	// the first space separates them.
	name, importPath, ok := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !ok {
		return "", fmt.Errorf("parse go list output %q", strings.TrimSpace(string(out)))
	}

	if name == "main" {
		return "", fmt.Errorf(
			"cannot generate a schema for a type in package main (%s): jsonschemagen imports "+
				"the target package to reflect over it, which Go does not allow for a main package; "+
				"move the type to an importable (non-main) package",
			importPath,
		)
	}

	return importPath, nil
}

// moduleInfo holds the path and directory of a Go module.
type moduleInfo struct {
	Path  string `json:"Path"`
	Dir   string `json:"Dir"`
	GoMod string `json:"GoMod"`
}

// resolveModuleInfo returns the path and directory of the main module that
// contains the current package.
func resolveModuleInfo() (string, string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-json")
	out, err := cmd.Output()
	if err != nil {
		return "", "", cmdError(err)
	}

	goMod, err := currentGoMod()
	if err != nil {
		return "", "", fmt.Errorf("resolve current go.mod: %w", err)
	}

	return selectMainModule(out, goMod)
}

// selectMainModule picks the main module from a `go list -m -json` stream.
//
// Under a Go workspace (go.work), `go list -m -json` with no module argument
// emits a concatenated JSON stream with one object per workspace module, all
// flagged as main. The module whose go.mod matches goMod (the current
// directory's module, from `go env GOMOD`) is selected, so generation targets
// the right module rather than whichever object appears first.
//
// An empty goMod means the caller is outside a module, where the first object
// is the right answer. Inside a module a stream with no matching object means
// the current module is absent, so returning an arbitrary module would point
// generation at the wrong source tree; that is reported as an error rather than
// silently falling back to the first object.
func selectMainModule(stream []byte, goMod string) (string, string, error) {
	dec := json.NewDecoder(bytes.NewReader(stream))

	var (
		firstPath, firstDir string
		haveFirst           bool
	)

	for dec.More() {
		var info moduleInfo

		err := dec.Decode(&info)
		if err != nil {
			return "", "", fmt.Errorf("parse module info: %w", err)
		}

		if goMod != "" && info.GoMod == goMod {
			return info.Path, info.Dir, nil
		}

		if !haveFirst {
			firstPath, firstDir, haveFirst = info.Path, info.Dir, true
		}
	}

	if !haveFirst {
		return "", "", fmt.Errorf("parse module info: no module reported")
	}

	if goMod != "" {
		return "", "", fmt.Errorf("parse module info: no module matches %q", goMod)
	}

	return firstPath, firstDir, nil
}

// currentGoMod returns the absolute path of the go.mod for the current
// directory's main module via `go env GOMOD`. An empty string with a nil error
// means the current directory is not within a module (the os.DevNull sentinel
// `go env GOMOD` reports). A failure of the command itself is returned as an
// error rather than an empty string, so a transient failure is not mistaken for
// being outside a module, which would silently target the first workspace
// module instead of the current one.
func currentGoMod() (string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return "", cmdError(err)
	}

	goMod := strings.TrimSpace(string(out))
	// `go env GOMOD` reports os.DevNull when outside a module.
	if goMod == os.DevNull {
		return "", nil
	}

	return goMod, nil
}

// resolveJSONSchemaDir returns the local directory of the jsonschema module.
func resolveJSONSchemaDir() (string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-json", jsonschemaModule)
	out, err := cmd.Output()
	if err != nil {
		return "", cmdError(err)
	}

	var info moduleInfo

	err = json.Unmarshal(out, &info)
	if err != nil {
		return "", fmt.Errorf("parse jsonschema module info: %w", err)
	}

	return info.Dir, nil
}

const jsonschemaModule = "go.jacobcolvin.com/x/jsonschema"

func createTempDir(cfg config, importPath, modPath, modDir, jsonschemaDir string) (string, error) {
	tempDir, err := os.MkdirTemp("", "jsonschemagen-*")
	if err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	// Render main.go.
	var mainBuf bytes.Buffer

	err = renderMainGo(&mainBuf, cfg, importPath)
	if err != nil {
		os.RemoveAll(tempDir)

		return "", fmt.Errorf("render main.go: %w", err)
	}

	err = os.WriteFile(filepath.Join(tempDir, "main.go"), mainBuf.Bytes(), 0o644)
	if err != nil {
		os.RemoveAll(tempDir)

		return "", fmt.Errorf("write main.go: %w", err)
	}

	// Render go.mod.
	goMod := renderGoMod(modPath, modDir, jsonschemaDir)

	err = os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goMod), 0o644)
	if err != nil {
		os.RemoveAll(tempDir)

		return "", fmt.Errorf("write go.mod: %w", err)
	}

	// Seed go.sum with the merged checksums of the user's module and the
	// jsonschema module. The temp module requires jsonschema (via a local
	// replace), whose transitive dependencies' checksums live in jsonschema's
	// own go.sum but are absent from a user module that does not import
	// jsonschema. Without them, go mod tidy must re-resolve from the network
	// and fails in an offline/air-gapped sandbox. A read miss on either file is
	// expected (a module may have no dependencies yet); a write failure is not.
	sum := mergeGoSum(filepath.Join(modDir, "go.sum"), filepath.Join(jsonschemaDir, "go.sum"))
	if len(sum) > 0 {
		err = os.WriteFile(filepath.Join(tempDir, "go.sum"), sum, 0o644)
		if err != nil {
			os.RemoveAll(tempDir)

			return "", fmt.Errorf("write go.sum: %w", err)
		}
	}

	return tempDir, nil
}

// mergeGoSum reads the given go.sum files and returns their union, preserving
// first-seen order and dropping duplicate and blank lines. Missing files are
// skipped. The result is empty when no file yields any entry.
//
// Entries are keyed on a line's module-and-version prefix (its first two
// fields). When two files give the same key the same checksum, the duplicate is
// dropped. When they give it conflicting checksums (only possible from a
// corrupted or stale go.sum, since a checksum is derived from the module's
// content), the entry is omitted entirely rather than guessing which is correct,
// so go mod tidy re-resolves that module's checksum from the cache or proxy.
func mergeGoSum(paths ...string) []byte {
	type sumEntry struct {
		line     string
		conflict bool
	}

	entries := make(map[string]*sumEntry)

	var order []string

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}

		for line := range strings.SplitSeq(string(data), "\n") {
			// Tolerate CRLF-terminated go.sum files (e.g. produced by a
			// line-ending normalization rule): a stray trailing \r would both
			// defeat the conflict comparison against an LF twin and write an
			// invalid \r-suffixed checksum line that go mod tidy rejects.
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				continue
			}

			key := goSumKey(line)

			existing, seen := entries[key]
			if !seen {
				entries[key] = &sumEntry{line: line}
				order = append(order, key)

				continue
			}

			if existing.line != line {
				existing.conflict = true
			}
		}
	}

	var b bytes.Buffer

	for _, key := range order {
		entry := entries[key]
		if entry.conflict {
			continue
		}

		b.WriteString(entry.line)
		b.WriteByte('\n')
	}

	return b.Bytes()
}

// goSumKey returns the deduplication key for a go.sum line: its
// module-and-version prefix, the first two space-separated fields (the second
// carries the optional /go.mod suffix that distinguishes the module-zip and
// go.mod checksums). A line without two fields keys on its whole self.
func goSumKey(line string) string {
	first := strings.IndexByte(line, ' ')
	if first < 0 {
		return line
	}

	second := strings.IndexByte(line[first+1:], ' ')
	if second < 0 {
		return line
	}

	return line[:first+1+second]
}

func renderGoMod(modPath, modDir, jsonschemaDir string) string {
	var b strings.Builder

	b.WriteString("module _jsonschemagen_tmp\n\n")
	b.WriteString("go " + goDirectiveVersion() + "\n\n")

	b.WriteString("require (\n")
	b.WriteString("\t" + modPath + " v0.0.0\n")

	if modPath != jsonschemaModule {
		b.WriteString("\t" + jsonschemaModule + " v0.0.0\n")
	}

	b.WriteString(")\n\n")

	b.WriteString("replace " + modPath + " => " + quotePath(modDir) + "\n")

	if modPath != jsonschemaModule {
		b.WriteString("replace " + jsonschemaModule + " => " + quotePath(jsonschemaDir) + "\n")
	}

	return b.String()
}

// goVersionPattern matches the "goMAJOR.MINOR" prefix of a toolchain version
// string. It anchors at the start, after an optional "devel " token, so it
// reads only the leading version and never a "goMAJOR.MINOR" sequence embedded
// later in the build metadata. This covers every form runtime.Version()
// produces: "go1.26.0", a release candidate "go1.25rc1", and a development
// build "devel go1.26-abc123 ...".
var goVersionPattern = regexp.MustCompile(`^(?:devel )?go(\d+)\.(\d+)`)

// defaultGoDirectiveVersion is the fallback "go" directive value used when the
// running toolchain version cannot be parsed. It must be a valid directive so
// the generated go.mod always parses.
const defaultGoDirectiveVersion = "1.21"

// goDirectiveVersion returns a valid major.minor Go version for the go.mod "go"
// directive, derived from the running toolchain rather than hardcoded.
//
// Runtime.Version() is not always a plain "goMAJOR.MINOR.PATCH" string: release
// candidates report "go1.25rc1" and development builds report
// "devel go1.26-abc123 ...". Extracting just the major.minor pair keeps the
// emitted directive valid across all of these, since a raw release-candidate or
// multi-token devel string is rejected by the go.mod parser.
func goDirectiveVersion() string {
	m := goVersionPattern.FindStringSubmatch(runtime.Version())
	if m == nil {
		return defaultGoDirectiveVersion
	}

	return m[1] + "." + m[2]
}

// checkReplaceDir rejects a module directory the go tool cannot place in a
// replace directive. An empty path means `go list -m -json` reported no local
// directory for the module -- it is known to the build but not extracted into
// the module cache (a proxy-only state on a fresh checkout) -- which would emit
// a `replace MODULE =>` line with no target that the modfile parser rejects.
// A backslash trips a different parser rule: the modfile parser treats any
// backslash in a replacement path as a Windows path and rejects it on a
// non-Windows system -- even when the path is quoted, because it inspects the
// unquoted value -- so quoting cannot rescue it. A backslash is a legal POSIX
// filename byte, so a checkout under such a directory is what triggers it.
// Catching both here turns the otherwise cryptic downstream "go mod tidy"
// failure into a clear message at the input boundary.
func checkReplaceDir(label, dir string) error {
	if dir == "" {
		return fmt.Errorf(
			"%s is empty: the module reported no local directory; run `go mod download` first",
			label,
		)
	}

	if strings.Contains(dir, `\`) {
		return fmt.Errorf(
			"%s %q contains a backslash, which the go tool rejects as a Windows path in a replace directive",
			label, dir,
		)
	}

	return nil
}

// quotePath quotes a filesystem path for use in a go.mod replace directive when
// a bare token would be misparsed. The go.mod lexer splits a bare token on
// whitespace and rejects an unquoted token that contains a quote ("unquoted
// string cannot contain quote"), so the path is quoted when it holds a space,
// quote, or backtick, or any control character (a newline or carriage return
// would otherwise inject a bare line break into the directive). All of these
// are legal in POSIX filenames. A backslash is rejected upstream by
// [checkReplaceDir] (quoting cannot make the go tool accept it), so it is not
// handled here.
func quotePath(p string) string {
	if strings.ContainsAny(p, " \"`") || strings.ContainsFunc(p, func(r rune) bool {
		return r < 0x20
	}) {
		return strconv.Quote(p)
	}

	return p
}

//nolint:grouper // Template var kept apart from goVersionPattern; merging unrelated globals hurts readability.
var mainGoTmpl = template.Must(template.New("main.go").Parse(`package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
	{{- if .Validate}}
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
	{{- end}}

	target "{{.ImportPath}}"
)

func main() {
	t := reflect.TypeFor[target.{{.TypeName}}]()
	opts := []jsonschema.GenerateOption{
		{{- if .Draft7}}
		jsonschema.WithDraft(jsonschema.Draft7),
		{{- end}}
		{{- if .Comments}}
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
		{{- end}}
		{{- if .AdditionalProperties}}
		jsonschema.WithAdditionalProperties(true),
		{{- end}}
		{{- if .Validate}}
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		{{- end}}
	}
	schema, err := jsonschema.Generate(context.Background(), t, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(schema, "", {{.IndentLiteral}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}
`))

type templateData struct {
	ImportPath           string
	TypeName             string
	IndentLiteral        string
	Draft7               bool
	Comments             bool
	AdditionalProperties bool
	Validate             bool
}

func renderMainGo(w io.Writer, cfg config, importPath string) error {
	// Guard against injection: the type name and import path are interpolated
	// into a Go source template.
	if !token.IsIdentifier(cfg.TypeName) {
		return fmt.Errorf("invalid type name %q: must be a Go identifier", cfg.TypeName)
	}

	// The type is referenced as target.<TypeName> from a separate generated
	// package, so an unexported name is inaccessible and would otherwise fail
	// late with an opaque "undefined: target.<name>" compiler error. A non-empty
	// name is guaranteed by token.IsIdentifier above, and token.IsExported then
	// applies Go's own definition of exported-ness (the first rune is an
	// upper-case letter), handling a non-ASCII initial letter correctly.
	if !token.IsExported(cfg.TypeName) {
		return fmt.Errorf("invalid type name %q: must be an exported (capitalized) Go identifier", cfg.TypeName)
	}

	if !isValidImportPath(importPath) {
		return fmt.Errorf("invalid import path %q", importPath)
	}

	data := templateData{
		ImportPath:           importPath,
		TypeName:             cfg.TypeName,
		Draft7:               cfg.Draft == "7",
		Comments:             cfg.Comments,
		AdditionalProperties: cfg.AdditionalProperties,
		Validate:             cfg.Validate,
		IndentLiteral:        fmt.Sprintf("%q", cfg.Indent),
	}

	return mainGoTmpl.Execute(w, data)
}

// isValidImportPath reports whether p is a plausible Go import path, rejecting
// characters that could break out of the import declaration string literal. It
// also rejects the DEL (0x7f) and C1 (0x80-0x9f) control characters, which have
// no place in an import path and could corrupt the generated source or a
// terminal rendering an error that echoes the path.
func isValidImportPath(p string) bool {
	if p == "" {
		return false
	}

	for _, r := range p {
		switch {
		// C0 control characters (including tab, 0x09), plus DEL and C1.
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return false
		case r == '"', r == '`', r == '\\', r == ' ':
			return false
		}
	}

	return true
}

func runGenerate(tempDir string) ([]byte, error) {
	// The temp module is self-contained, so neutralize an inherited workspace or
	// vendor mode before invoking the go tool in it: an exported GOWORK lists the
	// user's modules rather than this throwaway one, and GOFLAGS=-mod=vendor
	// demands a vendor dir the temp module lacks. Either would fail the commands
	// below. Appending to os.Environ() keeps GOPATH, GOCACHE, PATH, and proxy
	// settings intact.
	hermeticEnv := append(os.Environ(), "GOWORK=off", "GOFLAGS=")

	tidy := exec.CommandContext(context.Background(), "go", "mod", "tidy")
	tidy.Dir = tempDir
	tidy.Env = hermeticEnv

	out, err := tidy.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go mod tidy: %w: %s", err, out)
	}

	run := exec.CommandContext(context.Background(), "go", "run", ".")
	run.Dir = tempDir
	run.Env = hermeticEnv

	out, err = run.Output()
	if err != nil {
		return nil, fmt.Errorf("generate: %w", cmdError(err))
	}

	return out, nil
}

// cmdError surfaces the trimmed stderr of an *exec.ExitError in the message
// while wrapping the original error, so callers can still recover the exit
// status via errors.As. A non-ExitError (or one without stderr) is returned
// unchanged.
func cmdError(err error) error {
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if ok && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}

	return err
}
