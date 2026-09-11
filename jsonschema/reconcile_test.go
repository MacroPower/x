package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// The split probe element types carry a type-level schema through
// [jsonschema.WithTypeSchema]. One per JSON type family is enough: each case
// runs its own generation, so the same Go type can carry a different type-level
// schema in each.
type (
	splitNumber float64
	splitString string
	splitList   []string
	splitObject map[string]string
)

// splitProbe is one instance value fed to both occurrences, with the verdict
// both must return.
type splitProbe struct {
	value any
	valid bool
}

// splitCase is one authorable keyword's differential case: a type-level value
// supplied uniformly through [jsonschema.WithTypeSchema], the same authored
// value on a T field and a *T field, and the instances both must agree on.
//
// Field order is tuned for struct packing (govet fieldalignment); the grouping
// is not semantic.
type splitCase struct {
	// The elem type is the field's Go type, which the type-level schema overrides.
	elem reflect.Type
	// Every occurrence of elem derives from the typeValue schema.
	typeValue *jsonschema.Schema
	// The canvas hook authors a keyword the jsonschema tag cannot spell, by
	// writing the field's authored canvas from a tag interpreter.
	canvas func(*jsonschema.Schema)
	// The constrain hook authors through the constraints facade instead, for
	// a keyword whose authored form is a facade placement rather than a
	// canvas write, such as the not branch ForbidSchema lands under allOf.
	constrain func(*jsonschema.Constraints) error
	// The ok value satisfies both occurrences, so a probe on one field can hold
	// the other field valid.
	ok any
	// The tag is the jsonschema struct tag both occurrences carry.
	tag string
	// The probes are the non-null instances both occurrences must agree on.
	probes []splitProbe
	// The validate options enable an opt-in assertion (format, content) so the
	// keyword under test actually gates the instance.
	validate []jsonschema.ValidateOption
}

// generate builds the two-field document schema for the case: a value field and
// a pointer field carrying the identical authored value over the identical
// type-level schema. The struct type is assembled reflectively so the authored
// tag can vary per case.
func (c splitCase) generate(t *testing.T) *jsonschema.Schema {
	t.Helper()

	field := func(name, jsonName string, t reflect.Type) reflect.StructField {
		tag := `json:"` + jsonName + `"`
		if c.tag != "" {
			tag += ` jsonschema:"` + c.tag + `"`
		}

		if c.canvas != nil || c.constrain != nil {
			tag += ` probe:"canvas"`
		}

		return reflect.StructField{Name: name, Type: t, Tag: reflect.StructTag(tag)}
	}

	opts := []jsonschema.GenerateOption{
		jsonschema.WithTypeSchema(c.elem, jsonschema.TypeSchema{Value: c.typeValue}),
	}

	if c.canvas != nil || c.constrain != nil {
		opts = append(opts, jsonschema.WithTagInterpreter("probe", jsonschema.TagInterpreterFunc(
			func(_ context.Context, fc jsonschema.FieldContext, _ jsonschema.Tag) error {
				if c.canvas != nil {
					c.canvas(fc.Canvas)
				}

				if c.constrain != nil {
					return c.constrain(fc.Constraints())
				}

				return nil
			},
		)))
	}

	doc := reflect.StructOf([]reflect.StructField{
		field("Plain", "plain", c.elem),
		field("Ptr", "ptr", reflect.PointerTo(c.elem)),
	})

	s, err := jsonschema.Generate(t.Context(), doc, opts...)
	require.NoError(t, err)

	return s
}

