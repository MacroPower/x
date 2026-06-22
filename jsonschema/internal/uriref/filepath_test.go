package uriref_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

func TestFilePathFromURI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri  string
		want string
	}{
		"file scheme no authority":   {uri: "file:///schema.json", want: "schema.json"},
		"file scheme with host":      {uri: "file://host/dir/schema.json", want: "dir/schema.json"},
		"file scheme extra slash":    {uri: "file:////schema.json", want: "schema.json"},
		"file scheme opaque":         {uri: "file:schema.json", want: "schema.json"},
		"file scheme opaque nested":  {uri: "file:sub/schema.json", want: "sub/schema.json"},
		"file scheme opaque encoded": {uri: "file:a%20b.json", want: "a b.json"},
		"file scheme path encoded":   {uri: "file:///a%20b.json", want: "a b.json"},
		"relative path":              {uri: "schema.json", want: "schema.json"},
		"nested relative path":       {uri: "sub/schema.json", want: "sub/schema.json"},
		"leading slash fallback":     {uri: "/abs/schema.json", want: "abs/schema.json"},
		"relative path with query":   {uri: "a/b.json?v=1", want: "a/b.json"},
		"file scheme with query":     {uri: "file:///a/b.json?v=1", want: "a/b.json"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, uriref.FilePathFromURI(tc.uri))
		})
	}
}
