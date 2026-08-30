// Package schemaclone deep-copies a [jsonschema.Schema] structurally, one field
// at a time, driven by the canonical field table in
// [go.jacobcolvin.com/x/jsonschema/internal/schemafield]. The validation walk and
// the inliner both need an independent copy of a schema whose in-place mutation
// cannot corrupt the caller's value, so the copy logic is centralized here as a
// single source of truth.
//
// The copy is faithful rather than normalized. It reproduces the source graph's
// shape, including a node reached through two paths and a pointer cycle, and it
// keeps each value's Go type as it stands, so a [json.Number] holds its literal
// and a schema riding in an unknown keyword stays a schema. No graph shape
// defeats the copy, so [Clone] has no error return. A caller that must not
// hold a cyclic graph, because something downstream marshals it, takes the
// same copy through [CloneChecked] and reads the cycle report, which names
// where the loop closes. A caller that needs the report and not the copy calls
// [FindCycle].
package schemaclone

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// Cycle names where a loop closes. Both members are RFC 6901 JSON Pointers
// rooted at the schema the walk started from, so that schema itself renders as
// the empty string.
//
// Where a graph holds more than one loop, the report names one of them, and
// names the same one on every rerun. The field table fixes which keyword the
// walk descends first, and every container it descends walks its members in
// sorted order. One shape escapes that guarantee. A map keyed by anything but
// a string sorts on each key's printed form, so two keys printing alike hold
// the order Go's map iteration gave them, which a rerun may vary.
//
// Two positions render approximately, because [encoding/json] writes the value
// under a name no pointer segment recovers. A struct field tagged json:"-"
// renders under its Go name, though the output holds no such key. An embedded
// struct renders as a segment of its own under its Go type name, though
// [encoding/json] promotes its fields to the enclosing object and writes no
// segment for the embed.
type Cycle struct {
	// Path is the pointer where the loop closes.
	Path string

	// Target is the pointer where the walk entered the node Path returns to.
	Target string
}

// Clone returns a deep copy of s. Two kinds of value stay shared with the
// source, both named below; nothing else does.
//
// Upstream [jsonschema.Schema.CloneSchemas] is shallow for the non-sub-schema
// fields (Extra, Enum, Const, Default, Examples), sharing their backing maps,
// slices, and pointers with the original. Clone copies those too, which is what
// remote-ref isolation demands. The validator's document caches hold copies
// independent of the resolver-owned schemas, so no later walk of a cached
// document can reach the caller's or the resolver's values.
//
// The copy reproduces the source's pointer graph rather than flattening it. A
// schema reachable through two paths is copied once and reachable through two
// paths in the copy, and a cycle is copied as a cycle, so a caller walks the
// result under the same pointer dedup the input needs. A nil s clones to nil, at
// the root and at every sub-schema position alike.
//
// Both shared kinds are immutable in practice. The numeric bound fields
// (Minimum, MaxItems, and their siblings) keep the source's *float64 and *int
// pointers, since nothing writes through them. An unexported struct field
// inside one of the any-typed value fields (Const, Enum, Examples, Extra) keeps
// whatever the shallow struct copy gave it, because reflection cannot write
// such a field and [encoding/json] never serializes it.
func Clone(s *jsonschema.Schema) *jsonschema.Schema {
	cp, _ := CloneChecked(s)

	return cp
}

// CloneChecked returns [Clone]'s copy along with a report of where the source
// closes a cycle that passes through a schema: a path returning to a *Schema it
// is already inside, or to a container it is already inside after crossing a
// schema. A nil report means the source holds neither.
//
// The report rides along on the walk that builds the copy, which reaches every
// edge [json.Marshal] follows. A caller needs it when something downstream
// marshals the copy, because a cyclic schema graph defeats [encoding/json]'s
// cycle detection. Upstream MarshalJSON re-enters [json.Marshal] with a fresh
// encoder at every nesting level, so no single encoder ever sees the repeat and
// the marshal recurses into a stack overflow the runtime cannot recover from.
//
// A cycle closed without passing through a schema, a container holding itself,
// is not reported and needs no report, since [encoding/json] sees that one
// within a single encoder and returns an ordinary error.
func CloneChecked(s *jsonschema.Schema) (*jsonschema.Schema, *Cycle) {
	c := cloner{
		schemas:     map[*jsonschema.Schema]*jsonschema.Schema{},
		values:      map[valueKey]any{},
		onPath:      map[*jsonschema.Schema]int{},
		onPathValue: map[valueKey]valueEntry{},
	}

	cp := c.schema(s)

	return cp, c.cycle
}

