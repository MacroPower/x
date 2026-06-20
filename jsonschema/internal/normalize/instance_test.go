package normalize_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

func TestAccepted(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   any
		want bool
	}{
		"nil":                    {in: nil, want: true},
		"bool":                   {in: true, want: true},
		"string":                 {in: "x", want: true},
		"float64":                {in: 1.5, want: true},
		"json.Number":            {in: json.Number("5"), want: true},
		"accepted slice":         {in: []any{1.0, "x", nil}, want: true},
		"accepted map":           {in: map[string]any{"a": 1.0}, want: true},
		"slice with struct leaf": {in: []any{struct{}{}}, want: false},
		"map with struct leaf":   {in: map[string]any{"a": struct{}{}}, want: false},
		"bare struct":            {in: struct{}{}, want: false},
		"raw int":                {in: 5, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalize.Accepted(tc.in))
		})
	}
}

func TestTypeName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   any
		want string
	}{
		"null":                {in: nil, want: "null"},
		"boolean":             {in: true, want: "boolean"},
		"string":              {in: "x", want: "string"},
		"integer json.Number": {in: json.Number("5"), want: "integer"},
		"number json.Number":  {in: json.Number("5.5"), want: "number"},
		"integer float":       {in: 5.0, want: "integer"},
		"number float":        {in: 5.5, want: "number"},
		"object":              {in: map[string]any{}, want: "object"},
		"array":               {in: []any{}, want: "array"},
		"unknown":             {in: struct{}{}, want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalize.TypeName(tc.in))
		})
	}
}

func TestMatchesType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   any
		typ  string
		want bool
	}{
		"null match":             {in: nil, typ: "null", want: true},
		"boolean match":          {in: true, typ: "boolean", want: true},
		"string match":           {in: "x", typ: "string", want: true},
		"integer match":          {in: json.Number("5"), typ: "integer", want: true},
		"number match":           {in: 5.5, typ: "number", want: true},
		"number via json.Number": {in: json.Number("5"), typ: "number", want: true},
		"object match":           {in: map[string]any{}, typ: "object", want: true},
		"array match":            {in: []any{}, typ: "array", want: true},
		"string not integer":     {in: "5", typ: "integer", want: false},
		"float not integer":      {in: 5.5, typ: "integer", want: false},
		"unknown type":           {in: "x", typ: "bogus", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalize.MatchesType(tc.in, tc.typ))
		})
	}
}
