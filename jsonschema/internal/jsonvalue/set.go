package jsonvalue

import "slices"

// setIndexLimit is the largest member count a Set scans linearly. Up to it a
// scan allocates nothing and Equal stops at the first difference, where a
// lookup hashes the probe in full; past it the hash index bounds a lookup by
// the members sharing the probe's hash rather than by the member count.
const setIndexLimit = 8

// Set holds JSON values for membership tests under [Value.Equal]. A set of at
// most setIndexLimit members compares the probe against each; a larger one
// indexes its members by [Value.Hash] and compares the probe only against
// the members sharing its hash, so a lookup in an enum of hundreds of members
// costs one hash and a short chain rather than a scan of every member.
type Set struct {
	members []Value

	// The last map holds the index of the latest member with each hash and
	// prev links that member back to the one before it with the same hash,
	// so a lookup walks the chain without a per-hash slice. Both are nil
	// for a set below the index limit.
	last map[uint64]int
	prev []int
}

// NewSet returns a Set over members, which it takes ownership of.
func NewSet(members []Value) *Set {
	s := &Set{members: members}
	if len(members) <= setIndexLimit {
		return s
	}

	s.last = make(map[uint64]int, len(members))
	s.prev = make([]int, len(members))

	for i, m := range members {
		h := m.Hash()

		j, ok := s.last[h]
		if !ok {
			j = -1
		}

		s.last[h] = i
		s.prev[i] = j
	}

	return s
}

// Contains reports whether some member is [Value.Equal] to v.
func (s *Set) Contains(v Value) bool {
	if s.last == nil {
		return slices.ContainsFunc(s.members, func(m Value) bool { return m.Equal(v) })
	}

	j, ok := s.last[v.Hash()]
	if !ok {
		return false
	}

	for ; j >= 0; j = s.prev[j] {
		if s.members[j].Equal(v) {
			return true
		}
	}

	return false
}

// Members returns the set's members in the order given to [NewSet]. The
// slice is the Set's own and must not be mutated.
func (s *Set) Members() []Value { return s.members }
