// Package jsonequal implements DoS-guarded, JSON-semantic value equality for
// the const and enum keywords and the matching content hash for the
// uniqueItems check. It layers on [go.jacobcolvin.com/x/jsonschema/internal/numrat]
// for exact decimal comparison: numbers compare through a guarded local walk
// over their canonical decomposition (exact at any size, linear in the
// literal), never through upstream [jsonschema.Equal]'s uncapped
// [big.Rat.SetString] expansion, whose cost is quadratic in an adversarial
// multi-megabyte or large-exponent literal. A float64 operand is interpreted
// through its shortest decimal ([numrat.Float64ToRat]), so float64(0.1)
// equals the decoded literal 0.1 under uniqueItems just as it does under
// const, enum, and the numeric-bound keywords. Values outside the
// decoded-JSON shapes (hand-built const/enum containers) delegate to the
// upstream comparison, hardened against its panics. Non-finite floats (NaN,
// ±Inf) are treated as unequal to
// everything, including themselves, matching the numeric-bound keywords. Cyclic
// values (containers that contain themselves, which have no JSON serialization)
// are likewise unequal to everything, including themselves, and are detected up
// front so the walks terminate instead of overflowing the stack.
package jsonequal

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
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

// equalJSONValues reports JSON-semantic equality like [jsonschema.Equal],
// with numbers compared through the guarded local walk (see [equalGuarded]):
// exact at any size, linear in the literal, and with a float64 interpreted
// through its shortest decimal so the result agrees with const, enum, and
// the numeric-bound keywords. Delegating numbers to the upstream would
// instead expand every [json.Number] through an uncapped [big.Rat.SetString]
// (quadratic in an adversarial literal, see numrat.MaxNumberLen) and expand
// a float64 to its exact binary value, under which float64(0.1) never
// equals the decoded literal 0.1.
func equalJSONValues(a, b any) bool {
	// A cyclic value (a container that contains itself) has no JSON
	// serialization, so it is unequal to everything, including itself. This
	// check must run first: every other walk here (containsNonFiniteFloat,
	// equalGuarded, and the upstream [jsonschema.Equal]) assumes a finite
	// tree and would recurse without bound on a cycle, aborting the process
	// with a fatal stack overflow that recover cannot catch.
	if containsCycle(a) || containsCycle(b) {
		return false
	}

	// A non-finite float64 (NaN, +Inf, -Inf) is not a representable JSON number,
	// and upstream's big.Rat.SetFloat64 collapses all three (and zero) toward the
	// same value, so jsonschema.Equal would report NaN==NaN, +Inf==-Inf, and
	// NaN==0 as equal. Treat any value containing a non-finite float as unequal
	// to everything, including itself, matching how the numeric-bound keywords
	// already treat such floats as "not a number".
	if containsNonFiniteFloat(a) || containsNonFiniteFloat(b) {
		return false
	}

	return equalGuarded(a, b)
}

// equalUpstream delegates to [jsonschema.Equal] for values outside the shapes
// the local walks understand (hand-built const/enum containers such as the
// map[any]any gopkg.in/yaml.v2 decodes documents into, typed slices, structs),
// converting any panic into "unequal". The upstream comparison checks only
// the [reflect.Kind] before iterating maps, so a map[any]any const against a
// decoded map[string]any instance panics inside [reflect.Value.MapIndex] (an
// interface{} key is not assignable to a string key), and other exotic kinds
// (non-nil funcs, channels) panic explicitly. A pair upstream cannot compare
// safely is treated like a non-finite float: unequal.
func equalUpstream(a, b any) bool {
	eq := false

	func() {
		defer func() {
			if recover() != nil {
				// The pair stays reported unequal.
				eq = false
			}
		}()

		eq = jsonschema.Equal(a, b)
	}()

	return eq
}

// containsCycle reports whether v is or contains a self-referential []any,
// map[string]any, or map[any]any: a container that is, directly or
// transitively, its own ancestor. The internal/normalize package deliberately
// tolerates such instances (its cycle guard stops at the back-edge and keeps
// the value), so they can reach the equality and hash walks here, which
// otherwise assume finite trees and would recurse without bound. A cyclic
// value has no JSON serialization, so
// callers treat it like a non-finite float: unequal to everything, including
// itself. []any and map[string]any are the shapes a decoded or normalized
// JSON instance can take; map[any]any is the YAML-decoded shape a hand-built
// const/enum can carry.
func containsCycle(v any) bool {
	return containsCycleOnPath(v, map[[2]uintptr]bool{})
}

