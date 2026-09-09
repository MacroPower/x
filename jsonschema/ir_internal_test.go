package jsonschema

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// overrideEffect is what a type= override does to one node field.
type overrideEffect uint8

const (
	// An effectKept field is a fact of the position, not of the type, and
	// survives the override unchanged.
	effectKept overrideEffect = iota
	// An effectReset field is a type-derived fact and returns to its zero
	// value.
	effectReset
	// The effectPayload field takes the override on a private clone.
	effectPayload
	// The effectCopy field receives the value copy of the node as reflected.
	effectCopy
	// An effectByKind field is a child slot, kept only where the new type
	// keeps the container kind and reset otherwise.
	effectByKind
)

// overrideEffects declares the fate of every node field under an override.
// TestOverrideTypeClassifiesEveryNodeField holds it total, so a field added
// to node fails until it is classified here.
var overrideEffects = map[string]overrideEffect{
	"payload":  effectPayload,
	"overrode": effectCopy,
	"ghostWon": effectByKind,
	"def":      effectReset,
	"occ":      effectReset,
	"stance":   effectReset,
	"null":     effectReset,
	"verbatim": effectReset,
	"kind":     effectByKind,
	"typ":      effectByKind,
	"items":    effectByKind,
	"prefix":   effectByKind,
	"props":    effectByKind,
	"embeds":   effectByKind,
	"fallback": effectByKind,
	"authored": effectKept,
	"origin":   effectKept,
	"isBody":   effectKept,
	"composed": effectKept,
	"hooked":   effectKept,
	"isField":  effectKept,
}

// nodeFields reads every field of a node by name, so the tests compare
// fields generically without reaching unexported fields through reflect.
// The classification guard holds it total as well.
func nodeFields(n *node) map[string]any {
	return map[string]any{
		"payload":  n.payload,
		"def":      n.def,
		"typ":      n.typ,
		"items":    n.items,
		"authored": n.authored,
		"origin":   n.origin,
		"overrode": n.overrode,
		"props":    n.props,
		"prefix":   n.prefix,
		"embeds":   n.embeds,
		"ghostWon": n.ghostWon,
		"occ":      n.occ,
		"stance":   n.stance,
		"null":     n.null,
		"kind":     n.kind,
		"fallback": n.fallback,
		"isBody":   n.isBody,
		"composed": n.composed,
		"verbatim": n.verbatim,
		"hooked":   n.hooked,
		"isField":  n.isField,
	}
}

// TestOverrideTypeClassifiesEveryNodeField pins that every field of node has
// exactly one declared fate under a type= override, and that nodeFields reads
// every one, so a field added to node cannot slip past the override unnoticed.
func TestOverrideTypeClassifiesEveryNodeField(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[node]()

	names := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		names = append(names, field.Name)
	}

	classified := make([]string, 0, len(overrideEffects))
	for name := range overrideEffects {
		classified = append(classified, name)
	}

	read := make([]string, 0, len(overrideEffects))
	for name := range nodeFields(&node{}) {
		read = append(read, name)
	}

	assert.ElementsMatch(t, names, classified, "every node field takes a row in overrideEffects")
	assert.ElementsMatch(t, names, read, "nodeFields reads every node field")
}

type overrideVerbatim struct{}

// overrideComposed is an embedded type a WithTypeSchema value intercepts, so
// the struct embedding it composes through allOf and its field G is a
// ghost-won name on that struct's node.
type overrideComposed struct {
	G int `json:"g"`
}

type overrideNamed struct {
	A int `json:"a"`
}

