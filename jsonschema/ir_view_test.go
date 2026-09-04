package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// viewMutator is a tag interpreter that writes through every pointer it is
// handed except the canvas: the sibling's base through Parent, and its own
// base. Each is a private copy, so none of the writes reaches the output.
type viewMutator struct{}

func (viewMutator) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	field.Base.Description = "through base"
	field.Parent.Description = "through parent"

	for _, sibling := range field.Parent.Properties {
		sibling.Description = "through a sibling"
	}

	field.Canvas.Title = "through the canvas"

	return nil
}

// TestHookPointersArePrivateCopies pins that a hook declares through its
// canvas and its return values alone. A write through Parent or Base lands on
// a copy the generator never reads back, apart from Parent.Required.
func TestHookPointersArePrivateCopies(t *testing.T) {
	t.Parallel()

	type doc struct {
		A string `json:"a" mut:"x"`
		B string `json:"b"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("mut", viewMutator{}))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"a":{"type":"string","title":"through the canvas"},
			"b":{"type":"string"}
		},
		"required":["a","b"],
		"additionalProperties":false
	}`, string(got))
}

// TestHookParentRequiredIsReadBack pins the one write the generator reads
// back from the parent view: a name an interpreter appends to Required.
func TestHookParentRequiredIsReadBack(t *testing.T) {
	t.Parallel()

	type doc struct {
		A string `json:"a,omitempty" req:"x"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("req", jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
				field.Parent.Required = append(field.Parent.Required, field.Name)

				return nil
			},
		)))
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, s.Required)
}

// slotChild is the referenced type an extender grafts a branch onto.
type slotChild struct {
	X int `json:"x"`
}

// slotParent holds one reference the extender edits in place and one it
// leaves alone.
type slotParent struct {
	Grafted *slotChild `json:"grafted" jsonschema:"description=grafted"`
	Plain   *slotChild `json:"plain"`
}

// TestExtenderSlotAdditionsLandOnTheChild pins the node-backed-with-additions
// outcome: an extender that appends an allOf branch to a child reference's
// slot, and touches nothing the slot already carried, has the branch emitted
// on that reference beneath the null wrapper and beside the tag description,
// exactly as if it had written on the child's own base.
func TestExtenderSlotAdditionsLandOnTheChild(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[slotParent](t.Context(),
		jsonschema.WithTypeSchemaExtenderFor[slotParent](
			func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
				grafted := ts.Value.Properties["grafted"]
				grafted.AllOf = append(grafted.AllOf, &jsonschema.Schema{MinProperties: new(1)})

				return nil
			},
		))
	require.NoError(t, err)

	got, err := json.Marshal(s.Properties["grafted"])
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"description":"grafted",
		"anyOf":[
			{"$ref":"#/$defs/slotChild","allOf":[{"minProperties":1}]},
			{"type":"null"}
		]
	}`, string(got))

	plain, err := json.Marshal(s.Properties["plain"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"anyOf":[{"$ref":"#/$defs/slotChild"},{"type":"null"}]}`, string(plain))
}

// TestExtenderReplacedSlotIsEmittedAsWritten pins the literal outcome: a slot
// whose pristine fields the extender changed is the extender's own schema, and
// the field node behind it (its tag description included) no longer renders.
func TestExtenderReplacedSlotIsEmittedAsWritten(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[slotParent](t.Context(),
		jsonschema.WithTypeSchemaExtenderFor[slotParent](
			func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
				ts.Value.Properties["grafted"].Ref = ""
				ts.Value.Properties["grafted"].Type = "integer"

				return nil
			},
		))
	require.NoError(t, err)

	got, err := json.Marshal(s.Properties["grafted"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"integer"}`, string(got))
}