var (
	// The splitCases table holds one differential case per authorable keyword
	// whose assertion an instance can observe. TestReconcileSplitCoversAuthored
	// checks the table against [keywordmeta.Authored] as an exact partition with
	// splitSkips, so a new authorable keyword cannot land uncovered.
	splitCases = map[string]splitCase{
		keyword.Minimum: {
			elem:      reflect.TypeFor[splitNumber](),
			typeValue: &jsonschema.Schema{Type: "number", Minimum: new(0.0)},
			tag:       "minimum=5",
			ok:        10,
			probes: []splitProbe{
				{value: 5, valid: true},
				{value: 4, valid: false},
				{value: 0, valid: false},
			},
		},
		keyword.Maximum: {
			elem:      reflect.TypeFor[splitNumber](),
			typeValue: &jsonschema.Schema{Type: "number", Maximum: new(100.0)},
			tag:       "maximum=50",
			ok:        10,
			probes: []splitProbe{
				{value: 50, valid: true},
				{value: 51, valid: false},
				{value: 100, valid: false},
			},
		},
		keyword.ExclusiveMinimum: {
			// The authored endpoint changes which keyword represents the lower side
			// (kind minimum cleared, exclusiveMinimum written), the case the split's
			// deliberate-removal guard exists for.
			elem:      reflect.TypeFor[splitNumber](),
			typeValue: &jsonschema.Schema{Type: "number", Minimum: new(0.0)},
			tag:       "exclusiveMinimum=5",
			ok:        10,
			probes: []splitProbe{
				{value: 6, valid: true},
				{value: 5, valid: false},
				{value: 0, valid: false},
			},
		},
		keyword.ExclusiveMaximum: {
			elem:      reflect.TypeFor[splitNumber](),
			typeValue: &jsonschema.Schema{Type: "number", Maximum: new(100.0)},
			tag:       "exclusiveMaximum=50",
			ok:        10,
			probes: []splitProbe{
				{value: 49, valid: true},
				{value: 50, valid: false},
				{value: 100, valid: false},
			},
		},
		keyword.MultipleOf: {
			// The replace case the whole table exists for: conjoining the authored 6
			// with the type's 4 would accept only multiples of 12.
			elem:      reflect.TypeFor[splitNumber](),
			typeValue: &jsonschema.Schema{Type: "number", MultipleOf: new(4.0)},
			tag:       "multipleOf=6",
			ok:        6,
			probes: []splitProbe{
				{value: 6, valid: true},
				{value: 18, valid: true},
				{value: 4, valid: false},
			},
		},
		keyword.MinLength: {
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", MinLength: new(1)},
			tag:       "minLength=3",
			ok:        "abcd",
			probes: []splitProbe{
				{value: "abc", valid: true},
				{value: "ab", valid: false},
			},
		},
		keyword.MaxLength: {
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", MaxLength: new(10)},
			tag:       "maxLength=4",
			ok:        "abc",
			probes: []splitProbe{
				{value: "abcd", valid: true},
				{value: "abcde", valid: false},
			},
		},
		keyword.Pattern: {
			// Disjoint patterns: conjoining them rejects every string.
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", Pattern: "^[0-9]+$"},
			tag:       "pattern=^[a-z]+$",
			ok:        "abc",
			probes: []splitProbe{
				{value: "abc", valid: true},
				{value: "123", valid: false},
			},
		},
		keyword.Format: {
			// Disjoint formats, asserted on demand: conjoining them rejects every
			// string.
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", Format: "ipv4"},
			tag:       "format=email",
			ok:        "a@b.example",
			probes: []splitProbe{
				{value: "a@b.example", valid: true},
				{value: "1.2.3.4", valid: false},
			},
			validate: []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
		},
		// The const tag authors the same value the type-level schema already
		// pins, and the enum tag is the sole author over a bare typed schema:
		// a disagreeing authored const and any authored enum beside the type's
		// own are ErrConstraintConflict at tag application, so these are the
		// only compositions whose split an instance can observe.
		keyword.Const: {
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", Const: new(any("fixed"))},
			tag:       "const=fixed",
			ok:        "fixed",
			probes: []splitProbe{
				{value: "fixed", valid: true},
				{value: "other", valid: false},
			},
		},
		keyword.Enum: {
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string"},
			tag:       "enum=a|b",
			ok:        "a",
			probes: []splitProbe{
				{value: "b", valid: true},
				{value: "c", valid: false},
			},
		},
		keyword.MinItems: {
			elem:      reflect.TypeFor[splitList](),
			typeValue: &jsonschema.Schema{Type: "array", MinItems: new(1)},
			tag:       "minItems=2",
			ok:        []any{"a", "b"},
			probes: []splitProbe{
				{value: []any{"a", "b"}, valid: true},
				{value: []any{"a"}, valid: false},
			},
		},
		keyword.MaxItems: {
			elem:      reflect.TypeFor[splitList](),
			typeValue: &jsonschema.Schema{Type: "array", MaxItems: new(5)},
			tag:       "maxItems=2",
			ok:        []any{"a"},
			probes: []splitProbe{
				{value: []any{"a", "b"}, valid: true},
				{value: []any{"a", "b", "c"}, valid: false},
			},
		},
		keyword.UniqueItems: {
			elem:      reflect.TypeFor[splitList](),
			typeValue: &jsonschema.Schema{Type: "array"},
			tag:       "uniqueItems=true",
			ok:        []any{"a", "b"},
			probes: []splitProbe{
				{value: []any{"a", "b"}, valid: true},
				{value: []any{"a", "a"}, valid: false},
			},
		},
		keyword.MinProperties: {
			elem:      reflect.TypeFor[splitObject](),
			typeValue: &jsonschema.Schema{Type: "object", MinProperties: new(1)},
			tag:       "minProperties=2",
			ok:        map[string]any{"a": "1", "b": "2"},
			probes: []splitProbe{
				{value: map[string]any{"a": "1", "b": "2"}, valid: true},
				{value: map[string]any{"a": "1"}, valid: false},
			},
		},
		keyword.MaxProperties: {
			elem:      reflect.TypeFor[splitObject](),
			typeValue: &jsonschema.Schema{Type: "object", MaxProperties: new(5)},
			tag:       "maxProperties=2",
			ok:        map[string]any{"a": "1"},
			probes: []splitProbe{
				{value: map[string]any{"a": "1", "b": "2"}, valid: true},
				{value: map[string]any{"a": "1", "b": "2", "c": "3"}, valid: false},
			},
		},
		keyword.ContentEncoding: {
			// The jsonschema tag cannot spell the content keywords, so the canvas
			// hook stands in for the interpreter that would author them.
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string"},
			canvas:    func(s *jsonschema.Schema) { s.ContentEncoding = "base64" },
			ok:        "YWJj",
			probes: []splitProbe{
				{value: "YWJj", valid: true},
				{value: "not base64!", valid: false},
			},
			validate: []jsonschema.ValidateOption{jsonschema.WithContent(true)},
		},
		keyword.ContentMediaType: {
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string"},
			canvas:    func(s *jsonschema.Schema) { s.ContentMediaType = "application/json" },
			ok:        `{"a":1}`,
			probes: []splitProbe{
				{value: `{"a":1}`, valid: true},
				{value: "not json", valid: false},
			},
			validate: []jsonschema.ValidateOption{jsonschema.WithContent(true)},
		},
		keyword.Not: {
			// The type-level not forbids one value and the facade forbids a
			// length; ForbidSchema lands the latter as allOf:[{not: ...}] on
			// the value branch, beside the type's own not, for both
			// occurrences.
			elem:      reflect.TypeFor[splitString](),
			typeValue: &jsonschema.Schema{Type: "string", Not: &jsonschema.Schema{Const: &splitReserved}},
			constrain: func(c *jsonschema.Constraints) error {
				return c.ForbidSchema(&jsonschema.Schema{MinLength: new(3), MaxLength: new(3)})
			},
			ok: "ab",
			probes: []splitProbe{
				{value: "ab", valid: true},
				{value: "abcd", valid: true},
				{value: "abc", valid: false},
				{value: "reserved", valid: false},
			},
		},
	}

	// The splitReserved value is the const the not case's type-level not
	// forbids.
	splitReserved any = "reserved"

	// The splitSkips map holds the authorable keywords the differential table
	// deliberately omits, each with the reason no differential case can be
	// written for it. A skip is a declared entry, not an absence, so an uncovered
	// keyword fails the partition instead of slipping through unnoticed.
	splitSkips = map[string]string{
		keyword.Description:   "annotation: carries no assertion, so agreement is vacuous",
		keyword.Title:         "annotation: carries no assertion, so agreement is vacuous",
		keyword.Default:       "annotation: carries no assertion, so agreement is vacuous",
		keyword.Deprecated:    "annotation: carries no assertion, so agreement is vacuous",
		keyword.ReadOnly:      "annotation: carries no assertion, so agreement is vacuous",
		keyword.WriteOnly:     "annotation: carries no assertion, so agreement is vacuous",
		keyword.Examples:      "annotation: carries no assertion, so agreement is vacuous",
		keyword.Comment:       "annotation: carries no assertion, so agreement is vacuous",
		keyword.ContentSchema: "annotation: recorded but never evaluated, so agreement is vacuous",
	}
)

