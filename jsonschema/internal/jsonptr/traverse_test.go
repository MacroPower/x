package jsonptr_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

func TestTraverseSchema(t *testing.T) {
	t.Parallel()

	// Each leaf is a distinct pointer so a match can be asserted by identity.
	var (
		defLeaf       = &jsonschema.Schema{Type: "string"}
		definitidLeaf = &jsonschema.Schema{Type: "string"}
		propLeaf      = &jsonschema.Schema{Type: "integer"}
		patternLeaf   = &jsonschema.Schema{Type: "number"}
		depSchemaLeaf = &jsonschema.Schema{Type: "boolean"}
		itemsLeaf     = &jsonschema.Schema{Type: "string"}
		itemsArr0     = &jsonschema.Schema{Type: "integer"}
		itemsArr1     = &jsonschema.Schema{Type: "boolean"}
		addPropsLeaf  = &jsonschema.Schema{Type: "string"}
		addItemsLeaf  = &jsonschema.Schema{Type: "string"}
		notLeaf       = &jsonschema.Schema{Type: "null"}
		ifLeaf        = &jsonschema.Schema{Title: "if"}
		thenLeaf      = &jsonschema.Schema{Title: "then"}
		elseLeaf      = &jsonschema.Schema{Title: "else"}
		containsLeaf  = &jsonschema.Schema{Type: "integer"}
		propNamesLeaf = &jsonschema.Schema{Pattern: "^x"}
		unevalPLeaf   = &jsonschema.Schema{Title: "unevaluatedProperties"}
		unevalILeaf   = &jsonschema.Schema{Title: "unevaluatedItems"}
		contentLeaf   = &jsonschema.Schema{Type: "object"}
		allOf0        = &jsonschema.Schema{Title: "allOf0"}
		anyOf1        = &jsonschema.Schema{Title: "anyOf1"}
		oneOf0        = &jsonschema.Schema{Title: "oneOf0"}
		prefix1       = &jsonschema.Schema{Title: "prefix1"}
		nestedChild   = &jsonschema.Schema{Type: "string"}
	)

	root := &jsonschema.Schema{
		Defs:        map[string]*jsonschema.Schema{"Foo": defLeaf},
		Definitions: map[string]*jsonschema.Schema{"Bar": definitidLeaf},
		Properties: map[string]*jsonschema.Schema{
			"p":      propLeaf,
			"nested": {Properties: map[string]*jsonschema.Schema{"deep": nestedChild}},
		},
		PatternProperties:     map[string]*jsonschema.Schema{"^pat": patternLeaf},
		DependentSchemas:      map[string]*jsonschema.Schema{"d": depSchemaLeaf},
		Items:                 itemsLeaf,
		AdditionalProperties:  addPropsLeaf,
		AdditionalItems:       addItemsLeaf,
		Not:                   notLeaf,
		If:                    ifLeaf,
		Then:                  thenLeaf,
		Else:                  elseLeaf,
		Contains:              containsLeaf,
		PropertyNames:         propNamesLeaf,
		UnevaluatedProperties: unevalPLeaf,
		UnevaluatedItems:      unevalILeaf,
		ContentSchema:         contentLeaf,
		AllOf:                 []*jsonschema.Schema{allOf0},
		AnyOf:                 []*jsonschema.Schema{{Title: "anyOf0"}, anyOf1},
		OneOf:                 []*jsonschema.Schema{oneOf0},
		PrefixItems:           []*jsonschema.Schema{{Title: "prefix0"}, prefix1},
	}

	// A schema whose items keyword takes the Draft-07 array form. Items and
	// ItemsArray are mutually exclusive, so this lives in its own root.
	arrayItemsRoot := &jsonschema.Schema{
		ItemsArray: []*jsonschema.Schema{itemsArr0, itemsArr1},
	}

	// Draft-07 dependencies keyword reads from DependencySchemas.
	dependenciesRoot := &jsonschema.Schema{
		DependencySchemas: map[string]*jsonschema.Schema{"d": depSchemaLeaf},
	}

	tests := map[string]struct {
		in   *jsonschema.Schema
		segs []string
		want *jsonschema.Schema
	}{
		"empty segments returns the schema itself": {in: root, segs: nil, want: root},
		"nil schema returns nil":                   {in: nil, segs: []string{"properties", "p"}, want: nil},

		"$defs member":             {in: root, segs: []string{"$defs", "Foo"}, want: defLeaf},
		"definitions member":       {in: root, segs: []string{"definitions", "Bar"}, want: definitidLeaf},
		"properties member":        {in: root, segs: []string{"properties", "p"}, want: propLeaf},
		"patternProperties member": {in: root, segs: []string{"patternProperties", "^pat"}, want: patternLeaf},
		"dependentSchemas member":  {in: root, segs: []string{"dependentSchemas", "d"}, want: depSchemaLeaf},
		"dependencies member (draft-07)": {
			in:   dependenciesRoot,
			segs: []string{"dependencies", "d"},
			want: depSchemaLeaf,
		},

		"items single schema":   {in: root, segs: []string{"items"}, want: itemsLeaf},
		"items array index":     {in: arrayItemsRoot, segs: []string{"items", "1"}, want: itemsArr1},
		"additionalProperties":  {in: root, segs: []string{"additionalProperties"}, want: addPropsLeaf},
		"additionalItems":       {in: root, segs: []string{"additionalItems"}, want: addItemsLeaf},
		"not":                   {in: root, segs: []string{"not"}, want: notLeaf},
		"if":                    {in: root, segs: []string{"if"}, want: ifLeaf},
		"then":                  {in: root, segs: []string{"then"}, want: thenLeaf},
		"else":                  {in: root, segs: []string{"else"}, want: elseLeaf},
		"contains":              {in: root, segs: []string{"contains"}, want: containsLeaf},
		"propertyNames":         {in: root, segs: []string{"propertyNames"}, want: propNamesLeaf},
		"unevaluatedProperties": {in: root, segs: []string{"unevaluatedProperties"}, want: unevalPLeaf},
		"unevaluatedItems":      {in: root, segs: []string{"unevaluatedItems"}, want: unevalILeaf},
		"contentSchema":         {in: root, segs: []string{"contentSchema"}, want: contentLeaf},

		"allOf index":       {in: root, segs: []string{"allOf", "0"}, want: allOf0},
		"anyOf index":       {in: root, segs: []string{"anyOf", "1"}, want: anyOf1},
		"oneOf index":       {in: root, segs: []string{"oneOf", "0"}, want: oneOf0},
		"prefixItems index": {in: root, segs: []string{"prefixItems", "1"}, want: prefix1},

		"nested recursion through properties": {
			in:   root,
			segs: []string{"properties", "nested", "properties", "deep"},
			want: nestedChild,
		},

		"unknown keyword returns nil":             {in: root, segs: []string{"bogus"}, want: nil},
		"missing map key returns nil":             {in: root, segs: []string{"$defs", "Missing"}, want: nil},
		"out-of-range slice index returns nil":    {in: root, segs: []string{"allOf", "9"}, want: nil},
		"non-canonical index returns nil":         {in: root, segs: []string{"prefixItems", "01"}, want: nil},
		"map keyword without member returns nil":  {in: root, segs: []string{"$defs"}, want: nil},
		"slice keyword without index returns nil": {in: root, segs: []string{"allOf"}, want: nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := jsonptr.TraverseSchema(tt.in, tt.segs)

			assert.Same(t, tt.want, got)
		})
	}
}
