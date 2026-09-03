// Package main implements the jsonschemagen CLI tool, which generates JSON
// Schema files from Go types at build time.
//
// Usage:
//
//	//go:generate go run go.jacobcolvin.com/x/jsonschema/cmd/jsonschemagen -type Config -o config.schema.json
//
// The tool builds a small helper program that imports the target package, calls
// [jsonschema.Generate], and writes the resulting JSON to a hand-off file the
// tool reads back, reusing the library's
// generation pipeline rather than duplicating it. The helper is compiled inside
// the target's own module and supplied to the go tool through a build overlay,
// so nothing is written to the source tree and the go tool handles module
// resolution, replace directives, and checksum verification. That module must be
// able to resolve go.jacobcolvin.com/x/jsonschema (via a require, a workspace, or
// a tool directive).
package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// jsonschemaModule is the module path of the jsonschema library the helper
// imports; it seeds the remediation hint when a build cannot resolve it.
const jsonschemaModule = "go.jacobcolvin.com/x/jsonschema"

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

	// The indent string is embedded verbatim into the generated helper's
	// jsontext.WithIndent call, which permits only spaces and tabs and panics
	// on anything else only when the helper runs. Reject the rest up front so
	// the error names the flag.
	if strings.TrimLeft(cfg.Indent, " \t") != "" {
		return fmt.Errorf("invalid -indent %q: must contain only spaces and tabs", cfg.Indent)
	}

	goMod, err := ensureInModule()
	if err != nil {
		return err
	}

	// The build mode (workspace vs single module) governs how every go command
	// below is invoked, so resolve it once. A workspace ignores -mod and rejects
	// it; a single module needs a -mod flag to bypass an inherited vendor
	// directory (readonly where the user's real go.mod is in play, mod where a
	// redirected modfile absorbs the writes).
	inWork, err := inWorkspace()
	if err != nil {
		return err
	}

	importPath, err := resolveImportPath(inWork)
	if err != nil {
		return fmt.Errorf("resolve import path: %w", err)
	}

	output, err := runGenerate(goMod, cfg, importPath, inWork)
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
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

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
// main package up front: jsonschemagen builds a helper that imports the target
// package to reflect over it, and Go forbids importing a package main, which
// would otherwise fail late with the opaque "is a program, not an importable
// package" build error. The package name comes from the same `go list` call, so
// the check costs no extra process.
//
// Outside a workspace it passes -mod=readonly so an inherited vendor directory
// (auto-selected when vendor/modules.txt exists) does not make `go list` fail
// on a vendor tree the helper does not use. Readonly, not mod: this `go list`
// runs against the user's real go.mod with no -modfile redirect, and -mod=mod
// would let an untidy module be silently tidied here, fetching from the network
// and rewriting the user's go.mod/go.sum. An untidy module instead fails with
// go's standard "run go mod tidy" guidance. -mod is rejected in workspace mode,
// where the workspace already governs resolution.
func resolveImportPath(inWork bool) (string, error) {
	args := []string{"list"}
	if !inWork {
		args = append(args, "-mod=readonly")
	}

	args = append(args, "-f", "{{.Name}} {{.ImportPath}}", ".")

	cmd := exec.CommandContext(context.Background(), "go", args...)
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

// ensureInModule verifies the tool is running inside a module and returns the
// absolute path of that module's go.mod, via `go env GOMOD`. A command failure
// is returned as an error rather than mistaken for being outside a module; the
// os.DevNull sentinel (or an empty value in GOPATH mode) that `go env GOMOD`
// reports outside a module yields a clear error, since generation must run
// within the target package's module.
func ensureInModule() (string, error) {
	cmd := exec.CommandContext(context.Background(), "go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return "", cmdError(err)
	}

	goMod := strings.TrimSpace(string(out))
	if goMod == "" || goMod == os.DevNull {
		return "", fmt.Errorf("jsonschemagen must run inside a module (no go.mod found for the current directory)")
	}

	return goMod, nil
}

// runGenerate builds and runs the helper inside the user's module and returns
// the generated schema. The helper writes the schema to a hand-off file in the
// temp dir rather than to its stdout: the helper imports the target package, so
// every init function in that package and its transitive dependencies runs
// before generation, and anything they print to stdout would otherwise be
// captured as part of the schema and corrupt the output. Stray helper stdout is
// forwarded to stderr instead, so a chatty init stays visible without entering
// the data channel.
//
// The helper source is supplied through a
// build overlay so nothing is written to the source tree: a virtual package
// directory under the current directory (the target package dir) maps to a
// backing file in a system temp dir. Placing the virtual package under the
// current directory keeps an internal/ target importable, since the helper is a
// descendant of every internal/ parent the target sits beneath.
//
// GOWORK and vendor/module mode are the user's real build context and are
// inherited rather than neutralized. In a workspace the module graph and
// go.work.sum are already complete. A single-module build seeds a redirected
// modfile so the helper's dependencies (e.g. golang.org/x/tools for -comments)
// resolve from the module cache without mutating the user's go.mod/go.sum.
func runGenerate(goMod string, cfg config, importPath string, inWork bool) ([]byte, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "jsonschemagen-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	schemaPath := filepath.Join(tempDir, "schema.json")

	var helper bytes.Buffer

	err = renderMainGo(&helper, cfg, importPath, schemaPath)
	if err != nil {
		return nil, fmt.Errorf("render helper: %w", err)
	}

	backing := filepath.Join(tempDir, "main.go")

	err = os.WriteFile(backing, helper.Bytes(), 0o644)
	if err != nil {
		return nil, fmt.Errorf("write helper: %w", err)
	}

	// The virtual directory never exists on disk; the overlay maps its main.go
	// to the backing file. A dot prefix keeps it out of `go list ./...` and
	// similar wildcards, mirroring how the go tool ignores dot directories.
	pkgBase := "." + filepath.Base(tempDir)
	virtual := filepath.Join(cwd, pkgBase, "main.go")
	pkgArg := "./" + pkgBase

	overlayPath := filepath.Join(tempDir, "overlay.json")

	err = writeOverlay(overlayPath, virtual, backing)
	if err != nil {
		return nil, err
	}

	env := os.Environ()
	args := []string{"run"}

	if !inWork {
		genMod, err := seedModule(tempDir, goMod, cwd, overlayPath, pkgArg, env)
		if err != nil {
			return nil, err
		}

		// -mod=mod on the run bypasses automatic vendor-mode selection (a
		// vendor/modules.txt the helper's dependencies are absent from); the
		// redirected modfile keeps every write in the temp dir.
		args = append(args, "-mod=mod", "-modfile="+genMod)
	}

	args = append(args, "-overlay="+overlayPath, pkgArg)

	run := exec.CommandContext(context.Background(), "go", args...)
	run.Dir = cwd
	run.Env = env

	out, err := run.Output()
	if err != nil {
		return nil, generateError(err)
	}

	// Anything on the helper's stdout is init-time noise from the target
	// package or its dependencies, never schema data; surface it on stderr.
	if len(out) > 0 {
		fmt.Fprintf(os.Stderr, "%s", out)
	}

	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read generated schema: %w", err)
	}

	return schema, nil
}