// FindCycle returns the cycle report [CloneChecked] hands back beside its copy,
// and nothing else. It serves a caller that keeps the graph it was handed
// rather than a copy, and still has to refuse a cyclic one because something
// downstream marshals it.
//
// It builds the copy and drops it. The walk that finds a cycle is the walk that
// builds the copy, since each field's own closure in the
// [go.jacobcolvin.com/x/jsonschema/internal/schemafield] table drives that walk,
// and each closure writes into a destination the walk hands it. A walk that
// skipped the allocation would be a second implementation of the cycle rule,
// free to drift from [CloneChecked].
func FindCycle(s *jsonschema.Schema) *Cycle {
	_, cyc := CloneChecked(s)

	return cyc
}

// valueKeywords maps a value-bearing field's Go name to the keyword its
// contents sit under, which the path walk pushes before descending into it.
// Extra is absent on purpose, because its own keys are top-level keywords and
// the map walk pushes each of them. DependencyStrings and DependentRequired
// carry a CloneDeep that never reaches the copier, so they need no entry
// either. TestValueKeywordsCoverCopiedFields holds this map to that rule.
var valueKeywords = map[string]string{
	"Const":    keyword.Const,
	"Enum":     keyword.Enum,
	"Examples": keyword.Examples,
}

// cloner carries one Clone call's identity memos. The schemas memo pairs each
// source schema with its copy, and the values memo does the same for the
// containers held in the any-typed value fields. Both collapse a node reached
// twice onto one copy, which is also what lets a cyclic walk terminate.
//
// The remaining fields track the current path. The two onPath maps are keyed by
// the schemas and containers the walk is inside and record where the walk
// entered each, a path length for a schema and a [valueEntry] for a container;
// path holds the decoded segments leading to the position being copied; depth
// counts the schemas crossed; and cycle keeps the first loop the walk closed.
// Field order here answers to govet fieldalignment.
type cloner struct {
	schemas     map[*jsonschema.Schema]*jsonschema.Schema
	values      map[valueKey]any
	onPath      map[*jsonschema.Schema]int
	onPathValue map[valueKey]valueEntry
	cycle       *Cycle
	path        []string
	depth       int
}

// valueEntry records where the walk entered a container. The schema depth tells
// a back edge that crossed a schema from one that did not, and the path length
// addresses the container itself.
type valueEntry struct {
	depth   int
	pathLen int
}

// render writes the first n segments of the current path as an RFC 6901 JSON
// Pointer. The empty prefix is the root, which renders as the empty string.
func (c *cloner) render(n int) string {
	var out strings.Builder

	for _, segment := range c.path[:n] {
		out.WriteByte('/')
		out.WriteString(jsonptr.Escape(segment))
	}

	return out.String()
}

// reportCycle records the loop closing at the current position. The entered
// parameter is the path length at which the walk entered the node the loop
// returns to, so rendering that prefix gives the node's own pointer. The first
// loop the walk finds is the one it keeps.
func (c *cloner) reportCycle(entered int) {
	if c.cycle == nil {
		c.cycle = &Cycle{Path: c.render(len(c.path)), Target: c.render(entered)}
	}
}

// enterValue records a container's copy in the memo and marks the container as
// being on the current path, so a later hit on it can tell a back edge from a
// second, independent visit.
func (c *cloner) enterValue(key valueKey, keyed bool, cp any) {
	if !keyed {
		return
	}

	c.values[key] = cp
	c.onPathValue[key] = valueEntry{depth: c.depth, pathLen: len(c.path)}
}

// leaveValue takes a filled container off the path. Its memo entry stays, since
// a later visit still reuses the copy.
func (c *cloner) leaveValue(key valueKey, keyed bool) {
	if keyed {
		delete(c.onPathValue, key)
	}
}

