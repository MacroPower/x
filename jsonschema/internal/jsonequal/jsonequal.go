// Package jsonequal implements DoS-guarded, JSON-semantic value equality for
// the const and enum keywords and the matching content hash for the
// uniqueItems check. It layers on [go.jacobcolvin.com/x/jsonschema/internal/numrat]
// for exact decimal comparison: upstream's [jsonschema.Equal] expands every
// [json.Number] through an uncapped [big.Rat.SetString], so an adversarial
// multi-megabyte or large-exponent literal costs quadratic time and large
// allocations. When either side carries such a number the comparison routes
// through a guarded local walk that compares numbers via their canonical
// decomposition (exact at any size); otherwise it delegates to the upstream for
// full JSON semantics. Non-finite floats (NaN, ±Inf) are treated as unequal to
// everything, including themselves, matching the numeric-bound keywords.
package jsonequal

import (
	"encoding/json"
	"math"
	"math/big"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// equalSchemaInstance reports JSON-semantic equality between a schema-authored
// value (from const/enum) and a decoded instance value.
//
// The schema side is parsed without UseNumber, so a JSON number there is a
// float64 holding the nearest binary value (schema 0.1 is 0.1000...0555). The
// instance side decodes through UseNumber, so its numbers are [json.Number]
// decimal literals. Expanding the schema float through [big.Rat.SetFloat64]
// would compare its exact binary value, which can never equal the literal 0.1,
// so the schema float is instead expanded through its shortest decimal
// ([numrat.Float64ToRat]) to match how the numeric-bound keywords convert schema
// values. The two sides then compare as exact rationals, recursing through
// arrays and objects.
//
// JSON Schema treats booleans as distinct from numbers, so true never equals 1
// and false never equals 0; the numeric branch only fires when both sides are
// numeric kinds.
func equalSchemaInstance(schemaVal, instance any) bool {
	if sr, ok := numrat.SchemaNumberRat(schemaVal); ok {
		return equalRatInstance(sr, instance)
	}

	switch sv := schemaVal.(type) {
	case nil:
		return instance == nil
	case bool:
		iv, ok := instance.(bool)

		return ok && sv == iv

	case string:
		iv, ok := instance.(string)

		return ok && sv == iv

	case []any:
		iv, ok := instance.([]any)
		if !ok || len(sv) != len(iv) {
			return false
		}

		for i := range sv {
			if !equalSchemaInstance(sv[i], iv[i]) {
				return false
			}
		}

		return true

	case map[string]any:
		iv, ok := instance.(map[string]any)
		if !ok || len(sv) != len(iv) {
			return false
		}

		for k, item := range sv {
			other, exists := iv[k]
			if !exists || !equalSchemaInstance(item, other) {
				return false
			}
		}

		return true
	}

	// Schema values outside the JSON shapes above reach here only from a
	// hand-built const/enum, since the upstream parser yields float64 for a
	// schema number; the common case is a json.Number whose magnitude exceeds
	// the cheap-expansion bounds (an in-bounds one is already handled by
	// numrat.SchemaNumberRat above). Route through equalJSONValues so such a literal
	// is compared canonically rather than through upstream's uncapped
	// big.Rat.SetString, which would cost quadratic time (see numrat.MaxNumberLen).
	return equalJSONValues(schemaVal, instance)
}

// equalRatInstance reports whether a schema value, already expanded to the
// rational sr, equals the numeric instance. It mirrors the numeric branch of
// [equalSchemaInstance]: a non-numeric instance never matches.
func equalRatInstance(sr *big.Rat, instance any) bool {
	ir, ok := numrat.ToBigRat(instance)
	if !ok {
		return false
	}

	return sr.Cmp(ir) == 0
}

// EqualWithRat compares a schema-authored const or enum value to an instance,
// using a rational precomputed for the value's top-level numeric form when one
// is available. A nil schemaRat runs the general [equalSchemaInstance]
// comparison, which for a non-numeric value is identical and for a numeric value
// recomputes the same rational; the cache only removes that repeated work.
func EqualWithRat(schemaVal any, schemaRat *big.Rat, instance any) bool {
	if schemaRat == nil {
		return equalSchemaInstance(schemaVal, instance)
	}

	return equalRatInstance(schemaRat, instance)
}

// equalJSONValues reports JSON-semantic equality like [jsonschema.Equal], with
// a guard for adversarial numbers: the upstream comparison expands every
// [json.Number] through an uncapped [big.Rat.SetString], so a multi-megabyte
// or large-exponent literal costs quadratic time and large allocations (see
// numrat.MaxNumberLen). When either value contains such a number the comparison runs
// through a guarded local walk; otherwise it delegates to [jsonschema.Equal]
// for full upstream semantics.
func equalJSONValues(a, b any) bool {
	// A non-finite float64 (NaN, +Inf, -Inf) is not a representable JSON number,
	// and upstream's big.Rat.SetFloat64 collapses all three (and zero) toward the
	// same value, so jsonschema.Equal would report NaN==NaN, +Inf==-Inf, and
	// NaN==0 as equal. Treat any value containing a non-finite float as unequal
	// to everything, including itself, matching how the numeric-bound keywords
	// already treat such floats as "not a number".
	if containsNonFiniteFloat(a) || containsNonFiniteFloat(b) {
		return false
	}

	if containsUnboundedNumber(a) || containsUnboundedNumber(b) {
		return equalGuarded(a, b)
	}

	return jsonschema.Equal(a, b)
}

// containsNonFiniteFloat reports whether v, or any element of an []any or
// map[string]any it contains, is a non-finite float64 (NaN or ±Inf). Such a
// value can only enter through validation (JSON decoding never yields one); the
// other container kinds cannot hold one.
func containsNonFiniteFloat(v any) bool {
	switch val := v.(type) {
	case float64:
		return math.IsInf(val, 0) || math.IsNaN(val)

	case []any:
		return slices.ContainsFunc(val, containsNonFiniteFloat)

	case map[string]any:
		for _, item := range val {
			if containsNonFiniteFloat(item) {
				return true
			}
		}
	}

	return false
}

// containsUnboundedNumber walks the container shapes a decoded JSON instance
// can take and reports whether any [json.Number] inside is outside the
// cheap-expansion bounds (or not a decimal literal at all). Values of other
// container types cannot hold a [json.Number] produced by JSON decoding, so
// only these shapes need walking.
func containsUnboundedNumber(v any) bool {
	switch val := v.(type) {
	case json.Number:
		d, ok := numrat.ParseDecNumber(string(val))

		return !ok || !d.ExactlyComparable()

	case []any:
		if slices.ContainsFunc(val, containsUnboundedNumber) {
			return true
		}

	case map[string]any:
		for _, item := range val {
			if containsUnboundedNumber(item) {
				return true
			}
		}
	}

	return false
}

// equalGuarded mirrors [jsonschema.Equal] over the JSON instance shapes while
// comparing numbers via their canonical decomposition, which is exact at any
// size without expanding the literal: two decimal literals are equal exactly
// when their decompositions match, and a number outside the cheap-expansion
// bounds can never equal a float64 or integer (those expand to at most ~767
// significant decimal digits within exponent ±324). Container types other
// than the decoded-JSON shapes fall through to [jsonschema.Equal], which is
// safe because they cannot hold a decoded [json.Number].
func equalGuarded(a, b any) bool {
	an, aNum := a.(json.Number)
	bn, bNum := b.(json.Number)

	switch {
	case aNum && bNum:
		da, oka := numrat.ParseDecNumber(string(an))
		db, okb := numrat.ParseDecNumber(string(bn))
		if !oka || !okb {
			// Not decimal literals: textual identity, mirroring upstream's
			// kind-level comparison for numbers big.Rat cannot parse.
			return oka == okb && string(an) == string(bn)
		}

		if da != db {
			return false
		}

		if da.ExactlyComparable() {
			// Within the cheap-expansion bounds the canonical decomposition is
			// exact, so equal structs denote equal values.
			return true
		}

		// Both magnitudes exceed the clamp, so equal structs only prove the
		// clamped exponents match. Two distinct huge numbers (1e1073741824 and
		// 1e2147483648) share a clamped DecNumber, so confirm their exact,
		// unclamped exponents agree to keep them distinct, matching upstream's
		// uncapped big.Rat comparison.
		return numrat.DecCanonicalExp(string(an)).Cmp(numrat.DecCanonicalExp(string(bn))) == 0

	case aNum:
		return guardedNumberEqual(an, b)
	case bNum:
		return guardedNumberEqual(bn, a)
	}

	switch av := a.(type) {
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}

		for i := range av {
			if !equalGuarded(av[i], bv[i]) {
				return false
			}
		}

		return true

	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}

		for k, item := range av {
			other, exists := bv[k]
			if !exists || !equalGuarded(item, other) {
				return false
			}
		}

		return true
	}

	return jsonschema.Equal(a, b)
}