// TestReconcileSplitConsistency is the differential guard over the null split's
// keyword partition, generalizing the multipleOf and pattern consistency tests:
// a value field and a pointer field carrying the identical authored value over
// the identical type-level schema must accept and reject the identical non-null
// instances. A keyword partitioned onto the wrong branch breaks this -- a
// replace-semantics keyword on the wrapper conjoins the authored value with the
// type value restored beneath it, so pointer-ness silently changes what the
// field accepts.
func TestReconcileSplitConsistency(t *testing.T) {
	t.Parallel()

	for name, tc := range splitCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := tc.generate(t)

			accepts := func(plain, ptr any) bool {
				instance := map[string]any{"plain": plain, "ptr": ptr}

				return jsonschema.Validate(t.Context(), s, instance, tc.validate...) == nil
			}

			require.True(t, accepts(tc.ok, tc.ok),
				"the baseline value must satisfy both occurrences")
			require.True(t, accepts(tc.ok, nil),
				"the pointer occurrence must still admit null")

			for _, p := range tc.probes {
				assert.Equal(t, p.valid, accepts(p.value, tc.ok),
					"value occurrence disagrees on %v", p.value)
				assert.Equal(t, p.valid, accepts(tc.ok, p.value),
					"pointer occurrence disagrees on %v", p.value)
			}
		})
	}
}

