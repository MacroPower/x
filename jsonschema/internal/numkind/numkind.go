// Package numkind maps Go integer reflect kinds to the bit width their values
// parse at, so a constant a field cannot hold overflows during parsing. The
// reflection generator and the validate-tag interpreter live in separate
// packages but parse integer tag values against the same widths, so the mapping
// is centralized here to keep a single source of truth.
package numkind

import (
	"reflect"
	"strconv"
)

// IntBitSize returns the bit width to parse a signed-integer field value at, so
// a value the field cannot hold overflows during parsing. Plain int is
// platform-dependent ([strconv.IntSize]); the sized kinds map to their fixed
// widths.
func IntBitSize(k reflect.Kind) int {
	switch k {
	case reflect.Int8:
		return 8
	case reflect.Int16:
		return 16
	case reflect.Int32:
		return 32
	case reflect.Int64:
		return 64
	default: // reflect.Int
		return strconv.IntSize
	}
}

// UintBitSize returns the bit width to parse an unsigned-integer field value at,
// so a value the field cannot hold overflows during parsing. Plain uint and
// uintptr are platform-dependent ([strconv.IntSize]); the sized kinds map to
// their fixed widths.
func UintBitSize(k reflect.Kind) int {
	switch k {
	case reflect.Uint8:
		return 8
	case reflect.Uint16:
		return 16
	case reflect.Uint32:
		return 32
	case reflect.Uint64:
		return 64
	default: // reflect.Uint, reflect.Uintptr
		return strconv.IntSize
	}
}
