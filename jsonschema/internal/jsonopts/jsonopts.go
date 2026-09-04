// Package jsonopts classifies every [encoding/json] Options constructor by
// what schema generation does with it: honored, ignored, refused, or a
// combinator that sets nothing on its own. [Table] is the one list, and
// [Refused] and [Honored] read a joined Options value against it.
//
// Nothing enumerates the options set on an Options value, so an unlisted
// constructor cannot be refused at run time. The in-package test parses the
// toolchain source under GOROOT and fails when a constructor has no row, which
// is what keeps the table complete across toolchain bumps.
package jsonopts

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"

	jsonv1 "encoding/json"
)

// Class is what generation does with one option.
type Class uint8

const (
	// ClassHonored options change the generated schema so it matches the
	// marshal under them.
	ClassHonored Class = iota
	// ClassIgnored options never change the JSON value a marshal writes, so
	// the schema is the same with or without them.
	ClassIgnored
	// ClassRefused options change the marshaled shape in a way generation
	// does not model, so setting one fails generation.
	ClassRefused
	// ClassCombinator constructors set nothing on their own.
	ClassCombinator
)

// Row classifies one constructor. Key is its import path and name; Set
// reports whether a joined Options value carries the option, and is nil for a
// combinator and for a bundle whose constituents carry their own rows.
type Row struct {
	Set   func(json.Options) bool
	Key   string
	Class Class
}

// Name returns the constructor's bare name, the form a refusal reports.
func (r Row) Name() string {
	return r.Key[strings.LastIndex(r.Key, ".")+1:]
}

// Flags holds the honored options read off a joined value.
type Flags struct {
	// NilSliceNull is [json.FormatNilSliceAsNull]: every slice and []byte
	// occurrence admits null.
	NilSliceNull bool
	// NilMapNull is [json.FormatNilMapAsNull]: every map occurrence admits
	// null.
	NilMapNull bool
	// OmitZeroFields is [json.OmitZeroStructFields]: every struct field
	// behaves as ,omitzero, so no required entries are emitted.
	OmitZeroFields bool
}

