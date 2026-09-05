package jsonvalue

import (
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// Equal reports JSON-semantic equality: null, booleans, and strings by value,
// arrays element by element, objects member by member regardless of order,
// and numbers by the value they denote. Two numbers are equal exactly when
// their canonical decompositions match, which costs nothing past the parse;
// two over-cap literals additionally compare their exact exponents at cost
// linear in the literals, so an adversarial literal is never expanded. A
// number with no numeric value equals only another unparseable literal with
// the same text; a non-finite Go float equals nothing, itself included. An
// Invalid value equals nothing.
func (v Value) Equal(o Value) bool {
	if v.kind != o.kind {
		return false
	}

	switch v.kind {
	case Null:
		return true
	case Bool:
		return v.b == o.b
	case String:
		return v.str == o.str
	case Number:
		return numbersEqual(v, o)

	case Array:
		if len(v.arr) != len(o.arr) {
			return false
		}

		for i := range v.arr {
			if !v.arr[i].Equal(o.arr[i]) {
				return false
			}
		}

		return true

	case Object:
		if len(v.obj) != len(o.obj) {
			return false
		}

		for k, m := range v.obj {
			other, ok := o.obj[k]
			if !ok || !m.Equal(other) {
				return false
			}
		}

		return true

	case Invalid:
		return false
	}

	return false
}

// numbersEqual is the Number case of [Value.Equal].
func numbersEqual(a, b Value) bool {
	// A non-finite float has no value to equal, not even its own.
	if (a.float && a.num == numNone) || (b.float && b.num == numNone) {
		return false
	}

	if a.num == numNone || b.num == numNone {
		// Textual identity for literals no decimal grammar parses, the way
		// a kind-level comparison treats them.
		return a.num == numNone && b.num == numNone && a.str == b.str
	}

	if *a.dec != *b.dec {
		return false
	}

	// Equal decompositions share the exactly-comparable verdict, which is
	// derived from the decomposition alone. Within the cap the canonical
	// form is unique, so equal structs denote equal values.
	if a.num == numExact {
		return true
	}

	// Both magnitudes exceed the clamp, so equal structs only prove the
	// clamped exponents match. Two distinct huge numbers (1e1073741824 and
	// 1e2147483648) share a clamped decomposition, so confirm their exact,
	// unclamped exponents agree; the comparison is linear in the literals.
	return numrat.DecCanonicalExpEqual(a.str, b.str)
}

// Hash returns a content hash consistent with [Value.Equal]: equal values
// hash equally. A number hashes its canonical decomposition, so every
// spelling of one value (1, 1.0, 1e0, and a Go float64 1) shares a bucket
// without expanding the literal.
func (v Value) Hash() uint64 {
	switch v.kind {
	case Null:
		return 0
	case Bool:
		if v.b {
			return 1
		}

		return 2

	case String:
		return stringHash(v.str)

	case Number:
		if v.num == numNone {
			return stringHash(v.str) + 5
		}

		h := stringHash(v.dec.Sig())*31 + numHash(int64(v.dec.Exp()))
		if v.dec.Neg() {
			h = h*31 + 1
		}

		return h + 8

	case Array:
		h := uint64(6)
		for _, e := range v.arr {
			h = h*31 + e.Hash()
		}

		return h

	case Object:
		h := uint64(7)
		for k, m := range v.obj {
			// Fold each key with its own value before summing, so permuting
			// which key holds which value changes the per-entry term. The sum
			// keeps the result independent of map iteration order, which is
			// required: equal objects must hash equally.
			x := stringHash(k)*1099511628211 ^ m.Hash()
			x ^= x >> 33
			x *= 0xff51afd7ed558ccd
			x ^= x >> 33
			h += x
		}

		return h

	case Invalid:
		return 9
	}

	return 9
}

// pairwiseLimit is the largest array HasDuplicates compares pairwise. Up to
// it the quadratic scan allocates nothing and Equal stops at the first
// difference, where a hash walks every element in full; at eight the two
// scans cost the same on two-member objects, and strings cross over later.
const pairwiseLimit = 8

// HasDuplicates reports whether two elements of arr are [Value.Equal]. An
// array of at most pairwiseLimit elements compares each pair directly; a
// longer one hashes every element with [Value.Hash] and compares only
// elements sharing a hash, so the scan stays near linear in the element
// count.
func HasDuplicates(arr []Value) bool {
	if len(arr) <= pairwiseLimit {
		for i := range arr {
			if slices.ContainsFunc(arr[i+1:], arr[i].Equal) {
				return true
			}
		}

		return false
	}

	// The last map holds the index of the latest element with each hash and
	// prev links that element back to the one before it with the same hash,
	// so a collision walks the chain without a per-hash slice.
	last := make(map[uint64]int, len(arr))
	prev := make([]int, len(arr))

	for i, item := range arr {
		h := item.Hash()

		j, ok := last[h]
		if !ok {
			j = -1
		}

		last[h] = i
		prev[i] = j

		for ; j >= 0; j = prev[j] {
			if arr[j].Equal(item) {
				return true
			}
		}
	}

	return false
}

// stringHash folds a string into a 64-bit hash.
func stringHash(s string) uint64 {
	var h uint64

	for i := range len(s) {
		h = h*31 + uint64(s[i])
	}

	return h
}

// numHash folds an integer into a 64-bit hash.
//
//nolint:gosec // Overflow is intentional for hash distribution.
func numHash(n int64) uint64 {
	return uint64(n)*2654435761 + 3
}