// writeOverlay writes a go build overlay mapping the virtual helper file to its
// backing file. The go tool reads the overlay to build a package whose source
// lives outside the directory it nominally occupies.
func writeOverlay(path, virtual, backing string) error {
	overlay := struct {
		Replace map[string]string `json:"Replace"`
	}{
		Replace: map[string]string{virtual: backing},
	}

	data, err := json.Marshal(overlay)
	if err != nil {
		return fmt.Errorf("marshal overlay: %w", err)
	}

	err = os.WriteFile(path, data, 0o644)
	if err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}

	return nil
}

// inWorkspace reports whether a Go workspace is active, via `go env GOWORK`
// (empty or "off" means no workspace). Under a workspace the module graph and
// checksums come from go.work/go.work.sum, and `-modfile` is rejected, so the
// caller must not seed a redirected modfile.
func inWorkspace() (bool, error) {
	cmd := exec.CommandContext(context.Background(), "go", "env", "GOWORK")
	out, err := cmd.Output()
	if err != nil {
		return false, cmdError(err)
	}

	work := strings.TrimSpace(string(out))

	return work != "" && work != "off", nil
}

// seedModule copies the user's go.mod and go.sum into the temp dir and lets the
// go tool complete the helper's requirements and checksums into those copies
// from the module cache, so a single-module build resolves the helper's
// dependencies without touching the user's files. It returns the path of the
// redirected modfile. `go get` does not accept a `-mod` flag; it ignores
// vendoring and resolves against the module cache.
func seedModule(tempDir, goMod, cwd, overlayPath, pkgArg string, env []string) (string, error) {
	genMod := filepath.Join(tempDir, "gen.mod")

	err := copyFile(goMod, genMod)
	if err != nil {
		return "", fmt.Errorf("copy go.mod: %w", err)
	}

	// The go tool derives the sum file from the modfile name (.mod -> .sum); a
	// module with no dependencies has no go.sum yet, which `go get` then creates.
	err = copyFile(filepath.Join(filepath.Dir(goMod), "go.sum"), filepath.Join(tempDir, "gen.sum"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("copy go.sum: %w", err)
	}

	get := exec.CommandContext(context.Background(), "go", "get",
		"-modfile="+genMod, "-overlay="+overlayPath, pkgArg)
	get.Dir = cwd
	get.Env = env

	out, err := get.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if hint := unresolvedHint(msg); hint != "" {
			return "", fmt.Errorf("resolve helper dependencies: %w: %s\n\t%s", err, msg, hint)
		}

		return "", fmt.Errorf("resolve helper dependencies: %w: %s", err, msg)
	}

	return genMod, nil
}