// overrideFixture carries one field per node kind and per reset fact: a
// leaf with kind-derived bounds, a pointer occurrence, a builtin string
// type, a list, a tuple, a map, an inline struct, an extracted struct, and
// a verbatim hook type.
type overrideFixture struct {
	I  int64  `json:"i"`
	P  *int64 `json:"p"`
	In struct {
		X int `json:"x"`
	} `json:"in"`
	C struct {
		overrideComposed
	} `json:"c"`
	M map[string]*int  `json:"m"`
	T time.Time        `json:"t"`
	N overrideNamed    `json:"n"`
	V overrideVerbatim `json:"v"`
	S []string         `json:"s"`
	A [2]int           `json:"a"`
}

// reflectOverrideFixture reflects the fixture through a fresh run and
// returns its property nodes by JSON name.
func reflectOverrideFixture(t *testing.T) map[string]*node {
	t.Helper()

	g := newConfig([]GenerateOption{
		WithTypeSchemaFor[overrideVerbatim](TypeSchema{Verbatim: &Schema{Type: typename.Object}}),
		WithTypeSchemaFor[overrideComposed](TypeSchema{Value: &Schema{Type: typename.Object}}),
	}).forRun(t.Context())

	root, err := g.schemaForType(reflect.TypeFor[overrideFixture](), false)
	require.NoError(t, err)

	body := root
	if root.kind == kindRef {
		body = root.def.body
	}

	require.Equal(t, kindObject, body.kind)

	props := map[string]*node{}
	for i := range body.props {
		props[body.props[i].name] = body.props[i].schema
	}

	return props
}

// TestOverrideTypeMatchesReflection pins the in-place override against the
// declared fates for every node kind and every JSON type name: kept fields
// equal the reflected node, reset fields are zero, the payload is a private
// clone carrying the new type, the copy is the node as reflected, and the
// child slots follow the kind table.
func TestOverrideTypeMatchesReflection(t *testing.T) {
	t.Parallel()

	typeNames := []string{
		typename.String, typename.Integer, typename.Number, typename.Boolean,
		typename.Null, typename.Array, typename.Object,
	}

	kinds := map[nodeKind]bool{}
	for _, n := range reflectOverrideFixture(t) {
		kinds[n.kind] = true
	}

	for _, kind := range []nodeKind{kindValue, kindObject, kindList, kindTuple, kindMap, kindRef} {
		require.True(t, kinds[kind], "the fixture reaches node kind %d", kind)
	}

	for _, typeName := range typeNames {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()

			props := reflectOverrideFixture(t)

			require.True(t, props["v"].verbatim, "the verbatim hook type reflects verbatim")
			require.True(t, props["p"].occ.pointer, "the pointer field reflects as a pointer occurrence")
			require.NotNil(t, props["n"].def, "the extracted struct reflects as a reference")
			require.NotNil(t, props["i"].payload.Minimum, "the int64 reflects with kind-derived bounds")
			require.Equal(t, []string{"g"}, props["c"].ghostWon, "the composed embed promotes a ghost-won name")

			for name, n := range props {
				pre := *n
				before := nodeFields(n)
				keepsChildren := (pre.kind == kindList || pre.kind == kindTuple) && typeName == typename.Array ||
					(pre.kind == kindMap || pre.kind == kindObject) && typeName == typename.Object

				n.overrideType(typeName)

				after := nodeFields(n)

				for field, effect := range overrideEffects {
					label := name + "." + field

					switch effect {
					case effectKept:
						assert.Equal(t, before[field], after[field], label)
					case effectReset:
						assert.Zero(t, after[field], label)
					case effectPayload:
						assert.NotSame(t, pre.payload, n.payload, label+" is a private clone")
						assert.Equal(t, typeName, n.payload.Type, label)
						assert.Equal(t, before["payload"], pre.payload, label+" leaves the reflected payload alone")

					case effectCopy:
						require.NotNil(t, n.overrode, label)
						assert.Equal(t, pre, *n.overrode, label+" is the node as reflected")

					case effectByKind:
						if keepsChildren {
							assert.Equal(t, before[field], after[field], label+" is kept with the container kind")
						} else {
							assert.Zero(t, after[field], label+" is dropped with the container kind")
						}
					}
				}
			}
		})
	}

	t.Run("a second pair keeps the first copy", func(t *testing.T) {
		t.Parallel()

		n := reflectOverrideFixture(t)["s"]
		n.overrideType(typename.Array)

		first := n.overrode
		require.Equal(t, kindList, n.kind)

		n.overrideType(typename.String)
		assert.Same(t, first, n.overrode)
		assert.Equal(t, kindValue, n.kind)
		assert.Nil(t, n.items)
	})
}