// TestReconcileSplitCoversAuthored pins the differential table against
// [keywordmeta.Authored] as an exact partition: every authorable keyword is
// either covered by a case or skipped with a reason, and nothing is both or
// neither. Asserting equality rather than containment is what stops a new
// keyword from landing uncovered.
func TestReconcileSplitCoversAuthored(t *testing.T) {
	t.Parallel()

	authored := keywordmeta.Names(keywordmeta.Authored)

	partition := slices.Sorted(maps.Keys(splitCases))
	for name := range splitSkips {
		assert.NotContains(t, splitCases, name, "keyword %q is both covered and skipped", name)

		partition = append(partition, name)
	}

	slices.Sort(partition)

	assert.Equal(t, authored, partition,
		"the covered and skipped keywords must partition keywordmeta.Authored exactly")
}

// hookOneOfLevels is a named container whose extender adds a oneOf, an
// applicator the instance type does not gate.
type hookOneOfLevels []any

func (hookOneOfLevels) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.OneOf = []*jsonschema.Schema{
		{Items: &jsonschema.Schema{Type: "string"}},
		{Items: &jsonschema.Schema{Type: "integer"}},
	}

	return nil
}

// hookNotLevels is a named container whose extender adds a not.
type hookNotLevels []any

func (hookNotLevels) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Not = &jsonschema.Schema{Items: &jsonschema.Schema{Type: "boolean"}}

	return nil
}

// TestReconcileHookApplicatorSparesNull pins that a nullable container whose
// type-level hook added an applicator the instance type does not gate takes
// the anyOf form in both definition modes, so the null the occurrence
// admits is accepted. The type-list decision used to look for const, enum,
// and allOf alone, so a hook's oneOf or not rode the ["null", "array"] list
// inline under WithDefinitions(false), judged the null, and rejected it,
// while the extracted form accepted it.
func TestReconcileHookApplicatorSparesNull(t *testing.T) {
	t.Parallel()

	type oneOfDoc struct {
		M *hookOneOfLevels `json:"m"`
	}

	type notDoc struct {
		M *hookNotLevels `json:"m"`
	}

	type nilSliceNotDoc struct {
		Tags hookNotTags `json:"tags"`
	}

	type unrestrictedEnumDoc struct {
		F hookUnrestrictedText `json:"f" jsonschema:"enum=a|b"`
	}

	type forbidLabelsDoc struct {
		L hookNilMapLabels `json:"l" nonempty:"true"`
	}

	forbidEmpty := func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
		return field.Constraints().ForbidSchema(&jsonschema.Schema{MaxProperties: new(0)})
	}

	nonempty := jsonschema.WithTagInterpreter("nonempty", jsonschema.TagInterpreterFunc(forbidEmpty))

	cases := map[string]struct {
		generate func(defs bool) (*jsonschema.Schema, error)
		valid    []string
		invalid  []string
	}{
		"a not on a definition body under nil-as-null": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[nilSliceNotDoc](t.Context(), jsonschema.WithDefinitions(defs),
					jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true)))
			},
			valid:   []string{`{"tags":null}`, `{"tags":["a"]}`},
			invalid: []string{`{"tags":[]}`},
		},
		"an enum beside a reference to an unrestricted body": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[unrestrictedEnumDoc](t.Context(), jsonschema.WithDefinitions(defs))
			},
			valid:   []string{`{"f":null}`, `{"f":"a"}`},
			invalid: []string{`{"f":"c"}`},
		},
		"an interpreter forbid beside a reference to a nil-as-null map": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[forbidLabelsDoc](t.Context(), jsonschema.WithDefinitions(defs), nonempty,
					jsonschema.WithJSONOptions(json.FormatNilMapAsNull(true)))
			},
			valid:   []string{`{"l":null}`, `{"l":{"a":"b"}}`},
			invalid: []string{`{"l":{}}`},
		},
		"a not on a pointer root": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				forbidEmptyArray := func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Not = &jsonschema.Schema{MaxItems: new(0)}

					return nil
				}

				return jsonschema.GenerateFor[*[]string](t.Context(), jsonschema.WithDefinitions(defs),
					jsonschema.WithTypeSchemaExtenderFor[[]string](jsonschema.TypeSchemaExtenderFunc(forbidEmptyArray)))
			},
			valid:   []string{`null`, `["a"]`},
			invalid: []string{`[]`},
		},
		"oneOf": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[oneOfDoc](t.Context(), jsonschema.WithDefinitions(defs))
			},
			valid:   []string{`{"m":null}`, `{"m":["a"]}`, `{"m":[1]}`},
			invalid: []string{`{"m":[true]}`},
		},
		"not": {
			generate: func(defs bool) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[notDoc](t.Context(), jsonschema.WithDefinitions(defs))
			},
			valid:   []string{`{"m":null}`, `{"m":["a"]}`},
			invalid: []string{`{"m":[true]}`},
		},
	}

	for name, tc := range cases {
		for _, defs := range []bool{false, true} {
			t.Run(name+" definitions="+strconv.FormatBool(defs), func(t *testing.T) {
				t.Parallel()

				s, err := tc.generate(defs)
				require.NoError(t, err)

				v, err := jsonschema.Compile(t.Context(), s)
				require.NoError(t, err)

				for _, instance := range tc.valid {
					err := v.ValidateJSON(t.Context(), []byte(instance))
					require.NoError(t, err, "instance %s", instance)
				}

				for _, instance := range tc.invalid {
					err := v.ValidateJSON(t.Context(), []byte(instance))
					require.Error(t, err, "instance %s", instance)
				}
			})
		}
	}
}

