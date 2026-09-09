package differentialtest_test

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"

	playground "github.com/go-playground/validator/v10"
)

// Rig 4 -- tag spellings. The shape rigs draw tags from a vocabulary of
// well-formed spellings, so they compare what the two validators do with a
// rule and never what they do with a string. This rig fuzzes the tag string
// itself against go-playground's own parser: for one field of a drawn kind
// carrying an arbitrary validate tag, the two sides must agree on whether the
// tag is usable at all, and where both find it usable, on the verdict over a
// fixed set of probe values plus one fuzzed value.
//
// Go-playground reports every fault in a tag -- an unknown key, a key its
// grammar refuses, a kind a validator cannot check, a parameter it cannot
// parse -- as a panic from Struct, and the kind and parameter panics fire
// lazily on the first value that reaches them, so the oracle runs every probe
// under recover and refuses the tag if any probe panics. Struct also stops at
// the first part that rejects a value, so a later part's panic hides behind
// an earlier rejection (min=7,min on a short slice), and the oracle runs each
// part on its own as well, under the dives before it. This side reports a
// fault as a generation error, classified by sentinel: the documented
// vocabulary gap ([validate.ErrUnrecognizedValidator]), a conflict between
// rules, or a refusal of the spelling.
//
// The decision matrix:
//
//   - both refuse: agreement.
//   - go-playground refuses, this side accepts: a spelling go-playground would
//     panic on at run time is accepted here, a finding.
//   - go-playground accepts, this side refuses: a legal tag is an error here,
//     a finding.
//   - go-playground accepts, this side reports the vocabulary gap: the
//     documented divergence, no comparison.
//   - go-playground accepts, this side reports a conflict: agreement only if
//     go-playground rejects every probe, since an unsatisfiable rule set is
//     what a conflict claims.
//   - both accept: the verdicts must agree probe by probe.
//
// Every spelling the rig does not compare is an entry in spellingExclusions,
// each with a reason, and TestSpellingExclusionsAreReasoned pins what each
// catches and what it must not.

// spellingKind is one field type the draw can put the fuzzed tag on, with the
// probe values the verdict comparison runs. The probes include the zero
// value, values on either side of the bounds the seed spellings name, and for
// a collection the nil, empty, duplicate-bearing, and distinct forms.
type spellingKind struct {
	name   string
	typ    reflect.Type
	probes []any
}

// spellingKinds is the draw pool of kinds: the shape rig's nine plus the
// widths whose parameter parsing differs in go-playground (uint8 and uint64
// take asUint, int64 asInt, float32 asFloat32).
func spellingKinds() []spellingKind {
	return []spellingKind{
		{name: "string", typ: reflect.TypeFor[string](), probes: []any{
			"", "a", "ab", "abc", "abcd", "abcde", "abcdef", "alpha", "fixed", "banned", "1", "true",
		}},
		{name: "int", typ: reflect.TypeFor[int](), probes: []any{0, 1, 2, 3, 4, 5, 7, 8, 9, 10, 11, -1}},
		{
			name:   "int8",
			typ:    reflect.TypeFor[int8](),
			probes: []any{int8(0), int8(1), int8(3), int8(7), int8(10), int8(-1)},
		},
		{
			name:   "int64",
			typ:    reflect.TypeFor[int64](),
			probes: []any{int64(0), int64(1), int64(3), int64(7), int64(10)},
		},
		{
			name:   "uint8",
			typ:    reflect.TypeFor[uint8](),
			probes: []any{uint8(0), uint8(1), uint8(3), uint8(7), uint8(10)},
		},
		{
			name:   "uint64",
			typ:    reflect.TypeFor[uint64](),
			probes: []any{uint64(0), uint64(1), uint64(3), uint64(7), uint64(10)},
		},
		{name: "float64", typ: reflect.TypeFor[float64](), probes: []any{0.0, 0.5, 1.5, 3.0, 7.0, 9.5, 10.0, -1.0}},
		{
			name:   "float32",
			typ:    reflect.TypeFor[float32](),
			probes: []any{float32(0), float32(1.5), float32(3), float32(10)},
		},
		{name: "bool", typ: reflect.TypeFor[bool](), probes: []any{false, true}},
		{name: "[]string", typ: reflect.TypeFor[[]string](), probes: []any{
			[]string(nil),
			[]string{},
			[]string{"a"},
			[]string{"a", "a"},
			[]string{"a", "b"},
			[]string{"ab", "cd", "ef"},
			[]string{"ab", "cd", "ef", "gh"},
		}},
		{name: "[]int8", typ: reflect.TypeFor[[]int8](), probes: []any{
			[]int8(nil), []int8{}, []int8{1}, []int8{1, 1}, []int8{1, 2}, []int8{1, 2, 3}, []int8{1, 2, 3, 4},
		}},
		{name: "map[string]int", typ: reflect.TypeFor[map[string]int](), probes: []any{
			map[string]int(nil),
			map[string]int{},
			map[string]int{"a": 1},
			map[string]int{"a": 1, "b": 1},
			map[string]int{"a": 1, "b": 2, "c": 3},
			map[string]int{"a": 1, "b": 2, "c": 3, "d": 4},
		}},
		{name: "[]byte", typ: reflect.TypeFor[[]byte](), probes: []any{
			[]byte(nil), []byte{}, []byte("abc"), []byte("abcdef"),
		}},
	}
}