// copyFile copies src to dst. A missing src is reported as an [os.ErrNotExist]
// error so callers can distinguish an optional source from a real failure.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, data, 0o644)
}

var mainGoTmpl = template.Must(template.New("main.go").Parse(`package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	data, err := json.Marshal(schema, jsontext.WithIndent({{.IndentLiteral}}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	data = append(data, '\n')
	err = os.WriteFile({{.OutputLiteral}}, data, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
`))

type templateData struct {
	ImportPath           string
	TypeName             string
	IndentLiteral        string
	OutputLiteral        string
	Draft7               bool
	Comments             bool
	AdditionalProperties bool
	Validate             bool
}

// renderMainGo renders the helper program. The schema lands at outPath, a
// tool-owned file in the temp dir interpolated as a quoted Go string literal
// (like the indent), so the helper's stdout stays free for init-time noise from
// the target package's dependency graph.
func renderMainGo(w io.Writer, cfg config, importPath, outPath string) error {
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
		OutputLiteral:        fmt.Sprintf("%q", outPath),
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

// unresolvedHint returns a remediation hint when go output shows the module
// cannot resolve the generation dependencies, or "" when no such signature is
// present. It is appended to both the seed and build failures, which are the two
// points a missing dependency surfaces (a missing checksum, or no module
// providing the package).
func unresolvedHint(output string) string {
	if strings.Contains(output, "missing go.sum entry") ||
		strings.Contains(output, "no required module provides package") ||
		strings.Contains(output, "cannot find module providing package") {
		return fmt.Sprintf(
			"hint: %s and its generation dependencies must be resolvable in this module; "+
				"run `go mod tidy` (or `go work sync` in a workspace)",
			jsonschemaModule,
		)
	}

	return ""
}

// generateError wraps a failed helper build, appending [unresolvedHint] when the
// go tool's stderr shows the module cannot resolve the generation dependencies.
func generateError(err error) error {
	wrapped := cmdError(err)

	if hint := unresolvedHint(wrapped.Error()); hint != "" {
		return fmt.Errorf("generate: %w\n\t%s", wrapped, hint)
	}

	return fmt.Errorf("generate: %w", wrapped)
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
