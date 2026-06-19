package numkind_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
)

func TestIsInteger(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		kind reflect.Kind
		want bool
	}{
		"int":     {kind: reflect.Int, want: true},
		"int8":    {kind: reflect.Int8, want: true},
		"int16":   {kind: reflect.Int16, want: true},
		"int32":   {kind: reflect.Int32, want: true},
		"int64":   {kind: reflect.Int64, want: true},
		"uint":    {kind: reflect.Uint, want: true},
		"uint8":   {kind: reflect.Uint8, want: true},
		"uint16":  {kind: reflect.Uint16, want: true},
		"uint32":  {kind: reflect.Uint32, want: true},
		"uint64":  {kind: reflect.Uint64, want: true},
		"uintptr": {kind: reflect.Uintptr, want: true},
		"float32": {kind: reflect.Float32, want: false},
		"float64": {kind: reflect.Float64, want: false},
		"string":  {kind: reflect.String, want: false},
		"bool":    {kind: reflect.Bool, want: false},
		"slice":   {kind: reflect.Slice, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numkind.IsInteger(tc.kind))
		})
	}
}

func TestIsUnsigned(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		kind reflect.Kind
		want bool
	}{
		"uint":    {kind: reflect.Uint, want: true},
		"uint8":   {kind: reflect.Uint8, want: true},
		"uint16":  {kind: reflect.Uint16, want: true},
		"uint32":  {kind: reflect.Uint32, want: true},
		"uint64":  {kind: reflect.Uint64, want: true},
		"uintptr": {kind: reflect.Uintptr, want: true},
		"int":     {kind: reflect.Int, want: false},
		"int64":   {kind: reflect.Int64, want: false},
		"float32": {kind: reflect.Float32, want: false},
		"float64": {kind: reflect.Float64, want: false},
		"string":  {kind: reflect.String, want: false},
		"bool":    {kind: reflect.Bool, want: false},
		"slice":   {kind: reflect.Slice, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numkind.IsUnsigned(tc.kind))
		})
	}
}

func TestIsFloat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		kind reflect.Kind
		want bool
	}{
		"float32": {kind: reflect.Float32, want: true},
		"float64": {kind: reflect.Float64, want: true},
		"int":     {kind: reflect.Int, want: false},
		"int64":   {kind: reflect.Int64, want: false},
		"uint":    {kind: reflect.Uint, want: false},
		"uintptr": {kind: reflect.Uintptr, want: false},
		"string":  {kind: reflect.String, want: false},
		"bool":    {kind: reflect.Bool, want: false},
		"slice":   {kind: reflect.Slice, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numkind.IsFloat(tc.kind))
		})
	}
}

func TestIntBitSize(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 8, numkind.IntBitSize(reflect.Int8))
	assert.Equal(t, 64, numkind.IntBitSize(reflect.Int64))
}

func TestUintBitSize(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 16, numkind.UintBitSize(reflect.Uint16))
	assert.Equal(t, 64, numkind.UintBitSize(reflect.Uint64))
}