// spellingExclusion is one class of spelling the rig does not compare, with
// the reason. A decision exclusion skips the spelling entirely. A verdict
// exclusion keeps the acceptance comparison and skips the value comparison,
// since the two sides are known to disagree on values there by design.
type spellingExclusion struct {
	reason  string
	catches func(tag string, kind spellingKind, schemaJSON string, err error) bool
	verdict bool
}

const (
	reasonIntegerLiteralBase0     = "an integer literal outside the JSON grammar, a value or a size, is refused where go-playground reads it in base 0"
	reasonOneOfTokenUnmatchable   = "a oneof with no token, or a token no value's text can equal, is refused where go-playground accepts a rule that matches nothing"
	reasonFloatSpellingNonDecimal = "a non-decimal or non-finite float spelling is refused where go-playground reads it through strconv"
	reasonBoolSpelling            = "a boolean spelling other than true or false is refused where go-playground reads it through strconv.ParseBool"
	reasonPartLenient             = "a blank, dash, or space-padded part is skipped or trimmed here where go-playground refuses the tag"
	reasonOneOfSequenceRetarget   = "oneof on a sequence retargets onto the element schemas by design, where go-playground panics"
	reasonByteSliceElements       = "a byte slice is one base64 string: go-playground counts its bytes, which the base64 length cannot express, and reaches elements the schema cannot"
	reasonControlTagSkipped       = "a control tag governs when go-playground runs and has no schema form, so the schema is the stricter side on the values it skips"
	reasonStringRuleOnOtherKind   = "a string validator on a non-string kind matches reflect's description of the value in go-playground and rejects everything, where this side refuses the shape"
	reasonScalarOutOfRange        = "a scalar the field's kind cannot hold is refused where go-playground compares it at the field's width and matches nothing, everything, or the rounded value"
	reasonBoundNotRepresentable   = "a bound the schema cannot ship exactly is refused under the exact-representability policy where go-playground compares it at the field's width"
	reasonStrayParameter          = "a parameter on a validator that takes none is refused where go-playground ignores it"
	reasonRepeatedKeyword         = "two validators setting one schema keyword are refused where go-playground applies both, since a schema carries one value per keyword"
)

