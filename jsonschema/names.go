package jsonschema

import (
	"path"
	"reflect"
	"sort"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// defaultNamer returns a definition name for a Go type. Characters that are not
// valid in a definitions key or its JSON Pointer $ref token are replaced with
// underscores so the generated reference resolves.
func defaultNamer(t reflect.Type) string {
	return jsonptr.SafeToken(t.Name())
}

// defaultNamerFunc adapts [defaultNamer] to the [Namer] interface, for the
// default namer configuration ([WithNamer] given nil, and the initial
// generator state).
func defaultNamerFunc() Namer {
	return NamerFunc(func(tc TypeContext) string { return defaultNamer(tc.Type) })
}

// schemaName returns the configured namer's name for t, deferring to the
// default namer when the namer answers "". The deferral lets a [Namer]
// rename some types and pass the rest through, and keeps a partial namer
// from producing an empty definitions key and the broken "#/$defs/" ref
// that would follow. A non-empty answer is run through the same sanitizer
// the default namer uses, so characters invalid in a definitions key or its
// JSON Pointer $ref token (such as '/' and '~') cannot produce a dangling or
// misresolving reference. The sanitizer never empties a non-empty name, so the
// deferral semantics are preserved.
func (g *generator) schemaName(t reflect.Type) string {
	if name := g.namer.SchemaName(TypeContext{Type: t, Draft: g.draft}); name != "" {
		return jsonptr.SafeToken(name)
	}

	return defaultNamer(t)
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
			// Run the prefix through the same sanitizer the rest of the package
			// uses: a package path element may legally contain a JSON Pointer
			// special character (the tilde, allowed in module paths), which
			// would otherwise misresolve the generated $ref token.
			baseCandidates[i] = jsonptr.SafeToken(path.Base(t.PkgPath())) + "_" + name
		}

		// Pick the first scheme whose names are unique within the group and do
		// not clash with any name already reserved in newDefs. The full-path
		// fallback is constructed only on escalation, the uncommon case, so the
		// usual base-scheme path does not build a second candidate slice.
		chosen := baseCandidates
		if !candidatesUsable(baseCandidates, used) {
			// Candidate scheme 2 (fallback): prefix with the full import path.
			// The sanitizer subsumes the slash replacement and also handles the
			// tilde and the other characters invalid in a $ref token.
			fullCandidates := make([]string, len(types))
			for i, t := range types {
				fullCandidates[i] = jsonptr.SafeToken(t.PkgPath()) + "_" + name
			}

			chosen = fullCandidates
		}

		for i, t := range types {
			// Even the full-path scheme can collide (two type arguments of a
			// generic differing only by a path separator, or a retained def
			// matching the constructed name). Suffix to guarantee uniqueness so
			// no schema is dropped and every refRecord points at its own def.
			finalName := uniqueName(chosen[i], used)

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
		// A refRecord whose schema no longer carries a $ref has had it
		// intentionally cleared (an explicit type= override drops the bare $ref
		// of a $defs-extracted field). Re-pointing it would re-emit a stale $ref
		// next to the override, producing {"type":..,"$ref":..}, which is
		// unsatisfiable under 2020-12. Before this runs, wrapRefForDraft7
		// repoints its record to the inner $ref (whose Ref is set), so an empty
		// Ref here is only ever an override, never a reference awaiting rename.
		if rec.schema.Ref == "" {
			continue
		}

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
func candidatesUsable(candidates []string, used map[string]bool) bool {
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
func uniqueName(name string, used map[string]bool) string {
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
