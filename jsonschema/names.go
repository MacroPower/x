package jsonschema

import (
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
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
// map and referenced via $ref (as opposed to being inlined): an extractable
// type, under a run with definitions enabled.
func (g *run) shouldExtract(t reflect.Type) bool {
	return g.definitions && extractable(t)
}

// extractable reports whether a type is one the definitions map holds: a
// named struct, or a named type implementing JSONSchemaProvider or
// JSONSchemaExtender. It is a fact of the type alone; [run.shouldExtract]
// adds the run's WithDefinitions setting.
func extractable(t reflect.Type) bool {
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

// emittedDefs returns the def entries render will emit: every entry the
// root graph reaches, through node links and the raw $ref strings in
// payloads, minus the root's own entry when the root is a bare $ref that
// [run.maybeInlineRoot] inlines (one no other node references). It reads the
// root's wrapper decision, so it runs after [run.resolveNullability], and it
// keys on the provisional tokens, so it runs before [run.finalizeRefs].
func (g *run) emittedDefs(root *node) map[*defEntry]bool {
	emitted := map[*defEntry]bool{}
	g.walkReachable(root, emitted, func(*node) {}, nil)

	if root.kind == kindRef && !root.null.wrap && !g.referencedElsewhere(root, root.def) {
		delete(emitted, root.def)
	}

	return emitted
}

// assignDefNames assigns each def entry its final $defs key, disambiguating
// name collisions among the emitted entries by prefixing with the package's
// base directory name, then with the full import path if collisions persist.
// An entry outside emitted (one a type= override, a tag replacing a
// definition keyword, or root inlining orphaned)
// never reaches the output, so it joins no collision group and takes a
// token-shaped name instead, which no emitted key can spell. It runs before
// render, so renderRef emits final names directly; because refs are defEntry
// pointer links, no ref re-pointing pass is needed for them. The provisional
// tokens the reflection phase wrote into payloads are rewritten afterward by
// [run.finalizeRefs].
func (g *run) assignDefNames(emitted map[*defEntry]bool) {
	prefix := g.profile.refPrefix()

	// Group emitted entries by their pre-disambiguation base name.
	byBase := map[string][]*defEntry{}

	for _, e := range g.defs {
		if !emitted[e] {
			e.name = strings.TrimPrefix(e.token, prefix)

			continue
		}

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

// finalizeRefs rewrites every provisional def token in the IR to the final
// $ref string assignDefNames settled, so no phase after it sees a token. A
// token sits in a ref node's own payload, and in any literal a type-level
// hook copied it into, such as a branch grafted beside a property, a slot
// replaced wholesale, or a "$ref" member inside an extension keyword's
// value. A ref a hook spelled by hand as a base name is rewritten the same
// way, to the final key of the entry [run.payloadRefTargets] resolves it
// to, so a name the collision pass escalated does not leave the hand-spelled
// ref dangling. The walk covers the root graph, every def body whether
// or not the root reaches it, and the node a type= override replaced, whose
// view the jsonschema tag reads later. An entry render never emits keeps its
// token-shaped name, so a field inside an orphaned body whose type is another
// orphaned entry reads that spelling in its Base; only a hook on a body that
// is never rendered observes it. Payload subtrees are scanned once each
// through the scanned set, so a subtree a hook aliased into two slots is
// rewritten once.
func (g *run) finalizeRefs(root *node) {
	prefix := g.profile.refPrefix()

	targets := g.payloadRefTargets()

	final := make(map[string]string, len(targets))
	for ref, e := range targets {
		final[ref] = prefix + e.name
	}

	scanned := map[*Schema]bool{}

	var rewrite func(s *Schema)

	rewrite = func(s *Schema) {
		if s == nil || scanned[s] {
			return
		}

		scanned[s] = true

		if name, ok := final[s.Ref]; ok {
			s.Ref = name
		}

		for _, child := range schemafield.Children(s) {
			rewrite(child)
		}

		extraRefs(s.Extra, func(ref string) string {
			if name, ok := final[ref]; ok {
				return name
			}

			return ref
		}, rewrite)
	}

	seen := map[*defEntry]bool{}

	var visit func(n *node)

	visit = func(n *node) {
		rewrite(n.payload)

		if n.overrode != nil {
			walkNodes(n.overrode, seen, visit)
		}
	}

	walkNodes(root, seen, visit)

	for _, e := range g.defs {
		if !seen[e] {
			seen[e] = true
			walkNodes(e.body, seen, visit)
		}
	}
}

// extraRefs visits every "$ref" string the extension keywords in extra hold,
// descending through the map[string]any and []any values a JSON-shaped
// extension carries, and stores what visit returns in the string's place. A
// *Schema riding in an extension keyword goes to visitSchema, which resumes
// the caller's own sub-schema walk on it. [run.finalizeRefs] and
// [run.walkReachable] both read extension values through it, so a token a
// type-level hook copied into one is rewritten to the final key and counts
// as a reachability edge, as a copy in a sub-schema slot does. Only a string
// under the "$ref" member name is a reference; no other extension string is
// read.
func extraRefs(extra map[string]any, visit func(ref string) string, visitSchema func(*Schema)) {
	var walk func(v any)

	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, item := range v {
				if ref, ok := item.(string); ok && k == keyword.Ref {
					v[k] = visit(ref)

					continue
				}

				walk(item)
			}

		case []any:
			for _, item := range v {
				walk(item)
			}

		case *Schema:
			visitSchema(v)
		}
	}

	if extra != nil {
		walk(extra)
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