// spellingExclusions is the record of everything this rig leaves out, in the
// order the classifier tries them.
func spellingExclusions() []spellingExclusion {
	return []spellingExclusion{
		{reason: reasonOrOperatorUnmodeled, catches: func(tag string, _ spellingKind, _ string, _ error) bool {
			return strings.Contains(tag, "|")
		}},
		{reason: reasonKeysBlockUnmodeled, catches: func(tag string, _ spellingKind, _ string, _ error) bool {
			return spells(tag, "keys") || spells(tag, "endkeys")
		}},
		{reason: reasonCrossFieldUnmodeled, catches: func(tag string, _ spellingKind, _ string, _ error) bool {
			return anyPart(tag, func(key, _ string) bool {
				return strings.Contains(key, "field") || strings.HasPrefix(key, "required_") ||
					strings.HasPrefix(key, "excluded_") || strings.HasPrefix(key, "skip_")
			})
		}},
		{reason: reasonPartLenient, catches: func(tag string, _ spellingKind, _ string, _ error) bool {
			for part := range strings.SplitSeq(tag, ",") {
				if part == "" || part == "-" || strings.TrimSpace(part) != part {
					return true
				}
			}

			return false
		}},
		{reason: reasonOneOfSequenceRetarget, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			return spells(tag, "oneof") && isCollectionKind(kind.typ) && !spellsAfterDive(tag, "oneof")
		}},
		{reason: reasonByteSliceElements, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			return kind.typ == reflect.TypeFor[[]byte]() && anyPart(tag, func(key, _ string) bool {
				return key == "dive" || key == "unique" || isNumericRuleKey(key)
			})
		}},
		{reason: reasonIntegerLiteralBase0, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			// A float kind reads its parameter as a float and a bool as a
			// bool; every other kind reads an integer, as a value or a size.
			if isFloatKind(kind.typ) || kind.typ.Kind() == reflect.Bool {
				return false
			}

			return anyPart(tag, func(key, param string) bool {
				if !isNumericRuleKey(key) || key == "oneof" {
					return false
				}

				// Go-playground's unsigned parser refuses the plus the signed
				// one takes, so what it reads depends on the kind.
				var err error

				if isUnsignedKind(kind.typ) {
					_, err = strconv.ParseUint(param, 0, 64)
				} else {
					_, err = strconv.ParseInt(param, 0, 64)
				}

				return err == nil && constraint.CheckIntegerLiteral(param) != nil
			})
		}},
		{reason: reasonOneOfTokenUnmatchable, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			return anyPart(tag, func(key, param string) bool {
				if key != "oneof" {
					return false
				}

				tokens := oneOfTokens(param)
				if len(tokens) == 0 {
					return true
				}

				if !isIntegerKind(kind.typ) {
					return false
				}

				for _, tok := range tokens {
					if !canonicalInteger(tok, isUnsignedKind(kind.typ)) {
						return true
					}
				}

				return false
			})
		}},
		{reason: reasonFloatSpellingNonDecimal, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			if !isFloatKind(kind.typ) {
				return false
			}

			return anyPart(tag, func(key, param string) bool {
				if !isNumericRuleKey(key) {
					return false
				}

				f, err := strconv.ParseFloat(param, 64)
				if err != nil {
					return false
				}

				_, decErr := constraint.ParseDecimalFloat(param)

				return decErr != nil || math.IsInf(f, 0) || math.IsNaN(f)
			})
		}},
		{reason: reasonBoolSpelling, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			if kind.typ.Kind() != reflect.Bool {
				return false
			}

			return anyPart(tag, func(key, param string) bool {
				if key != "eq" && key != "ne" {
					return false
				}

				_, err := strconv.ParseBool(param)

				return err == nil && param != "true" && param != "false"
			})
		}},

		{reason: reasonScalarOutOfRange, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			return anyRule(tag, kind.typ, func(key, param string, target reflect.Type) bool {
				if key != "eq" && key != "ne" && key != "len" && key != "oneof" {
					return false
				}

				if isFloatKind(target) {
					// A float literal the width rounds: go-playground parses
					// it at that width and matches the rounded value.
					for _, tok := range oneOfTokens(param) {
						if errors.Is(constraint.CheckFloatLiteral(tok, target.Kind()), constraint.ErrNotRepresentable) {
							return true
						}
					}

					return false
				}

				if !isIntegerKind(target) {
					return false
				}

				for _, tok := range oneOfTokens(param) {
					if constraint.CheckIntegerLiteral(tok) != nil {
						continue
					}

					if isUnsignedKind(target) {
						_, wide := strconv.ParseUint(tok, 10, 64)
						_, narrow := strconv.ParseUint(tok, 10, target.Bits())

						if wide == nil && narrow != nil {
							return true
						}

						continue
					}

					_, wide := strconv.ParseInt(tok, 10, 64)
					_, narrow := strconv.ParseInt(tok, 10, target.Bits())

					if wide == nil && narrow != nil {
						return true
					}
				}

				return false
			})
		}},
		{reason: reasonBoundNotRepresentable, catches: func(tag string, kind spellingKind, _ string, _ error) bool {
			return anyRule(tag, kind.typ, func(key, param string, target reflect.Type) bool {
				if !isIntegerKind(target) && !isFloatKind(target) {
					return false
				}

				if !contains([]string{"min", "max", "gt", "lt", "gte", "lte"}, key) {
					return false
				}

				_, err := constraint.ParseNumericBound(param, target.Kind())

				return errors.Is(err, constraint.ErrNotRepresentable)
			})
		}},
		{reason: reasonStrayParameter, catches: func(_ string, _ spellingKind, _ string, err error) bool {
			return err != nil && strings.Contains(err.Error(), "takes no parameter")
		}},
		{reason: reasonRepeatedKeyword, catches: func(_ string, _ spellingKind, _ string, err error) bool {
			return errors.Is(err, validate.ErrRepeatedKeyword)
		}},
		{reason: reasonStringRuleOnOtherKind, catches: func(_ string, kind spellingKind, _ string, err error) bool {
			if kind.typ.Kind() == reflect.String || !errors.Is(err, tagmodel.ErrUnsupported) {
				return false
			}

			msg := err.Error()

			return strings.Contains(msg, "format is not supported on") ||
				strings.Contains(msg, "pattern is not supported on") ||
				strings.Contains(msg, "content encoding is not supported on") ||
				strings.Contains(msg, "content media type is not supported on")
		}},

		// The remaining entries are verdict exclusions: the tag is usable on
		// both sides, and the value comparison is what the record excludes.
		{
			reason:  reasonFormatDelegated,
			verdict: true,
			catches: func(_ string, _ spellingKind, schemaJSON string, _ error) bool {
				return strings.Contains(schemaJSON, `"format":`)
			},
		},
		{
			reason:  reasonPatternDialect,
			verdict: true,
			catches: func(_ string, _ spellingKind, schemaJSON string, _ error) bool {
				return strings.Contains(schemaJSON, `"pattern":`)
			},
		},
		{
			reason:  reasonContentUnmodeled,
			verdict: true,
			catches: func(_ string, _ spellingKind, schemaJSON string, _ error) bool {
				return strings.Contains(schemaJSON, `"contentEncoding":`) ||
					strings.Contains(schemaJSON, `"contentMediaType":`)
			},
		},
		{
			reason:  reasonUniqueMapNoOp,
			verdict: true,
			catches: func(tag string, kind spellingKind, _ string, _ error) bool {
				return spells(tag, "unique") && kind.typ.Kind() == reflect.Map
			},
		},
	}
}