// hookNotTags is a named slice whose extender forbids the empty array, an
// applicator the array type does not gate, so its null under a nil-as-null
// format flag must take the anyOf form on the definition body too.
type hookNotTags []string

func (hookNotTags) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Not = &jsonschema.Schema{MaxItems: new(0)}

	return nil
}

// hookUnrestrictedText is a named string whose extender empties the schema,
// so its extracted definition is the unrestricted body that admits null on
// its own.
type hookUnrestrictedText string

func (hookUnrestrictedText) JSONSchemaExtend(
	_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema,
) error {
	*ts.Value = jsonschema.Schema{}

	return nil
}

// hookNilMapLabels is a named map with a no-op extender, so it is extracted
// and its definition body carries the null a nil-as-null format flag adds.
type hookNilMapLabels map[string]string

func (hookNilMapLabels) JSONSchemaExtend(context.Context, jsonschema.TypeContext, *jsonschema.TypeSchema) error {
	return nil
}

// hookTypedLevels is a named container whose extender declares the type
// nullable and writes the natural, redundant Type into the bare payload; the
// null encoding on its extracted def body must fold that authored type into
// the type list.
type hookTypedLevels []string

func (hookTypedLevels) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Type = "array"
	ts.Nullability = jsonschema.NullAllowed

	return nil
}

// TestReconcileHookAuthoredTypeSlot pins that the null encoding honors a
// hook-authored Type or Types on a nullable occurrence's bare payload instead
// of stacking its own write beside it. An extender may set the natural,
// redundant type slot ("add, remove, or modify any fields"); the encoding
// must still emit exactly one of Type/Types, or the generated schema fails
// to marshal ("both Type and Types are set") far from the cause. A container
// is not nullable on its own under encoding/json/v2, so the null admission
// comes from the extender's [jsonschema.NullAllowed] stance.
func TestReconcileHookAuthoredTypeSlot(t *testing.T) {
	t.Parallel()

	type doc struct {
		T []string `json:"t"`
	}

	t.Run("authored Type folds into the null type list", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[[]string](
				func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Type = "array"
					ts.Nullability = jsonschema.NullAllowed

					return nil
				},
			),
		)
		require.NoError(t, err)

		prop := s.Properties["t"]
		require.Empty(t, prop.Type)
		require.Equal(t, []string{"null", "array"}, prop.Types)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})

	t.Run("authored Types survives the bare container type restore", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[[]string](
				func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Types = []string{"array"}

					return nil
				},
			),
		)
		require.NoError(t, err)

		prop := s.Properties["t"]
		require.Empty(t, prop.Type)
		require.Equal(t, []string{"array"}, prop.Types)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})

	t.Run("authored Type on an extracted def body", func(t *testing.T) {
		t.Parallel()

		type named struct {
			L hookTypedLevels `json:"l"`
		}

		s, err := jsonschema.GenerateFor[named](t.Context())
		require.NoError(t, err)

		require.Len(t, s.Defs, 1)

		// An extracted def keeps the bare payload with the authored Type; the
		// stance's null admission rides each reference's anyOf wrapper instead
		// of the def body.
		for _, def := range s.Defs {
			require.Equal(t, "array", def.Type)
			require.Empty(t, def.Types)
		}

		prop := s.Properties["l"]
		require.Len(t, prop.AnyOf, 2)
		require.Equal(t, "null", prop.AnyOf[1].Type)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})
}

