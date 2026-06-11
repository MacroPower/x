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
		return os.WriteFile(cfg.Output, output, 0o644)
	}

	_, err = stdout.Write(output)

	return err
}

// resolveImportPath returns the import path of the current package.
func resolveImportPath() (string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", ".")
	out, err := cmd.Output()
	if err != nil {
		return "", cmdError(err)
	}

	return strings.TrimSpace(string(out)), nil
}

// moduleInfo holds the path and directory of a Go module.
type moduleInfo struct {
	Path  string `json:"Path"`
	Dir   string `json:"Dir"`
	GoMod string `json:"GoMod"`
}

// resolveModuleInfo returns the path and directory of the main module that
// contains the current package.
//
// Under a Go workspace (go.work), `go list -m -json` with no module argument
// emits a concatenated JSON stream with one object per workspace module, all
// flagged as main. The stream is decoded with a [json.Decoder] loop and the
// module whose go.mod matches `go env GOMOD` (the current directory's module)
// is selected, so generation targets the right module rather than whichever
// object happens to appear first.
func resolveModuleInfo() (string, string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-json")
	out, err := cmd.Output()
	if err != nil {
		return "", "", cmdError(err)
	}

	// GoMod is the current directory's module file; it is empty outside a
	// module, in which case the first decoded object is used as a fallback.
	goMod := currentGoMod()

	dec := json.NewDecoder(bytes.NewReader(out))

	var (
		firstPath, firstDir string
		haveFirst           bool
	)

	for dec.More() {
		var info moduleInfo

		err = dec.Decode(&info)
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

	return firstPath, firstDir, nil
}

// currentGoMod returns the absolute path of the go.mod for the current
// directory's main module via `go env GOMOD`, or an empty string if the
// command fails or the current directory is not within a module.
func currentGoMod() string {
	cmd := exec.CommandContext(context.Background(), "go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	goMod := strings.TrimSpace(string(out))
	// `go env GOMOD` reports os.DevNull when outside a module.
	if goMod == os.DevNull {
		return ""
	}

	return goMod
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
		return "", fmt.Errorf("create temp dir: %w", err)
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
func mergeGoSum(paths ...string) []byte {
	seen := make(map[string]struct{})

	var b bytes.Buffer

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}

		for line := range strings.SplitSeq(string(data), "\n") {
			if line == "" {
				continue
			}

			if _, dup := seen[line]; dup {
				continue
			}

			seen[line] = struct{}{}

			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	return b.Bytes()
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
// string. It anchors on the leading "go" so it ignores the trailing build
// metadata in non-release versions such as "go1.25rc1" or the embedded version
// in development builds like "devel go1.26-abc123 ...".
var goVersionPattern = regexp.MustCompile(`go(\d+)\.(\d+)`)

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

// quotePath quotes a filesystem path for use in a go.mod replace directive
// when it contains whitespace, which would otherwise break parsing.
func quotePath(p string) string {
	if strings.ContainsAny(p, " \t") {
		return strconv.Quote(p)
	}

	return p
}

//nolint:grouper // Template var kept apart from goVersionPattern; merging unrelated globals hurts readability.
var mainGoTmpl = template.Must(template.New("main.go").Parse(`package main

import (
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
		jsonschema.WithComments(true),
		{{- end}}
		{{- if .AdditionalProperties}}
		jsonschema.WithAdditionalProperties(true),
		{{- end}}
		{{- if .Validate}}
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
		{{- end}}
	}
	schema, err := jsonschema.Generate(t, opts...)
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
// characters that could break out of the import declaration string literal.
func isValidImportPath(p string) bool {
	if p == "" {
		return false
	}

	for _, r := range p {
		if r < 0x20 || r == '"' || r == '`' || r == '\\' || r == ' ' || r == '\t' {
			return false
		}
	}

	return true
}

func runGenerate(tempDir string) ([]byte, error) {
	tidy := exec.CommandContext(context.Background(), "go", "mod", "tidy")
	tidy.Dir = tempDir

	out, err := tidy.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go mod tidy: %w: %s", err, out)
	}

	run := exec.CommandContext(context.Background(), "go", "run", ".")
	run.Dir = tempDir

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
	var exitErr *exec.ExitError

	if ok := errors.As(err, &exitErr); ok && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}

	return err
}