// controlTags are the go-playground control tags this dialect skips. A tag
// carrying one is compared one way: go-playground rejecting a value implies
// the schema rejects it, since the schema never learns which values the
// control tag told go-playground to skip (reasonControlTagSkipped).
var (
	controlTags = []string{"omitempty", "omitnil", "omitzero", "structonly", "nostructlevel"}

	// OneOfTokenRegexp is go-playground's oneof splitter: a single-quoted run
	// or an unquoted run of non-space characters, with the quotes stripped
	// afterward, so a lone quote is an empty token.
	oneOfTokenRegexp = regexp.MustCompile(`'[^']*'|\S+`)
)

// spellingCell is the matrix cell one spelling lands in.
type spellingCell int

const (
	cellExcluded spellingCell = iota
	cellBothRefuse
	cellVocabularyGap
	cellConflict
	cellCompared
	cellVerdictExcluded
)

func (c spellingCell) String() string {
	return [...]string{"excluded", "both refuse", "vocabulary gap", "conflict", "compared", "verdict excluded"}[c]
}

// classifySpelling returns the first exclusion catching the spelling, or nil.
// Decision exclusions read the tag alone; verdict exclusions also read the
// generated schema, which is empty until this side has accepted the tag.
func classifySpelling(tag string, kind spellingKind, schemaJSON string, err error) *spellingExclusion {
	for _, ex := range spellingExclusions() {
		if ex.verdict && schemaJSON == "" {
			continue
		}

		if ex.catches(tag, kind, schemaJSON, err) {
			return &ex
		}
	}

	return nil
}

// ourVerdict is this side's classification of a tag.
type ourVerdict int

const (
	ourAccepts ourVerdict = iota
	ourGap
	ourConflict
	ourRefuses
)

// spellingStruct builds the one-field struct type carrying tag on a field of
// kind. The tag is quoted so any byte sequence survives the struct tag
// grammar; reflect and go-playground both read it back through Unquote.
func spellingStruct(tag string, kind spellingKind) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{
		Name: "V",
		Type: kind.typ,
		Tag:  reflect.StructTag(`json:"v" validate:` + strconv.Quote(tag)),
	}})
}

// probeValues returns one struct value per probe, plus one filled from the
// fuzzed blob, each with the field set.
func probeValues(typ reflect.Type, kind spellingKind, valueBlob []byte) []reflect.Value {
	out := make([]reflect.Value, 0, len(kind.probes)+1)

	for _, probe := range kind.probes {
		v := reflect.New(typ).Elem()
		v.Field(0).Set(reflect.ValueOf(probe).Convert(kind.typ))

		out = append(out, v)
	}

	v := reflect.New(typ)
	fuzzfill.Fill(v, valueBlob)

	out = append(out, v.Elem())

	return out
}

// referenceOutcome runs go-playground over the probes. It reports the
// verdict per probe, or that the tag is refused, which go-playground signals
// by panicking from Struct on some probe, of the whole tag or of any one part.
func referenceOutcome(tag string, kind spellingKind, probes []reflect.Value) ([]bool, bool) {
	if !referenceRunsEveryPart(tag, kind, probes) {
		return nil, true
	}

	reference := playground.New(playground.WithRequiredStructEnabled())
	rejects := make([]bool, len(probes))

	for i, probe := range probes {
		reject, ok := referenceVerdict(reference, probe.Interface())
		if !ok {
			return nil, true
		}

		rejects[i] = reject
	}

	return rejects, false
}

// referenceRunsEveryPart reports whether go-playground runs every part of the
// tag without a panic. Struct returns at the first part that rejects a value,
// so a part after one that rejects every probe is never reached and its panic
// stays latent until a value passes the earlier part. Each part therefore runs
// alone over the probes, under the dives before it so it targets what it
// targets in the whole tag.
func referenceRunsEveryPart(tag string, kind spellingKind, probes []reflect.Value) bool {
	var dives []string

	for part := range strings.SplitSeq(tag, ",") {
		segment := strings.Join(append(slices.Clone(dives), part), ",")
		typ := spellingStruct(segment, kind)
		reference := playground.New(playground.WithRequiredStructEnabled())

		for _, probe := range probes {
			val := reflect.New(typ).Elem()
			val.Field(0).Set(probe.Field(0))

			//nolint:errcheck // A validation error is a verdict, not a fault; only the panic matters.
			_, panicked := recoverStruct(reference, val.Interface())
			if panicked {
				return false
			}
		}

		if part == "dive" {
			dives = append(dives, part)
		}
	}

	return true
}

// referenceVerdict reports go-playground's verdict on one value, recovering
// from the panic it raises for a tag it cannot run. An
// InvalidValidationError cannot arise, since every probe is a struct.
func referenceVerdict(reference *playground.Validate, val any) (bool, bool) {
	err, panicked := recoverStruct(reference, val)
	if panicked {
		return false, false
	}

	if err == nil {
		return false, true
	}

	if _, isValidation := errors.AsType[playground.ValidationErrors](err); isValidation {
		return true, true
	}

	return false, false
}

