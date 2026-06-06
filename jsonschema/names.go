package jsonschema

import (
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// defNameReplacer rewrites characters that are unsafe in a definitions key and
// the JSON Pointer reference token that points at it. Brackets and commas appear
// in generic type names (e.g. Box[pkg.T]); the slash and tilde are the RFC 6901
// JSON Pointer separator and escape characters, which a generic type argument's
// import path (e.g. Box[example.com/foo/bar.T]) would otherwise embed verbatim
// and break the resulting $ref fragment.
var defNameReplacer = strings.NewReplacer(
	"[", "_",
	"]", "_",
	",", "_",
	"/", "_",
	"~", "_",
)

// defaultNamer returns a definition name for a Go type. Characters that are not
// valid in a definitions key or its JSON Pointer $ref token are replaced with
// underscores so the generated reference resolves.
func defaultNamer(t reflect.Type) string {
	return defNameReplacer.Replace(t.Name())
}

// shouldExtract reports whether a type should be extracted to the definitions
// map and referenced via $ref (as opposed to being inlined).
func (g *generator) shouldExtract(t reflect.Type) bool {
	if !g.definitions {
		return false
	}

	// Only named types can be extracted.
	if t.Name() == "" {
		return false
	}

	// Named struct types are always extracted.
	if t.Kind() == reflect.Struct {
		return true
	}

	// Named non-struct types are extracted only if they implement
	// JSONSchemaProvider or JSONSchemaExtender.
	return implementsProvider(t) || implementsExtender(t)
}

// disambiguateDefs resolves name collisions in the definitions map by prefixing
// with the package's base directory name, then with the full import path
// if collisions persist.
func (g *generator) disambiguateDefs() {
	var hasCollision bool

	for _, types := range g.defsNameToTypes {
		if len(types) > 1 {
			hasCollision = true
			break
		}
	}

	if !hasCollision {
		return
	}

	newDefs := make(map[string]*Schema, len(g.defs))

	// Used reserves every name placed in newDefs so that a disambiguated name
	// can never silently overwrite a retained non-colliding def or a name
	// produced for a different collision group.
	used := make(map[string]bool, len(g.defs))

	// First pass: retain all non-colliding names verbatim and reserve them.
	// Colliding groups are collected for a deterministic second pass; iterating
	// g.defs directly would resolve groups in randomized order and make the
	// chosen disambiguation (and thus which schema wins a residual clash)
	// nondeterministic.
	var collisionNames []string

	for name, schema := range g.defs {
		if len(g.defsNameToTypes[name]) <= 1 {
			newDefs[name] = schema
			used[name] = true

			continue
		}

		collisionNames = append(collisionNames, name)
	}

	sort.Strings(collisionNames)

	// Second pass: disambiguate each collision group, escalating the naming
	// scheme until every name is unique against everything already placed.
	for _, name := range collisionNames {
		types := g.defsNameToTypes[name]

		// Candidate scheme 1: prefix with the package's base directory name. The
		// underscore separator keeps the package and type name distinct so two
		// groups whose base+name concatenate to the same string (package "foo"
		// with type "BarBaz" versus package "fooBar" with type "Baz") do not
		// force an unnecessary escalation to the full-path scheme.
		baseCandidates := make([]string, len(types))
		for i, t := range types {
			baseCandidates[i] = path.Base(t.PkgPath()) + "_" + name
		}

		// Candidate scheme 2 (fallback): prefix with the full import path.
		fullCandidates := make([]string, len(types))
		for i, t := range types {
			fullCandidates[i] = strings.ReplaceAll(t.PkgPath(), "/", "_") + "_" + name
		}

		// Pick the first scheme whose names are unique within the group and do
		// not clash with any name already reserved in newDefs.
		chosen := baseCandidates
		if !g.candidatesUsable(baseCandidates, used) {
			chosen = fullCandidates
		}

		for i, t := range types {
			// Even the full-path scheme can collide (two type arguments of a
			// generic differing only by a path separator, or a retained def
			// matching the constructed name). Suffix to guarantee uniqueness so
			// no schema is dropped and every refRecord points at its own def.
			finalName := g.uniqueName(chosen[i], used)

			s := g.typeToDefSchema[t]
			if s == nil {
				s = g.defs[name]
			}

			newDefs[finalName] = s
			used[finalName] = true
			g.typeToDefName[t] = finalName
		}
	}

	g.defs = newDefs

	// Update all tracked $ref schemas to point to the correct new names.
	for _, rec := range g.refRecords {
		newName := g.typeToDefName[rec.target]
		if newName == "" {
			continue
		}

		rec.schema.Ref = g.draft.refPrefix() + newName
	}
}

// candidatesUsable reports whether the disambiguation candidates are mutually
// distinct and free of any name already reserved in used. A scheme that fails
// this check would map two types in the same group to one key, or shadow a
// retained def, so the caller escalates to a stronger scheme.
func (g *generator) candidatesUsable(candidates []string, used map[string]bool) bool {
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if used[c] || seen[c] {
			return false
		}

		seen[c] = true
	}

	return true
}

// uniqueName returns name if it is not yet reserved in used, otherwise it
// appends the smallest numeric suffix that is. This is the last-resort
// guarantee that every definitions key is distinct so no schema is silently
// overwritten.
func (g *generator) uniqueName(name string, used map[string]bool) string {
	if !used[name] {
		return name
	}

	for i := 2; ; i++ {
		candidate := name + "_" + strconv.Itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}
