// Package schemaclone deep-copies a [jsonschema.Schema] via a JSON round-trip,
// then restores the render-only PropertyOrder field the round-trip drops. The
// validation walk and the inliner both need an independent copy of a schema
// whose in-place mutation cannot corrupt the caller's value, so the copy logic
// is centralized here as a single source of truth.
package schemaclone

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

var (
	// ErrCyclic reports a cyclic schema pointer graph handed to [Clone]. A cycle
	// cannot round-trip through JSON: the upstream MarshalJSON re-enters
	// [json.Marshal] with a fresh encoder at every nesting level, so
	// encoding/json's per-encoder cycle detection never triggers and the marshal
	// recurses until an unrecoverable stack overflow. Clone detects the cycle up
	// front and returns this ordinary error instead.
	ErrCyclic = errors.New("cyclic schema graph")

	// The schemaType and schemaPtrType sentinels let the reflection walk in
	// [checkAcyclicReflect] recognize a schema nested in a typed container.
	schemaType    = reflect.TypeFor[jsonschema.Schema]()
	schemaPtrType = reflect.TypeFor[*jsonschema.Schema]()
)

// Children returns the direct sub-schemas of a schema in a stable order. [Clone]
// walks src and its copy in lockstep through one such function to pair nodes; the
// jsonschema package supplies its SubschemaEntries traversal in this shape.
type Children func(*jsonschema.Schema) []*jsonschema.Schema

// Clone deep-copies s via a JSON round-trip and returns the copy.
//
// Upstream [jsonschema.Schema.CloneSchemas] is shallow for non-sub-schema fields
// (Extra, Enum, Const, Default, Examples): it shares their backing maps, slices,
// and pointers with the original. A round-trip through JSON instead yields an
// independent copy of every serializable field, which is what remote-ref
// isolation requires so [jsonschema.Schema.Resolve]'s in-place mutations cannot
// corrupt the caller's schema. The render-only PropertyOrder field carries
// json:"-", so the round-trip drops it; it is restored afterward (via children)
// so a clone preserves property ordering. Every other serializable field
// round-trips as an independent copy.
//
// A nil s clones to nil. Without the guard the round-trip would fabricate the
// false (reject-everything) schema: nil marshals as JSON null, which the
// upstream UnmarshalJSON reads as the boolean schema false.
//
// A cyclic schema graph cannot round-trip through JSON, so Clone rejects it
// with an error wrapping [ErrCyclic] before marshaling. The cycle check walks
// the sub-schema graph children supplies plus the any-typed value fields
// (Const, Enum, Examples, Extra), so a *Schema smuggled through an any-typed
// container is detected too, including typed containers ([]*Schema, a map of
// schemas, an exported struct field) that [json.Marshal] reaches by reflection.
func Clone(s *jsonschema.Schema, children Children) (*jsonschema.Schema, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // A nil schema clones to nil.
	}

	err := checkAcyclic(s, children, map[*jsonschema.Schema]visit{})
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	var cp jsonschema.Schema

	err = json.Unmarshal(data, &cp)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	restorePropertyOrder(s, &cp, children)

	return &cp, nil
}

// visit is a node's state in the checkAcyclic depth-first walk: the zero value
// means unseen, visiting marks a node on the current path, and visited marks a
// node whose subtree is fully explored.
type visit uint8

const (
	visiting visit = iota + 1
	visited
)

// checkAcyclic walks the sub-schema graph under s depth-first and returns an
// error wrapping [ErrCyclic] when a schema pointer repeats on the current
// path. Only an on-path repeat is a cycle: a pointer reachable along several
// acyclic paths (a DAG) marshals fine and passes. A fully explored node is
// skipped on re-visit, keeping the walk linear in the number of edges.
func checkAcyclic(s *jsonschema.Schema, children Children, state map[*jsonschema.Schema]visit) error {
	if s == nil {
		return nil
	}

	switch state[s] {
	case visiting:
		return fmt.Errorf("clone schema: %w", ErrCyclic)
	case visited:
		return nil
	}

	state[s] = visiting

	for _, child := range children(s) {
		err := checkAcyclic(child, children, state)
		if err != nil {
			return err
		}
	}

	// A *Schema can also ride in the any-typed value fields; json.Marshal
	// serializes those edges like any sub-schema, so the cycle check must
	// cross them too.
	values := []any{s.Enum, s.Examples, s.Extra}
	if s.Const != nil {
		values = append(values, *s.Const)
	}

	for _, v := range values {
		err := checkAcyclicValue(v, children, state)
		if err != nil {
			return err
		}
	}

	state[s] = visited

	return nil
}

