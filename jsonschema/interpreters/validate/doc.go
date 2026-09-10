// Package validate provides a [jsonschema.TagInterpreter] that maps
// go-playground/validator/v10 struct tag syntax to JSON Schema constraints.
//
// It does not import or depend on the validator library. It is a pure
// tag-syntax-to-schema mapper that adopts the validator tag naming convention
// for ecosystem consistency, so users who already annotate structs with
// validate tags get schema generation for free.
//
// The interpreter declares each constraint as a fact on the field's authored
// canvas ([jsonschema.FieldContext.Canvas]) rather than mutating a merged
// schema, and generation composes those facts with the field's type-derived
// schema. On a nilable field that composition splits across the null encoding.
// A value constraint such as eq or oneof lands on the value branch of the
// anyOf[value, null] wrapper, so the permitted null stays valid. A forbidden
// value (ne) and length or numeric bounds move to the wrapper itself. A nilable
// slice, map, or []byte carrying no const or enum takes the ["null", base] type
// list instead, where every keyword is a plain sibling of that list. Element
// constraints (dive, and oneof on a sequence)
// reach the element schemas through the field's element contexts.
//
// # Usage
//
// Register the interpreter when generating a schema:
//
//	schema, err := jsonschema.GenerateFor[MyType](ctx,
//	    jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
//	)
//
// # Supported Tags
//
// Tags are type-aware: the same tag (e.g., min) maps to different JSON Schema
// keywords depending on the Go field type.
//
// Presence:
//
//   - required: adds the field to the parent's "required" array, even where
//     json:",omitempty" or json:",omitzero" would normally exclude it. A
//     property whose value is null satisfies that entry on its own, so wherever
//     the occurrence admits null the interpreter also forbids it: a string,
//     number, bool, slice, map, []byte, struct, text-marshaling type, or
//     interface, as a pointer or bare. A pointer is nilable, and so are a
//     bare slice, map, and []byte, each of which carries a null branch of its
//     own, and so is an interface. Encoding/json writes null for a nil value,
//     which go-playground's required rejects. Where the occurrence admits no
//     null, as under a type schema declaring NullForbidden, the interpreter
//     writes no forbidden null and the type rejects a null instance on its
//     own. A raw JSON value is the one shape that forbids nothing, below.
//
//     A non-pointer field also gets a type-specific non-zero constraint:
//     minLength: 1 for strings, minItems: 1 for slices,
//     minProperties: 1 for maps, const: true for bools, and a not forbidding 0
//     for numbers. On a bare container that constraint measures a size, which a
//     null instance does not carry, so the field needs the forbidden null
//     beside it. A fixed array gets no size constraint: its schema pins the
//     length already, and go-playground's required on an array, which rejects
//     the zero array, has no schema form. A pointer field gets the forbidden
//     null and no such constraint, since go-playground reads required on a
//     pointer as "must be non-nil" and says nothing about the pointed-to
//     value, which may be zero.
//
//     A shape with no non-zero form the schema can express gets the required
//     entry and the forbidden null alone: a struct, a text-marshaling type
//     such as [time.Time], and an opaque value such as an interface. A nil
//     pointer to one, or a nil interface, marshals as null, which
//     go-playground's required rejects, while a non-nil one may hold the zero
//     value and passes there, so no floor or forbidden zero follows the null.
//     A reference to a $defs entry takes the form the definition declares, so
//     a reference to a struct definition is in this list while a reference to
//     a string definition gets the string floor beside its $ref. A byte slice
//     falls on one side or the other, depending on its schema. A []byte
//     encodes as a base64 string and gets minLength: 1 on that string, while
//     a byte-slice type whose schema is not a string (json.RawMessage) gets
//     neither the floor nor the forbidden null, since a RawMessage holding
//     the literal null is a non-nil value go-playground accepts.
//
//     A dive carries the whole rule onto the element schemas, so
//     dive,required on a [][]string forbids null and floors the size on each
//     inner slice.
//
// String constraints:
//
//   - min=N / gte=N: minLength
//   - max=N / lte=N: maxLength
//   - len=N: minLength and maxLength
//   - gt=N: minLength: N+1
//   - lt=N: maxLength: N-1
//   - oneof=a b c: enum (space-separated values; single-quoted runs group
//     multi-word values, e.g. oneof='New York' Boston). A second oneof
//     narrows the first to the values both list, as go-playground applies
//     each in turn, and one sharing no value with it is a conflict.
//   - eq=val: const
//   - ne=val: forbids the value via not (not.const for a single value, composed
//     into not.enum or allOf when several values are forbidden, e.g. required+ne)
//
// Numeric constraints:
//
//   - min=N / gte=N: minimum
//   - max=N / lte=N: maximum
//   - gt=N: exclusiveMinimum
//   - lt=N: exclusiveMaximum
//   - oneof=1 2 3: enum (space-separated, parsed as integers). Each token
//     must be the canonical spelling go-playground compares the value's
//     text against, so +1 and 01 are errors rather than an enum that
//     admits a value go-playground rejects. On a float kind oneof is an
//     error ([ErrOneOfKind]), since go-playground panics on it; the
//     jsonschema tag's enum= lists float values.
//   - eq=N: const
//   - ne=N: forbids the value via not (not.const for a single value, composed
//     into not.enum or allOf when several values are forbidden, e.g. required+ne)
//   - len=N: const (value equals N)
//
// Numeric, length, and count bounds are contributed through the field's shared
// Constraints facade, so this interpreter applies the one 2^53
// exact-representability policy and the one intersection the jsonschema tag and
// the Go kind also merge through, rather than a private bound path.
//
// Every integer literal is a JSON integer: an optional minus, then a single
// zero or a digit run with no leading zero. That covers a bound or an eq, ne,
// len, or oneof value on an integer-kind field, and a length or count bound
// on any field. The go-playground parser reads the parameter in base 0, where
// 010 is eight and 0x10 sixteen, and refuses a leading plus on an unsigned
// field while taking it on a signed one, so every such spelling is an error
// here rather than a number that means something else there. A minus sign
// on an unsigned field is an error too ([ErrUnsignedLiteral]), -0 included,
// which the shared literal grammar would otherwise read as zero. A
// float-kind field takes the decimal spellings strconv reads, plus included.
//
// Numeric bounds intersect with the bounds derived from the field's Go type:
// a tag bound wider than the type's range clamps to the type limit (int8 with
// max=200 emits maximum: 127), matching the jsonschema tag's bound handling.
// A float-kind bound is the shortest decimal of a value of the field's
// width, as go-playground reads the parameter at that width: lt=10.0000001
// on a float32 field is an error, since every float32 near it renders as 10
// and go-playground compares against 10 there, and so is a literal beyond
// the width's range.
// Scalar values (eq, ne, oneof, and len on a numeric field) are instead
// range-checked against the field's Go type, and a value the type cannot hold
// is an error, mirroring the jsonschema tag's const/enum behavior. A float
// eq or ne value takes the same width rule as a bound.
//
// Some fields serialize a scalar Go value as a quoted string, so the generated
// schema has type string: a json:",string" numeric or bool field, and equally a
// numeric or bool type that marshals itself as text. The rule is stated in terms
// of that *shape* rather than of the json tag, so both are covered by one
// behavior. Scalar value rules (eq, ne, oneof, len, and required's non-zero
// check) compare against the serialized form: each value is parsed against the
// field's Go type (keeping the range check above), converted back to that type,
// and marshaled, so a non-canonical spelling such as eq=5.0 or eq=1e2 constrains
// the canonical text ("5", "100") the field actually emits, and a
// string-marshaling type constrains whatever text it writes rather than the
// number's own spelling. A oneof token on an integer kind is the exception:
// go-playground's oneof formats the integer with strconv and compares that
// text against the raw tokens whatever the json tag says, so a token must be
// the canonical spelling on a coerced integer as on a native one, and -0 is
// refused rather than enumerating a "0" that comparison never matches.
//
// A coerced float has two serializations of its zero, since Go's negative zero
// compares equal to zero and encoding/json writes the sign bit for it. The
// forbid side names both texts, so required and ne=0 emit not.enum ["0", "-0"].
// That matches go-playground, which rejects the negative zero wherever it
// rejects zero. The pin side names the canonical text alone, so eq=0 emits
// const "0" and a oneof listing zero enumerates "0". Either one rejects a "-0"
// instance. For eq=0 that is stricter than go-playground, which accepts the
// value.
//
// Numeric bounds (min, max, gt, lt, gte, lte) have no faithful mapping onto that
// serialized string -- minimum and friends constrain JSON numbers, not the
// quoted instance -- so they are rejected with an error rather than silently
// dropped as an inert numeric keyword on a string schema.
//
// The format, pattern, and content tags are errors on such a field too
// ([ErrStringRuleKind]). Go-playground runs every string validator against
// the Go value by kind rather than against the text the field marshals, so
// number and numeric accept every numeric field and the other string
// validators reject every one, whatever the text says. A keyword over that
// text would agree with neither verdict. The refusal keys on the Go kind, so
// a quoted [encoding/json.Number], a string kind whose text go-playground
// does read, keeps its string validators. A non-string kind that marshals
// itself as text, such as [time.Time], takes the same refusal, since
// go-playground runs the validators over reflect's description of the value
// and panics on json. So does a []byte field for the format and pattern tags
// and for base64, since go-playground runs them over reflect's description of
// the slice, which no base64 text equals, and panics on uri and url. The json
// tag stays on a []byte: it reads the raw bytes there, as contentMediaType
// reads the decoded content.
//
// A json:",string" string field double-encodes (the value abc marshals as the
// JSON string "\"abc\""), so its scalar rules (eq, ne, oneof, and required's
// non-zero check) compare against that quoted text, while the rules that would
// measure or match the unquoted value -- the length bounds and the string
// keywords -- are rejected with an error for the same reason numeric bounds
// are above.
//
// An [encoding/json.Number] is the one Go string kind exempt from that rule.
// [encoding/json] writes it as the number it holds, so a quoted one emits its
// literal once-quoted and follows the coerced-numeric rules above instead. A
// bare one takes the numeric rules on the number's value, while go-playground
// validates it as the string kind it is (rune length for the bounds, text
// equality for eq, ne, and oneof), so the two sides disagree on such a field.
// A oneof token outside the JSON number grammar is refused, since
// [encoding/json] writes no such literal and no marshaled value equals it.
//
// Length and size bounds (minLength/maxLength, minItems/maxItems,
// minProperties/maxProperties) from several rules in one tag intersect
// independently of order: a floor only rises and a ceiling only falls, and len=N
// pins both to N. A len incompatible with a min/max or required therefore yields
// an unsatisfiable range rather than overriding the other bound. A ceiling rule
// that resolves below zero (lt<=0, or a negative max/lte) likewise yields an
// unsatisfiable range, since go-playground rejects every value of such a field
// including the empty one, rather than clamping to a permissive maxLength: 0.
//
// Boolean constraints:
//
//   - eq=true / eq=false: const
//   - ne=true / ne=false: not (forbids the value)
//
// A oneof on a bool is an error ([ErrOneOfKind]), since go-playground panics
// on it; eq pins one value, and the jsonschema tag's enum= lists both.
//
// Combining required with eq=false on a bool is an error
// ([ErrConflictingConstraints]): required on a bool pins the value to true,
// which contradicts a const of false.
//
// Array/slice constraints:
//
//   - min=N / gte=N: minItems
//   - max=N / lte=N: maxItems
//   - len=N: minItems and maxItems
//   - gt=N: minItems: N+1
//   - lt=N: maxItems: N-1
//   - eq=N: minItems and maxItems (length equals N)
//   - ne=N: not (forbids length N)
//   - unique: uniqueItems: true. The unique=<field> form asserts uniqueness of
//     one named field across struct elements, which uniqueItems (whole-element
//     comparison) cannot express, so it is an error rather than being silently
//     weakened. On a shape with no array to constrain -- a string, number,
//     bool, or struct -- unique is likewise an error; a map is the one
//     exception, documented under the map constraints below. A slice,
//     array, or map whose elements a map cannot key (a map, slice, or func
//     element, behind a pointer or not) is an error too ([ErrUniqueKind]),
//     since go-playground hashes each element and panics there.
//   - oneof=a b c: enum on the item schemas, parsed against the element type
//     (each element must be one of the values; [][]T descends to the innermost
//     element schema). A []byte field has no item schema (it encodes as a
//     base64 string), so oneof on it is an error.
//
// Map constraints:
//
//   - min=N / gte=N: minProperties
//   - max=N / lte=N: maxProperties
//   - len=N: minProperties and maxProperties
//   - gt=N: minProperties: N+1
//   - lt=N: maxProperties: N-1
//   - eq=N: minProperties and maxProperties (entry count equals N)
//   - ne=N: not (forbids entry count N)
//   - unique: a documented no-op. Unlike the shapes where unique is rejected,
//     go-playground's unique-on-map does mean something -- the map's values must
//     be distinct -- but JSON Schema has no object-side counterpart to
//     uniqueItems, so there is nothing faithful to emit.
//
// Format tags (mapped to "format"):
//
//   - email, url (-> "uri"), uri (-> "uri-reference"), uuid, ipv4, ipv6, hostname
//
// hostname maps to the RFC 1123 hostname format, which admits a label
// beginning with a digit; go-playground's hostname is the RFC 952 grammar,
// which does not, so "1host" passes the schema and fails go-playground.
//
// The uri tag maps to the uri-reference format rather than uri, because
// go-playground's uri is [net/url.ParseRequestURI], which accepts an absolute
// path such as "/a" that the uri format rejects. The wider format admits the
// relative references go-playground refuses ("a/b", "?q", "#f", and the
// empty string), so those pass the schema and fail go-playground. The url
// tag maps to the uri format, which admits a bare scheme ("http:") that
// go-playground's url refuses.
//
// The mapping tightens as well. Go-playground's url and uri are [net/url]
// parses, which take a space, a non-ASCII character, or a "|" as written,
// while the uri and uri-reference formats hold the RFC 3986 grammar, which
// admits those characters only percent-encoded. So "http://example.com/a b"
// passes go-playground's url and uri and fails both formats. The ipv4 tag
// maps to the ipv4 format, which reads dotted-quad text alone, while
// go-playground's ipv4 is [net.ParseIP] followed by To4, which also takes
// the IPv4-mapped IPv6 text "::ffff:1.2.3.4"; its ipv6 refuses that text
// and the ipv6 format accepts it. A format asserts only where the run
// checks formats, under Draft-07 by default and under Draft 2020-12 with
// [jsonschema.WithFormats] or the format-assertion vocabulary, and is an
// annotation otherwise.
//
// Two validators in one tag that map to one keyword, such as email and url on
// format or alpha and numeric on pattern, are refused with
// [ErrRepeatedKeyword]. A schema carries one value per keyword, and
// go-playground applies both validators, so keeping either one alone would
// silently drop a constraint.
//
// Pattern tags (mapped to "pattern"):
//
//   - alpha: ^[a-zA-Z]+$
//   - alphanum: ^[a-zA-Z0-9]+$
//   - numeric: ^[-+]?[0-9]+(?:\.[0-9]+)?$
//   - number: ^[0-9]+$
//   - ascii: ^[\x00-\x7F]*$
//
// Content tags:
//
//   - json (-> contentMediaType: "application/json")
//   - base64 (-> contentEncoding: "base64")
//
// On a raw JSON field (json.RawMessage) both are documented no-ops, like
// unique on a map: the rules are real runtime checks over the raw bytes, but
// the content keywords describe a string carrying an encoded document, and a
// raw field's instance is whatever JSON value it holds, already decoded, so
// there is nothing faithful to emit.
//
// # Dive
//
// The dive tag descends into the element type of a slice, array, or map,
// applying subsequent constraints to the items or additionalProperties
// sub-schema. Multiple dive tags can be chained for nested containers:
//
//	Tags [][]string `validate:"min=1,dive,max=5,dive,min=3"`
//
// This produces minItems: 1 on the outer slice, maxItems: 5 on the inner slice
// schema (the outer slice's items), and minLength: 3 on the string element
// schema.
//
// For maps, dive descends into the additionalProperties sub-schema (the value
// type). When dive descends through a pointer element type (e.g., []*int),
// constraints after dive apply to the underlying type's schema.
//
// Descending is all dive does. A constraint written after it runs against the
// element through the same path a sequence-wide rule takes, so on a slice or
// array `oneof=a b` and `dive,oneof=a b` produce identical schemas, and each
// element's own shape (including a string-coerced or text-marshaling one)
// decides how the constraint applies. Two divergences from that are deliberate:
// on a map, dive descends to the values while a bare oneof is an error, because
// dive says "descend" explicitly and a bare oneof on a map has no go-playground
// element meaning; and on a []byte both forms are an error, because the field
// encodes as a single base64 string with no element schema either one could
// reach.
//
// # Skipped and Unrecognized Tags
//
// Some tags carry no JSON Schema representation and are skipped: cross-field and
// conditional validators (eqfield, required_if, skip_unless, ...), control tags
// that govern when validation runs (omitempty, structonly, ...), and the
// constraints inside a keys...endkeys block (map-key constraints are not
// modeled). A block runs from the keys to the first endkeys, or to the end
// of the tag when none follows, as go-playground's parser collects it, so
// every part between is a key constraint and is skipped once it names a
// validator go-playground registers, since go-playground parses the block
// with the same lookup, and a keys inside the block closes on that same
// first endkeys. A keys not immediately
// following a dive is an error ([ErrKeysPlacement]), inside a block as much
// as outside one, as go-playground refuses it, and so is a keys after a dive
// into a slice or array, since only go-playground's map branch reads a block
// and the others dereference a nil validation, and so is a keys that is the
// last part of the tag, since go-playground's collector reads at least one
// part after it. An endkeys with no keys block
// open is an error too ([ErrEndkeysPlacement]) unless it is the last part
// of the tag, where go-playground's parser ends the chain and the
// interpreter skips it. A control tag, dive,
// keys, and endkeys are each matched as a whole
// part, as go-playground matches them, so one carrying a parameter
// (omitempty=) is an unrecognized validator rather than the tag it starts
// with, and so is one written as an OR alternative (omitempty|min=1), which
// go-playground looks up as a validator and refuses. A trailing dive with
// nothing after it applies nothing to the
// elements and is a no-op, as it is in go-playground, while a dive on a field
// with no elements to reach is an error whatever follows it, since
// go-playground panics on a dive over anything but a slice, array, or map. A
// blank or "-" part is skipped, and a key is trimmed of surrounding
// whitespace, where go-playground refuses the tag. A parameter keeps its
// whitespace, since go-playground compares against the padded literal: eq=a
// followed by a space pins "a " and eq= followed by a space pins the single
// space. The | OR operator is not modeled
// either: within a single comma group the pipe separates OR alternatives, of
// which only the first is interpreted, so later comma-separated constraints
// still apply; an alternative with no key, or one naming a validator neither
// side knows, anywhere in the group, is an error, since go-playground looks
// every alternative up and refuses the tag.
//
// Any other key that is not a recognized constraint causes Interpret to return
// [ErrUnrecognizedValidator] rather than being silently consumed, so a typo'd
// or unsupported validator surfaces at generation time instead of yielding a
// schema that quietly drops the intended constraint.
//
// # Implementation
//
// This package owns this dialect's grammar and nothing more: splitting the tag,
// the OR and escape handling, the dive and keys blocks, the skipped tags, and a
// table naming the operation each validator spells. What an operation does to a
// given field is the shared constraint model's, which the jsonschema struct tag
// runs through as well, so the two dialects cannot drift on a rule they both
// express. Every constraint is contributed through [jsonschema.Constraints];
// this package holds no scalar parser and writes no schema keyword directly,
// which is what keeps the two interpretations one.
package validate
