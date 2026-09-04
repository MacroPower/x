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
func (g *run) schemaName(t reflect.Type) string {
	if name := g.namer.SchemaName(TypeContext{Type: t, Draft: g.draft}); name != "" {
		return jsonptr.SafeToken(name)
	}

	return defaultNamer(t)
}

// shouldExtract reports whether a type should be extracted to the definitions
// map and referenced via $ref (as opposed to being inlined).
func (g *run) shouldExtract(t reflect.Type) bool {
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

// assignDefNames assigns each def entry its final $defs key, disambiguating
// name collisions by prefixing with the package's base directory name, then
// with the full import path if collisions persist. It runs before render, so
// renderRef emits final names directly; because refs are defEntry pointer
// links, no ref re-pointing pass is needed.
func (g *run) assignDefNames() {
	// Group entries by their pre-disambiguation base name.
	byBase := map[string][]*defEntry{}
	for _, e := range g.defs {
		byBase[e.baseName] = append(byBase[e.baseName], e)
	}

	// Used reserves every assigned name so a disambiguated name can never
	// silently shadow a singleton or a name produced for another group.
	used := make(map[string]bool, len(g.defs))

	// First pass: singletons keep their base name verbatim and reserve it.
	// Colliding groups are collected for a deterministic second pass; iterating
	// the map directly would resolve groups in randomized order and make the
	// chosen disambiguation nondeterministic.
	var collisionBases []string

	for base, entries := range byBase {
		if len(entries) <= 1 {
			entries[0].name = base
			used[base] = true

			continue
		}

		collisionBases = append(collisionBases, base)
	}

	sort.Strings(collisionBases)

	// Second pass: disambiguate each collision group, escalating the naming
	// scheme until every name is unique against everything already placed.
	for _, base := range collisionBases {
		entries := byBase[base]

		// Candidate scheme 1: prefix with the package's base directory name. The
		// underscore separator keeps the package and type name distinct so two
		// groups whose base+name concatenate to the same string (package "foo"
		// with type "BarBaz" versus package "fooBar" with type "Baz") do not
		// force an unnecessary escalation to the full-path scheme.
		baseCandidates := make([]string, len(entries))
		for i, e := range entries {
			// Run the prefix through the same sanitizer the rest of the package
			// uses: a package path element may legally contain a JSON Pointer
			// special character (the tilde, allowed in module paths), which
			// would otherwise misresolve the generated $ref token.
			baseCandidates[i] = jsonptr.SafeToken(path.Base(e.typ.PkgPath())) + "_" + base
		}

		// Pick the first scheme whose names are unique within the group and do
		// not clash with any name already reserved. The full-path fallback is
		// constructed only on escalation, the uncommon case.
		chosen := baseCandidates
		if !candidatesUsable(baseCandidates, used) {
			// Candidate scheme 2 (fallback): prefix with the full import path.
			// The sanitizer subsumes the slash replacement and also handles the
			// tilde and the other characters invalid in a $ref token.
			fullCandidates := make([]string, len(entries))
			for i, e := range entries {
				fullCandidates[i] = jsonptr.SafeToken(e.typ.PkgPath()) + "_" + base
			}

			chosen = fullCandidates
		}

		for i, e := range entries {
			// Even the full-path scheme can collide (two type arguments of a
			// generic differing only by a path separator, or a singleton matching
			// the constructed name). Suffix to guarantee uniqueness.
			finalName := uniqueName(chosen[i], used)
			e.name = finalName
			used[finalName] = true
		}
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
