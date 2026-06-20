package annotations_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/annotations"
)

func TestChild(t *testing.T) {
	t.Parallel()

	// A nil parent does not track, so it yields a nil child that discards
	// everything written to it.
	var nilParent *annotations.Set

	assert.Nil(t, nilParent.Child())

	parent := annotations.New()
	child := parent.Child()
	require.NotNil(t, child)
	assert.NotSame(t, parent, child)
}

func TestMergeUnion(t *testing.T) {
	t.Parallel()

	dst := annotations.New()
	dst.RecordProperty("a")
	dst.RecordItem(0)
	dst.ExtendItems(2)

	src := annotations.New()
	src.RecordProperty("b")
	src.RecordItem(5)
	src.ExtendItems(1) // smaller watermark must not lower dst's

	dst.Merge(src)

	assert.True(t, dst.Evaluated("a"))
	assert.True(t, dst.Evaluated("b"))
	assert.True(t, dst.ItemEvaluated(5))
	// The watermark stays at the larger value (2), so index 1 is still covered.
	assert.True(t, dst.ItemEvaluated(1))
	assert.False(t, dst.AllPropertiesSet())
	assert.False(t, dst.AllItemsSet())
}

func TestMergeFlagsOR(t *testing.T) {
	t.Parallel()

	dst := annotations.New()

	src := annotations.New()
	src.SetAllProperties()
	src.SetAllItems()

	dst.Merge(src)

	assert.True(t, dst.AllPropertiesSet())
	assert.True(t, dst.AllItemsSet())
}

func TestMergeNilIsNoop(t *testing.T) {
	t.Parallel()

	dst := annotations.New()
	dst.RecordProperty("a")

	dst.Merge(nil) // nil other contributes nothing
	assert.True(t, dst.Evaluated("a"))

	var nilDst *annotations.Set

	assert.NotPanics(t, func() { nilDst.Merge(dst) }) // nil receiver is a no-op
}

func TestExtendItemsWatermark(t *testing.T) {
	t.Parallel()

	s := annotations.New()
	assert.False(t, s.ItemEvaluated(3))

	s.ExtendItems(4)
	assert.True(t, s.ItemEvaluated(3))
	assert.False(t, s.ItemEvaluated(4))

	// A smaller end never lowers the watermark.
	s.ExtendItems(2)
	assert.True(t, s.ItemEvaluated(3))
}

func TestNilSetReadsEmptyIgnoresWrites(t *testing.T) {
	t.Parallel()

	var s *annotations.Set

	assert.False(t, s.Evaluated("x"))
	assert.False(t, s.ItemEvaluated(0))
	assert.False(t, s.AllPropertiesSet())
	assert.False(t, s.AllItemsSet())

	// Writes on a nil Set are ignored rather than panicking.
	assert.NotPanics(t, func() {
		s.RecordProperty("x")
		s.RecordItem(0)
		s.SetAllProperties()
		s.SetAllItems()
		s.ExtendItems(3)
	})
}