// splitExample is the string type whose inline override carries examples of
// its own in TestReconcileExamplesAuthoredByContent.
type splitExample string

// TestReconcileExamplesAuthoredByContent pins that an authored examples list
// is judged by its content, not its length: on a nullable field whose type
// carries examples of its own, a list of the same length but different
// members moves to the wrapper with the type's list restored beneath it, and
// a re-set to the type's own list leaves no wrapper sibling. The row used to
// compare lengths alone, so an equal-length list stayed on the value branch
// and replaced the type's examples while a longer one moved to the wrapper.
func TestReconcileExamplesAuthoredByContent(t *testing.T) {
	t.Parallel()

	type doc struct {
		X *splitExample `ex:"x" json:"x"`
	}

	tests := map[string]struct {
		authored []any
		want     string
	}{
		"different members of the same length": {
			authored: []any{"b"},
			want:     `{"examples":["b"],"anyOf":[{"type":"string","examples":["a"]},{"type":"null"}]}`,
		},
		"the type's own list": {
			authored: []any{"a"},
			want:     `{"anyOf":[{"type":"string","examples":["a"]},{"type":"null"}]}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(),
				jsonschema.WithTypeSchemaFor[splitExample](jsonschema.TypeSchema{
					Value: &jsonschema.Schema{Type: "string", Examples: []any{"a"}},
				}),
				jsonschema.WithTagInterpreter("ex", jsonschema.TagInterpreterFunc(
					func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
						field.Canvas.Examples = tc.authored

						return nil
					},
				)),
			)
			require.NoError(t, err)

			got, err := json.Marshal(s.Properties["x"])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// notWriter is a tag interpreter that writes a forbidden subschema straight
// to the canvas not slot rather than through the facade.
type notWriter struct{}

func (notWriter) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	field.Canvas.Not = &jsonschema.Schema{MinLength: new(5)}

	return nil
}

// TestReconcileCanvasNotSubschemaRidesValueBranch pins where a subschema an
// interpreter writes to the canvas not lands. The not slot is wrapper-scoped
// so a bare value forbid can reject a null, but a subschema naming no type
// matches null vacuously, so from the wrapper it would reject the null a
// pointer field admits. It moves under allOf on the value branch, the
// placement the facade's ForbidSchema gives it, so T and *T agree on every
// non-null instance and *T still admits null.
func TestReconcileCanvasNotSubschemaRidesValueBranch(t *testing.T) {
	t.Parallel()

	type doc struct {
		P *string `forbid:"long" json:"p"`
		S string  `forbid:"long" json:"s"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("forbid", notWriter{}))
	require.NoError(t, err)

	// The pointer field's value branch sits inside its null wrapper; the
	// plain field is its own value branch.
	branches := map[string]*jsonschema.Schema{
		"p": s.Properties["p"].AnyOf[0],
		"s": s.Properties["s"],
	}

	for name, branch := range branches {
		assert.Nil(t, s.Properties[name].Not, "property %q keeps the wrapper's not slot free", name)
		assert.Nil(t, branch.Not, "property %q keeps the value branch's not slot free", name)
		require.Len(t, branch.AllOf, 1, "property %q carries the forbid under allOf", name)
		assert.Equal(t, 5, *branch.AllOf[0].Not.MinLength)
	}

	tests := map[string]struct {
		instance map[string]any
		valid    bool
	}{
		"null pointer":     {instance: map[string]any{"p": nil, "s": "ab"}, valid: true},
		"short values":     {instance: map[string]any{"p": "ab", "s": "ab"}, valid: true},
		"long pointer":     {instance: map[string]any{"p": "abcdef", "s": "ab"}, valid: false},
		"long non-pointer": {instance: map[string]any{"p": "ab", "s": "abcdef"}, valid: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), s, tc.instance)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
