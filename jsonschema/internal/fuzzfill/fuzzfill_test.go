package fuzzfill_test

import (
	"bytes"
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// containers is the fill target: one slice, one map, one byte slice, and a
// trailing scalar that moves whenever a draw ahead of it changes. Its fields
// sit one level below the struct, well inside the recursion cap, so these tests
// read the nil-container draw rather than the zeroing atDepthLimit does at the
// cap.
type containers struct {
	Words []string       `json:"words"`
	Table map[string]int `json:"table"`
	Blob  []byte         `json:"blob"`
	Tail  int            `json:"tail"`
}

// blobs is the entropy population these tests draw from. It spans an exhausted
// cursor, an all-zero draw whose every bit reads false, a saturated draw whose
// every bit reads true, and three patterns between them.
func blobs() map[string][]byte {
	return map[string][]byte{
		"exhausted": {},
		"zeros":     make([]byte, 64),
		"ones":      bytes.Repeat([]byte{0x01}, 96),
		"sevens":    bytes.Repeat([]byte{0x07}, 128),
		"saturated": bytes.Repeat([]byte{0xff}, 80),
		"ramp":      rampBlob(),
	}
}

// rampBlob is long enough to fill every field of containers from real entropy
// rather than from the cursor's zero extension.
func rampBlob() []byte {
	blob := make([]byte, 256)
	for i := range blob {
		blob[i] = byte(i*7 + 3)
	}

	return blob
}

func fillContainers(t *testing.T, data []byte, opts ...fuzzfill.Option) containers {
	t.Helper()

	var val containers

	fuzzfill.Fill(reflect.ValueOf(&val), data, opts...)

	return val
}

// TestFillAllocatesEveryContainer pins the default draw. A slice, a map, and a
// byte slice all arrive allocated, empty at worst, whatever the blob says, so a
// rig that wants the null a nil container marshals to has to ask for it.
func TestFillAllocatesEveryContainer(t *testing.T) {
	t.Parallel()

	for name, blob := range blobs() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			val := fillContainers(t, blob)

			assert.NotNil(t, val.Words, "the slice must be allocated")
			assert.NotNil(t, val.Table, "the map must be allocated")
			assert.NotNil(t, val.Blob, "the byte slice must be allocated")
		})
	}
}

// TestFillWithNilContainersDrawsBothSides pins that the option reaches every
// container kind and draws each of them both ways over blobs(). An option that
// only ever drew one side would leave a rig comparing nothing new.
func TestFillWithNilContainersDrawsBothSides(t *testing.T) {
	t.Parallel()

	for name, isNil := range map[string]func(containers) bool{
		"slice":      func(val containers) bool { return val.Words == nil },
		"map":        func(val containers) bool { return val.Table == nil },
		"byte slice": func(val containers) bool { return val.Blob == nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var drewNil, drewSet int

			for _, blob := range blobs() {
				if isNil(fillContainers(t, blob, fuzzfill.WithNilContainers())) {
					drewNil++
				} else {
					drewSet++
				}
			}

			assert.Positive(t, drewNil, "the option never drew a nil %s", name)
			assert.Positive(t, drewSet, "the option always drew a nil %s", name)
		})
	}
}

// TestFillWithFullAllocatesEverything pins that the option leaves no pointer
// nil and no slice or map empty at any depth short of the cap, over every
// blob, the saturated one included. A saturated blob draws a zero length for
// every collection on the default path, which is the blind spot the option
// exists to close.
func TestFillWithFullAllocatesEverything(t *testing.T) {
	t.Parallel()

	type deep struct {
		Next  *deep
		Words []string
		Table map[string]int
	}

	for name, blob := range blobs() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var val deep

			fuzzfill.Fill(reflect.ValueOf(&val), blob, fuzzfill.WithFull())

			// The chain ends at the depth cap, where the filler zeroes every
			// container, so the last link is the one level the option does
			// not reach.
			depth := 0
			for cur := &val; cur.Next != nil; cur = cur.Next {
				assert.NotEmpty(t, cur.Words, "the slice at depth %d must be non-empty", depth)
				assert.NotEmpty(t, cur.Table, "the map at depth %d must be non-empty", depth)

				depth++
			}

			assert.Greater(t, depth, 1, "the pointer chain must be allocated past the root")
		})
	}
}

// TestFillDefaultDrawIsGolden pins what the default path fills from a fixed
// blob. Fill reads the nil-container bit only under WithNilContainers, which
// keeps every committed fuzz corpus entry decoding to one stable value; this
// assertion is where a draw that read that bit unconditionally would show up.
func TestFillDefaultDrawIsGolden(t *testing.T) {
	t.Parallel()

	// Compared as text rather than as JSON, since a decode would round every
	// integer here through a float64 and stop pinning its low-order digits.
	want := `{"words":[],"table":{"":-4121404043104227081," @\\|,":6223240672740737159},` +
		`"blob":"Zw==","tail":7959404820854577311}`

	// Deterministic map ordering: encoding/json/v2 randomizes member order by
	// default, and this assertion pins exact bytes.
	doc, err := json.Marshal(fillContainers(t, rampBlob()), json.Deterministic(true))
	require.NoError(t, err)
	assert.Equal(t, want, string(doc))
}