// recoverStruct calls Struct and reports whether it panicked instead of
// returning.
func recoverStruct(reference *playground.Validate, val any) (error, bool) {
	var (
		err      error
		panicked bool
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()

		err = reference.Struct(val)
	}()

	//nolint:wrapcheck // The verdict reads the error's own type, and a test helper reports rather than wraps.
	return err, panicked
}

// classifyOurError sorts a generation error into the matrix's rows.
func classifyOurError(err error) ourVerdict {
	switch {
	case err == nil:
		return ourAccepts
	case errors.Is(err, validate.ErrUnrecognizedValidator):
		return ourGap
	case errors.Is(err, jsonschema.ErrConstraintConflict):
		return ourConflict
	default:
		return ourRefuses
	}
}

// runSpelling runs one spelling through the matrix and reports the cell it
// landed in. A disagreement fails t.
func runSpelling(t *testing.T, tag string, kind spellingKind, valueBlob []byte) spellingCell {
	t.Helper()

	if ex := classifySpelling(tag, kind, "", nil); ex != nil {
		return cellExcluded
	}

	ctx := t.Context()
	typ := spellingStruct(tag, kind)
	probes := probeValues(typ, kind, valueBlob)

	refRejects, refRefused := referenceOutcome(tag, kind, probes)

	schema, err := jsonschema.Generate(ctx, typ,
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
	ours := classifyOurError(err)

	switch {
	case refRefused && ours == ourAccepts:
		t.Fatalf("go-playground refuses validate:%q on %s but this side accepts it", tag, kind.name)
	case refRefused:
		return cellBothRefuse
	case ours == ourGap:
		return cellVocabularyGap
	case ours == ourRefuses && classifySpelling(tag, kind, "", err) != nil:
		return cellExcluded
	case ours == ourRefuses:
		t.Fatalf("go-playground accepts validate:%q on %s but this side refuses it: %v", tag, kind.name, err)
	case ours == ourConflict:
		for i, reject := range refRejects {
			if !reject {
				t.Fatalf("this side reports a conflict for validate:%q on %s but go-playground accepts %#v: %v",
					tag, kind.name, probes[i].Field(0).Interface(), err)
			}
		}

		return cellConflict
	}

	schemaJSON, err := json.Marshal(schema)
	require.NoError(t, err)

	if ex := classifySpelling(tag, kind, string(schemaJSON), nil); ex != nil {
		return cellVerdictExcluded
	}

	validator, err := jsonschema.Compile(ctx, schema)
	require.NoError(t, err, "compile the schema for validate:%q on %s", tag, kind.name)

	oneWay := anyPart(tag, func(key, _ string) bool { return contains(controlTags, key) })
	required := spells(tag, "required")

	for i, probe := range probes {
		value := probe.Field(0)
		if required && emptyBareCollection(value) {
			continue // reasonRequiredCollectionEmptyFloor
		}

		instance, err := json.Marshal(probe.Interface())
		if err != nil {
			continue
		}

		schemaReject := validator.ValidateJSON(ctx, instance) != nil

		agree := refRejects[i] == schemaReject
		if oneWay {
			agree = !refRejects[i] || schemaReject
		}

		if !agree {
			t.Fatalf("validators disagree on validate:%q on %s\n"+
				"value:         %#v\n"+
				"marshaled:     %s\n"+
				"go-playground: reject=%v\n"+
				"schema:        reject=%v\n"+
				"schema doc:    %s",
				tag, kind.name, value.Interface(), instance, refRejects[i], schemaReject, schemaJSON)
		}
	}

	return cellCompared
}

// drawSpellingKind picks a kind from the pool by the fuzzed byte.
func drawSpellingKind(b byte) spellingKind {
	kinds := spellingKinds()

	return kinds[int(b)%len(kinds)]
}

// spellingSeed is one seed of the corpus: a tag on the kind at an index of
// spellingKinds.
type spellingSeed struct {
	tag  string
	kind byte
}

// spellingSeeds is the seed corpus: every pool entry of the shape rig on the
// kind whose pool it belongs to, the spellings past fixes pinned, the integer
// literals go-playground reads in base 0, and validators outside this
// dialect's vocabulary.
func spellingSeeds() []spellingSeed {
	var seeds []spellingSeed

	kinds := spellingKinds()
	index := map[reflect.Type]byte{}

	for i, kind := range kinds {
		index[kind.typ] = byte(i)
	}

	for _, tk := range tagKinds() {
		for _, spelling := range tk.pool {
			seeds = append(seeds, spellingSeed{tag: spelling, kind: index[tk.typ]})
		}
	}

	str, integer, unsigned, float, narrow, boolean, sequence := index[reflect.TypeFor[string]()],
		index[reflect.TypeFor[int]()], index[reflect.TypeFor[uint8]()],
		index[reflect.TypeFor[float64]()], index[reflect.TypeFor[float32]()],
		index[reflect.TypeFor[bool]()], index[reflect.TypeFor[[]string]()]

	return append(seeds,
		spellingSeed{tag: "isdefault", kind: str},
		spellingSeed{tag: "email,url", kind: str},
		spellingSeed{tag: "oneof=+1 01 2", kind: integer},
		spellingSeed{tag: "oneof=true false", kind: boolean},
		spellingSeed{tag: "oneof=1 2", kind: float},
		spellingSeed{tag: "oneof=0.1 2", kind: narrow},
		spellingSeed{tag: "eq=", kind: str},
		spellingSeed{tag: "eq", kind: str},
		spellingSeed{tag: "unique=", kind: sequence},
		spellingSeed{tag: "required,dive", kind: sequence},
		spellingSeed{tag: "|required", kind: str},
		spellingSeed{tag: "min=+1", kind: unsigned},
		spellingSeed{tag: "min=010", kind: unsigned},
		spellingSeed{tag: "min=0x10", kind: integer},
		spellingSeed{tag: "eq=010", kind: integer},
		spellingSeed{tag: "min=+1.5", kind: float},
		spellingSeed{tag: "min=0x1p-2", kind: float},
		spellingSeed{tag: "eq=t", kind: boolean},
		spellingSeed{tag: "required,eq=false", kind: boolean},
		spellingSeed{tag: "omitempty,min=3", kind: integer},
		spellingSeed{tag: "e164", kind: str},
		spellingSeed{tag: "boolean", kind: str},
		spellingSeed{tag: "cve", kind: str},
		spellingSeed{tag: "email", kind: str},
		spellingSeed{tag: "alpha", kind: str},
		spellingSeed{tag: "", kind: str},
		spellingSeed{tag: "min=3,", kind: integer},
		spellingSeed{tag: "required, min=3", kind: integer},
		spellingSeed{tag: "dive,min=2", kind: sequence},
		spellingSeed{tag: "min=abc", kind: integer},
		spellingSeed{tag: "email", kind: integer},
		spellingSeed{tag: "oneof", kind: str},
		spellingSeed{tag: "oneof=-0", kind: index[reflect.TypeFor[int8]()]},
		spellingSeed{tag: "email=0", kind: boolean},
		spellingSeed{tag: "lt=10000000000000000", kind: index[reflect.TypeFor[int64]()]},
		spellingSeed{tag: "lt=10000000000000001", kind: index[reflect.TypeFor[float32]()]},
		spellingSeed{tag: "len=1000", kind: index[reflect.TypeFor[int8]()]},
		spellingSeed{tag: "len=3", kind: boolean},
		spellingSeed{tag: "oneof=a b", kind: sequence},
		// A part behind one that rejects every probe still has to run.
		spellingSeed{tag: "min=7,min", kind: sequence},
	)
}

// FuzzValidatorTagSpellings asserts the matrix over fuzzed tag strings.
func FuzzValidatorTagSpellings(f *testing.F) {
	f.Helper()

	blob := differentialSeeds()[1]

	for _, s := range spellingSeeds() {
		f.Add(s.tag, s.kind, blob)
	}

	f.Fuzz(func(t *testing.T, tag string, kindByte byte, valueBlob []byte) {
		runSpelling(t, tag, drawSpellingKind(kindByte), valueBlob)
	})
}

// TestSpellingSeedsReachEveryCell runs the seed corpus under plain go test
// and asserts every matrix cell the rig can reach without a finding is
// reached, so the corpus cannot quietly stop exercising one.
func TestSpellingSeedsReachEveryCell(t *testing.T) {
	t.Parallel()

	blob := differentialSeeds()[1]
	reached := map[spellingCell]int{}

	// The seeds run in one test rather than as subtests, since the tally
	// is one map and a failure already names the tag and kind.
	for _, s := range spellingSeeds() {
		reached[runSpelling(t, s.tag, drawSpellingKind(s.kind), blob)]++
	}

	for _, cell := range []spellingCell{
		cellExcluded, cellBothRefuse, cellVocabularyGap, cellConflict, cellCompared, cellVerdictExcluded,
	} {
		assert.Positive(t, reached[cell], "no seed reaches the %q cell", cell)
	}
}

// TestSpellingExclusionsAreReasoned pins what each exclusion catches and what
// it must not, so a predicate cannot widen into the spellings the rig is
// meant to compare.
func TestSpellingExclusionsAreReasoned(t *testing.T) {
	t.Parallel()

	for _, ex := range spellingExclusions() {
		assert.NotEmpty(t, ex.reason, "an exclusion states no reason")
	}

	kinds := map[string]spellingKind{}
	for _, kind := range spellingKinds() {
		kinds[kind.name] = kind
	}

	tests := map[string]struct {
		tag    string
		kind   string
		schema string
		err    error
		want   string
	}{
		"stray parameter": {
			tag: "email=0", kind: "bool",
			err:  errors.New("validate tag: email: takes no parameter, got \"0\""),
			want: reasonStrayParameter,
		},
		"repeated keyword": {
			tag: "email,url", kind: "string",
			err:  fmt.Errorf("%w: email and url", validate.ErrRepeatedKeyword),
			want: reasonRepeatedKeyword,
		},
		"one keyword-setting validator is compared": {
			tag: "email", kind: "string",
		},
		"parameter on a control tag is compared": {
			tag: "omitempty=", kind: "uint64",
			err: fmt.Errorf("%w %q", validate.ErrUnrecognizedValidator, "omitempty="),
		},
		"string rule on a number": {
			tag: "email", kind: "int64",
			err:  fmt.Errorf("%w: format is not supported on a number", tagmodel.ErrUnsupported),
			want: reasonStringRuleOnOtherKind,
		},
		"string rule on a string is compared": {
			tag: "email", kind: "string",
			err: fmt.Errorf("%w: format is not supported on a number", tagmodel.ErrUnsupported),
		},
		"other refusal on a number is compared": {
			tag: "min=abc", kind: "int64",
			err: errors.New("invalid integer"),
		},
		"OR operator": {tag: "required|min=3", kind: "string", want: reasonOrOperatorUnmodeled},
		"keys block": {
			tag:  "dive,keys,min=1,endkeys",
			kind: "map[string]int",
			want: reasonKeysBlockUnmodeled,
		},
		"cross-field":                  {tag: "eqfield=Other", kind: "string", want: reasonCrossFieldUnmodeled},
		"required_if":                  {tag: "required_if=Other x", kind: "string", want: reasonCrossFieldUnmodeled},
		"trailing comma":               {tag: "min=3,", kind: "int", want: reasonPartLenient},
		"space after comma":            {tag: "required, min=3", kind: "int", want: reasonPartLenient},
		"dash part":                    {tag: "-,min=3", kind: "int", want: reasonPartLenient},
		"oneof on bool":                {tag: "oneof=true false", kind: "bool", want: ""},
		"oneof on float":               {tag: "oneof=1 2", kind: "float64", want: ""},
		"oneof on a sequence":          {tag: "oneof=a b", kind: "[]string", want: reasonOneOfSequenceRetarget},
		"oneof after dive is compared": {tag: "dive,oneof=a b", kind: "[]string", want: ""},
		"dive on a byte slice":         {tag: "dive,min=1", kind: "[]byte", want: reasonByteSliceElements},
		"size bound on a byte slice":   {tag: "min=1", kind: "[]byte", want: reasonByteSliceElements},
		"required on a byte slice":     {tag: "required", kind: "[]byte", want: ""},
		"leading zero":                 {tag: "min=010", kind: "int", want: reasonIntegerLiteralBase0},
		"hex prefix":                   {tag: "eq=0x10", kind: "int", want: reasonIntegerLiteralBase0},
		"leading plus on signed":       {tag: "min=+1", kind: "int", want: reasonIntegerLiteralBase0},
		"leading plus on unsigned":     {tag: "min=+1", kind: "uint8", want: ""},
		"leading zero on a size":       {tag: "min=010", kind: "string", want: reasonIntegerLiteralBase0},
		"invalid octal on a size":      {tag: "min=080", kind: "[]string", want: ""},
		"unparseable is compared":      {tag: "min=abc", kind: "int", want: ""},
		"oneof token with a fraction":  {tag: "oneof=1.0 2", kind: "int", want: reasonOneOfTokenUnmatchable},
		"oneof canonical is compared":  {tag: "oneof=1 2", kind: "int", want: ""},
		"bare oneof":                   {tag: "oneof", kind: "string", want: reasonOneOfTokenUnmatchable},
		"empty oneof":                  {tag: "oneof=", kind: "int", want: reasonOneOfTokenUnmatchable},
		"lone quote token":             {tag: "oneof=' 0", kind: "int", want: reasonOneOfTokenUnmatchable},
		"negative zero token":          {tag: "oneof=-0", kind: "int8", want: reasonOneOfTokenUnmatchable},
		"quoted token is compared":     {tag: "oneof='New York' Boston", kind: "string", want: ""},
		"string oneof is compared":     {tag: "oneof=a b", kind: "string", want: ""},
		"scalar beyond the width":      {tag: "len=1000", kind: "int8", want: reasonScalarOutOfRange},
		"unsigned beyond the width":    {tag: "eq=256", kind: "uint8", want: reasonScalarOutOfRange},
		"scalar within the width":      {tag: "eq=100", kind: "int8", want: ""},
		"bound beyond the width":       {tag: "min=1000", kind: "int8", want: ""},
		"bound beyond 2^53":            {tag: "lt=10000000000000000", kind: "int64", want: reasonBoundNotRepresentable},
		"bound beyond 2^53 after a dive": {
			tag: "dive,min=10000000000000000", kind: "[]int8", want: reasonBoundNotRepresentable,
		},
		"scalar beyond the width after a dive": {tag: "dive,len=1000", kind: "[]int8", want: reasonScalarOutOfRange},
		"float scalar the width rounds":        {tag: "eq=10.0000001", kind: "float32", want: reasonScalarOutOfRange},
		"float bound the width rounds": {
			tag:  "lt=10.0000001",
			kind: "float32",
			want: reasonBoundNotRepresentable,
		},
		"float shortest decimal is compared": {tag: "lt=0.1", kind: "float32", want: ""},
		"bound at 2^53 is compared":          {tag: "lt=9007199254740992", kind: "int64", want: ""},
		"float bound beyond 2^53": {
			tag:  "lt=10000000000000001",
			kind: "float32",
			want: reasonBoundNotRepresentable,
		},
		"float fraction is compared": {tag: "lt=1.5", kind: "float32", want: ""},
		"hex float":                  {tag: "min=0x1p-2", kind: "float64", want: reasonFloatSpellingNonDecimal},
		"infinity":                   {tag: "max=inf", kind: "float64", want: reasonFloatSpellingNonDecimal},
		"float plus is compared":     {tag: "min=+1.5", kind: "float64", want: ""},
		"bool spelling":              {tag: "eq=t", kind: "bool", want: reasonBoolSpelling},
		"bool literal is compared":   {tag: "eq=true", kind: "bool", want: ""},
		"format": {
			tag:    "email",
			kind:   "string",
			schema: `{"format":"email"}`,
			want:   reasonFormatDelegated,
		},
		"pattern": {
			tag:    "alpha",
			kind:   "string",
			schema: `{"pattern":"^[a-z]+$"}`,
			want:   reasonPatternDialect,
		},
		"content": {
			tag:    "base64",
			kind:   "string",
			schema: `{"contentEncoding":"base64"}`,
			want:   reasonContentUnmodeled,
		},
		"unique on a map": {
			tag:    "unique",
			kind:   "map[string]int",
			schema: `{}`,
			want:   reasonUniqueMapNoOp,
		},
		"plain rule is compared": {tag: "min=3", kind: "int", schema: `{"minimum":3}`, want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			kind, ok := kinds[tc.kind]
			require.True(t, ok, "unknown kind %q", tc.kind)

			got := classifySpelling(tc.tag, kind, tc.schema, tc.err)
			if tc.want == "" {
				assert.Nil(t, got, "the rig must compare %q on %s", tc.tag, tc.kind)

				return
			}

			require.NotNil(t, got, "the rig must exclude %q on %s", tc.tag, tc.kind)
			assert.Equal(t, tc.want, got.reason)
		})
	}
}

