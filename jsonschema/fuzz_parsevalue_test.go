package jsonschema_test

import (
	"encoding/json/v2"
	"testing"
)

// FuzzParseSchemaValueExactRepair searches for a document whose float64-leaf
// variant parses differently through ParseSchemaValue's direct exact copy
// than through the marshal round trip it replaced. Plain v2 decoding gives
// the variant float64 leaves, the shape whose repair depends on the exact
// copy rendering the same shortest-decimal literal the encoder writes.
func FuzzParseSchemaValueExactRepair(f *testing.F) {
	f.Add([]byte(`true`))
	f.Add([]byte(`{"const":9007199254740993}`))
	f.Add([]byte(`{"enum":[0.1,1,"s",null]}`))
	f.Add([]byte(`{"examples":[{"const":0.1}]}`))
	f.Add([]byte(`{"myext":{"const":9007199254740993},"$ref":"#/myext"}`))
	f.Add([]byte(`{"multipleOf":1e-320}`))
	f.Add([]byte(`{"properties":{"a":{"enum":[1e21,1e-9,-0.0]}}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var doc any

		err := json.Unmarshal(data, &doc)
		if err != nil {
			t.Skip("not a JSON document")
		}

		switch doc.(type) {
		case map[string]any, bool:
		default:
			t.Skip("not a schema document shape")
		}

		assertParseValueMatchesRemarshal(t, doc)
	})
}