// checkAcyclicValue extends the checkAcyclic walk across a JSON-shaped value
// held in an any-typed field, recursing through containers until it finds
// schema pointers to feed back into the graph walk. The common shapes parsed
// JSON produces (map[string]any, []any) take the concrete fast path; every
// other type falls through to a reflection walk, because [json.Marshal]
// reaches a schema nested in any marshalable typed container ([]*Schema, a
// map of schemas, an exported struct field) just as readily.
func checkAcyclicValue(v any, children Children, state map[*jsonschema.Schema]visit) error {
	switch val := v.(type) {
	case nil:
		return nil
	case *jsonschema.Schema:
		return checkAcyclic(val, children, state)
	case jsonschema.Schema:
		return checkAcyclic(&val, children, state)
	case map[string]any:
		for _, elem := range val {
			err := checkAcyclicValue(elem, children, state)
			if err != nil {
				return err
			}
		}

	case []any:
		for _, elem := range val {
			err := checkAcyclicValue(elem, children, state)
			if err != nil {
				return err
			}
		}

	default:
		return checkAcyclicReflect(reflect.ValueOf(v), children, state)
	}

	return nil
}

// checkAcyclicReflect walks a value of a type the concrete switch in
// [checkAcyclicValue] does not name, mirroring encoding/json's reflection:
// pointers, interfaces, slices, arrays, maps, and exported struct fields can
// all hold a nested schema whose marshal re-enters the schema graph.
// Unexported struct fields are skipped because [json.Marshal] never
// serializes them. Kinds with no interior values (strings, numbers, channels,
// funcs) need no check. A schema encountered as a pointer, interface, or
// struct value is routed back through [checkAcyclicValue], whose concrete
// cases feed it into the graph walk.
func checkAcyclicReflect(rv reflect.Value, children Children, state map[*jsonschema.Schema]visit) error {
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}

		if rv.Type() == schemaPtrType {
			return checkAcyclicValue(rv.Interface(), children, state)
		}

		return checkAcyclicReflect(rv.Elem(), children, state)

	case reflect.Interface:
		if rv.IsNil() {
			return nil
		}

		return checkAcyclicValue(rv.Interface(), children, state)

	case reflect.Struct:
		if rv.Type() == schemaType {
			return checkAcyclicValue(rv.Interface(), children, state)
		}

		for i := range rv.NumField() {
			if !rv.Type().Field(i).IsExported() {
				continue
			}

			err := checkAcyclicReflect(rv.Field(i), children, state)
			if err != nil {
				return err
			}
		}

	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			err := checkAcyclicReflect(rv.Index(i), children, state)
			if err != nil {
				return err
			}
		}

	case reflect.Map:
		for iter := rv.MapRange(); iter.Next(); {
			err := checkAcyclicReflect(iter.Value(), children, state)
			if err != nil {
				return err
			}
		}

	default:
		// Every other kind has no interior values a schema can hide in.
	}

	return nil
}

// restorePropertyOrder copies the render-only PropertyOrder field (json:"-", so
// dropped by [Clone]'s JSON round-trip) from src onto cp at every node, walking
// both in lockstep through children, and re-copies the any-typed value fields
// number-preservingly (see restoreNumberValues). Because cp is a JSON clone of
// src, the two share an identical sub-schema structure and children (which
// orders map-held entries deterministically) yields matching orders. Each slice
// is cloned so cp stays unaliased from src. A JSON clone is always a finite
// tree, so the recursion terminates.
func restorePropertyOrder(src, cp *jsonschema.Schema, children Children) {
	if src == nil || cp == nil {
		return
	}

	if src.PropertyOrder != nil {
		cp.PropertyOrder = slices.Clone(src.PropertyOrder)
	}

	restoreNumberValues(src, cp)

	srcChildren := children(src)
	cpChildren := children(cp)

	if len(srcChildren) != len(cpChildren) {
		return // Structural mismatch; nothing safe to pair.
	}

	for i := range srcChildren {
		restorePropertyOrder(srcChildren[i], cpChildren[i], children)
	}
}

// restoreNumberValues re-copies the any-typed value fields (Const, Enum,
// Examples, Extra) from src onto cp with numbers preserved. The round-trip's
// plain Unmarshal decodes their numbers as float64, silently rounding a
// [json.Number] beyond float64 precision and changing what a cloned const/enum
// accepts; a marshal + UseNumber decode of the source value keeps the literal
// exact while still yielding a copy fully unaliased from src. A value that
// fails the re-copy (an unmarshalable Extra member) keeps the plain
// round-tripped form.
func restoreNumberValues(src, cp *jsonschema.Schema) {
	if src.Const != nil {
		if v, ok := copyJSONValue(*src.Const); ok {
			cp.Const = &v
		}
	}

	if src.Enum != nil {
		if v, ok := copyJSONValue(src.Enum); ok {
			if list, isList := v.([]any); isList {
				cp.Enum = list
			}
		}
	}

	if src.Examples != nil {
		if v, ok := copyJSONValue(src.Examples); ok {
			if list, isList := v.([]any); isList {
				cp.Examples = list
			}
		}
	}

	if src.Extra != nil {
		if v, ok := copyJSONValue(src.Extra); ok {
			if m, isMap := v.(map[string]any); isMap {
				cp.Extra = m
			}
		}
	}
}

// copyJSONValue deep-copies a JSON-shaped value via a marshal + UseNumber
// decode, so numbers come back as exact [json.Number] literals rather than
// rounded float64s. It reports ok=false on any error so the caller can keep
// its fallback value.
func copyJSONValue(v any) (any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var out any

	err = dec.Decode(&out)
	if err != nil {
		return nil, false
	}

	return out, true
}