// revisitValue reports a cycle when a memo hit lands on a container still being
// filled and the walk entered at least one schema since. That is the shape a
// marshal recurses into, because the path leaves the container, crosses a
// schema, and comes back. A container that merely holds itself crosses no
// schema and stays unreported, since [encoding/json] catches that one on its
// own.
func (c *cloner) revisitValue(key valueKey) {
	if entry, onPath := c.onPathValue[key]; onPath && c.depth > entry.depth {
		c.reportCycle(entry.pathLen)
	}
}

// valueKey identifies a container by its type and its backing storage.
type valueKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	typ reflect.Type
	//nolint:unused // Read via struct equality when used as a map key.
	ptr uintptr
	//nolint:unused // Read via struct equality when used as a map key.
	size int
}

// containerKey returns the memo key for a container of size elements, and
// whether that container has an identity to key on. A pointer keys as a
// one-element container, since its identity is the address alone. An empty
// container has none, because Go serves every zero-size allocation from one
// address, so two distinct empty containers would share a key and collapse into
// one copy.
func containerKey(rv reflect.Value, size int) (valueKey, bool) {
	if size == 0 {
		return valueKey{}, false
	}

	return valueKey{typ: rv.Type(), ptr: rv.Pointer(), size: size}, true
}

// schema returns the copy of s, making it on first sight. It records the memo
// entry before filling the fields, so a cycle re-entering s finds the copy under
// construction and closes the loop instead of recursing.
func (c *cloner) schema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}

	if cp, seen := c.schemas[s]; seen {
		if entered, onPath := c.onPath[s]; onPath {
			c.reportCycle(entered)
		}

		return cp
	}

	cp := new(jsonschema.Schema)
	c.schemas[s] = cp
	c.onPath[s] = len(c.path)
	c.depth++

	c.fill(cp, s)

	c.depth--
	delete(c.onPath, s)

	return cp
}

// fill copies every field of src onto dst. The shallow struct copy carries the
// scalars, and the field table then handles each field that owns something
// mutable. Each field's own closure rebuilds a sub-schema container through the
// memo, copies a container with a mutable interior value by value, or
// reallocates the container's header.
//
// Fill pushes the keyword addressing each field's contents, then truncates the
// path back, so the path always names the position being copied. A
// single-schema field takes no push here, since its closure passes the keyword
// itself as the child's one segment. The table's order fixes which keyword the
// walk descends first.
func (c *cloner) fill(dst, src *jsonschema.Schema) {
	*dst = *src

	for i := range schemafield.Fields {
		f := &schemafield.Fields[i]
		mark := len(c.path)

		switch {
		case f.CloneSubschemas != nil:
			if f.Shape != schemafield.Single {
				c.path = append(c.path, f.Keyword)
			}

			f.CloneSubschemas(dst, c.subschema)

		case f.CloneDeep != nil:
			if kw := valueKeywords[f.Name]; kw != "" {
				c.path = append(c.path, kw)
			}

			f.CloneDeep(dst, c.value)

		case f.CloneContainer != nil:
			f.CloneContainer(dst)
		}

		c.path = c.path[:mark]
	}
}

// subschema copies one sub-schema of the field being filled, appending the
// segment that field's clone closure supplies. It appends unconditionally, so a
// map key that is the empty string addresses its child the same way any other
// key does.
func (c *cloner) subschema(token string, sub *jsonschema.Schema) *jsonschema.Schema {
	mark := len(c.path)
	c.path = append(c.path, token)

	cp := c.schema(sub)
	c.path = c.path[:mark]

	return cp
}

// schemaValue copies a schema held as a value rather than a pointer. Such a
// schema has no address the memo can key on, and a copy of it cannot be shared
// anyway, so only its sub-schema pointers route through the memo. It still
// counts toward the path depth. Upstream's MarshalJSON takes a value receiver,
// so a schema value re-enters [json.Marshal] with a fresh encoder exactly as a
// pointer does, and a loop closing around one recurses just as far.
func (c *cloner) schemaValue(src *jsonschema.Schema) jsonschema.Schema {
	var out jsonschema.Schema

	c.depth++

	c.fill(&out, src)

	c.depth--

	return out
}