// nullRule is one sentence of the null decision: the facts it speaks for
// and the verdict it gives. The first rule whose when holds decides.
type nullRule struct {
	when    func(f nullFacts) bool
	name    string
	verdict bool
}

// admitRules spell out admitNull in order, one sentence each.
func admitRules() []nullRule {
	return []nullRule{
		{
			name: "a composed embed branch admits no null", verdict: false,
			when: func(f nullFacts) bool { return f.role == roleComposed },
		},
		{
			name: "a verbatim leaf admits no null", verdict: false,
			when: func(f nullFacts) bool { return f.verbatim },
		},
		{
			name: "a body whose payload names null admits it", verdict: true,
			when: func(f nullFacts) bool { return f.role == roleBody && f.declaresNull },
		},
		{
			name: "a body admits the container null its entry's stance does not veto", verdict: true,
			when: func(f nullFacts) bool {
				return f.role == roleBody && f.defStance != NullForbidden && f.containerNull()
			},
		},
		{
			name: "a body admits nothing else", verdict: false,
			when: func(f nullFacts) bool { return f.role == roleBody },
		},
		{
			name: "an occurrence whose payload names null admits it", verdict: true,
			when: func(f nullFacts) bool { return f.declaresNull },
		},
		{
			name: "a reference whose body admits null on its own admits it", verdict: true,
			when: func(f nullFacts) bool { return f.ref && f.targetNull },
		},
		{
			name: "an unrestricted occurrence admits null", verdict: true,
			when: func(f nullFacts) bool { return f.unrestricted },
		},
		{
			name: "a NullAllowed entry stance grants null to a reference", verdict: true,
			when: func(f nullFacts) bool { return f.defStance == NullAllowed },
		},
		{
			name: "a NullForbidden entry stance vetoes null on a reference", verdict: false,
			when: func(f nullFacts) bool { return f.defStance == NullForbidden },
		},
		{
			name: "a NullAllowed stance grants null", verdict: true,
			when: func(f nullFacts) bool { return f.stance == NullAllowed },
		},
		{
			name: "a NullForbidden stance vetoes null", verdict: false,
			when: func(f nullFacts) bool { return f.stance == NullForbidden },
		},
		{
			name: "a pointer position admits null", verdict: true,
			when: func(f nullFacts) bool { return f.pointer },
		},
		{
			name: "a container whose nil marshals as null admits it", verdict: true,
			when: func(f nullFacts) bool { return f.containerNull() },
		},
		{
			name: "any other position admits no null", verdict: false,
			when: func(nullFacts) bool { return true },
		},
	}
}

// wrapRules spell out wrapNull in order, for a node that admits null.
func wrapRules() []nullRule {
	return []nullRule{
		{
			name: "no wrapper where the payload names null itself", verdict: false,
			when: func(f nullFacts) bool { return f.declaresNull },
		},
		{
			name: "no wrapper on a reference whose body admits null on its own", verdict: false,
			when: func(f nullFacts) bool { return f.ref && f.targetNull },
		},
		{
			name: "no wrapper on a reference to a container body whose type list carries the null", verdict: false,
			when: func(f nullFacts) bool { return f.ref && f.defStance != NullForbidden && f.containerNull() },
		},
		{
			name: "a wrapper everywhere else", verdict: true,
			when: func(nullFacts) bool { return true },
		},
	}
}

