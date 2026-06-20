package jsonptr

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// SchemaAtJSONPointer navigates root's JSON encoding by segments and returns
// the located value as a Schema when it is itself a schema (a JSON object or
// boolean), or nil otherwise. The walk starts from base (root's base URI) and
// tracks $id members of the objects it descends through, so the returned base
// is the one in effect at the located schema; the target's own $id is left to
// the caller during registration.
func SchemaAtJSONPointer(root *jsonschema.Schema, segments []string, base string) (*jsonschema.Schema, string) {
	data, err := json.Marshal(root)
	if err != nil {
		return nil, ""
	}

	var node any

	err = json.Unmarshal(data, &node)
	if err != nil {
		return nil, ""
	}

	for i, seg := range segments {
		// Crossing into an intermediate object that establishes a resource
		// ($id) rebases everything below it. The starting root is skipped:
		// its own $id is already reflected in base.
		if i > 0 {
			if obj, ok := node.(map[string]any); ok {
				if id, ok := obj["$id"].(string); ok && id != "" && !uriref.IsFragmentOnly(id) {
					base = uriref.StripFragment(uriref.ResolveURI(base, id))
				}
			}
		}

		switch container := node.(type) {
		case map[string]any:
			next, ok := container[seg]
			if !ok {
				return nil, ""
			}

			node = next

		case []any:
			idx, ok := ParseArrayIndex(seg)
			if !ok || idx >= len(container) {
				return nil, ""
			}

			node = container[idx]

		default:
			return nil, ""
		}
	}

	switch node.(type) {
	case map[string]any, bool:
		target, err := json.Marshal(node)
		if err != nil {
			return nil, ""
		}

		var schema jsonschema.Schema

		err = json.Unmarshal(target, &schema)
		if err != nil {
			return nil, ""
		}

		return &schema, base

	default:
		return nil, ""
	}
}
