// Package fieldset resolves which JSON names a Go struct type marshals, in
// three phases. Two mirror [encoding/json]'s field collection: a breadth-first
// walk that records every sighting of a name, and a dominance pass that picks
// one winner per name. The third is this package's own, classifying each winner
// as a property, an allOf-composed embed, or a ghost. The walk also records
// each embedded fallback field (a non-anonymous json:",embed" field of a
// string-keyed map or [encoding/json/jsontext.Value]), and the dominance pass
// keeps the shallowest (a same-depth tie drops them all), whose members the
// caller models as the parent object's extra-member constraint.
//
// The caller injects composition detection as a [ComposedFunc], so the package
// reflects over a type without depending on schema generation. A composed embed
// still promotes its fields as far as [encoding/json] is concerned, so its
// subtree joins the walk as a ghost subtree. Those names compete in resolution,
// and a winning ghost becomes no property, because the embed's allOf branch
// carries its assertion.
//
// Splitting the phases is what lets a test compare one of them against
// [encoding/json] directly. [Resolve] and [Classify] are pure functions of their
// inputs. [Collector.Collect] is the only phase that consults the callback, and
// [Collector.Of] recurses into each composed embed the shadow marking needs.
package fieldset

import (
	"fmt"
	"reflect"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// ComposedFunc reports whether an embedded struct type is composed via allOf
// rather than having its fields promoted.
//
// [Collector.Collect] calls it only for an embedded field whose type is a
// struct, and only outside a ghost subtree, since [encoding/json] knows nothing
// of composition and promotes a nested embed's fields like any other. It must
// be deterministic and idempotent per type, because the package memoizes
// nothing. A callback that answers differently across calls, or that reports an
// error through its answer, does its own caching.
type ComposedFunc func(reflect.Type) bool

// Collector runs the three phases over a type. It carries the in-flight set
// that terminates the ghost recursion, so a self- or mutually composed type
// resolves instead of recursing without bound.
type Collector struct {
	composed ComposedFunc
	inFlight map[reflect.Type]bool
}

// NewCollector returns a Collector that classifies embedded struct types
// through composed.
func NewCollector(composed ComposedFunc) *Collector {
	return &Collector{composed: composed, inFlight: map[reflect.Type]bool{}}
}

// Sighting is one occurrence of a JSON name found by the breadth-first walk.
//
// Field order is tuned for struct packing (govet fieldalignment).
type Sighting struct {
	// GhostOwner names the composed embed type a ghost sighting belongs to;
	// see [Sighting.Ghost].
	GhostOwner reflect.Type
	// Info is the field's parsed json tag, recorded once by the walk so
	// [Classify] does not re-run the tag grammar on each winner. For a
	// declaration v2 refuses, it holds the recovered reading the walk's error
	// was reported against.
	Info jsontag.Info
	// Name is the key the sighting was recorded under: the field's JSON name,
	// or the synthetic composition key when ComposeAllOf is set.
	Name string
	// StructField is the Go field the sighting came from, with Index rewritten
	// to the path from the walk's root type.
	StructField reflect.StructField
	// Depth is the embedding level the sighting was found at, counting from
	// zero at the walk's root type.
	Depth int
	// Optional is true when the field is promoted through a pointer-typed
	// embedded struct. [encoding/json] omits such fields when the embedded
	// pointer is nil, so they are not required.
	Optional bool
	// Tagged is true when the field's JSON name comes from an explicit json
	// tag name rather than the Go field name. [encoding/json]'s tie-break for
	// fields colliding on a JSON name at the same depth keeps the field only
	// if exactly one of them is tagged; this records the input to that rule.
	Tagged bool
	// ComposeAllOf marks a synthetic sighting for an embedded type composed
	// via allOf rather than a real promoted field. It is carried explicitly
	// instead of being inferred from the synthetic name's prefix, so a user
	// field whose JSON name happens to start with that prefix is not
	// misclassified as a composition.
	ComposeAllOf bool
	// Ghost marks a sighting of a composed embed's promoted JSON name.
	// [encoding/json] promotes those fields normally, so they compete in name
	// resolution (shadowing deeper real fields, annihilating on ties), but a
	// winning ghost never becomes a property, because the embed's allOf branch
	// carries its assertion.
	Ghost bool
}

// Key groups sightings. The ComposeAllOf flag puts synthetic allOf
// compositions in a namespace disjoint from real JSON names, so a user field
// whose JSON name equals a composition's synthetic key cannot collide with it
// and shadow the composition in the same-depth tie-break.
//
// Field order is tuned for struct packing (govet fieldalignment).
type Key struct {
	// Name is the JSON name, or the synthetic composition key.
	Name string
	// ComposeAllOf distinguishes the two namespaces.
	ComposeAllOf bool
}

// Collection is the breadth-first walk's output.
type Collection struct {
	// Order lists each key in the order the walk first sighted it, which is
	// load-bearing. [Resolve] orders [Resolution.Winners] by it and [Classify]
	// carries that order into [Result.GhostWon], which the caller appends to
	// the object's property order.
	Order []Key
	// ByName holds every sighting of each key, in walk order. A type embedded
	// more than once at one depth contributes each of its fields twice, so the
	// same-depth tie-break drops the name.
	ByName map[Key][]Sighting
	// Scanned lists the composed embed types whose promoted fields the shadow
	// marking needs, deduplicated and in walk order. A type already in flight
	// is absent, since its subtree is a self- or mutually composed cycle;
	// [Classify] leaves such an embed's branch unconditional.
	Scanned []reflect.Type
	// Fallbacks lists every embedded fallback sighting in walk order, so the
	// breadth-first walk makes Depth nondecreasing. A fallback of a type
	// embedded more than once at one depth is recorded twice, so the
	// same-depth tie drops it, mirroring ByName's dup handling.
	Fallbacks []Fallback
}

// Resolution is the dominance pass's output.
type Resolution struct {
	// Annihilated maps each real JSON name dropped by the same-depth tie-break
	// to the depth that dropped it. The marshaled object carries no such name.
	Annihilated map[string]int
	// Fallback is the embedded fallback the dominance rule kept: the
	// shallowest sighting, unless another sighting ties its depth, which
	// silently drops them all, matching encoding/json/v2.
	Fallback *Fallback
	// Winners lists the dominant sighting for each key that resolved, in
	// [Collection.Order] order.
	Winners []Sighting
}

// outcome is the per-name verdict [Classify] builds for the shadow marking. It
// records the depth that claimed a real JSON name, whether a same-depth
// ambiguity annihilated it, and, when a composed embed's ghost won, which embed
// type owns it.
//
// Field order is tuned for struct packing (govet fieldalignment).
type outcome struct {
	ghostOwner  reflect.Type
	depth       int
	annihilated bool
}

// Field is one resolved struct field: a property the caller emits, or an
// embedded type it composes via allOf.
//
// Field order is tuned for struct packing (govet fieldalignment).
type Field struct {
	// JSONName is the key the field marshals under. It is empty exactly when
	// ComposeViaAllOf is set, since a composed embed contributes no name of
	// its own.
	JSONName string
	// StructField is the Go field, with Index the path from the walk's root
	// type.
	StructField reflect.StructField
	// Omitempty reports the ",omitempty" option and nothing else: whether the
	// option can ever omit the field depends on the field's kind under
	// encoding/json/v2, which the caller judges. Promotion through a pointer
	// embed is carried on Optional instead, since a nil pointer embed omits
	// the field whatever its kind.
	Omitempty bool
	// Omitzero reports the ",omitzero" option.
	Omitzero bool
	// JSONString reports the ",string" option.
	JSONString bool
	// ComposeViaAllOf marks an embedded type the caller composes rather than
	// promotes.
	ComposeViaAllOf bool
	// Optional is true for a field or allOf-composed embed reached through a
	// pointer-typed embedded field (directly or via an enclosing pointer
	// embed). [encoding/json] omits the embed's entire contribution when the
	// pointer is nil, so neither a promoted field nor the composed schema may
	// be unconditionally required.
	Optional bool
	// Shadowed marks an allOf-composed embed at least one of whose promoted
	// JSON names loses [encoding/json]'s field resolution to a real field. The
	// marshaled object then carries the winner's value under that name, or
	// drops the name on an ambiguity tie. Either way the composed schema's
	// claim on it does not hold, so the branch must not be unconditional.
	Shadowed bool
	// ShadowPartial marks a shadowed composed embed that still promotes at
	// least one unshadowed name. Only the (now conditional) branch evaluates
	// that name, so the parent object must stay open or a failing branch would
	// leave it unevaluated and rejected.
	ShadowPartial bool
}

// Fallback is an embedded fallback field: a non-anonymous json:",embed" field
// of type [encoding/json/jsontext.Value] or of a string-keyed map, whose
// members encoding/json/v2 splices into the parent JSON object after the
// named fields.
//
// Field order is tuned for struct packing (govet fieldalignment).
type Fallback struct {
	// Type is the field type with one unnamed pointer level removed:
	// [encoding/json/jsontext.Value], or the string-keyed map type.
	Type reflect.Type
	// StructField is the Go field, with Index the path from the walk's root
	// type.
	StructField reflect.StructField
	// Depth is the embedding level the walk sighted the field at.
	Depth int
}

// Result is the classification pass's output.
type Result struct {
	// Fallback is the embedded fallback the dominance rule kept, nil when the
	// type declares none or a same-depth tie drops them all. It emits no
	// property; the caller models its members as the object's extra-member
	// constraint.
	Fallback *Fallback
	// Fields lists the resolved fields in the order they are declared in the
	// Go source, a promoted field sorting at its embed's position.
	Fields []Field
	// GhostWon lists the names a composed embed's promoted field won. The
	// marshaled object carries each of them, and the embed's allOf branch
	// rather than an emitted property carries its assertion.
	GhostWon []string
}

// Of runs all three phases over t, recursing into each composed embed the
// shadow marking needs. It is the entry point. An in-flight guard spans the
// whole pipeline, so the recursion terminates whichever phase reaches it. The
// error is the first fault [encoding/json/v2] reports for the walked types;
// the Result beside it is what the rest of the walk yields.
func (c *Collector) Of(t reflect.Type) (Result, error) {
	_, _, out, err := c.phases(t)

	return out, err
}

// phases runs the pipeline and returns every phase's output, so a test can
// assert against an intermediate one under the pipeline's in-flight guard.
func (c *Collector) phases(t reflect.Type) (Collection, Resolution, Result, error) {
	// Mark t in-progress so the ghost recursion below terminates on a self- or
	// mutually composed cycle instead of recursing without bound.
	if owned := !c.inFlight[t]; owned {
		c.inFlight[t] = true
		defer delete(c.inFlight, t)
	}

	col, err := c.Collect(t)
	res := Resolve(col)

	promoted, promErr := c.promoted(col)
	if err == nil {
		err = promErr
	}

	return col, res, Classify(res, promoted), err
}

// promoted resolves each composed embed type the walk scanned. The shadow
// marking compares those fields against the enclosing resolution.
func (c *Collector) promoted(col Collection) (map[reflect.Type][]Field, error) {
	out := make(map[reflect.Type][]Field, len(col.Scanned))
	if len(col.Scanned) == 0 {
		return out, nil
	}

	var firstErr error

	for _, ft := range col.Scanned {
		r, err := c.Of(ft)
		if firstErr == nil {
			firstErr = err
		}

		out[ft] = r.Fields
	}

	return out, firstErr
}

// embedEntry is a struct type queued for processing at the next depth. A ghost
// entry is a composed embed's subtree. [encoding/json] promotes its fields
// normally, so they walk here and compete in resolution, but every sighting
// they record is a ghost owned by ghostOwner, the composed embed whose allOf
// branch carries the assertions.
type embedEntry struct {
	typ        reflect.Type
	ghostOwner reflect.Type
	index      []int
	optional   bool
	ghost      bool
}

// Collect walks t breadth-first, recording every sighting of a JSON name.
//
// The walk goes level by level, matching [encoding/json]'s field collection:
// all fields at depth d are recorded before any embed at depth d+1 is descended
// into, and a struct type is processed only once, at its shallowest occurrence.
// A depth-first walk would mark a deep occurrence of a type as visited and then
// skip a shallower embed of the same type, silently dropping fields that
// [encoding/json] promotes.
//
// The error is the first fault [encoding/json/v2]'s makeStructFields would
// report while walking the same types: a malformed json tag, two fields of
// one struct declaration conflicting over a JSON name, an embedded non-struct
// without an explicit name, an embedded type carrying marshal or unmarshal
// methods, two embedded fallback fields in one struct declaration, or a
// struct with fields but nothing serializable and no json tags. The walk
// continues past an error, recovering the way v2 does, so the Collection
// describes the rest of the type.
//
//nolint:nestif // Mirrors encoding/json's field collection logic which is inherently nested.
func (c *Collector) Collect(t reflect.Type) (Collection, error) {
	col := Collection{ByName: map[Key][]Sighting{}}

	var firstErr error

	keepErr := func(err error) {
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}

	// TypeErrf mirrors v2's SemanticError context: the fault names the struct
	// type it was found in.
	typeErrf := func(t reflect.Type, format string, args ...any) error {
		//nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
		return fmt.Errorf("Go type %s: %w", t, fmt.Errorf(format, args...))
	}

	// Record adds a sighting of a JSON name. The dup flag marks fields of a
	// struct type embedded more than once at the same depth. Such a sighting is
	// recorded twice so the same-depth ambiguity resolution drops the name,
	// matching encoding/json's annihilation of fields from repeated embeds.
	record := func(name string, s Sighting, dup bool) {
		key := Key{Name: name, ComposeAllOf: s.ComposeAllOf}
		s.Name = name

		if _, seen := col.ByName[key]; !seen {
			col.Order = append(col.Order, key)
		}

		col.ByName[key] = appendDup(col.ByName[key], s, dup)
	}

	// Embedded types composed via allOf get a synthetic key from allOfName.
	// The key is stable per type, so the same type composed at one depth
	// collides into a single name and its two sightings annihilate as
	// ambiguous, matching encoding/json's treatment of a type embedded twice at
	// the same depth; a deeper re-occurrence is shadowed by the shallower one.
	// The per-type index keeps distinct types apart even when their names match
	// across packages, so it depends on walk order and stays local here.
	allOfNames := map[reflect.Type]string{}
	allOfName := func(ft reflect.Type) string {
		if n, ok := allOfNames[ft]; ok {
			return n
		}

		n := fmt.Sprintf("__allof__%s__%d", ft.Name(), len(allOfNames))
		allOfNames[ft] = n

		return n
	}

	// Visited tracks every struct type processed during the walk. A type is
	// processed only at its shallowest level: a deeper re-occurrence (including
	// a self-embedding type T struct{ *T; X int }) is skipped, because its
	// fields are shadowed by the shallower ones, matching encoding/json.
	visited := map[reflect.Type]bool{}

	next := []embedEntry{{typ: t}}

	var count, nextCount map[reflect.Type]int

	for depth := 0; len(next) > 0; depth++ {
		current := next
		next = nil
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, e := range current {
			if visited[e.typ] {
				continue
			}

			visited[e.typ] = true

			// A type embedded more than once at this depth contributes every
			// field twice so the resolution drops them all as ambiguous.
			dup := count[e.typ] > 1

			// Two embedded fallback fields in one struct declaration are a v2
			// error; v2 still records both, and the same-depth tie then drops
			// them. Tracked per declaration, like v2's embeddedFallbackIndex.
			var firstFallbackInDecl string

			// Two fields of one struct declaration conflicting over a JSON
			// name are a v2 error (a cross-embed tie still silently
			// annihilates). Keyed per declaration, like v2's namesIndex.
			directNames := map[string]string{}
			checkConflict := func(name, goName string) {
				if prev, ok := directNames[name]; ok {
					keepErr(typeErrf(e.typ,
						"Go struct fields %s and %s conflict over JSON object name %q",
						prev, goName, name))

					return
				}

				directNames[name] = goName
			}

			// V2 refuses a struct that has fields but nothing serializable
			// and no json tags at all (an errors.New value, typically).
			var hasAnyJSONTag, hasAnyJSONField bool

			for i := range e.typ.NumField() {
				f := e.typ.Field(i)
				fieldIndex := append(slices.Clone(e.index), i)
				f.Index = fieldIndex

				if _, hasTag := f.Tag.Lookup("json"); hasTag {
					hasAnyJSONTag = true
				}

				info, tagErr := jsontag.Parse(f)
				if tagErr != nil {
					keepErr(typeErrf(e.typ, "%v", tagErr))
				}

				if info.JSONName == "" {
					continue // json:"-", or an unexported non-embedded field
				}

				hasAnyJSONField = true

				// The ",embed" option gives a named field promotion semantics;
				// Go embedding without an explicit name implies it.
				embedded := info.Embed || (f.Anonymous && !info.TaggedName)

				optioned := info.Omitempty || info.Omitzero || info.JSONString ||
					info.Case != "" || info.Format != ""
				if (embedded && optioned) || (info.Embed && info.TaggedName) {
					keepErr(typeErrf(e.typ,
						"Go struct field %s cannot have any options other than `embed` specified",
						f.Name))

					if info.TaggedName {
						embedded = false // v2 recovers by treating it as a regular field
					}
				}

				if embedded {
					ft := f.Type
					// Only an unnamed pointer derefs, since v2's
					// indirectType stops at a named pointer type, which then
					// fails the non-struct embed check below like any other
					// leaf.
					embeddedViaPointer := ft.Kind() == reflect.Pointer && ft.Name() == ""
					if embeddedViaPointer {
						ft = ft.Elem()
					}

					if ft.Kind() != reflect.Struct {
						// V2 embeds only structs, plus the two fallback forms on
						// a non-anonymous field under an explicit ",embed"
						// option; an anonymous non-struct is an error however it
						// is tagged, recovered as a leaf field named by the type
						// identifier.
						if f.Anonymous {
							keepErr(typeErrf(e.typ,
								"embedded Go struct field %s of non-struct type must be explicitly given a JSON name",
								f.Name))
						} else {
							// A non-anonymous field reaches here only through
							// info.Embed, and jsontag guarantees it is exported.
							// V2 checks the marshaler methods first
							// (jsontext.Value exempt), dropping the field.
							if ft != reflectkind.TypeJSONTextValue &&
								reflectkind.ImplementsAnyMarshalMethod(ft) {
								keepErr(typeErrf(
									e.typ,
									"embedded Go struct field %s of type %s must not implement marshal or unmarshal methods",
									f.Name,
									ft,
								))

								continue
							}

							switch {
							case reflectkind.IsEmbeddedFallback(ft):
								// A valid fallback: v2 splices its members into
								// the parent object after the named fields. Two
								// in one declaration are an error, and v2 still
								// records both so the same-depth tie drops them.
								if firstFallbackInDecl != "" {
									keepErr(typeErrf(e.typ,
										"embedded Go struct fields %s and %s cannot both be a Go map or jsontext.Value",
										firstFallbackInDecl, f.Name))
								}

								firstFallbackInDecl = f.Name

								col.Fallbacks = appendDup(col.Fallbacks,
									Fallback{StructField: f, Type: ft, Depth: depth}, dup)

								continue

							case ft.Kind() == reflect.Map && ft.Key().Kind() == reflect.String:
								keepErr(typeErrf(
									e.typ,
									"embedded map field %s of type %s must have a string key that does not implement marshal or unmarshal methods",
									f.Name,
									ft,
								))

							default:
								keepErr(typeErrf(
									e.typ,
									"embedded Go struct field %s of type %s must be a Go struct, Go map of string key, or jsontext.Value",
									f.Name,
									ft,
								))
							}
						}

						if !f.IsExported() {
							// An unexported leaf cannot serialize; v2 reports it
							// and drops the field.
							continue
						}

						// Recovered as a regular leaf field: it participates in
						// normal shadowing and ambiguity resolution under the
						// unqualified type identifier (without the type
						// arguments [reflect.Type.Name] carries for an
						// instantiated generic type).
						checkConflict(f.Name, f.Name)
						record(f.Name, Sighting{
							StructField: f, Depth: depth, Optional: e.optional,
							Info: info, Ghost: e.ghost, GhostOwner: e.ghostOwner,
						}, dup)

						continue
					}

					// V2 refuses to promote an embedded type carrying any
					// marshal or unmarshal method (the methods make the
					// promoted shape unknowable). The check is not reached
					// when a promoted method replaces the outer type, since
					// the generator answers that before walking fields.
					if reflectkind.ImplementsAnyMarshalMethod(ft) {
						keepErr(typeErrf(e.typ,
							"embedded Go struct field %s of type %s must not implement marshal or unmarshal methods",
							f.Name, ft))
					}

					// A ghost subtree skips the composition probe, since encoding/json knows
					// nothing of composition and promotes the nested embed's
					// fields like any other, so the subtree flattens below and
					// its names stay in the competition.
					if !e.ghost && c.composed(ft) {
						// Compose via allOf: treat as a single entry. A pointer
						// embed makes the composition optional, since a nil
						// pointer contributes nothing to the marshaled object.
						record(
							allOfName(ft),
							Sighting{
								StructField:  f,
								Depth:        depth,
								Optional:     e.optional || embeddedViaPointer,
								ComposeAllOf: true,
							},
							false,
						)

						// Ghost sightings: encoding/json still promotes the
						// composed embed's fields normally, so its subtree
						// joins this walk as a ghost entry and its names
						// compete in resolution -- shadowing, same-depth
						// annihilation (including a tie inside the embed that
						// a replay of its resolved winners would miss), and
						// the tag tie-break -- exactly as the flat
						// encoding/json walk resolves them. A subtree skipped
						// by the in-flight guard (a self- or mutually composed
						// type) leaves its names out, keeping the pre-ghost
						// conservative behavior for that cycle.
						if !c.inFlight[ft] {
							if !slices.Contains(col.Scanned, ft) {
								col.Scanned = append(col.Scanned, ft)
							}

							nextCount[ft]++
							if nextCount[ft] == 1 {
								next = append(next, embedEntry{
									typ:        ft,
									index:      fieldIndex,
									optional:   e.optional || embeddedViaPointer,
									ghost:      true,
									ghostOwner: ft,
								})
							}
						}

						continue
					}

					// Queue for the next depth. A type queued more than once at
					// the same depth is processed once but counted, so its
					// fields annihilate as ambiguous, matching encoding/json.
					nextCount[ft]++
					if nextCount[ft] == 1 {
						// Fields reached through a pointer embed are optional
						// because a nil embed omits them entirely.
						next = append(next, embedEntry{
							typ:        ft,
							index:      fieldIndex,
							optional:   e.optional || embeddedViaPointer,
							ghost:      e.ghost,
							ghostOwner: e.ghostOwner,
						})
					}

					continue
				}

				// An unexported field reaches this named-field path only as an
				// anonymous field carrying a tag name (jsontag excludes the
				// non-anonymous ones). Encoding/json/v2 accepts it only as a
				// struct it can walk without calling a method: a non-struct
				// type has no fields to walk, and a marshal method or an
				// omitzero IsZero would have to be called through the
				// unexported field, which reflection forbids. V2 drops the
				// field from its walk, so it neither conflicts nor records.
				if !f.IsExported() {
					ft := f.Type
					if ft.Kind() == reflect.Pointer && ft.Name() == "" {
						ft = ft.Elem()
					}

					switch {
					case ft.Kind() != reflect.Struct:
						keepErr(typeErrf(e.typ, "Go struct field %s is not exported", f.Name))

						continue

					case reflectkind.ImplementsAnyMarshalMethod(ft) ||
						(info.Omitzero && reflectkind.ImplementsIsZeroer(ft)):
						keepErr(typeErrf(e.typ,
							"Go struct field %s is not exported for method calls", f.Name))

						continue
					}
				}

				// Stable encoding/json/v2 parses the `format` tag option but
				// supports no value of it on a struct field: any appearance
				// makes marshaling the struct a SemanticError.
				if info.Format != "" {
					keepErr(typeErrf(e.typ,
						"Go struct field %s has unsupported `format` tag option", f.Name))
				}

				// A regular named field: a plain field, an embedded field with
				// an explicit json name (encoding/json does not promote it; an
				// options-only tag like json:",omitempty" has no name and took
				// the promotion path above), or the leaf recovery for an
				// erroneous embed.
				checkConflict(info.JSONName, f.Name)
				record(
					info.JSONName,
					Sighting{
						StructField: f, Depth: depth, Optional: e.optional,
						Tagged: info.TaggedName, Info: info,
						Ghost: e.ghost, GhostOwner: e.ghostOwner,
					},
					dup,
				)
			}

			if e.typ.NumField() > 0 && !hasAnyJSONTag && !hasAnyJSONField {
				//nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				keepErr(fmt.Errorf("Go struct %s has no exported fields", e.typ))
			}
		}
	}

	return col, firstErr
}

// appendDup appends v once, or twice under the dup flag, which marks a struct
// type embedded more than once at one depth: the double record is what makes
// the same-depth tie drop the name or fallback, matching encoding/json.
func appendDup[T any](s []T, v T, dup bool) []T {
	s = append(s, v)
	if dup {
		s = append(s, v)
	}

	return s
}

// Resolve applies [encoding/json]'s dominance rules to each key. The shallowest
// depth wins, and an explicit json tag breaks a same-depth collision. Resolve
// drops every candidate when none or more than one of them is tagged.
func Resolve(col Collection) Resolution {
	res := Resolution{Annihilated: map[string]int{}}

	// The fallback dominance mirrors v2's: the shallowest sighting wins, and
	// a second sighting at that depth silently drops them all. The walk's
	// breadth-first order makes Fallbacks[0] the shallowest, so v2's rule
	// reduces to comparing the first two depths.
	if n := len(col.Fallbacks); n == 1 ||
		(n > 1 && col.Fallbacks[0].Depth != col.Fallbacks[1].Depth) {
		fb := col.Fallbacks[0]
		res.Fallback = &fb
	}

	for _, key := range col.Order {
		candidates := col.ByName[key]
		if len(candidates) == 0 {
			continue
		}

		minDepth := candidates[0].Depth
		for ci := 1; ci < len(candidates); ci++ {
			if candidates[ci].Depth < minDepth {
				minDepth = candidates[ci].Depth
			}
		}

		var atMin []Sighting

		for ci := range candidates {
			if candidates[ci].Depth == minDepth {
				atMin = append(atMin, candidates[ci])
			}
		}

		// Multiple fields collide on this JSON name at the shallowest depth. The
		// tie-break follows encoding/json. If exactly one of them has an
		// explicit json tag name, that field wins; if none or more than one is
		// tagged, they are all dropped as ambiguous.
		if len(atMin) > 1 {
			var tagged []Sighting

			for ci := range atMin {
				if atMin[ci].Tagged {
					tagged = append(tagged, atMin[ci])
				}
			}

			if len(tagged) != 1 {
				// Annihilated is keyed by plain name, so writing a composition
				// key there would mark a real field of the same spelling
				// annihilated. Only a field named like the synthetic key of an
				// embed composed twice at one depth reaches that.
				if !key.ComposeAllOf {
					res.Annihilated[key.Name] = minDepth
				}

				continue
			}

			atMin = tagged
		}

		res.Winners = append(res.Winners, atMin[0])
	}

	return res
}

// Classify turns each winner into a property, an allOf-composed embed, or a
// ghost, then marks the composed embeds whose promoted names the resolution
// took away. The promoted map carries each scanned composed embed type's own
// resolved fields; a type absent from it was skipped as a self- or mutually
// composed cycle and keeps its unconditional branch.
func Classify(res Resolution, promoted map[reflect.Type][]Field) Result {
	// Each real JSON name's outcome feeds the shadow marking below. Folding the
	// annihilated names in first rather than in resolution order is safe
	// because every write targets a distinct name: an annihilated name has no
	// winner, a composed embed writes no outcome, and a real winner's parsed
	// JSON name is the key it was recorded under.
	outcomes := make(map[string]outcome, len(res.Winners)+len(res.Annihilated))
	for name, depth := range res.Annihilated {
		outcomes[name] = outcome{depth: depth, annihilated: true}
	}

	out := Result{Fallback: res.Fallback}

	for i := range res.Winners {
		w := &res.Winners[i]
		if w.Ghost {
			// A composed embed's promoted field won the name, so Classify emits
			// no property; the embed's allOf branch carries the assertion. The
			// real fields it defeated stay out of the result too, matching the
			// value encoding/json actually marshals under the name. Result
			// reports the name, since the caller's closed object must still
			// evaluate it.
			outcomes[w.Name] = outcome{depth: w.Depth, ghostOwner: w.GhostOwner}
			out.GhostWon = append(out.GhostWon, w.Name)

			continue
		}

		if w.ComposeAllOf {
			out.Fields = append(out.Fields, Field{
				StructField:     w.StructField,
				ComposeViaAllOf: true,
				Optional:        w.Optional,
			})

			continue
		}

		info := w.Info
		if info.JSONName == "" {
			continue
		}

		outcomes[info.JSONName] = outcome{depth: w.Depth}

		out.Fields = append(out.Fields, Field{
			StructField: w.StructField,
			JSONName:    info.JSONName,
			Omitempty:   info.Omitempty,
			Omitzero:    info.Omitzero,
			JSONString:  info.JSONString,
			Optional:    w.Optional,
		})
	}

	// The breadth-first walk sights names level by level, so the winners arrive
	// with all depth-0 names before any promoted ones. Sorting by index path
	// restores source declaration order, matching encoding/json's byIndex
	// ordering. A promoted field sorts at its embed's position.
	slices.SortStableFunc(out.Fields, func(a, b Field) int {
		return slices.Compare(a.StructField.Index, b.StructField.Index)
	})

	markShadowedCompositions(out.Fields, outcomes, promoted)

	return out
}

// markShadowedCompositions flags each allOf-composed embed whose promoted JSON
// names [encoding/json] resolves away from the embed: a shallower (or
// same-depth winning) real field carries the winner's value under the name, the
// marshaled object drops an annihilated name entirely, and another embed's
// winning ghost carries that embed's value. In each case the composed branch
// would assert this embed's constraints against a value it does not produce, so
// the branch must not be unconditional. The outcomes map is the enclosing
// resolution's per-name verdict, ghost sightings included, so it already
// replays the tag tie-break.
func markShadowedCompositions(
	fields []Field,
	outcomes map[string]outcome,
	promoted map[reflect.Type][]Field,
) {
	for i := range fields {
		fi := &fields[i]
		if !fi.ComposeViaAllOf {
			continue
		}

		ft := fi.StructField.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		embedFields, scanned := promoted[ft]
		if !scanned {
			continue
		}

		embedDepth := len(fi.StructField.Index) - 1

		var shadowedAny, unshadowedAny bool

		for j := range embedFields {
			p := &embedFields[j]
			if p.ComposeViaAllOf {
				// A nested composition's names are opaque to this analysis;
				// assume it contributes to the marshaled object.
				unshadowedAny = true

				continue
			}

			// The promoted name sits at the embed's depth plus its own depth
			// within the embed type.
			de := embedDepth + len(p.StructField.Index)

			out, ok := outcomes[p.JSONName]

			switch {
			case !ok:
				// A backstop. Every promoted name of a scanned embed is sighted
				// in the enclosing ghost walk, so an outcome always exists.
				// Keep the branch's claim if one ever does not.
				unshadowedAny = true
			case !out.annihilated && out.ghostOwner == ft && out.depth == de:
				// This embed's own ghost won the name, so the marshaled object
				// carries the embed's value there.
				unshadowedAny = true
			case out.depth <= de:
				// A real field won the tie-break, the name annihilated, or
				// another embed claimed it at or above this depth.
				shadowedAny = true
			default:
				// A backstop. The ghost walk cannot sight a name deeper than
				// the embed promotes it, so no outcome sits deeper than de.
				unshadowedAny = true
			}
		}

		fi.Shadowed = shadowedAny
		fi.ShadowPartial = shadowedAny && unshadowedAny
	}
}
