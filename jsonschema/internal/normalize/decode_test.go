package normalize_test

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

func TestDecodeJSONInstance(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  any
		err   string
	}{
		"number uses json.Number": {
			input: `5`,
			want:  json.Number("5"),
		},
		"negative zero keeps its sign": {
			input: `-0`,
			want:  json.Number("-0"),
		},
		"out of range exponent keeps its text": {
			input: `1e400`,
			want:  json.Number("1e400"),
		},
		"integer beyond 2^53 keeps its digits": {
			input: `9007199254740993`,
			want:  json.Number("9007199254740993"),
		},
		"literals": {
			input: `[null, true, false, "aé\n"]`,
			want:  []any{nil, true, false, "aé\n"},
		},
		"empty containers are non-nil": {
			input: `{"o": {}, "a": []}`,
			want:  map[string]any{"o": map[string]any{}, "a": []any{}},
		},
		"nested containers": {
			input: `{"a": [1, {"b": [2.5, "x"]}], "c": null}`,
			want: map[string]any{
				"a": []any{json.Number("1"), map[string]any{"b": []any{json.Number("2.5"), "x"}}},
				"c": nil,
			},
		},
		"surrounding whitespace is accepted": {
			input: " \n\"x\"  \n",
			want:  "x",
		},
		"trailing value is rejected": {
			input: `true false`,
			err:   "invalid character 'f' after top-level value",
		},
		"trailing object is rejected": {
			input: `{"type":"object"} {}`,
			err:   "after top-level value",
		},
		"trailing garbage is rejected": {
			input: `{"a":1} x`,
			err:   "invalid character 'x'",
		},
		"truncated object is rejected": {
			input: `{`,
			err:   "JSON decode:",
		},
		"truncated string is rejected": {
			input: `"abc`,
			err:   "JSON decode:",
		},
		"empty input is rejected": {
			input: ``,
			err:   "unexpected EOF",
		},
		"whitespace only is rejected": {
			input: "   ",
			err:   "unexpected EOF after offset 3",
		},
		"duplicate member names are rejected": {
			input: `{"a":1,"a":2}`,
			err:   `duplicate object member name "a"`,
		},
		"invalid UTF-8 in a string is rejected": {
			input: "\"\xff\"",
			err:   "invalid UTF-8",
		},
		"invalid UTF-8 in a member name is rejected": {
			input: "{\"\xff\":1}",
			err:   "invalid UTF-8",
		},
		"control character in a string is rejected": {
			input: "\"a\x01b\"",
			err:   "JSON decode:",
		},
		"leading zero is rejected": {
			input: `01`,
			err:   "JSON decode:",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := normalize.DecodeJSONInstance([]byte(tt.input))
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeJSONInstanceSyntacticError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input      string
		wantOffset int64
	}{
		"trailing value": {
			input:      `1 2`,
			wantOffset: 2,
		},
		"empty input": {
			input:      ``,
			wantOffset: 0,
		},
		"duplicate name": {
			input:      `{"a":1,"a":2}`,
			wantOffset: 7,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := normalize.DecodeJSONInstance([]byte(tt.input))

			var synErr *jsontext.SyntacticError

			require.ErrorAs(t, err, &synErr)
			assert.Equal(t, tt.wantOffset, synErr.ByteOffset)
		})
	}
}

func TestDecodeJSONInstanceDepth(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		depth int
		err   string
	}{
		"below the cap":  {depth: 9999},
		"at the cap":     {depth: 10000},
		"above the cap":  {depth: 10001, err: "exceeded max depth"},
		"far beyond cap": {depth: 100000, err: "exceeded max depth"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			input := strings.Repeat("[", tt.depth) + strings.Repeat("]", tt.depth)

			got, err := normalize.DecodeJSONInstance([]byte(input))
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)

				return
			}

			require.NoError(t, err)

			nested := got
			for range tt.depth - 1 {
				elems, ok := nested.([]any)
				require.True(t, ok)
				require.Len(t, elems, 1)

				nested = elems[0]
			}

			assert.Equal(t, []any{}, nested)
		})
	}
}

var errRead = errors.New("boom")

// failingReader serves its data and then returns errRead instead of EOF.
type failingReader struct {
	data string
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errRead
	}

	r.done = true

	return copy(p, r.data), nil
}

// TestDecodeJSONInstanceReusesDecoder pins the pooled decoder: a document
// decoded after an input that ended mid-value comes back whole, and a result
// keeps its strings and numbers after the caller overwrites the bytes it
// decoded and the decoder has read another document over the same buffer.
func TestDecodeJSONInstanceReusesDecoder(t *testing.T) {
	t.Parallel()

	_, err := normalize.DecodeJSONInstance([]byte(`{"a": [1, `))
	require.Error(t, err)

	first := []byte(`{"key": "value", "n": 12345}`)

	got, err := normalize.DecodeJSONInstance(first)
	require.NoError(t, err)

	for i := range first {
		first[i] = 'x'
	}

	_, err = normalize.DecodeJSONInstance([]byte(`["other", 99999]`))
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"key": "value", "n": json.Number("12345")}, got)
}

func TestDecodeJSONInstanceReader(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		reader io.Reader
		want   any
		err    string
		errIs  error
	}{
		"decodes like the byte form": {
			reader: strings.NewReader(`{"a": [1, "x"], "b": -0}`),
			want:   map[string]any{"a": []any{json.Number("1"), "x"}, "b": json.Number("-0")},
		},
		"trailing data is rejected": {
			reader: strings.NewReader(`1 2`),
			err:    "after top-level value",
		},
		"duplicate member names are rejected": {
			reader: strings.NewReader(`{"a":1,"a":2}`),
			err:    "duplicate object member name",
		},
		"read error surfaces": {
			reader: &failingReader{data: `{"a": 1`},
			err:    "JSON decode:",
			errIs:  errRead,
		},
		"read error after the value surfaces": {
			reader: &failingReader{data: `{"a": 1}`},
			err:    "JSON decode:",
			errIs:  errRead,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := normalize.DecodeJSONInstanceReader(tt.reader)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)

				if tt.errIs != nil {
					require.ErrorIs(t, err, tt.errIs)
				}

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// benchmarkDocument builds a JSON document of n objects, each holding scalar
// members, a nested object, and a small array, so a decode benchmark walks
// every token kind.
func benchmarkDocument(n int) []byte {
	var sb strings.Builder

	sb.WriteString(`{"items":[`)

	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}

		fmt.Fprintf(&sb, `{"id":%d,"name":"item-%d","ratio":%d.5,"active":%t,`, i, i, i, i%2 == 0)
		sb.WriteString(`"meta":{"tags":["a","b","c"],"score":1e-3,"parent":null}}`)
	}

	sb.WriteString(`]}`)

	return []byte(sb.String())
}

func BenchmarkDecodeJSONInstance(b *testing.B) {
	data := benchmarkDocument(300)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		_, err := normalize.DecodeJSONInstance(data)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeJSONInstanceReader(b *testing.B) {
	data := string(benchmarkDocument(300))

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		_, err := normalize.DecodeJSONInstanceReader(strings.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
	}
}