// containsCycleOnPath is the recursive core of [containsCycle]. It mirrors
// internal/normalize's guard: the on-path set keys on {data pointer, len}
// rather than the pointer alone, so a reslice sharing its parent's data
// pointer is not mistaken for a back-edge, and entries are removed on exit so
// shared (DAG) substructure is not reported as a cycle.
func containsCycleOnPath(v any, onPath map[[2]uintptr]bool) bool {
	switch val := v.(type) {
	case []any:
		key := [2]uintptr{reflect.ValueOf(val).Pointer(), uintptr(len(val))}
		if onPath[key] {
			return true
		}

		onPath[key] = true
		defer delete(onPath, key)

		for _, item := range val {
			if containsCycleOnPath(item, onPath) {
				return true
			}
		}

	case map[string]any:
		key := [2]uintptr{reflect.ValueOf(val).Pointer(), uintptr(len(val))}
		if onPath[key] {
			return true
		}

		onPath[key] = true
		defer delete(onPath, key)

		for _, item := range val {
			if containsCycleOnPath(item, onPath) {
				return true
			}
		}

	case map[any]any:
		// The shape gopkg.in/yaml.v2 decodes documents into; a hand-built
		// const/enum can carry it, and a cycle through it would otherwise
		// overflow the stack in the walks below (a map key cannot hold a
		// container, so only the values need following).
		key := [2]uintptr{reflect.ValueOf(val).Pointer(), uintptr(len(val))}
		if onPath[key] {
			return true
		}

		onPath[key] = true
		defer delete(onPath, key)

		for _, item := range val {
			if containsCycleOnPath(item, onPath) {
				return true
			}
		}
	}

	return false
}

// containsNonFiniteFloat reports whether v, or any element of an []any,
// map[string]any, or map[any]any it contains, is a non-finite float64 (NaN or
// ±Inf). Such a value can only enter through validation (JSON decoding never
// yields one); the other container kinds cannot hold one.
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

	case map[any]any:
		// A hand-built const/enum can carry the map[any]any shape
		// gopkg.in/yaml.v2 decodes documents into; upstream folds a NaN inside
		// one toward zero like any other non-finite float, so it must be
		// screened here too.
		for _, item := range val {
			if containsNonFiniteFloat(item) {
				return true
			}
		}
	}

	return false
}

// equalGuarded is the comparison walk over the JSON instance shapes. Numbers
// compare via their canonical decomposition, which is exact at any size
// without expanding the literal: two decimal literals are equal exactly when
// their decompositions match, and a number outside the cheap-expansion
// bounds can never equal a float64 or integer (those expand to at most ~767
// significant decimal digits within exponent ±324). A float64 against a
// [json.Number] compares through the shortest-decimal interpretation
// ([numrat.NumericRat]), so float64(1.1) equals the decoded literal 1.1 here
// just as it does under const, enum, and the numeric-bound keywords.
// Container types other than the decoded-JSON shapes fall through to
// [equalUpstream]; a decoded [json.Number] can only appear inside the shapes
// handled here, so no adversarial literal reaches the upstream's uncapped
// parse.
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

	return equalUpstream(a, b)
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

// HasDuplicates checks for duplicate values using JSON-semantic equality.
// A cyclic element (see [containsCycle]) is unequal to everything, including
// itself, so it never counts as a duplicate.
func HasDuplicates(arr []any) bool {
	seen := make(map[uint64][]any, len(arr))

	for _, item := range arr {
		// A cyclic element is unequal to everything, including itself (see
		// containsCycle), so it can never form a duplicate. It must be
		// screened out before the other walks below, which assume finite
		// trees and would otherwise recurse without bound.
		if containsCycle(item) {
			continue
		}

		// A non-finite element is likewise unequal to everything, including
		// itself (see equalJSONValues), so it can neither be a duplicate nor
		// anchor one; screening it here once keeps the per-pair comparison
		// from re-walking both operands, as equalJSONValues would.
		if containsNonFiniteFloat(item) {
			continue
		}

		h := hashValue(item)
		for _, existing := range seen[h] {
			if equalGuarded(item, existing) {
				return true
			}
		}

		seen[h] = append(seen[h], item)
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
		// Integral floats within 2^53 have a shortest decimal equal to the
		// integer itself (the float64 spacing is at most 1, so no shorter
		// decimal denotes a different number), letting them take the cheap
		// integer hash directly. A larger integral float's shortest decimal
		// can denote a different integer (float64(1<<60) reads back as
		// 1152921504606847000), so it must go through the rational form below
		// to share a bucket with the json.Number spelling of that value.
		if val == math.Trunc(val) && val > -(1<<53) && val < 1<<53 {
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

		// Interpret the float through its shortest decimal, the same rational
		// the guarded equality compares (numrat.NumericRat), so float64(1.1)
		// and json.Number("1.1") share a bucket. Expanding the exact binary
		// value (big.Rat.SetFloat64) would split equal values across buckets.
		// The non-finite cases returned above, so the conversion cannot yield
		// nil.
		return ratHash(numrat.Float64ToRat(val))

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

		return ratHash(d.Rat())

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

// ratHash hashes the exact rational a numeric value denotes, so every
// spelling of one value (a float64 via its shortest decimal, a decimal
// literal via its canonical decomposition) lands in one bucket: the int64
// form when the value fits (matching the integer fast path above), the
// canonical fraction string otherwise. IsInt64 guards against silent
// truncation for integers beyond the int64 range.
func ratHash(r *big.Rat) uint64 {
	if r.IsInt() && r.Num().IsInt64() {
		return numHash(r.Num().Int64())
	}

	return stringHash(r.RatString()) + 4
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
