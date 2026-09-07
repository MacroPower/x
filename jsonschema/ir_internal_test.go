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