// guardedNumberEqual compares a [json.Number] against a non-Number value with
// the same semantics as [jsonschema.Equal]: numeric Go values compare
// mathematically across representations, everything else is unequal.
func guardedNumberEqual(n json.Number, b any) bool {
	d, ok := numrat.ParseDecNumber(string(n))
	if !ok {
		return false
	}

	br, ok := numrat.NumericRat(b)
	if !ok {
		return false
	}

	if !d.ExactlyComparable() {
		// Outside the bounds the value cannot equal any float64 or integer.
		return false
	}

	return d.Rat().Cmp(br) == 0
}

// equalClassified reports JSON-semantic equality like [equalJSONValues] but
// takes each operand's non-finite/unbounded classification precomputed, so a
// caller comparing one value against many (HasDuplicates) walks each value for
// those properties once rather than re-walking both operands on every pair. The
// dispatch is identical to equalJSONValues: a non-finite operand is unequal to
// everything; an unbounded operand routes through the guarded comparison;
// otherwise the full upstream semantics apply.
func equalClassified(a any, aNonFinite, aUnbounded bool, b any, bNonFinite, bUnbounded bool) bool {
	if aNonFinite || bNonFinite {
		return false
	}

	if aUnbounded || bUnbounded {
		return equalGuarded(a, b)
	}

	return jsonschema.Equal(a, b)
}