// value returns a deep copy of a value held in one of the any-typed fields. The
// concrete cases cover what parsed JSON and the schema graph produce, a
// [json.Number] included. That one is a string type, so the assignment carries
// the literal across exactly. Anything else goes to the reflection walk, which
// reaches a schema nested in a typed container just as [encoding/json] would.
func (c *cloner) value(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case bool, string, float64, json.Number:
		return val
	case *jsonschema.Schema:
		return c.schema(val)
	case jsonschema.Schema:
		return c.schemaValue(&val)
	case map[string]any:
		return c.valueMap(val)
	case []any:
		return c.valueSlice(val)
	default:
		return c.reflectValue(reflect.ValueOf(v)).Interface()
	}
}

// valueMap copies a JSON object, recording the copy before filling it so a map
// holding itself terminates. It walks the map's keys in sorted order.
func (c *cloner) valueMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	key, keyed := containerKey(reflect.ValueOf(m), len(m))
	if keyed {
		if seen, hit := c.values[key].(map[string]any); hit {
			c.revisitValue(key)

			return seen
		}
	}

	cp := make(map[string]any, len(m))
	c.enterValue(key, keyed, cp)

	for _, name := range slices.Sorted(maps.Keys(m)) {
		mark := len(c.path)
		c.path = append(c.path, name)

		cp[name] = c.value(m[name])
		c.path = c.path[:mark]
	}

	c.leaveValue(key, keyed)

	return cp
}

// valueSlice copies a JSON array, recording the copy before filling it so a
// slice holding itself terminates. The length is fixed up front, so the recorded
// copy and the filled one are the same array.
func (c *cloner) valueSlice(list []any) []any {
	if list == nil {
		return nil
	}

	key, keyed := containerKey(reflect.ValueOf(list), len(list))
	if keyed {
		if seen, hit := c.values[key].([]any); hit {
			c.revisitValue(key)

			return seen
		}
	}

	cp := make([]any, len(list))
	c.enterValue(key, keyed, cp)

	for i, elem := range list {
		mark := len(c.path)
		c.path = append(c.path, strconv.Itoa(i))

		cp[i] = c.value(elem)
		c.path = c.path[:mark]
	}

	c.leaveValue(key, keyed)

	return cp
}

// reflectValue copies a value of a type the concrete switch in [cloner.value]
// does not name, following the edges [encoding/json] serializes: pointers,
// interfaces, slices, arrays, maps, and exported struct fields. A schema found
// along the way routes back through the memo, so aliasing and cycles survive the
// walk. Unexported struct fields keep the value the whole-struct copy gave them,
// because encoding/json never serializes them and reflection cannot write them.
// Kinds with no interior (strings, numbers, funcs, channels) are returned as
// they stand.
func (c *cloner) reflectValue(rv reflect.Value) reflect.Value {
	switch rv.Kind() {
	case reflect.Pointer:
		return c.reflectPointer(rv)

	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}

		return reflect.ValueOf(c.value(rv.Interface()))

	case reflect.Struct:
		return c.reflectStruct(rv)

	case reflect.Slice:
		return c.reflectSlice(rv)

	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := range rv.Len() {
			mark := len(c.path)
			c.path = append(c.path, strconv.Itoa(i))

			out.Index(i).Set(c.reflectValue(rv.Index(i)))

			c.path = c.path[:mark]
		}

		return out

	case reflect.Map:
		return c.reflectMap(rv)

	default:
		return rv
	}
}

// reflectPointer copies a pointer and its pointee, routing a schema pointer
// through the memo so it keeps the identity the rest of the copy gives it.
func (c *cloner) reflectPointer(rv reflect.Value) reflect.Value {
	if rv.IsNil() {
		return rv
	}

	if s, isSchema := rv.Interface().(*jsonschema.Schema); isSchema {
		return reflect.ValueOf(c.schema(s))
	}

	key, keyed := containerKey(rv, 1)
	if keyed {
		if seen, hit := c.values[key]; hit {
			c.revisitValue(key)

			return reflect.ValueOf(seen)
		}
	}

	out := reflect.New(rv.Type().Elem())
	c.enterValue(key, keyed, out.Interface())

	out.Elem().Set(c.reflectValue(rv.Elem()))
	c.leaveValue(key, keyed)

	return out
}

