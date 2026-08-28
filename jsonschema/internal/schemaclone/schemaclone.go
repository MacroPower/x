// Package schemaclone deep-copies a [jsonschema.Schema] structurally, one field
// at a time, driven by the canonical field table in
// [go.jacobcolvin.com/x/jsonschema/internal/schemafield]. The validation walk and
// the inliner both need an independent copy of a schema whose in-place mutation
// cannot corrupt the caller's value, so the copy logic is centralized here as a
// single source of truth.
//
// The copy is faithful rather than normalized. It reproduces the source graph's
// shape, a node reached through two paths and a pointer cycle included, and it
// keeps each value's Go type as it stands, so a [json.Number] holds its literal
// and a schema riding in an unknown keyword stays a schema. No graph shape
// defeats the copy, so [Clone] has no error return.
package schemaclone

import (
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// Clone returns a deep copy of s that shares no mutable value with it.
//
// Upstream [jsonschema.Schema.CloneSchemas] is shallow for the non-sub-schema
// fields (Extra, Enum, Const, Default, Examples): it shares their backing maps,
// slices, and pointers with the original. Clone copies those too, which is what
// remote-ref isolation requires. The caches hold copies independent of the
// resolver-owned schemas, so no later walk of a cached document can reach the
// caller's or the resolver's values.
//
// The copy reproduces the source's pointer graph rather than flattening it. A
// schema reachable through two paths is copied once and reachable through two
// paths in the copy, and a cycle is copied as a cycle, so a caller walks the
// result under the same pointer dedup the input needs. A nil s clones to nil, at
// the root and at every sub-schema position alike.
//
// One value stays shared with the source: an unexported struct field inside one
// of the any-typed value fields (Const, Enum, Examples, Extra). Reflection
// cannot write such a field and [encoding/json] never serialized it, so the copy
// keeps whatever the shallow struct copy gave it.
func Clone(s *jsonschema.Schema) *jsonschema.Schema {
	c := cloner{
		schemas: map[*jsonschema.Schema]*jsonschema.Schema{},
		values:  map[valueKey]any{},
	}

	return c.schema(s)
}

// cloner carries one Clone call's identity memos. The schemas memo pairs each
// source schema with its copy, and the values memo does the same for the
// containers held in the any-typed value fields. Both are what make an aliased
// edge stay one edge and a cyclic walk terminate.
type cloner struct {
	schemas map[*jsonschema.Schema]*jsonschema.Schema
	values  map[valueKey]any
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
// whether that container has an identity to key on. An empty container has
// none: Go serves every zero-size allocation from one address, so two distinct
// empty containers would share a key and collapse into one copy.
func containerKey(rv reflect.Value, size int) (valueKey, bool) {
	if size == 0 {
		return valueKey{}, false
	}

	return valueKey{typ: rv.Type(), ptr: rv.Pointer(), size: size}, true
}

// schema returns the copy of s, making it on first sight. The memo entry is
// recorded before the fields are filled, so a cycle re-entering s finds the copy
// under construction and closes the loop instead of recursing.
func (c *cloner) schema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}

	if cp, seen := c.schemas[s]; seen {
		return cp
	}

	cp := new(jsonschema.Schema)
	c.schemas[s] = cp
	c.fill(cp, s)

	return cp
}

// fill copies every field of src onto dst. The shallow struct copy carries the
// scalars and the field table then handles each field that owns something
// mutable: a sub-schema container is rebuilt through the memo, a container with
// a mutable interior is copied value by value, and every other container has its
// header reallocated.
func (c *cloner) fill(dst, src *jsonschema.Schema) {
	*dst = *src

	for i := range schemafield.Fields {
		f := &schemafield.Fields[i]

		switch {
		case f.CloneSubschemas != nil:
			f.CloneSubschemas(dst, c.schema)
		case f.CloneDeep != nil:
			f.CloneDeep(dst, c.value)
		case f.CloneContainer != nil:
			f.CloneContainer(dst)
		}
	}
}

// schemaValue copies a schema held as a value rather than a pointer. Such a
// schema has no address the memo can key on, and a copy of it cannot be shared
// anyway, so only its sub-schema pointers route through the memo. Any cycle
// through a schema value re-enters a schema pointer, so the walk still
// terminates.
func (c *cloner) schemaValue(src *jsonschema.Schema) jsonschema.Schema {
	var out jsonschema.Schema

	c.fill(&out, src)

	return out
}

// value returns a deep copy of a value held in one of the any-typed fields. The
// concrete cases cover what parsed JSON and the schema graph produce, a
// [json.Number] included: it is a string type, so the assignment carries the
// literal across exactly. Anything else goes to the reflection walk, which
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
// holding itself terminates.
func (c *cloner) valueMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	key, keyed := containerKey(reflect.ValueOf(m), len(m))
	if keyed {
		if seen, hit := c.values[key].(map[string]any); hit {
			return seen
		}
	}

	cp := make(map[string]any, len(m))
	if keyed {
		c.values[key] = cp
	}

	for name, elem := range m {
		cp[name] = c.value(elem)
	}

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
			return seen
		}
	}

	cp := make([]any, len(list))
	if keyed {
		c.values[key] = cp
	}

	for i, elem := range list {
		cp[i] = c.value(elem)
	}

	return cp
}

// reflectValue copies a value of a type the concrete switch in [cloner.value]
// does not name, following the edges [encoding/json] serializes: pointers,
// interfaces, slices, arrays, maps, and exported struct fields. A schema found
// along the way routes back through the memo, so aliasing and cycles survive the
// walk. Unexported struct fields keep the value the whole-struct copy gave them,
// because encoding/json never serialized them and reflection cannot write them.
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
			out.Index(i).Set(c.reflectValue(rv.Index(i)))
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
			return reflect.ValueOf(seen)
		}
	}

	out := reflect.New(rv.Type().Elem())
	if keyed {
		c.values[key] = out.Interface()
	}

	out.Elem().Set(c.reflectValue(rv.Elem()))

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
		if !rv.Type().Field(i).IsExported() {
			continue
		}

		out.Field(i).Set(c.reflectValue(rv.Field(i)))
	}

	return out
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
			return reflect.ValueOf(seen)
		}
	}

	out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	if keyed {
		c.values[key] = out.Interface()
	}

	for i := range rv.Len() {
		out.Index(i).Set(c.reflectValue(rv.Index(i)))
	}

	return out
}

// reflectMap copies a map, recording the copy before filling it so a map
// reaching itself terminates. Keys are copied too, since a map keyed by an array
// can hold a pointer.
func (c *cloner) reflectMap(rv reflect.Value) reflect.Value {
	if rv.IsNil() {
		return rv
	}

	key, keyed := containerKey(rv, rv.Len())
	if keyed {
		if seen, hit := c.values[key]; hit {
			return reflect.ValueOf(seen)
		}
	}

	out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
	if keyed {
		c.values[key] = out.Interface()
	}

	for iter := rv.MapRange(); iter.Next(); {
		out.SetMapIndex(c.reflectValue(iter.Key()), c.reflectValue(iter.Value()))
	}

	return out
}