// firstRule returns the first rule whose when holds.
func firstRule(rules []nullRule, f nullFacts) nullRule {
	for _, r := range rules {
		if r.when(f) {
			return r
		}
	}

	panic("no rule holds")
}

// nullFactAxes lists every value each nullFacts field takes, keyed by field
// name, each value as the setter that writes it. TestNullDecisionRules holds
// the keys total against the struct, so a fact added to nullFacts is
// enumerated before it is decided.
func nullFactAxes() map[string][]func(*nullFacts) {
	bools := func(set func(*nullFacts, bool)) []func(*nullFacts) {
		return []func(*nullFacts){
			func(f *nullFacts) { set(f, false) },
			func(f *nullFacts) { set(f, true) },
		}
	}
	stances := func(set func(*nullFacts, Nullability)) []func(*nullFacts) {
		return []func(*nullFacts){
			func(f *nullFacts) { set(f, NullFromReflection) },
			func(f *nullFacts) { set(f, NullAllowed) },
			func(f *nullFacts) { set(f, NullForbidden) },
		}
	}

	containers := make([]func(*nullFacts), 0, 5)
	for _, c := range []containerKind{containerNone, containerSlice, containerMap, containerBytes, containerQuoted} {
		containers = append(containers, func(f *nullFacts) { f.container = c })
	}

	roles := make([]func(*nullFacts), 0, 3)
	for _, r := range []nullRole{roleOccurrence, roleBody, roleComposed} {
		roles = append(roles, func(f *nullFacts) { f.role = r })
	}

	return map[string][]func(*nullFacts){
		"container":    containers,
		"role":         roles,
		"stance":       stances(func(f *nullFacts, s Nullability) { f.stance = s }),
		"defStance":    stances(func(f *nullFacts, s Nullability) { f.defStance = s }),
		"pointer":      bools(func(f *nullFacts, b bool) { f.pointer = b }),
		"ref":          bools(func(f *nullFacts, b bool) { f.ref = b }),
		"verbatim":     bools(func(f *nullFacts, b bool) { f.verbatim = b }),
		"declaresNull": bools(func(f *nullFacts, b bool) { f.declaresNull = b }),
		"targetNull":   bools(func(f *nullFacts, b bool) { f.targetNull = b }),
		"unrestricted": bools(func(f *nullFacts, b bool) { f.unrestricted = b }),
		"nilSliceNull": bools(func(f *nullFacts, b bool) { f.nilSliceNull = b }),
		"nilMapNull":   bools(func(f *nullFacts, b bool) { f.nilMapNull = b }),
	}
}

// everyNullFacts calls visit with every combination of the axes.
func everyNullFacts(visit func(nullFacts)) {
	axes := nullFactAxes()

	names := make([]string, 0, len(axes))
	for name := range axes {
		names = append(names, name)
	}

	var rec func(i int, f nullFacts)

	rec = func(i int, f nullFacts) {
		if i == len(names) {
			visit(f)

			return
		}

		for _, set := range axes[names[i]] {
			set(&f)
			rec(i+1, f)
		}
	}

	rec(0, nullFacts{})
}

