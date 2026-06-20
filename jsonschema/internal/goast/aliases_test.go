package goast_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/goast"
)

const aliasSrc = `
package p

type Renamed Base

type Base struct {
	// FieldDoc is the doc.
	Field int
}

type Direct struct {
	// DirectDoc here.
	DF int
}

type CycleA CycleB
type CycleB CycleA

type NotStruct int

type Box[T any] struct {
	// BoxFieldDoc is the doc.
	BoxField int
}

type Pair[T, U any] struct {
	// PairFieldDoc is the doc.
	PF int
}

type GenericAlias Box[int]
type GenericListAlias Pair[int, string]
`

func TestStructFieldDocThroughAliases(t *testing.T) {
	t.Parallel()

	files := parseFiles(t, aliasSrc)

	tests := map[string]struct {
		typeName  string
		fieldName string
		wantDoc   string
		wantOK    bool
	}{
		"direct struct": {typeName: "Direct", fieldName: "DF", wantDoc: "DirectDoc here.", wantOK: true},
		"follows alias chain": {
			typeName:  "Renamed",
			fieldName: "Field",
			wantDoc:   "FieldDoc is the doc.",
			wantOK:    true,
		},
		"generic name stripped": {typeName: "Direct[int]", fieldName: "DF", wantDoc: "DirectDoc here.", wantOK: true},
		"follows generic instantiation alias": {
			typeName:  "GenericAlias",
			fieldName: "BoxField",
			wantDoc:   "BoxFieldDoc is the doc.",
			wantOK:    true,
		},
		"follows generic list instantiation alias": {
			typeName:  "GenericListAlias",
			fieldName: "PF",
			wantDoc:   "PairFieldDoc is the doc.",
			wantOK:    true,
		},
		"cyclic alias guarded":  {typeName: "CycleA", fieldName: "X", wantOK: false},
		"non-struct underlying": {typeName: "NotStruct", fieldName: "X", wantOK: false},
		"type not found":        {typeName: "Missing", fieldName: "X", wantOK: false},
		"field absent":          {typeName: "Direct", fieldName: "Nope", wantOK: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, ok := goast.StructFieldDocThroughAliases(files, tc.typeName, tc.fieldName)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantDoc, doc)
		})
	}
}