// HasDuplicates checks for duplicate values using JSON-semantic equality.
func HasDuplicates(arr []any) bool {
	// Each array element is classified once for the two properties equalJSONValues
	// would otherwise re-derive by walking both operands on every pair: whether it
	// contains a non-finite float and whether it contains an out-of-bounds number.
	// Classifying once turns the per-pair guard walks from O(n^2) into O(n).
	type entry struct {
		val       any
		nonFinite bool
		unbounded bool
	}

	seen := make(map[uint64][]entry, len(arr))

	for _, item := range arr {
		nonFinite := containsNonFiniteFloat(item)
		// A non-finite operand is never equal to anything, so its unboundedness is
		// never consulted; skip that walk.
		unbounded := !nonFinite && containsUnboundedNumber(item)

		h := hashValue(item)
		for _, existing := range seen[h] {
			if equalClassified(item, nonFinite, unbounded, existing.val, existing.nonFinite, existing.unbounded) {
				return true
			}
		}

		seen[h] = append(seen[h], entry{val: item, nonFinite: nonFinite, unbounded: unbounded})
	}

	return false
}

// hashValue produces a hash for JSON-semantic equality bucketing.
func hashValue(v any) uint64 {
	switch val := v.(type) {
	case nil:
		return 0
	case bool:
		if val {
			return 1
		}

		return 2

	case string:
		return stringHash(val)
	case float64:
		// Normalize: integers hash the same regardless of representation. The
		// fast path is restricted to the int64 range; an out-of-range float to
		// int64 conversion is platform-defined (saturates or wraps), so larger
		// integers fall through to the big.Rat path and stay consistent with the
		// json.Number branch (and with jsonschema.Equal).
		if val == math.Trunc(val) && val >= math.MinInt64 && val < math.MaxInt64 {
			return numHash(int64(val))
		}

		// Non-finite floats (NaN, ±Inf) have no big.Rat form, and equalJSONValues
		// short-circuits them to never-equal (even to themselves), so they need
		// not share a bucket. Give each a distinct constant so they avoid
		// colliding with numeric zero and each other, sparing the wasted equality
		// comparisons that a shared bucket would force.
		switch {
		case math.IsNaN(val):
			return 0x9e3779b97f4a7c15
		case math.IsInf(val, 1):
			return 0x9e3779b97f4a7c16
		case math.IsInf(val, -1):
			return 0x9e3779b97f4a7c17
		}

		r := new(big.Rat).SetFloat64(val)

		return stringHash(r.RatString()) + 4

	case json.Number:
		// DoS guard: expand only canonically cheap literals into a rational. A
		// number outside the bounds can only ever equal another such number
		// (see equalGuarded), and equal values share one canonical form, so
		// hashing that form keeps equal values colliding without the quadratic
		// parse or exponent expansion.
		d, ok := numrat.ParseDecNumber(string(val))
		if !ok {
			return stringHash(string(val)) + 5
		}

		if !d.ExactlyComparable() {
			h := stringHash(d.Sig())*31 + numHash(int64(d.Exp()))
			if d.Neg() {
				h = h*31 + 1
			}

			return h + 8
		}

		r := d.Rat()
		// IsInt64 guards against silent truncation for integers beyond the
		// int64 range, so they hash via RatString and stay consistent with
		// the float64 branch (and with the guarded equality's rat compare).
		if r.IsInt() && r.Num().IsInt64() {
			return numHash(r.Num().Int64())
		}

		return stringHash(r.RatString()) + 4

	case []any:
		h := uint64(6)
		for _, item := range val {
			h = h*31 + hashValue(item)
		}

		return h

	case map[string]any:
		h := uint64(7)
		for k, item := range val {
			// Fold each key with its own value before summing, so permuting which
			// key holds which value changes the per-entry term. A plain
			// XOR-then-sum is insensitive to that binding and buckets
			// permutation-objects ({"a":1,"b":2} vs {"a":2,"b":1}) together,
			// degrading HasDuplicates toward O(n^2) equalJSONValues calls. The sum
			// keeps the result independent of Go's randomized map iteration order,
			// which is required: equal objects must hash equally.
			x := stringHash(k)*1099511628211 ^ hashValue(item)
			x ^= x >> 33
			x *= 0xff51afd7ed558ccd
			x ^= x >> 33
			h += x
		}

		return h
	}

	return 0
}

func stringHash(s string) uint64 {
	var h uint64

	for i := range len(s) {
		h = h*31 + uint64(s[i])
	}

	return h
}

// numHash produces a hash for integer values, avoiding gosec G115.
//
//nolint:gosec // Overflow is intentional for hash distribution.
func numHash(n int64) uint64 {
	return uint64(n)*2654435761 + 3
}
