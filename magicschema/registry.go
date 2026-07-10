package magicschema

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrUnknownAnnotator indicates an annotator name with no registered parser.
// Configuration paths such as the cli subpackage's Config.NewGenerator
// additionally wrap [ErrInvalidOption], so their errors match both sentinels
// with [errors.Is].
var ErrUnknownAnnotator = errors.New("unknown annotator")

// Registry maps annotator names (as used in the --annotators flag) to
// prototype [Annotator] instances. Prototypes are never mutated; the
// generator calls [Annotator.ForContent] to obtain a prepared clone for
// each input file.
type Registry map[string]Annotator

// Add registers one or more annotators in the registry, using each
// annotator's [Annotator.Name] as the map key.
func (r Registry) Add(annotators ...Annotator) {
	for _, a := range annotators {
		r[a.Name()] = a
	}
}

// Lookup resolves annotator names to their registered prototypes, preserving
// the given order. Names must match registered names exactly; a miss returns
// an error wrapping [ErrUnknownAnnotator].
func (r Registry) Lookup(names ...string) ([]Annotator, error) {
	annotators := make([]Annotator, 0, len(names))

	for _, name := range names {
		annotator, ok := r[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownAnnotator, name)
		}

		annotators = append(annotators, annotator)
	}

	return annotators, nil
}

// Names returns the registered annotator names, sorted.
func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r))
}