// TestNullDecisionRules pins the null decision as an ordered list of
// sentences over the full cross product of nullFacts: every fact field has
// an axis, every combination decides as the first rule that speaks for it,
// and every rule speaks for at least one combination. The four past
// nullability fixes are named cases at the end.
func TestNullDecisionRules(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[nullFacts]()

	fields := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		fields = append(fields, field.Name)
	}

	axes := make([]string, 0, len(nullFactAxes()))
	for name := range nullFactAxes() {
		axes = append(axes, name)
	}

	require.ElementsMatch(t, fields, axes, "every nullFacts field has an axis")

	admits, wraps := admitRules(), wrapRules()
	fired := map[string]int{}
	cases := 0

	everyNullFacts(func(f nullFacts) {
		cases++

		admit := firstRule(admits, f)
		fired[admit.name]++
		require.Equal(t, admit.verdict, admitNull(f), "%+v: %s", f, admit.name)

		if !admit.verdict {
			assert.False(t, wrapNull(f, false), "%+v: no wrapper where no null is admitted", f)

			return
		}

		wrap := firstRule(wraps, f)
		fired[wrap.name]++
		require.Equal(t, wrap.verdict, wrapNull(f, true), "%+v: %s", f, wrap.name)
	})

	assert.Positive(t, cases)

	for _, r := range append(admits, wraps...) {
		assert.Positive(t, fired[r.name], "rule never fires: %s", r.name)
	}

	past := map[string]struct {
		facts nullFacts
		admit bool
		wrap  bool
	}{
		"d1850de6 a reference reads its body's container null": {
			facts: nullFacts{ref: true, container: containerSlice, nilSliceNull: true},
			admit: true, wrap: false,
		},
		"604af3d1 a pointer to a named container admits null": {
			facts: nullFacts{ref: true, pointer: true, container: containerSlice},
			admit: true, wrap: true,
		},
		"ada9e95d a NullForbidden entry vetoes the format null on the reference": {
			facts: nullFacts{ref: true, container: containerSlice, nilSliceNull: true, defStance: NullForbidden},
			admit: false, wrap: false,
		},
		"ada9e95d a NullForbidden entry vetoes the format null on the body": {
			facts: nullFacts{role: roleBody, container: containerSlice, nilSliceNull: true, defStance: NullForbidden},
			admit: false, wrap: false,
		},
		"a pointer embed branch composes without null": {
			facts: nullFacts{role: roleComposed, pointer: true},
			admit: false, wrap: false,
		},
	}

	for name, tc := range past {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			admit := admitNull(tc.facts)
			assert.Equal(t, tc.admit, admit)
			assert.Equal(t, tc.wrap, wrapNull(tc.facts, admit))
		})
	}
}

// emittedOrphan is a named struct a type= pair orphans in emittedRoot.
type emittedOrphan struct {
	X int `json:"x"`
}

// emittedKept is a named struct emittedRoot keeps referenced.
type emittedKept struct {
	Y int `json:"y"`
}

// emittedRoot is a bare-$ref root the render inlines, with one orphaned and
// one kept def beneath it.
type emittedRoot struct {
	A emittedOrphan `json:"a" jsonschema:"type=string"`
	B emittedKept   `json:"b"`
}

// emittedRecursive is a self-recursive root, which keeps its own def.
type emittedRecursive struct {
	Next *emittedRecursive `json:"next"`
}

// TestEmittedDefsMatchRender pins that the def set naming disambiguates over
// is the set render emits: emittedDefs, read before naming off the tokens
// and the root's wrapper decision, agrees with collectReferencedDefs, read
// after the field hooks and root inlining, on a type= orphan, an inlined
// root, and a self-recursive root.
func TestEmittedDefsMatchRender(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ  reflect.Type
		want []string
	}{
		"orphan beside an inlined root": {typ: reflect.TypeFor[emittedRoot](), want: []string{"emittedKept"}},
		"self-recursive root":           {typ: reflect.TypeFor[emittedRecursive](), want: []string{"emittedRecursive"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := newConfig(nil).forRun(t.Context())

			root, err := g.schemaForType(tc.typ, false)
			require.NoError(t, err)

			g.resolveNullability(root)

			emitted := g.emittedDefs(root)
			g.assignDefNames(emitted)
			g.finalizeRefs(root)
			require.NoError(t, g.applyFieldHooks(root))

			root = g.maybeInlineRoot(root)

			var got, want []string

			for _, e := range g.collectReferencedDefs(root) {
				got = append(got, e.name)
			}

			for e := range emitted {
				want = append(want, e.name)
			}

			assert.ElementsMatch(t, want, got, "the naming-time set is the render-time set")
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}