// anyPart reports whether pred holds for any comma-separated part of tag,
// split into its key and parameter the way both parsers split it.
func anyPart(tag string, pred func(key, param string) bool) bool {
	for part := range strings.SplitSeq(tag, ",") {
		key, param, _ := strings.Cut(part, "=")
		if pred(key, param) {
			return true
		}
	}

	return false
}

// anyRule reports whether pred holds for some part of the tag, given the type
// the part constrains: the field's own type before a dive and the element
// type after each one, as go-playground retargets a rule behind a dive.
func anyRule(tag string, typ reflect.Type, pred func(key, param string, target reflect.Type) bool) bool {
	for part := range strings.SplitSeq(tag, ",") {
		if part == "dive" {
			if isCollectionKind(typ) {
				typ = typ.Elem()
			}

			continue
		}

		key, param, _ := strings.Cut(part, "=")
		if pred(key, param, typ) {
			return true
		}
	}

	return false
}

// spellsAfterDive reports whether rule appears after a dive, where it applies
// to the elements on both sides.
func spellsAfterDive(tag, rule string) bool {
	_, after, found := strings.Cut(tag, "dive,")

	return found && spells(after, rule)
}

// canonicalInteger reports whether tok is the text go-playground formats an
// integer value as, which is the only spelling its oneof can match: a
// decimal with no plus, no leading zero, and no negative zero.
func canonicalInteger(tok string, unsigned bool) bool {
	if unsigned {
		n, err := strconv.ParseUint(tok, 10, 64)

		return err == nil && strconv.FormatUint(n, 10) == tok
	}

	n, err := strconv.ParseInt(tok, 10, 64)

	return err == nil && strconv.FormatInt(n, 10) == tok
}

// oneOfTokens splits a oneof parameter the way go-playground does.
func oneOfTokens(param string) []string {
	found := oneOfTokenRegexp.FindAllString(param, -1)
	for i, tok := range found {
		found[i] = strings.ReplaceAll(tok, "'", "")
	}

	return found
}

// isNumericRuleKey reports whether key takes a numeric parameter this rig
// spells on a numeric kind.
func isNumericRuleKey(key string) bool {
	return contains([]string{"min", "max", "gt", "lt", "gte", "lte", "len", "eq", "ne", "oneof"}, key)
}

func isIntegerKind(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func isUnsignedKind(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func isFloatKind(typ reflect.Type) bool {
	return typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64
}

func isCollectionKind(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}
