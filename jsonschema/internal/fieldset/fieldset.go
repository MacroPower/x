// Package fieldset resolves which JSON names a Go struct type marshals, in
// three phases. Two mirror [encoding/json]'s field collection: a breadth-first
// walk that records every sighting of a name, and a dominance pass that picks
// one winner per name. The third is this package's own, classifying each winner
// as a property, an allOf-composed embed, or a ghost.
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
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
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
}

// Resolution is the dominance pass's output.
type Resolution struct {
	// Annihilated maps each real JSON name dropped by the same-depth tie-break
	// to the depth that dropped it. The marshaled object carries no such name.
	Annihilated map[string]int
	// Winners lists the dominant sighting for each key that resolved, in
	// [Collection.Order] order.
	Winners []Sighting
}

// Outcome is the per-name verdict [Classify] builds for the shadow marking. It
// records the depth that claimed a real JSON name, whether a same-depth
// ambiguity annihilated it, and, when a composed embed's ghost won, which embed
// type owns it.
//
// Field order is tuned for struct packing (govet fieldalignment).
type Outcome struct {
	GhostOwner  reflect.Type
	Depth       int
	Annihilated bool
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
	// Omitempty reports the ",omitempty" option. It folds in promotion through
	// a pointer embed, since a nil pointer embed omits the field the same way.
	Omitempty bool
	// Omitzero reports the ",omitzero" option.
	Omitzero bool
	// JSONString reports the ",string" option.
	JSONString bool
	// ComposeViaAllOf marks an embedded type the caller composes rather than
	// promotes.
	ComposeViaAllOf bool
	// Optional is true for an allOf-composed embed reached through a
	// pointer-typed embedded field (directly or via an enclosing pointer
	// embed). [encoding/json] omits the embed's entire contribution when the
	// pointer is nil, so the composed schema must not be unconditionally
	// required. Regular fields fold this into Omitempty instead.
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

// Result is the classification pass's output.
type Result struct {
	// Fields lists the resolved fields in the order they are declared in the
	// Go source, a promoted field sorting at its embed's position.
	Fields []Field
	// GhostWon lists the names a composed embed's promoted field won. The
	// marshaled object carries each of them, and the embed's allOf branch
	// rather than an emitted property carries its assertion.
	GhostWon []string
}

// Of runs all three phases over t, recursing into each composed embed the
// shadow marking needs. It is the entry point, and the only place that takes
// the in-flight guard, so the guard spans the recursion whichever phase
// reaches it.
func (c *Collector) Of(t reflect.Type) Result {
	_, _, out := c.phases(t)

	return out
}

// phases runs the pipeline and returns every phase's output, so a test can
// assert against an intermediate one under the same in-flight guard Of takes.
func (c *Collector) phases(t reflect.Type) (Collection, Resolution, Result) {
	// Mark t in-progress so the ghost recursion below terminates on a self- or
	// mutually composed cycle instead of recursing without bound.
	if owned := !c.inFlight[t]; owned {
		c.inFlight[t] = true
		defer delete(c.inFlight, t)
	}

	col := c.Collect(t)
	res := Resolve(col)

	return col, res, Classify(res, c.promoted(col))
}

// promoted resolves each composed embed type the walk scanned. The shadow
// marking compares those fields against the enclosing resolution.
func (c *Collector) promoted(col Collection) map[reflect.Type][]Field {
	if len(col.Scanned) == 0 {
		return nil
	}

	out := make(map[reflect.Type][]Field, len(col.Scanned))
	for _, ft := range col.Scanned {
		out[ft] = c.Of(ft).Fields
	}

	return out
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
//nolint:nestif // Mirrors encoding/json's field collection logic which is inherently nested.
func (c *Collector) Collect(t reflect.Type) Collection {
	col := Collection{ByName: map[Key][]Sighting{}}

	// Record adds a sighting of a JSON name. The dup flag marks fields of a
	// struct type embedded more than once at the same depth: the sighting is
	// recorded twice so the same-depth ambiguity resolution drops the name,
	// matching encoding/json's annihilation of fields from repeated embeds.
	record := func(name string, s Sighting, dup bool) {
		key := Key{Name: name, ComposeAllOf: s.ComposeAllOf}
		s.Name = name

		if _, seen := col.ByName[key]; !seen {
			col.Order = append(col.Order, key)
		}

		col.ByName[key] = append(col.ByName[key], s)
		if dup {
			col.ByName[key] = append(col.ByName[key], s)
		}
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

			for i := range e.typ.NumField() {
				f := e.typ.Field(i)
				fieldIndex := append(slices.Clone(e.index), i)
				f.Index = fieldIndex

				if f.Anonymous {
					ft := f.Type
					embeddedViaPointer := ft.Kind() == reflect.Pointer
					if embeddedViaPointer {
						ft = ft.Elem()
					}

					// Skip unexported embedded non-struct types, matching
					// encoding/json behavior. Unexported embedded structs
					// still have their exported fields promoted.
					if !f.IsExported() && ft.Kind() != reflect.Struct {
						continue
					}

					tagVal, hasTag := f.Tag.Lookup("json")
					explicitName, _, _ := strings.Cut(tagVal, ",")

					if hasTag && jsontag.ValidName(explicitName) {
						// Embedded struct with an explicit json name is treated as
						// a regular named field; encoding/json does not promote it.
						// An options-only tag (json:",omitempty") has no name -- and
						// a name encoding/json rejects as invalid is discarded the
						// same way -- so both fall through to promotion below,
						// matching encoding/json.
						info := jsontag.Parse(f)
						if info.JSONName == "" {
							continue // json:"-"
						}

						record(
							info.JSONName,
							Sighting{
								StructField: f, Depth: depth, Optional: e.optional, Tagged: true,
								Ghost: e.ghost, GhostOwner: e.ghostOwner,
							},
							dup,
						)

						continue
					}

					if ft.Kind() == reflect.Struct {
						// Check whether this embedded struct is composed via allOf.
						// A ghost subtree skips the probe, since encoding/json knows
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

					// Embedded non-struct type (interfaces included): encoding/json
					// records it as a regular leaf field under the field name, never
					// flattened, so it participates in normal shadowing and
					// ambiguity resolution. The field name is the unqualified type
					// identifier, without the type arguments [reflect.Type.Name]
					// carries for an instantiated generic type.
					record(f.Name, Sighting{
						StructField: f, Depth: depth, Optional: e.optional,
						Ghost: e.ghost, GhostOwner: e.ghostOwner,
					}, dup)

					continue
				}

				if !f.IsExported() {
					continue
				}

				info := jsontag.Parse(f)
				if info.JSONName == "" {
					continue // json:"-"
				}

				record(
					info.JSONName,
					Sighting{
						StructField: f, Depth: depth, Optional: e.optional,
						Tagged: info.TaggedName, Ghost: e.ghost, GhostOwner: e.ghostOwner,
					},
					dup,
				)
			}
		}
	}

	return col
}

// Resolve applies [encoding/json]'s dominance rules to each key. The shallowest
// depth wins, and an explicit json tag breaks a same-depth collision. Resolve
// drops every candidate when none or more than one of them is tagged.
func Resolve(col Collection) Resolution {
	res := Resolution{Annihilated: map[string]int{}}

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
	outcomes := make(map[string]Outcome, len(res.Winners)+len(res.Annihilated))
	for name, depth := range res.Annihilated {
		outcomes[name] = Outcome{Depth: depth, Annihilated: true}
	}

	var out Result

	for i := range res.Winners {
		w := &res.Winners[i]
		if w.Ghost {
			// A composed embed's promoted field won the name, so Classify emits
			// no property; the embed's allOf branch carries the assertion. The
			// real fields it defeated stay out of the result too, matching the
			// value encoding/json actually marshals under the name. Result
			// reports the name, since the caller's closed object must still
			// evaluate it.
			outcomes[w.Name] = Outcome{Depth: w.Depth, GhostOwner: w.GhostOwner}
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

		info := jsontag.Parse(w.StructField)
		if info.JSONName == "" {
			continue
		}

		outcomes[info.JSONName] = Outcome{Depth: w.Depth}

		out.Fields = append(out.Fields, Field{
			StructField: w.StructField,
			JSONName:    info.JSONName,
			Omitempty:   info.Omitempty || w.Optional,
			Omitzero:    info.Omitzero,
			JSONString:  info.JSONString,
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
	outcomes map[string]Outcome,
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
				// No outcome recorded, so the name resolved to nothing because
				// the in-flight guard skipped its ghost. Keep the branch's claim.
				unshadowedAny = true
			case !out.Annihilated && out.GhostOwner == ft && out.Depth == de:
				// This embed's own ghost won the name, so the marshaled object
				// carries the embed's value there.
				unshadowedAny = true
			case out.Depth <= de:
				// A real field won the tie-break, the name annihilated, or
				// another embed claimed it at or above this depth.
				shadowedAny = true
			default:
				unshadowedAny = true
			}
		}

		fi.Shadowed = shadowedAny
		fi.ShadowPartial = shadowedAny && unshadowedAny
	}
}