// reflectStruct copies a struct value, deep-copying its exported fields over the
// whole-struct copy that carries the unexported ones.
func (c *cloner) reflectStruct(rv reflect.Value) reflect.Value {
	if s, isSchema := rv.Interface().(jsonschema.Schema); isSchema {
		return reflect.ValueOf(c.schemaValue(&s))
	}

	out := reflect.New(rv.Type()).Elem()
	out.Set(rv)

	for i := range rv.NumField() {
		field := rv.Type().Field(i)
		if !field.IsExported() {
			continue
		}

		mark := len(c.path)
		c.path = append(c.path, fieldToken(field))

		out.Field(i).Set(c.reflectValue(rv.Field(i)))

		c.path = c.path[:mark]
	}

	return out
}

// fieldToken addresses a struct field the way [encoding/json] writes it: the
// name its json tag gives, or the Go field name when the tag names none. Two
// shapes render approximately rather than exactly, each for its own reason. A
// field tagged json:"-" leaves no key in the output, so the Go name stands in
// for a segment the output has none of. An embedded struct writes no key
// either, since [encoding/json] promotes its fields to the enclosing object,
// so the Go type name here is one segment more than the output carries.
func fieldToken(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "" || name == "-" {
		return field.Name
	}

	return name
}

// reflectSlice copies a slice, recording the copy before filling it so a slice
// reaching itself terminates.
func (c *cloner) reflectSlice(rv reflect.Value) reflect.Value {
	if rv.IsNil() {
		return rv
	}

	key, keyed := containerKey(rv, rv.Len())
	if keyed {
		if seen, hit := c.values[key]; hit {
			c.revisitValue(key)

			return reflect.ValueOf(seen)
		}
	}

	out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	c.enterValue(key, keyed, out.Interface())

	for i := range rv.Len() {
		mark := len(c.path)
		c.path = append(c.path, strconv.Itoa(i))

		out.Index(i).Set(c.reflectValue(rv.Index(i)))

		c.path = c.path[:mark]
	}

	c.leaveValue(key, keyed)

	return out
}

// reflectMap copies a map, recording the copy before filling it so a map
// reaching itself terminates. Keys carry over unchanged, because
// [encoding/json] only serializes a map whose keys are strings or scalars, and
// copying a reference-typed key would leave the copy unequal to its source and
// unsearchable with the source's keys. Each key addresses its value by its
// printed form, and the walk walks the keys in sorted order.
func (c *cloner) reflectMap(rv reflect.Value) reflect.Value {
	if rv.IsNil() {
		return rv
	}

	key, keyed := containerKey(rv, rv.Len())
	if keyed {
		if seen, hit := c.values[key]; hit {
			c.revisitValue(key)

			return reflect.ValueOf(seen)
		}
	}

	out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
	c.enterValue(key, keyed, out.Interface())

	for _, entry := range sortedEntries(rv) {
		mark := len(c.path)
		c.path = append(c.path, entry.token)

		out.SetMapIndex(entry.key, c.reflectValue(rv.MapIndex(entry.key)))

		c.path = c.path[:mark]
	}

	c.leaveValue(key, keyed)

	return out
}

// mapEntry pairs a map key with the path segment addressing its value.
type mapEntry struct {
	key   reflect.Value
	token string
}

// sortedEntries lists a map's keys beside their printed form, in sorted order
// on that form. The order fixes which key the walk reaches first. Two keys
// printing alike keep the order [reflect.Value.MapKeys] gave them.
func sortedEntries(rv reflect.Value) []mapEntry {
	entries := make([]mapEntry, 0, rv.Len())

	for _, key := range rv.MapKeys() {
		entries = append(entries, mapEntry{key: key, token: fmt.Sprint(key.Interface())})
	}

	slices.SortStableFunc(entries, func(a, b mapEntry) int {
		return strings.Compare(a.token, b.token)
	})

	return entries
}