// Table lists every exported Options constructor in encoding/json,
// encoding/json/v2, and encoding/json/jsontext. Refused rows come in
// precedence order, so a value carrying several reports the first.
//
// StringifyNumbers stringifies numbers inside containers, beyond what the
// per-field ,string tag machinery reaches, and a marshaler's output shape is
// unknowable. The refused v1 compat rows change the marshaled shape too: a
// legacy omitempty drops fields v2 keeps, the byte-array form swaps base64
// for a number array, and ReportErrorsWithLegacySemantics makes v2 marshal a
// declaration generation refuses (a ,string tag on a bool, conflicting
// names, a tagged unexported field) in a best-effort form. DefaultOptionsV1
// bundles them all and is refused through them. The remaining v1 rows affect
// unmarshaling only, and the jsontext rows (whitespace, escaping, raw-value
// re-encoding, decoder tolerance) never change the JSON value a marshal
// writes.
var Table = []Row{
	// Package encoding/json/v2.
	{Key: "encoding/json/v2.FormatNilSliceAsNull", Class: ClassHonored, Set: boolSet(json.FormatNilSliceAsNull)},
	{Key: "encoding/json/v2.FormatNilMapAsNull", Class: ClassHonored, Set: boolSet(json.FormatNilMapAsNull)},
	{Key: "encoding/json/v2.OmitZeroStructFields", Class: ClassHonored, Set: boolSet(json.OmitZeroStructFields)},
	{Key: "encoding/json/v2.StringifyNumbers", Class: ClassRefused, Set: boolSet(json.StringifyNumbers)},
	{Key: "encoding/json/v2.Deterministic", Class: ClassIgnored, Set: boolSet(json.Deterministic)},
	{
		Key:   "encoding/json/v2.MatchCaseInsensitiveNames",
		Class: ClassIgnored,
		Set:   boolSet(json.MatchCaseInsensitiveNames),
	},
	{Key: "encoding/json/v2.RejectUnknownMembers", Class: ClassIgnored, Set: boolSet(json.RejectUnknownMembers)},
	{Key: "encoding/json/v2.WithUnmarshalers", Class: ClassIgnored, Set: func(o json.Options) bool {
		u, _ := json.GetOption(o, json.WithUnmarshalers)

		return u != nil
	}},
	{Key: "encoding/json/v2.DefaultOptionsV2", Class: ClassCombinator},
	{Key: "encoding/json/v2.JoinOptions", Class: ClassCombinator},

	// Package encoding/json (v1 compat).
	{
		Key:   "encoding/json.OmitEmptyWithLegacySemantics",
		Class: ClassRefused,
		Set:   boolSet(jsonv1.OmitEmptyWithLegacySemantics),
	},
	{Key: "encoding/json.FormatByteArrayAsArray", Class: ClassRefused, Set: boolSet(jsonv1.FormatByteArrayAsArray)},
	{
		Key:   "encoding/json.FormatBytesWithLegacySemantics",
		Class: ClassRefused,
		Set:   boolSet(jsonv1.FormatBytesWithLegacySemantics),
	},
	{
		Key:   "encoding/json.StringifyWithLegacySemantics",
		Class: ClassRefused,
		Set:   boolSet(jsonv1.StringifyWithLegacySemantics),
	},
	{
		Key:   "encoding/json.CallMethodsWithLegacySemantics",
		Class: ClassRefused,
		Set:   boolSet(jsonv1.CallMethodsWithLegacySemantics),
	},
	{
		Key:   "encoding/json.ReportErrorsWithLegacySemantics",
		Class: ClassRefused,
		Set:   boolSet(jsonv1.ReportErrorsWithLegacySemantics),
	},
	{Key: "encoding/json.FormatDurationAsNano", Class: ClassRefused, Set: boolSet(jsonv1.FormatDurationAsNano)},
	{Key: "encoding/json.DefaultOptionsV1", Class: ClassRefused},
	{
		Key:   "encoding/json.MatchCaseSensitiveDelimiter",
		Class: ClassIgnored,
		Set:   boolSet(jsonv1.MatchCaseSensitiveDelimiter),
	},
	{Key: "encoding/json.MergeWithLegacySemantics", Class: ClassIgnored, Set: boolSet(jsonv1.MergeWithLegacySemantics)},
	{
		Key:   "encoding/json.ParseBytesWithLooseRFC4648",
		Class: ClassIgnored,
		Set:   boolSet(jsonv1.ParseBytesWithLooseRFC4648),
	},
	{
		Key:   "encoding/json.ParseTimeWithLooseRFC3339",
		Class: ClassIgnored,
		Set:   boolSet(jsonv1.ParseTimeWithLooseRFC3339),
	},
	{
		Key:   "encoding/json.UnmarshalArrayFromAnyLength",
		Class: ClassIgnored,
		Set:   boolSet(jsonv1.UnmarshalArrayFromAnyLength),
	},

	// A marshaler's output shape is unknowable. It sits after the boolean
	// refusals so a value carrying both reports the boolean.
	{Key: "encoding/json/v2.WithMarshalers", Class: ClassRefused, Set: func(o json.Options) bool {
		m, _ := json.GetOption(o, json.WithMarshalers)

		return m != nil
	}},

	// Package encoding/json/jsontext.
	{
		Key:   "encoding/json/jsontext.AllowDuplicateNames",
		Class: ClassIgnored,
		Set:   boolSet(jsontext.AllowDuplicateNames),
	},
	{Key: "encoding/json/jsontext.AllowInvalidUTF8", Class: ClassIgnored, Set: boolSet(jsontext.AllowInvalidUTF8)},
	{
		Key:   "encoding/json/jsontext.CanonicalizeRawFloats",
		Class: ClassIgnored,
		Set:   boolSet(jsontext.CanonicalizeRawFloats),
	},
	{
		Key:   "encoding/json/jsontext.CanonicalizeRawInts",
		Class: ClassIgnored,
		Set:   boolSet(jsontext.CanonicalizeRawInts),
	},
	{Key: "encoding/json/jsontext.EscapeForHTML", Class: ClassIgnored, Set: boolSet(jsontext.EscapeForHTML)},
	{Key: "encoding/json/jsontext.EscapeForJS", Class: ClassIgnored, Set: boolSet(jsontext.EscapeForJS)},
	{Key: "encoding/json/jsontext.Multiline", Class: ClassIgnored, Set: boolSet(jsontext.Multiline)},
	{Key: "encoding/json/jsontext.PreserveRawStrings", Class: ClassIgnored, Set: boolSet(jsontext.PreserveRawStrings)},
	{Key: "encoding/json/jsontext.ReorderRawObjects", Class: ClassIgnored, Set: boolSet(jsontext.ReorderRawObjects)},
	{Key: "encoding/json/jsontext.SpaceAfterColon", Class: ClassIgnored, Set: boolSet(jsontext.SpaceAfterColon)},
	{Key: "encoding/json/jsontext.SpaceAfterComma", Class: ClassIgnored, Set: boolSet(jsontext.SpaceAfterComma)},
	{Key: "encoding/json/jsontext.WithIndent", Class: ClassIgnored, Set: stringSet(jsontext.WithIndent)},
	{Key: "encoding/json/jsontext.WithIndentPrefix", Class: ClassIgnored, Set: stringSet(jsontext.WithIndentPrefix)},
}

// boolSet builds the probe for a boolean constructor.
func boolSet(ctor func(bool) json.Options) func(json.Options) bool {
	return func(o json.Options) bool {
		v, _ := json.GetOption(o, ctor)

		return v
	}
}

// stringSet builds the probe for a string constructor, set when non-empty.
func stringSet(ctor func(string) json.Options) func(json.Options) bool {
	return func(o json.Options) bool {
		v, _ := json.GetOption(o, ctor)

		return v != ""
	}
}

// Refused reports the name of the first refused row set on opts, in table
// order, and whether there is one. GetOption sees through a joined
// DefaultOptionsV1 bundle, so probing the constituents refuses the bundle.
func Refused(opts json.Options) (string, bool) {
	for _, row := range Table {
		if row.Class == ClassRefused && row.Set != nil && row.Set(opts) {
			return row.Name(), true
		}
	}

	return "", false
}

// Honored reads the three honored flags off opts.
func Honored(opts json.Options) Flags {
	nilSlice, _ := json.GetOption(opts, json.FormatNilSliceAsNull)
	nilMap, _ := json.GetOption(opts, json.FormatNilMapAsNull)
	omitZero, _ := json.GetOption(opts, json.OmitZeroStructFields)

	return Flags{NilSliceNull: nilSlice, NilMapNull: nilMap, OmitZeroFields: omitZero}
}
