// Package jsonschema generates JSON Schema documents from Go types by
// reflection and validates JSON instances against schemas.
//
// It builds on [github.com/google/jsonschema-go/jsonschema] and adds
// customization interfaces, pluggable struct tag interpretation, Go doc
// comment extraction, Draft-07 and Draft 2020-12 support, and instance
// validation that reports every failure with its instance and schema path.
//
// This package re-exports the upstream [Schema] type through a type alias, so
// users import only this package. Two helpers ease hand-built schemas:
//
//   - The builtin new fills pointer fields, as in new(float64(0)) for
//     [Schema.Minimum].
//   - [Raw] and [MustRaw] marshal Go values for the raw-JSON fields such as
//     [Schema.Default], so jsonschema.MustRaw("15m") replaces a hand-written
//     [encoding/json.RawMessage] literal. MustRaw panics on a marshal error
//     and serves values known valid at compile time.
//
// # Entry Points
//
// The primary API is the generic function [GenerateFor]:
//
//	schema, err := jsonschema.GenerateFor[MyType](ctx)
//
// [Generate] takes a runtime [reflect.Type] for dynamic use:
//
//	schema, err := jsonschema.Generate(ctx, reflect.TypeFor[MyType]())
//
// Every form passes its context to the [DescriptionProvider] with each comment
// lookup, so the built-in provider's package loading can honor cancellation
// and deadlines. [MustGenerateFor] is [GenerateFor] with [context.Background]
// and panics on error, for package-scope variables and init-time generation,
// where a static type and fixed options either always succeed or always fail.
// [MustGenerate] is its [reflect.Type] form.
//
// Any Go type can be the root, struct or otherwise. [GenerateFor] on string
// produces {"type": "string"}, and on []int produces
// {"type": "array", "items": {"type": "integer"}}. The root schema always
// carries the $schema keyword. Sub-schemas and $defs entries never do.
//
// These one-shot forms apply their options per call. [NewGenerator] applies
// one option set once and returns a reusable [Generator], the generation-side
// counterpart of [Compile] and [Validator]. A Generator is safe for concurrent
// use when its configured hooks are. [GenerateWith] runs GenerateFor under a
// Generator, since Go methods cannot take type parameters:
//
//	gen := jsonschema.NewGenerator(opts...)
//	schema, err := jsonschema.GenerateWith[MyType](ctx, gen)
//
// # Errors
//
// Sentinel errors support matching with [errors.Is]:
//
//   - [ErrUnsupportedType] reports a Go type whose values [encoding/json/v2]
//     refuses: a func, chan, complex, or [unsafe.Pointer] kind, or
//     [time.Duration], which v2's native codec refuses with no format.
//   - [ErrUnsupportedMapKey] reports a map key [encoding/json/v2] cannot
//     encode as an object member name. V2 accepts a string, integer, or float
//     kind and any marshaler-bearing key. It refuses the exact [time.Duration]
//     key, since its native duration codec pre-empts the integer-kind
//     encoding.
//   - [ErrInvalidJSONField] reports a struct declaration or field
//     [encoding/json/v2] refuses: a malformed json tag, two fields of one
//     struct claiming one JSON name, a tagged unexported field, an invalid
//     embedded field or fallback (two fallback fields in one declaration, a
//     non-qualifying type or key under json:",embed", or json:",embed"
//     combined with a name or another option), json:",string" on a field that
//     encodes no number, a format tag option (cut from stable v2,
//     go.dev/issue/79071), or a struct with fields but none serializable
//     (every field unexported and untagged).
//
// Each of the three verdicts is v2's own, and generation reports v2's reason.
// No user marshal method runs during generation. Two gaps remain. A func
// field under omitzero marshals, since v2 never writes it, while generation
// refuses the func type. A type with a direct JSON marshaler marshals without
// v2 reading its field declarations, while generation reflects those fields
// and applies the field-set checks to them, so its refusal there is a
// conservative superset of v2's.
//
//   - [ErrProviderPanic] wraps a panic recovered from a [JSONSchemaProvider]
//     or [JSONSchemaExtender] method.
//   - [ErrConflictingTypeSchema] reports a malformed [TypeSchema] from a
//     type-level hook: more than one of Value, Verbatim, or Ref set, a Ref
//     naming a type that is not extractable to $defs, or a Ref alias chain
//     that cycles back to its own type.
//   - [ErrInvalidDefaultsInstance] reports a [WithDefaultsFrom] instance that
//     does not match the generated root type or does not marshal to a JSON
//     object.
//
// Every error carries the path to the offending field, as in
// "field \"data\": unsupported type".
//
// # Options
//
// Functional [GenerateOption] values configure [GenerateFor], [Generate], and
// [NewGenerator]:
//
//   - [WithDraft] sets the target draft ([Draft7] or [Draft2020]). The
//     returned [DraftOption] also serves validation and inlining, where it
//     overrides $schema draft detection (see Drafts below).
//   - [WithTagInterpreter] registers a [TagInterpreter] under the struct tag
//     key it reads (see Tag Interpreters below).
//   - [WithDescriptionProvider] sets the [DescriptionProvider] that supplies
//     type and field descriptions. [NewGoCommentProvider] constructs the
//     provider that extracts Go doc comments.
//   - [WithTypeSchema] overrides the schema for one Go type with a
//     [TypeSchema] (a bare value plus a [Nullability] stance, a
//     [TypeSchema.Verbatim] escape hatch, or a [TypeSchema.Ref] alias).
//     [WithTypeSchemaFor] is its generic form for a statically known type.
//   - [WithTypeSchemaProvider] registers a [TypeSchemaProvider] that supplies
//     schemas for whole families of types by predicate. It shares the
//     highest-priority resolution step with [WithTypeSchema].
//   - [WithTypeSchemaExtender] registers a [TypeSchemaExtender] that modifies
//     reflection-generated schemas, as [JSONSchemaExtender] does for types the
//     caller owns. [WithTypeSchemaExtenderFor] is its generic form for one
//     statically known type.
//   - [WithNamer] sets the definition namer (a [Namer], with [NamerFunc]
//     adapting a bare function). An empty name defers to the built-in namer,
//     so a partial namer renames only the types it recognizes.
//   - [WithDefinitions] controls $defs/$ref extraction (default true).
//   - [WithAdditionalProperties] controls whether object schemas allow extra
//     keys (default false).
//   - [WithJSONOptions] makes generation honor the [encoding/json/v2] marshal
//     options that change output shape (FormatNilSliceAsNull,
//     FormatNilMapAsNull, OmitZeroStructFields), so the schema matches a
//     marshal under the same options. A shape-changing option generation does
//     not model fails with [ErrUnsupportedJSONOption].
//   - [WithDefaultsFrom] seeds root property defaults from an instance of the
//     generated type (details below).
//   - [WithRootTitle] sets the root schema's title to the root type's name
//     when no title is otherwise present (default false; details below).
//
// [WithDefaultsFrom] marshals the instance with [encoding/json/v2] under the
// [WithJSONOptions] value in force, so seeded defaults match the caller's own
// marshal. Each top-level key of the output that matches a root property
// becomes that property's default, overwriting any default a struct tag set.
//
//   - Presence follows the json tags. Keys that omitempty or omitzero omit
//     contribute nothing.
//   - A nested struct, slice, or map value becomes a whole-value default on
//     its top-level property. Map values marshal in sorted key order, so a
//     map-valued default seeds identical bytes on every run.
//   - A nil slice, map, or []byte marshals as its empty instance ([], {}, ""),
//     which seeds like any other value.
//   - A nil pointer or interface carrying neither omitempty nor omitzero
//     marshals to JSON null. That null leaves a property admitting no null
//     untouched, so the property keeps whatever default its tag wrote. A
//     [NullForbidden] stance takes the null branch off every occurrence of
//     the type it covers.
//   - A pointer root seeds the object schema inside its nullable anyOf
//     wrapper, or the $defs entry that schema references. When a
//     self-referential root stays in $defs, the defaults apply to that
//     definition, shared by every recursive occurrence.
//   - Under [Draft7], a default landing on a $ref'd property moves the $ref
//     into an allOf wrap, the same shape tag defaults produce, because
//     Draft-07 readers ignore $ref siblings.
//   - An instance whose pointer-dereferenced type is not the generated type,
//     or that does not marshal to a JSON object, returns an error wrapping
//     [ErrInvalidDefaultsInstance].
//
// [WithRootTitle] honors the [WithNamer] namer and pointer-dereferences the
// root type first. Unnamed roots (anonymous structs, unnamed maps and slices)
// stay untitled, and a title from [WithTypeSchema], [JSONSchemaProvider], or
// [JSONSchemaExtender] is never overwritten. Under [Draft7], a
// self-referential root stays a bare $ref into definitions, where a reader
// would ignore a sibling title, so the title lands on the definitions entry,
// shared by every occurrence of the type.
//
// An option given a nil interface or pointer value restores the default
// behavior, at every entry point:
//
//   - [WithNamer] restores the built-in namer.
//   - [WithDescriptionProvider] restores no descriptions.
//   - [WithRefResolver] restores local-only ref resolution.
//   - [WithRefFallback] restores fatal expansion failures.
//   - [WithDefaultsFrom] restores no seeded defaults. A typed nil pointer is a
//     value rather than a reset. It marshals to JSON null and fails as a
//     non-object instance.
//
// Additive registrations ([WithTagInterpreter], [WithTypeSchemaProvider],
// [WithTypeSchemaExtender], [WithFormatValidator]) ignore a nil registration,
// since a nil identifies nothing to remove. [WithTypeSchema] takes a
// [TypeSchema] value rather than a pointer, so every call adds a registration,
// and a zero [TypeSchema] marks the type unrestricted ({}).
//
// # Type Mapping
//
// Go types map to JSON Schema types by kind:
//
//   - The primitive kinds string, bool, int, float32, and float64 map
//     directly. The bounded integer kinds (int8 through int64, uint8 through
//     uint64) carry minimum and maximum. The platform-dependent unsigned kinds
//     uint and uintptr carry minimum: 0 only.
//   - A pointer *T produces a nullable schema, the base schema wrapped in an
//     anyOf with a {"type": "null"} branch. Deeper pointers (**T) behave as
//     *T. A pointer to an unrestricted type (*interface{},
//     *[encoding/json.RawMessage]) produces the unrestricted schema ({}),
//     which already permits null.
//   - A slice []T produces an array with an items schema. A nil slice
//     marshals as [] under [encoding/json/v2]'s defaults, so it admits no
//     null. Under [WithJSONOptions] with FormatNilSliceAsNull every slice
//     occurrence admits null. []byte produces a base64-encoded string
//     instead, and a nil []byte marshals as "" under the defaults and follows
//     the slice option. Only the unnamed byte element type takes the base64
//     form. A []T whose element is a named uint8 type marshals as a number
//     array and produces one.
//   - An array [N]T produces a fixed-size array with minItems and maxItems
//     equal to N. [N]byte is the base64 exception again, a string whose
//     minLength and maxLength pin the exact encoded length.
//   - A map map[K]V produces an object with additionalProperties. A nil map
//     marshals as {} under the defaults, so it admits no null. Under
//     [WithJSONOptions] with FormatNilMapAsNull every map occurrence admits
//     null. K must be a string, integer, or float kind, or carry a marshaler
//     method (through its pointer method set included). A key v2 cannot name
//     returns [ErrUnsupportedMapKey], and so does the exact [time.Duration]
//     key, whose native v2 codec pre-empts the integer-kind encoding.
//   - An interface type produces the unrestricted schema ({}). A nil
//     interface marshals as null, so an interface whose schema an earlier
//     resolution step intercepts (a registered override, or an
//     [encoding.TextMarshaler] method set) admits null alongside the
//     intercepted schema, like a pointer.
//   - A struct produces an object with properties, required, and
//     additionalProperties: false by default.
//
// A pointer or intercepted interface occurrence always admits null, since a
// nil one marshals as null. A [NullForbidden] stance on the type drops the
// branch, so *T yields the bare value schema. Slices, maps, and []byte admit
// no null under the default marshal options, where a nil one marshals as its
// empty instance (see [WithJSONOptions]).
//
// Built-in overrides cover well-known types, matched by exact [reflect.Type],
// so a named type wrapping one (type MyTime [time.Time]) falls through to the
// later resolution steps:
//
//   - [time.Time] maps to {"type": "string", "format": "date-time"}.
//   - [encoding/json.RawMessage] maps to {}.
//   - [encoding/json.Number] maps to {"type": "number"}.
//   - [math/big.Int] maps to {"type": "integer"}, since its MarshalJSON emits
//     a bare number.
//   - [math/big.Rat] and [math/big.Float] map to {"type": "string"} with a
//     numeric pattern. [math/big.Float]'s pattern also admits the "+Inf" and
//     "-Inf" text it marshals for infinities.
//   - [log/slog.Level] maps to {"type": "string"}. It implements both direct
//     marshalers, and its MarshalJSON emits the level name as a JSON string,
//     so the override pins the string schema its output requires.
//
// [net/url.URL] has no override, and generation refuses it with
// [ErrInvalidJSONField]. It implements no marshaler interface, and reflecting
// it reaches [net/url.Userinfo], a struct with fields but none serializable,
// which [encoding/json/v2] refuses once a non-nil User pointer is marshaled.
// (A URL with a nil User marshals as "User": null, since v2 never reads the
// declarations behind a nil pointer.) [WithTypeSchemaFor] on [net/url.URL],
// or on [net/url.Userinfo] alone, declares a shape in place of the refusal.
//
// Types implementing [encoding.TextAppender] or [encoding.TextMarshaler] map
// to {"type": "string"}, checked before struct reflection.
//
// Generation refuses func, chan, complex, and [unsafe.Pointer] types with
// [ErrUnsupportedType], and [time.Duration] too, which [encoding/json/v2]
// refuses with no format. The refusal is v2's own verdict on a value of the
// type and runs after the override steps, so [WithTypeSchema] or a provider
// can still declare a shape for a duration.
//
// Go type aliases (defined with =) are invisible to reflection and behave as
// their underlying type. Only defined types (type MyString string) produce
// distinct names.
//
// # Type Resolution
//
// For each type, the first matching step decides the schema:
//
//  1. [TypeSchemaProvider] values registered with [WithTypeSchemaProvider] or
//     [WithTypeSchema], consulted newest registration first.
//  2. The [JSONSchemaProvider] interface.
//  3. Built-in overrides ([]byte, [time.Time], [encoding/json.Number], and
//     the rest of the list above).
//  4. Marshaler methods promoted from an embedded field. A promoted
//     [encoding/json/v2.MarshalerTo] or [encoding/json.Marshaler] makes the
//     schema unrestricted ({}), and a promoted [encoding.TextAppender] or
//     [encoding.TextMarshaler] makes it {"type": "string"}.
//  5. A direct [encoding.TextAppender] or [encoding.TextMarshaler]
//     implementation, for types implementing no JSON marshaler interface.
//  6. Kind-based reflection.
//
// A direct JSON marshaler implementation ([encoding/json/v2.MarshalerTo] or
// [encoding/json.Marshaler]) is not in this chain. Kind-based reflection
// handles a type that implements one directly, since the method can return
// any JSON type and reflection cannot know the output shape. That holds even
// when the type also implements a text marshaler. [encoding/json/v2] prefers
// the JSON method, so the text form never appears in the output and step 5
// does not claim the type as a string.
//
// Built-in overrides cover specific well-known JSON-marshaling types. Any
// other JSON-marshaling type takes [WithTypeSchema] or [JSONSchemaProvider].
//
// A marshaler promoted from an embedded field is different (step 4).
// [encoding/json/v2] resolves marshalers through the method set, so the
// promoted method serializes the whole outer struct, and reflecting its fields
// would describe a shape that never appears in the output.
//
// # Deviations
//
// The generated schema's model of a Go type differs from what
// [encoding/json/v2] emits for that type at two points, each specified in the
// section that owns it:
//
//   - Generation reflects a direct JSON marshaler implementation
//     ([encoding/json/v2.MarshalerTo] or [encoding/json.Marshaler]) by its Go
//     struct shape rather than its marshaled shape, so the schema can reject
//     output [encoding/json/v2] produces (see Type Resolution, above).
//   - [Draft7] omits additionalProperties: false from the parent under allOf
//     composition. That loosens the schema rather than tightening it, so it
//     cannot reject valid output (see Struct Fields, below).
//
// # Customization
//
// A type implements [JSONSchemaProvider] to supply its own schema and skip
// reflection, or [JSONSchemaExtender] to modify the reflection-generated
// schema after generation builds it. If a type implements both, only
// [JSONSchemaProvider] runs. Generation checks both value and pointer
// receivers. Both methods return an error, which aborts generation, for an
// implementation that cannot produce or adjust its schema. Generation still
// recovers a panic and wraps it with [ErrProviderPanic] as a backstop.
//
// Generation does not consult a provider or extender declared only in an
// interface's method set, since an interface value cannot be instantiated to
// call it. Such an interface produces the unrestricted schema ({}). With a
// provider, generation extracts it to $defs like any other
// provider-implementing named type. With an extender, the reflected
// unrestricted schema stands unextended.
//
// A type-level hook declares its intent through a [TypeSchema] rather than a
// pre-shaped schema, so generation applies the null encoding and resolves
// references itself. A provider (or [WithTypeSchema]) fills exactly one of
// three fields, and setting more than one fails with
// [ErrConflictingTypeSchema]. A zero TypeSchema marks the type unrestricted
// ({}).
//
//   - [TypeSchema.Value] is a bare value schema. The [Nullability] stance
//     ([NullAllowed] or [NullForbidden]) makes every occurrence of the type
//     admit null (or none), combined with each occurrence's pointer-ness, so
//     a hook never hand-shapes an anyOf[value, null] wrapper.
//   - [TypeSchema.Verbatim] renders exactly as authored, with no null
//     encoding.
//   - [TypeSchema.Ref] aliases the whole type to another Go type and renders
//     as a $ref to it.
//
// An extender receives the same envelope with [TypeSchema.Value] set to the
// reflection-generated schema to mutate in place, and may set
// [TypeSchema.Nullability] to declare a stance too. Those are the only fields
// an extender may set. Verbatim and Ref declare a replacement schema only a
// provider supplies, so an extender setting either fails with
// [ErrConflictingTypeSchema] instead of generation silently ignoring the
// declaration.
//
// When a registered provider ([WithTypeSchemaProvider] or [WithTypeSchema])
// or [JSONSchemaProvider] supplies the schema, [JSONSchemaExtender] does not
// run.
//
// A [TypeSchemaProvider] answers for whole families of types by predicate,
// such as every type implementing a third-party interface, where
// [WithTypeSchema] names one exact [reflect.Type] at a time:
//
//   - A provider answers [ErrTypeNotHandled] (or an error wrapping it) for a
//     type it does not handle. The type passes to the next provider and then
//     to the rest of the chain.
//   - A zero [TypeSchema] with a nil error marks the type unrestricted ({}),
//     mirroring [JSONSchemaProvider].
//   - Any other error aborts generation, for a provider that recognizes a
//     type but cannot produce its schema (an I/O failure, for example).
//   - Generation may consult a provider or an extender several times for one
//     type within a run (once per inline occurrence), so both must be
//     deterministic.
//
// [JSONSchemaExtender] requires owning the type. For a type the caller does
// not own, [WithTypeSchemaExtender] registers a [TypeSchemaExtender] that
// runs at the same point, after the type's own JSONSchemaExtend, and under
// the same rule that a replaced schema is never extended.
//
// Every hook receives what it needs to emit draft-appropriate keywords and to
// honor cancellation:
//
//   - The type-level hooks ([JSONSchemaProvider], [JSONSchemaExtender], their
//     registered counterparts [TypeSchemaProvider] and [TypeSchemaExtender],
//     [Namer], and the type half of [DescriptionProvider]) receive a
//     [TypeContext] carrying the Go type and the target [Draft] of the run.
//   - The field-level hooks (tag interpreters and
//     [DescriptionProvider.FieldDescription]) receive the [FieldContext]
//     counterpart, carrying the field's schema and surroundings alongside the
//     same Draft.
//   - All but [Namer], whose work is pure, also receive the context of the
//     Generate call in effect, so an implementation doing cancellable work
//     (loading a schema document, for example) can honor cancellation and
//     deadlines.
//
// Every single-method extension-point interface has a func type adapter
// following [net/http.HandlerFunc]: [TagInterpreterFunc],
// [FormatValidatorFunc], [TypeSchemaProviderFunc], [TypeSchemaExtenderFunc],
// [RefResolverFunc], [NamerFunc], and [RefFallbackFunc].
// [DescriptionProvider], the one two-method interface, has the struct adapter
// [DescriptionProviderFuncs] instead, whose nil fields answer "" with no
// error.
//
// An interface serving a named registration ([TagInterpreter] for a struct
// tag key, [FormatValidator] for a format name) takes the name at the
// registration site ([WithTagInterpreter], [WithFormatValidator]), following
// [net/http.Handle], so one implementation can serve several names. Each call
// then learns the name it runs under, the way a [net/http.Handler] reads the
// request path: [Tag.Key] for an interpreter, the name parameter for a format
// checker.
//
// # Tag Interpreters
//
// All struct tag interpretation beyond the json and jsonschema tags goes
// through the [TagInterpreter] interface. An interpreter receives three
// arguments:
//
//   - The Generate call's context, like the other generation-time hooks. An
//     interpreter that performs no cancellable work ignores it.
//   - A [Tag] carrying the struct tag key and value the call runs under.
//   - A [FieldContext] describing the field.
//
// The [FieldContext] carries:
//
//   - [FieldContext.Canvas], the schema the interpreter writes its keywords
//     to: value facts such as const and enum, annotations, and numeric,
//     string, and array bounds.
//   - [FieldContext.Base], the read-only type-derived schema, for dispatching
//     on the reflected shape.
//   - The parent schema, the field's JSON name, and its Go type.
//   - [FieldContext.Owner], the declaring struct type, which for a promoted
//     field is the embedded type. A [DescriptionProvider] reads the same
//     field.
//   - The full [reflect.StructField], for reading sibling struct tags such as
//     the json tag's options.
//   - The target [Draft], for emitting draft-appropriate keywords.
//   - [FieldContext.EffectiveFormat] and its sibling accessors, which report
//     the format, pattern, contentEncoding, and contentMediaType the field's
//     type already set, so a tag never overrides one of them.
//
// Generation merges the keywords on Canvas into the field's schema, so the
// interpreter never edits the type-derived schema itself:
//
//   - A const or enum the interpreter declares lands on the value branch of a
//     null-admitting field and keeps null valid.
//   - A bound the interpreter writes can only tighten the type's own bound,
//     never widen it. Generation keeps the stronger of the two, so the
//     interpreter need only intersect its own repeated rules within one tag.
//
// For bounds and value constraints an interpreter uses the [Constraints]
// facade [FieldContext.Constraints] returns:
//
//   - [Constraints.Apply] takes an [Op] and, for a bound, the [Axis] it
//     targets. [AxisAuto] lets the field's shape choose the axis, which is
//     what a rule-shaped validator (min, max) means, while naming a family
//     pins it.
//   - The facade applies the same 2^53 representability rule as the
//     jsonschema tag (see Struct Tag below).
//   - [Constraints.SetConst], [Constraints.SetEnum], [Constraints.Const],
//     [Constraints.Enum], [Constraints.Forbid], [Constraints.ForbidSchema],
//     and [Constraints.SetMultipleOf] remain as named conveniences for the
//     value set, where an interpreter usually runs its own conflict check
//     with its own wording first.
//
// One Apply call settles everything the rule needs:
//
//   - Coercion. On a field whose schema is a string because it serializes
//     itself as one, the facade compares scalar rules against that text.
//   - Element rules. A dive, or a sequence-wide oneof, reaches the item
//     schemas through the facade, which dispatches again on each element's
//     own shape. [FieldContext.ElementContexts] remains the accessor for
//     walking them directly.
//   - Intersect-only bounds. Each bound writes back only when it would not
//     loosen the effective bound, so a bound never weakens a stronger one the
//     field's type or an earlier rule set.
//   - Shape errors. A rule the field's shape cannot carry is an error naming
//     the reason rather than a keyword nothing enforces.
//   - Conflicts. A conflicting const or enum surfaces the public
//     [ErrConstraintConflict] sentinel.
//
// An interpreter that branches on what the field is classifies it once with
// [FieldContext.Shape], or with [ShapeOf] when no context is available,
// reading the field's Go type against its type-derived [FieldContext.Base].
// The context supplies two facts [ShapeOf] cannot:
//
//   - The json:",string" flag on an [encoding/json.Number] field. The type is
//     a string kind and the coerced base is a string schema, so only the flag
//     says the instance is a quoted number.
//   - Whether a pointer or interface occurrence admits null. That is the
//     generator's decision rather than the Go type's (a [Nullability] stance,
//     a [WithJSONOptions] format flag), so [ShapeOf] reports only the pointer
//     occurrences as admitting null. A context the generator did not build
//     falls back to the same answer.
//
// The generator decides every occurrence's null admission before any
// field-level hook runs, so a [Nullability] stance a type-level hook records
// for a self-referential type is in force when the field's tag and
// interpreters read it.
//
// The resulting [Shape] carries:
//
//   - The declared Go type.
//   - That type with its pointer chain followed.
//   - The kind a scalar literal parses at.
//   - Whether the occurrence admits null.
//   - The [Form], the JSON shape the instance actually takes. Form is not the
//     Go kind, so a field that encodes itself as a string (through
//     json:",string" or its own MarshalText) reads as [FormCoercedNumber] or
//     [FormTextString] rather than as a number every branch has to
//     special-case.
//
// Passing that same Shape to [FieldContext.ConstraintsFor] builds the facade
// without classifying the field a second time, which [FieldContext.Constraints]
// would.
//
// [WithTagInterpreter] registers each interpreter under the struct tag key it
// reads (following [net/http.Handle], so one implementation can serve several
// keys). Several interpreters can be registered, and they run in order, after
// the jsonschema tag. [TagInterpreterFunc] adapts a bare function, so a
// one-off interpreter needs no named type.
//
// # Definitions
//
// By default, generation extracts named struct types, and named types
// implementing [JSONSchemaProvider] or [JSONSchemaExtender], into $defs (or
// definitions for [Draft7]) and refers to each through $ref. Named primitive
// and composite types without these interfaces (e.g., type Tags []string)
// stay inline, and so does every anonymous struct type. Generation always
// refers to a circular type through $ref, even with definitions disabled.
//
// Generation disambiguates name collisions with the package's base directory
// name, then with the full import path if needed. For generic type
// instantiations, it replaces the brackets and commas in [reflect.Type.Name]
// with underscores to form the $defs key (e.g., "MyStruct[int]" becomes
// "MyStruct_int_"). The [WithNamer] option overrides this naming.
//
// A nullable reference (a pointer to a $ref'd type) wraps the $ref in anyOf
// beside a null branch: {"anyOf": [{"$ref": "..."}, {"type": "null"}]}.
//
// Every $defs entry sits at the root schema level, never inside a sub-schema.
// The root type's own schema sits directly in the root schema object unless
// the type is self-referential (recursive), in which case its schema lives in
// $defs and the root holds a $ref to it.
//
// # Struct Fields
//
// Struct fields follow [encoding/json/v2] conventions. The json tag sets the
// property name, and json:"-" excludes the field. A tag name is the run of
// characters before the first reserved rune (a comma, a backslash, or a quote
// character: single quote, double quote, or backtick). Any other rune run is a
// name, so json:"a b" names the property "a b".
//
// Generation refuses, with [ErrInvalidJSONField] and v2's own reason, every
// declaration [encoding/json/v2.Marshal] refuses:
//
//   - a name a backslash or quote character cuts short, and the json:"-,"
//     spelling;
//   - a json tag on an unexported field;
//   - two fields of one struct claiming one JSON name;
//   - an embedded non-struct without an explicit name;
//   - an embed option combined with any other option;
//   - a format tag option (cut from stable v2, go.dev/issue/79071);
//   - a struct with fields but none serializable (every field unexported and
//     untagged).
//
// The verdict is v2's, taken under the [WithJSONOptions] value in force, and
// no user marshal method runs during generation. Two gaps remain. A func field
// under omitzero is always omitted and so marshals, while generation refuses
// the func type. A type with a direct JSON marshaler marshals without v2
// reading its field declarations, but generation reflects those fields by
// policy and still applies the field checks, so its refusal there is a
// conservative superset of v2's.
//
// Unexported non-embedded fields without a json tag are excluded. Unexported
// embedded struct types still have their exported fields promoted.
//
// A field is required unless its tag says otherwise:
//
//   - omitzero drops the field from required.
//   - omitempty drops it only where the field's encoded value can be empty
//     (null, "", [], or {}): a string, map, slice, pointer, interface, struct,
//     zero-length array, or marshaler-bearing field. [encoding/json/v2]
//     never treats an encoded number or bool as empty, so omitempty on one
//     leaves the field required, and so does an [encoding/json.Number], the
//     one string kind whose marshaler writes 0 for the empty value.
//
// The json:",string" flag forces a {"type": "string"} schema exactly where it
// is what makes v2 write a string:
//
//   - The integer and float kinds, [encoding/json.Number], and pointers to
//     those at any depth take the string schema. A pointer occurrence keeps
//     its null branch beside the string.
//   - A type carrying a JSON or text marshaler, directly or through its
//     pointer method set, is neither quoted nor refused, since v2 routes it
//     through the method, which ignores the option. The field keeps the
//     schema the marshaler steps produce.
//   - A [encoding/json/jsontext.Value] marshals verbatim either way and keeps
//     its unrestricted schema.
//   - On any other type v2 refuses the flag, [time.Time] included (its native
//     codec pre-empts MarshalJSON and rejects the flag), and generation
//     refuses it with [ErrInvalidJSONField] and v2's reason.
//   - A type v2 refuses with or without the flag, [time.Duration] at any
//     pointer depth, is answered as if unflagged: [ErrUnsupportedType] under
//     the defaults, or the shape a [WithTypeSchemaFor] override or a provider
//     declares, since a type a hook declares is the caller's to describe.
//
// An embedded struct without a json tag, or with the explicit json:",embed"
// option, has its fields promoted into the parent schema. An embedded
// pointer-to-struct promotes the same way, but its fields are not required,
// since a nil embed omits them from the output. An embedded struct with an
// explicit JSON name (json:"base") is a regular named field, not promoted.
//
//   - A struct whose method set includes a JSON or text marshaler promoted
//     from an embedded field is not reflected field by field at all; the
//     promoted marshaler serializes the whole outer value (see Type
//     Resolution above).
//   - Where generation does read fields, it refuses an embedded struct type
//     carrying any marshal or unmarshal method of its own with
//     [ErrInvalidJSONField], as v2 refuses to promote it.
//   - An embedded struct type an earlier resolution step intercepts (a
//     registered [TypeSchemaProvider] or [JSONSchemaProvider]) composes via
//     allOf instead of promoting its fields. An embed reached through a
//     pointer composes as anyOf[schema, {}], since a nil pointer contributes
//     nothing to the marshaled object.
//   - A provider schema used for such an embed, registered or on-type, must
//     leave the object open (no additionalProperties: false). allOf evaluates
//     each branch against the whole object, so a closed branch rejects the
//     parent's sibling properties, and the generated schema then rejects the
//     struct's own marshaled JSON.
//   - Generation refuses an embedded field whose tag combines a promoting
//     form with any other option (json:",omitempty", json:",embed,omitzero")
//     with [ErrInvalidJSONField], as v2 refuses the declaration.
//
// An anonymous non-struct type must carry an explicit JSON name.
// [encoding/json/v2] refuses the untagged form and the json:",embed" form,
// and generation refuses both with [ErrInvalidJSONField]. With a name
// (json:"bag") the field is a regular leaf property under that name, a pointer
// adding nullability.
//
// A non-anonymous exported field of struct type (or unnamed pointer to struct)
// tagged json:",embed" is promoted exactly as an anonymous embed, since v2
// treats the option as Go embedding. The pointer form leaves its fields
// optional.
//
// A non-anonymous exported field tagged json:",embed" whose type, after one
// unnamed-pointer level, is a [encoding/json/jsontext.Value] or a map with a
// string key kind and no marshal or unmarshal method on its key type is v2's
// embedded fallback. [encoding/json/v2] splices the fallback's members into
// the parent object after the named fields, and the generated schema carries
// the constraint those members satisfy:
//
//   - A map fallback's value schema becomes the object's
//     additionalProperties, or its unevaluatedProperties beside allOf
//     composition under [Draft2020]. Beside allOf under [Draft7] the object
//     stays open, the dialect's usual degradation.
//   - A jsontext.Value fallback leaves the object open, since its members are
//     arbitrary JSON.
//   - A nil map, a nil pointer, and an empty jsontext.Value contribute no
//     members, so a fallback never adds a null branch.
//   - Generation ignores non-json tags on the fallback field, since the field
//     emits no property for a tag interpreter to constrain.
//   - One fallback survives per type, on v2's rules. The shallowest wins, and
//     a same-depth tie silently drops them all.
//   - Generation refuses with [ErrInvalidJSONField] two fallback fields in one
//     struct declaration, a fallback type or map key carrying marshal or
//     unmarshal methods, a non-string-keyed map or any other non-struct type
//     under json:",embed", and json:",embed" combined with a name or any
//     other option.
//   - Generation refuses a fallback map value type with no representation
//     (map[string]time.Duration, map[string]func()) with [ErrUnsupportedType]
//     under every option, where v2's marshal refuses the value.
//   - Where a self- or mutually composed embed cycle cuts a composed subtree
//     short, generation drops that subtree's fallback along with its names,
//     on the cycle's conservative terms.
//
// Embedded interface types are regular leaf fields on the same terms. An
// explicit JSON name is required, and v2 never flattens them; it always emits
// the key, null for a nil interface and the concrete value otherwise.
//
//   - A plain interface produces an unrestricted schema ({}), since its
//     concrete type is unknowable at compile time.
//   - An interface an earlier resolution step intercepts (a registered
//     override, or an [encoding.TextMarshaler] method set) uses the
//     intercepted schema with null admitted alongside.
//   - Generation refuses an unexported embedded interface, like every
//     unexported embedded non-struct type, with [ErrInvalidJSONField].
//     [encoding/json/v2] demands an explicit JSON name for a non-struct embed,
//     and an unexported field cannot carry one.
//
// Field shadowing and ambiguity follow v2's rules:
//
//   - Generation refuses two fields of one struct claiming one JSON name with
//     [ErrInvalidJSONField], as v2 refuses the declaration.
//   - Across embedding levels the v1 rules survive. Outer fields shadow
//     deeper fields of the same name, and a tie between names promoted from
//     different embeds at the same depth silently drops the name.
//   - A composed embed's promoted names take part in the same resolution,
//     exactly as [encoding/json] resolves them (shadowing deeper fields,
//     annihilating on same-depth ties with ties inside the embed included,
//     and applying the tag tie-break), even though they never become
//     properties. The embed's allOf branch carries their assertions.
//
// Under allOf composition, [Draft2020] puts unevaluatedProperties: false on
// the parent in place of additionalProperties: false, and each promoted name
// a composed embed contributes to the marshaled object gets a true property
// on the parent. The embed's branch carries the name's assertions but is not
// guaranteed to evaluate it (an unrestricted [TypeSchema] renders as true and
// evaluates nothing), and an unevaluated name would otherwise be rejected.
// [Draft7] omits additionalProperties: false from the parent when allOf is in
// use.
//
// Property order in the output matches the order fields appear in the Go
// struct definition (through the upstream PropertyOrder field). The field-free
// struct produces {"type": "object", "additionalProperties": false} with no
// properties or required fields. Generation refuses a struct with fields but
// none serializable (every field unexported and untagged) with
// [ErrInvalidJSONField], as v2 refuses to marshal one.
//
// # Struct Tag
//
// The jsonschema struct tag sets schema keywords directly on a field. A bare
// value (no = sign) is a description. Otherwise the tag is a comma-separated
// list of key=value pairs:
//
//	Port int `jsonschema:"description=Server port,minimum=1,maximum=65535"`
//
// Supported keys: type, description, title, default, examples, deprecated,
// readOnly, writeOnly, minimum, maximum, exclusiveMinimum, exclusiveMaximum,
// multipleOf, minLength, maxLength, pattern, format, minItems, maxItems,
// uniqueItems, minProperties, maxProperties, enum, and const. Pairs apply in
// order, and an unrecognized key is an error.
//
// The tag's syntax has a few rules:
//
//   - "|" separates enum and examples values, so a value cannot contain "|".
//   - Commas separate pairs, so a value containing a comma escapes it with a
//     backslash; jsonschema:"description=Hello\, World" sets the description
//     "Hello, World". A literal backslash is "\\".
//   - An empty value (const= or default=) is an error for every key except
//     const and default on a string field, where it means the empty string
//     ({"type":"string","const":""}).
//   - For values the tag cannot express, use [JSONSchemaExtender] or doc
//     comments through [WithDescriptionProvider].
//
// Values for default, const, enum, and examples parse against the JSON shape
// the field's instance takes, not against its Go kind alone. The two agree for
// an ordinary field and diverge where the field serializes itself as something
// else:
//
//   - A json:",string" numeric field, and equally a type that marshals itself
//     as text, has a string schema. The tag parses its scalars at the real Go
//     kind, keeps the range check (so const=200 on an int8 is an error), and
//     re-serializes the value to the text the field emits. A MarshalText int
//     whose value 3 writes as "L3" therefore gets const=3 as {"const":"L3"},
//     not the unsatisfiable {"type":"string","const":3}, and default=3
//     likewise.
//   - [encoding/json.Number] is a string kind but follows the same rules
//     under json:",string". [encoding/json/v2] writes it as the quoted number
//     it holds, verbatim rather than canonicalized, so const=5.0 pins "5.0"
//     and const=5 pins "5".
//
// Which occurrences admit null is generation's decision, not the Go type's
// (see Null Encoding below), and the literal null is a value exactly where
// that decision admits it:
//
//   - A pointer field takes default=null, and so does an interface
//     occurrence.
//   - A bare slice, map, or []byte takes the literal only where its
//     occurrence admits null. Under the default marshal options none does,
//     since [encoding/json/v2] writes a nil one as its empty instance, so the
//     tag refuses the literal. [WithJSONOptions] with FormatNilSliceAsNull or
//     FormatNilMapAsNull admits the null, and the literal with it, as the
//     Slices and Maps entries under Type Mapping describe.
//   - A pointer to a container takes the literal, since the pointer's own
//     null branch admits it.
//   - A type the package maps to a built-in leaf schema refuses the literal,
//     so [encoding/json.RawMessage] refuses it even though the {} it produces
//     admits null.
//   - A [Nullability] stance moves the decision either way. A value field of
//     a [NullAllowed] type takes the literal, and a pointer to a
//     [NullForbidden] one does not.
//   - A [TypeSchema.Verbatim] payload carries no null encoding, so a null
//     literal is an error on it.
//   - A container shape refuses const before the tag reads the value, so
//     const=null fails there for the same reason const=5 does, on a pointer
//     to a container as much as on a bare one.
//   - An enum on a sequence constrains the elements, and each element answers
//     from its own Go type rather than from the container's decision. This is
//     the one case that takes the literal against a schema admitting no null.
//     A []*string takes a null enum member even where a stance leaves the
//     element schema no null branch to match it, because the member belongs
//     to the element and the field's decision never reaches it.
//   - A type= pair replaces the occurrence, so the null admission the tag
//     read no longer applies and a null literal is an error wherever the pair
//     sits in the tag. The one exception is a literal that precedes
//     type=null, since that override names the null instance outright. A
//     literal after type=null is an error like every other scalar key there.
//
// Generation decides every occurrence's null admission before it applies any
// field's tag, a [Nullability] stance a type-level hook records for a
// self-referential type included. It therefore refuses a null literal against
// a reference the stance leaves unadmitted as it reads the tag, and the error
// names the struct whose schema carries the field.
//
// Generation checks a null literal a tag interpreter declares after every
// interpreter has run. It reads the default, const, enum, and examples keywords
// declared on every field and element, refuses a null wherever the occurrence
// admits none, and names the keyword holding it. [Constraints.Forbid] writes
// under not rather than into a value keyword, so a forbidden null stays and
// renders as a not beside the $ref.
//
// The type key replaces the reflected type entirely, for a Go type whose JSON
// representation differs from its reflection. It must name one of the seven
// JSON Schema types.
//
//   - The overridden field admits no null and is not a reference. It names a
//     concrete type in place of the one a pointer would make nil-able.
//   - When the new type is not numeric, the override also drops the numeric
//     bounds derived from the Go kind. A constraint keyword the tag sets
//     explicitly (a numeric bound such as minimum or multipleOf, or a string,
//     array, or object constraint) is the author's input, so combining it
//     with a type= override whose JSON type cannot use it is an error rather
//     than a silent discard, whether the keyword appears before or after the
//     pair.
//   - The override cannot rescue a type generation refuses before it reads
//     fields. A [time.Duration] field fails in type resolution, ahead of the
//     tag, so declaring a shape for a duration takes [WithTypeSchema] or a
//     provider.
//   - Pairs apply in order, and keys after type= still take effect.
//
// Because pairs apply in order, a default, const, enum, or examples value
// after a type= pair parses against the overridden JSON type rather than the
// field's Go type:
//
//   - string, integer, number, and boolean overrides parse later scalar
//     values as that type. An int64 field with
//     jsonschema:"type=string,default=15m" yields
//     {"type":"string","default":"15m"}, where the Go int64 kind would have
//     rejected "15m". The same keys before the pair still parse against the
//     Go type.
//   - After an override to array, object, or null there is no scalar type to
//     parse against, so those keys are an error.
//   - A null literal before the pair is an error for every override but
//     type=null, since the override replaces the occurrence that admitted the
//     null.
//   - An error on either side of the pair names the JSON type the override
//     installed rather than the field's Go kind.
//   - An enum after a type= override constrains the value schema itself, even
//     on a slice or array field. The redirection to the item schemas (next)
//     keys on the type scalars parse at, which an override replaces with a
//     non-sequence stand-in.
//
// On a slice or array field, enum constrains each element rather than the
// array value:
//
//   - The values land on the item schemas, where each element parses against
//     its own shape, so a coerced or text-marshaling element gets the same
//     treatment a coerced field does.
//   - Nested sequences descend to the innermost element schema.
//   - const, default, and examples remain whole-value constraints, so a
//     scalar value for one of them is an error on a sequence field. A null
//     value for default or examples names the sequence occurrence's own null
//     rather than an element's, so it follows the field's null decision,
//     refused on a bare slice and taken on a pointer to one.
//   - enum on []byte is an error, because a base64 string has no item schema.
//   - A validate dive or sequence-wide oneof reaches the same element
//     schemas, so the two dialects agree about what an element is.
//
// A keyword the field's shape cannot carry is an error rather than an inert
// keyword nothing enforces: minItems=3 on a string, or any numeric bound on a
// json:",string" field, whose instance is a quoted string that minimum cannot
// constrain. The check reads the shape from the field's schema rather than
// its Go kind alone, so a field whose type supplies a verbatim or overridden
// schema answers by what that schema declares.
//
// A key named twice in one tag resolves by what the keyword means:
//
//   - A repeated bound key intersects rather than overwriting.
//     minimum=5,minimum=3 keeps 5, matching how bounds from every other
//     source compose.
//   - A second const or enum, or one disagreeing with a value the field's
//     type already pins, is [ErrConstraintConflict]. Both fully describe the
//     allowed value, so neither can silently win.
//   - format and pattern replace what the field's type declared, since the
//     tag names the keyword outright (a tag interpreter defers to both).
//     Naming either twice in one tag is an error, because no precedence
//     applies there and dropping one of two stated values would be silent.
//
// Numeric, length, and count bounds from the Go kind, the jsonschema tag, and
// tag interpreters compose by one rule, whichever source set them:
//
//   - Bounds intersect order-independently. A weaker bound never loosens a
//     stronger one.
//   - A const or enum makes the kind-derived numeric bounds (an int8's
//     minimum/maximum, for instance) redundant, so generation drops them. A
//     const also subsumes an explicit bound, since it pins a single value, so
//     generation drops that bound too. An enum only restricts the value to a
//     set, so an explicit bound narrows it further and stays.
//     enum=10|20,minimum=15 keeps minimum and admits only 20.
//   - A conflict in the discrete value set aborts generation.
//   - An unsatisfiable range (a minimum above a maximum) renders as its
//     impossible bounds rather than loosening.
//   - A sequence or map element resolves by the same rule, from the keywords
//     authored on the element rather than on the field. Which dialect wrote
//     the element's const or enum does not matter. A []int8 with
//     jsonschema:"enum=1|2|3" and one with validate:"oneof=1 2 3" both drop
//     each element's kind-derived -128/127 range, and a bound authored on the
//     element survives alongside the pin.
//
// # Descriptions
//
// A [DescriptionProvider], registered with [WithDescriptionProvider], supplies
// type and field descriptions. A provider error aborts generation, matching
// the package's other generation hooks, so a provider doing I/O reports a
// failed lookup instead of silently dropping descriptions.
//
// The built-in [GoCommentProvider] (constructed with [NewGoCommentProvider])
// reads Go doc comments from source files for struct types, struct fields,
// and named types, using [go/ast] and [golang.org/x/tools/go/packages]. It
// follows these rules:
//
//   - When it cannot locate source files for a type, it skips the type
//     silently, so a binary deployed without sources generates schemas
//     without descriptions.
//   - It reports a canceled or expired Generate context as an error.
//   - It loads packages under the generator process's build context (its
//     GOOS, GOARCH, and active build tags). A type declared in a file that
//     context excludes gets no description, even when reflection sees the
//     type.
//   - It loads packages in the process working directory unless [WithLoadDir]
//     points it at another module's directory.
//   - A description in the jsonschema struct tag wins over the comment it
//     supplies.
//
// Any other implementation substitutes another source, such as comments
// pre-extracted at build time for a binary that deploys without source files,
// or fixed descriptions in tests. [ChainDescriptionProviders] composes
// providers, and the first non-empty description or the first error wins,
// which suits overrides for specific types backed by comment extraction.
// Field lookups receive the [FieldContext] tag interpreters get; for a field
// promoted from an embedded struct, [FieldContext.Owner] is the embedded type,
// where the field's doc comment lives.
//
// # Drafts
//
// The package supports [Draft7] and [Draft2020] (the default). The draft
// decides four things in the generated schema:
//
//   - The $schema URI.
//   - The definitions keyword, definitions or $defs.
//   - Where keywords beside a $ref go. Under [Draft7] a $ref'd field with
//     additional annotations from struct tags moves into an allOf wrap, and
//     for a nullable $ref field the wrap applies to the value branch of the
//     anyOf, where the field's const or enum lands. Under [Draft2020] sibling
//     keywords sit directly beside $ref.
//   - Whether allOf compositions close the object with unevaluatedProperties
//     or additionalProperties (see Struct Fields below).
//
// The [WithDraft] option serves generation, validation, and inlining alike.
// Generation targets the given draft. Validation and [Inline] use it in place
// of the draft they otherwise detect from the root schema's $schema field, for
// schema documents that omit $schema or carry one that does not reflect their
// dialect.
//
// Detection recognizes the Draft-07 and 2020-12 URIs. A document omitting
// $schema, or carrying a custom metaschema URI, defaults to [Draft2020]. A
// $schema declaring an official dialect this package does not implement
// (2019-09, draft-06, draft-04, or draft-03) fails [Compile] and [Inline]
// with [ErrUnsupportedDraft], so neither processes it under different
// semantics. A [WithDraft] override processes such a document explicitly.
//
// # Null Encoding
//
// Whether an occurrence admits null is decided once, from four inputs:
//
//   - the occurrence (a pointer or an intercepted interface admits null; a
//     value does not);
//   - the [Nullability] stance a type-level hook declared for the type
//     ([NullAllowed] grants it, [NullForbidden] withholds it);
//   - the container kind (a slice, map, or []byte admits null only where the
//     marshal writes one);
//   - the [WithJSONOptions] value (FormatNilSliceAsNull and
//     FormatNilMapAsNull are what make it write one).
//
// Every consumer reads that one answer: the null branch the schema carries,
// the null literal a tag or an interpreter may write, [FieldContext.Shape],
// and a [WithDefaultsFrom] null. A null-admitting field renders as
// anyOf[value, null], or as a ["null", base] type list for a container with
// no const or enum.
//
// Each keyword a field-level hook declares (the description provider, the
// jsonschema tag, or a tag interpreter) lands on the value branch or the null
// wrapper by a fixed rule, so it resolves to the same value on a nullable
// field as on a non-nullable one:
//
//   - const, enum, the string-content keywords, and the replacing keywords
//     pattern, format, and multipleOf stay on the value branch.
//   - Annotations and the authored bounds move to the wrapper.
//
// An authored bound only tightens the type's own bound, never weakens it,
// whether a tag, an interpreter, or a third-party writer set it. Generation
// keeps the stronger side.
//
// A const or enum an interpreter declares can disagree with one the type
// already carries:
//
//   - Against an inline override or provider value, generation reports the
//     conflict rather than overwriting it.
//   - Against a $defs-extracted type, the declared value rides beside the
//     $ref and the two compose conjunctively. An enum intersects and only
//     tightens, and a disagreeing const composes to a faithfully
//     unsatisfiable schema rather than aborting generation.
//
// # Validation
//
// The package validates JSON instances against schemas and returns structured
// errors with full path information and every failure collected. [Compile]
// does the per-schema work once (draft and vocabulary detection, the checks
// listed below, and reference resolution) and returns a [Validator] that is
// reused across instances and safe for concurrent use, with one method per
// instance shape:
//
//   - [Validator.Validate] validates a pre-parsed Go value (map[string]any,
//     []any, string, float64, [encoding/json.Number], bool, nil). It also
//     accepts the Go numeric kinds encoding/json does not produce (the signed
//     and unsigned integer types and float32) and normalizes them via
//     [Normalize], so values decoded from YAML or TOML validate directly
//     (integers exactly, at any magnitude). It rejects a self-referential
//     instance (a map or slice that contains itself) rather than walking it.
//   - [Validator.ValidateJSON] decodes raw JSON bytes with [encoding/json/v2],
//     reading every number as [encoding/json.Number] to preserve the integer
//     vs number distinction, then validates. Decoding rejects duplicate object
//     member names and invalid UTF-8 (RFC 7493), as v2 does.
//   - [Validator.ValidateReader] applies the [Validator.ValidateJSON] rules to
//     an [io.Reader], decoding it to EOF before validating. Numbers decode as
//     [encoding/json.Number], and the decoder rejects trailing data, duplicate
//     object member names, and invalid UTF-8.
//   - [Validator.ValidateValue] marshals a Go value with [encoding/json/v2] and
//     validates its JSON form, so an instance of the very type a schema was
//     generated for validates in one call. The json tags, omitempty and
//     omitzero, and MarshalJSON implementations all apply, exactly as a JSON
//     consumer of the value would see them. A non-pointer value marshals
//     through a pointer to a copy, so pointer-receiver MarshalJSON/MarshalText
//     implementations (big.Int's, for example) apply as they would for &v,
//     and a value instance validates identically to a pointer instance.
//
// A compiled Validator also reports what it validates. [Validator.Schema]
// returns a copy of the root schema it was compiled for, and [Validator.Draft]
// the draft in effect, so a validator can cross package boundaries without
// the schema riding alongside. The copy shares nothing with the caller's
// value, and a schema the caller's value reaches through two paths appears
// twice in the copy. The validator reads only its copy, so mutating the
// caller's value after [Compile] changes nothing. Treat the returned copy as
// read-only, and recompile after any change.
//
// The package-level [Validate] is the one one-shot form. It compiles the
// schema and validates one pre-parsed instance in a single call, for the
// quick check that does not warrant holding a Validator; raw bytes or a
// marshalable value are one Compile away. [MustCompile] is [Compile] but
// panics on error, for package-scope validators where a static schema and
// fixed options either always compile or always fail; it follows
// [MustGenerateFor], and [MustCompileJSON] is its [CompileJSON] counterpart
// (for embedded schema files, for example).
//
// A schema arriving as a JSON document rather than a [*Schema] has symmetric
// entry points:
//
//   - [CompileJSON] decodes data as a single JSON schema document (numbers as
//     [encoding/json.Number]; trailing data, duplicate object member names,
//     and invalid UTF-8 rejected) and compiles it with [Compile].
//   - [ParseSchema] is its decode half alone, returning the [*Schema]
//     uncompiled for consumers that work with the schema itself ([Inline],
//     [Walk], programmatic editing).
//   - [ParseSchemaValue] converts an already-decoded document (a bool or a
//     map[string]any, such as [Normalize] output) to a [*Schema]. A document
//     string holding invalid UTF-8 (reachable only from a hand-built or
//     non-JSON-sourced document) returns a wrapped encode error rather than a
//     silent rewrite to U+FFFD.
//
// All three return an error wrapping [ErrInvalidSchemaDocument] for a
// top-level value that is not an object or boolean. That includes JSON null,
// which unmarshaling into a [Schema] directly silently coerces to the false
// schema. Malformed JSON returns the wrapped decode error without the
// sentinel.
//
// Every compile and validate entry point takes a [context.Context] as its
// first parameter and carries it to the [RefResolver] (see Remote References
// below); the Must* forms pass [context.Background], the right context for
// the package-scope use they serve.
//
// On success every entry point returns nil. A validation failure returns an
// error that unwraps to [*ValidationError] via [errors.AsType].
// Non-validation failures return ordinary wrapped errors that do not unwrap
// to [*ValidationError]. These cover JSON decoding, an unaccepted instance
// type, an invalid schema document ([ErrInvalidSchemaDocument]), the
// compile-time sentinels listed below, [ErrNotResolved], and
// [ErrUnknownVocabulary].
//
// Compile rejects a malformed document before any instance is validated, so
// a typo surfaces at construction instead of silently rejecting or accepting
// every instance. Each refusal wraps one sentinel:
//
//   - [ErrInvalidType]: a type keyword naming anything other than the seven
//     JSON Schema types ("null", "boolean", "string", "integer", "number",
//     "object", "array"). [CheckTypeNames] runs the same check standalone
//     (see Traversal below), and the two produce textually identical errors.
//   - [ErrItemsArrayUnderDraft2020]: under [Draft2020], the array form of the
//     items keyword (what a JSON "items": [ ... ] parses into). That form is
//     the Draft-07 spelling of tuple validation. 2020-12 spells tuples with
//     prefixItems, so an array-form items would otherwise drop silently and
//     accept every element. Set the Draft-07 $schema (or [WithDraft]) for
//     tuple semantics, or use prefixItems.
//   - [ErrNegativeBound]: a negative length or count keyword (minLength,
//     maxLength, minItems, maxItems, minProperties, maxProperties,
//     minContains, maxContains). The spec fixes the domain as a non-negative
//     integer. A negative maximum would reject every instance, and a negative
//     minimum would never fire.
//   - [ErrNonPositiveMultipleOf]: a multipleOf that is not strictly greater
//     than zero. The spec requires a number above zero. A non-positive
//     multipleOf would reject every numeric instance while accepting every
//     non-numeric one. A strictly positive literal below the smallest
//     positive float64 (about 4.9e-324) is spec-valid but underflows to zero
//     when the document is decoded, so [ParseSchema] and [ParseSchemaValue]
//     drop the keyword in that case (at float64 precision it constrains
//     nothing) rather than letting the underflowed zero fail as an authored
//     one.
//   - [ErrConflictingSchemaFields]: a schema setting both Go fields of one
//     JSON keyword (Type and Types, Defs and Definitions, Items and
//     ItemsArray, a dependencies key in both maps).
//   - [ErrNilSubschema]: a nil *Schema element inside a sub-schema slice or
//     map.
//   - [ErrDuplicatePropertyOrder]: a PropertyOrder entry listed twice.
//   - [ErrSchemaCycle]: a loop that crosses a schema, through a sub-schema
//     keyword or a value field (Const, Enum, Examples, or Extra). The error
//     names the pointer where the loop closes and the pointer it returns to.
//   - [ErrIDCollision]: a schema the caller's value reaches through two
//     paths, when that schema carries a $id or an anchor. Compile works on a
//     copy in which such a schema appears twice. A plain duplicate is
//     accepted, but two copies of an identifier would claim one key.
//   - [ErrInvalidID]: an $id outside the keyword's domain. That is one that
//     does not parse, one carrying a fragment under Draft 2020-12 (core
//     section 8.2.1), or one that does not resolve to an absolute URI against
//     its enclosing base (the parent $id chain, or [WithBaseURI] for the
//     root, so a relative root $id compiles exactly when a base supplies the
//     absolute prefix). Under Draft-07 two forms go unchecked, an $id beside
//     a $ref (the draft ignores it) and a fragment-carrying $id (the anchor
//     spelling).
//   - [ErrInvalidBaseURI]: a [WithBaseURI] value that does not parse.
//   - [ErrMisplacedVocabulary]: a $vocabulary on a schema whose $schema does
//     not establish the 2020-12 dialect. A non-empty $schema must be exactly
//     "https://json-schema.org/draft/2020-12/schema", while an empty one
//     inherits the run's dialect, accepted under 2020-12 and rejected under
//     Draft-07, which predates the vocabulary concept.
//
// [Inline] applies these same checks to the root it is given, to each
// document its references reach, and to each [SubstituteRef] schema a
// fallback supplies, so the two entry points refuse the same documents for
// the same sentinels. One compile-time refusal has no Inline counterpart,
// [ErrUnknownVocabulary], which belongs to the vocabulary resolution Inline
// does not run. See Inlining below, where [WithRetrievalBase] and
// [WithRefFallback] each narrow the rest.
//
// Compile then resolves every reference reachable from the root ($ref and,
// under 2020-12, $dynamicRef). A reference that resolves to nothing while its
// document is present can never resolve later, so Compile rejects it with an
// error wrapping [ErrNotResolved] (or the resolver's reported error), naming
// the schema that bears it. Compile tolerates a reference whose document it
// cannot locate, and a validation run reports it instead (see Remote
// References below).
//
// An uncompilable pattern or patternProperties regex is deliberately not a
// compile error. It fails every string instance it would judge, at validation
// time.
//
// Instance numbers compare exactly. A validation run decodes them as
// [encoding/json.Number] and compares them as [math/big.Rat], with one bound
// on the work an adversarial literal can demand. For a JSON number whose
// exact value exceeds a cap of about 4096 significant digits or decimal
// exponent magnitude:
//
//   - The run still enforces minimum, maximum, exclusiveMinimum, and
//     exclusiveMaximum exactly.
//   - The run enforces multipleOf for an over-cap integer and skips it only
//     for an over-cap non-integer, whose fractional part the cap cannot hold.
//
// The schema side is float64. The bound keywords (minimum, maximum,
// exclusiveMinimum, exclusiveMaximum, multipleOf) are float64 fields, so an
// integer beyond 2^53 rounds when the schema is decoded, even though the
// instance value it is compared against is exact. Both const and enum values
// keep exact precision (decoded as [encoding/json.Number]).
//
// On the generation side, an authored bound the shipped float64 would not
// reproduce fails with [ErrBoundNotRepresentable] rather than silently
// loosening, whichever source set it (the jsonschema tag or any tag
// interpreter). Generation accepts an integer bound only when the float64's
// shortest-decimal interpretation, the value the schema renders and the
// validator enforces, equals the authored value. 2^60, which renders as
// 1152921504606847000, fails; 2^54, which renders as itself, passes. Generation
// caps a bound parsed at an integer-kind field at 2^53 outright.
//
// A validation run reads a float64 in a pre-parsed instance (JSON decoding
// always yields [encoding/json.Number]) at its shortest decimal value across
// all numeric keywords, including the uniqueItems comparison, so float64(0.1)
// and a decoded 0.1 are one value under const, enum, and uniqueItems alike.
//
// A number-shaped value with no numeric value to compare passes a bare type
// assertion but fails every numeric bound keyword present (minimum, maximum,
// exclusiveMinimum, exclusiveMaximum, multipleOf). That covers a non-finite
// float64 (NaN or an infinity, which JSON cannot represent but a Go instance
// can carry) and an [encoding/json.Number] whose literal is not a valid JSON
// number. A bound written to constrain a number fails closed rather than
// silently skipping a value it cannot compare.
//
// A const or enum value on a hand-built schema compares as the JSON the
// emitted document renders for it, not as the Go value it holds. The upstream
// Schema.MarshalJSON writes const, enum, and examples with [encoding/json]
// v1, so Compile reads each value the same way. A value decoded from a JSON
// document already carries the document shape, so the rule is visible only to
// schemas built in Go:
//
//   - A typed nil map, slice, or pointer matches the instance null rather
//     than [] or {}.
//   - A float32 compares at its 32-bit shortest decimal.
//   - An empty [encoding/json.Number] compares as 0.
//   - A string carrying invalid UTF-8 compares with each bad byte replaced by
//     U+FFFD.
//   - A struct matches its marshaled object, and a raw
//     [encoding/json/jsontext.Value] matches the value its bytes decode to.
//   - A func, a channel, a cyclic value, a map whose keys [encoding/json]
//     cannot render, and a value whose own marshaler panics have no JSON
//     form. Compile keeps the Go value, which compares unequal to every
//     instance.
//
// Validation is configured via [ValidateOption] values:
//
//   - [WithDraft] overrides the draft otherwise detected from the root
//     schema's $schema field, for schemas that omit $schema (which would
//     default to [Draft2020]) or carry one that does not reflect their
//     dialect.
//   - [WithRefResolver] sets a [RefResolver] for resolving remote $ref URIs.
//     A run calls the resolver only when local fragment resolution fails, and
//     at most once per distinct URI, since it remembers resolved schemas,
//     not-resolved answers, and errors alike. The resolver receives
//     the caller's context (see Remote References below).
//   - [WithBaseURI] sets the root document's base URI, the base its non-local
//     refs absolutize against when no root $id establishes one, and registers
//     the root under it so a ref absolutizing back to the root resolves
//     in-memory. The returned [RefOption] also serves inlining, the way
//     [WithRefResolver] and [WithDraft] serve several entry points.
//   - [WithFormatValidator] registers a custom format checker (a
//     [FormatValidator]) under the format name it checks, with
//     [FormatValidatorFunc] adapting a bare function. The checker receives
//     the validation run's context and the name each check runs under.
//   - [WithFormats] forces built-in format assertion on or off. By default a
//     run asserts format under Draft-07 and treats it as annotation-only
//     under Draft 2020-12 unless the format-assertion vocabulary is active.
//   - [WithContent] asserts contentEncoding (base64) and contentMediaType
//     (application/json) for string instances. Both are annotation-only by
//     default. Base64 follows the draft's citation, RFC 4648 under 2020-12
//     (line breaks rejected) and MIME base64 under Draft-07 (line breaks
//     ignored). The media-type assertion judges the decoded content as
//     application/json per RFC 8259, so duplicate member names and invalid
//     UTF-8 pass there even though the validation entry points reject both
//     in the instance document itself.
//   - [WithVocabularies] directly specifies the active vocabularies for the
//     validation run. The listed URIs are active, and every other vocabulary
//     is inactive.
//   - [WithMetaSchemaResolver] sets a [RefResolver] consulted with the root
//     schema's $schema URI to look up the metaschema whose $vocabulary map
//     controls which keyword groups are active. A [SchemaMap] serves fixed
//     metaschemas by exact $id, and [ChainResolvers] composes resolvers.
//
// A validation run detects the draft from the root schema's $schema field,
// and a [WithDraft] option overrides the detection. The draft decides:
//
//   - The keywords in force: items/additionalItems vs prefixItems/items, and
//     dependentRequired/dependentSchemas under 2020-12. Both drafts honor the
//     legacy dependencies keyword; under 2020-12 it stays accepted for
//     backward compatibility alongside dependentRequired/dependentSchemas.
//   - The $ref sibling behavior. Draft-07 ignores siblings; 2020-12 processes
//     them.
//   - Which anchors name resolution targets. $anchor and $dynamicAnchor do so
//     only under 2020-12; under Draft-07 they are unknown annotations, and a
//     $ref naming one does not resolve. Conversely the Draft-07 fragment-only
//     $id anchor form names a target only under Draft-07, because 2020-12
//     forbids a fragment in $id.
//
// A validation run collects every failure; it does not stop on the first
// error. The returned [*ValidationError] forms a tree. Compositional keywords
// (allOf, anyOf, oneOf, if/then/else, $ref, $dynamicRef, unevaluated*) wrap
// their child failures in intermediate [ValidationError.Causes] entries,
// while container keywords (properties, items, additionalProperties) flatten
// child failures into the parent's Causes, each retaining its full instance
// and schema path. The not keyword produces a childless leaf error. Two
// failures carry a borrowed keyword or location:
//
//   - A false subschema failure ("value is not allowed") carries the
//     applicator keyword that applied it (for example additionalProperties
//     for additionalProperties: false). A standalone boolean false schema has
//     no applicator context and leaves Keyword empty.
//   - A propertyNames violation constrains a key, which has no JSON Pointer
//     of its own, so it borrows the property's location. The surfaced error
//     carries Keyword "propertyNames" and an InstancePath pointing at the
//     offending property, with the inner keyword failure in its Causes.
//
// The InstancePath and SchemaPath JSON Pointers are typed as
// [jsontext.Pointer], so the stdlib's Tokens, Parent, and LastToken helpers
// apply directly. Every error validation produces also carries both paths in
// typed form. [ValidationError.InstanceSegments] and
// [ValidationError.SchemaSegments] return one [Segment] per reference token,
// each marked as an object key or an array index.
//
// The segments resolve two ambiguities a JSON Pointer leaves open, so a
// source-mapping consumer need not re-parse the pointers and guess. A pointer
// cannot distinguish array index 1 from an object property named "1" (or an
// allOf branch from a property named "allOf"'s member), and its member keys
// carry ~0/~1 escaping. Hand-constructed errors return nil from both.
//
// The Keyword* constants ([KeywordRequired], [KeywordRef], and the rest) name
// every keyword validation reports, so code branching on
// [ValidationError.Keyword] needs no raw keyword strings. Three methods
// flatten the tree for reporting:
//
//   - [ValidationError.Unwrap] returns the attached errors across the whole
//     tree for [errors.Is] and [errors.As].
//   - [ValidationError.Leaves] returns one entry per distinct concrete
//     failure, with the wrapper entries removed. A propertyNames error counts
//     as a leaf, naming the offending key.
//   - [ValidationError.TargetsKey] reports whether the failing keyword
//     constrains a key, name, or collection structure (required,
//     additionalProperties, propertyNames, minItems, minProperties, and the
//     like) rather than a value, so a source-mapping consumer can highlight
//     the key instead of the value.
//
// Built-in format checkers cover date-time, date, time, duration, email,
// idn-email, hostname, idn-hostname, uri, uri-reference, uri-template, iri,
// iri-reference, uuid, ipv4, ipv6, json-pointer, relative-json-pointer, and
// regex.
//
// Each checker asserts its format's defining grammar. Where that grammar
// admits more than one reading, or where the string alone cannot decide what
// the spec asks, the checker takes the position below; [WithFormatValidator]
// replaces any of them.
//
//   - regex is a structural ECMA-262 check, not a compile. It checks balanced
//     groups, terminated classes, and well-formed escapes. It accepts
//     backreferences and lookaround, since ECMA-262 has them and Go's RE2
//     does not, and every ASCII character is a valid ECMA-262 Annex B
//     identity escape, so it accepts "\a" and "\_" (and a bare "\c", an Annex
//     B ExtendedAtom in its own right). The format therefore accepts patterns
//     RE2 rejects. This is independent of the pattern keyword, which does use
//     RE2.
//   - uri-template accepts the RFC 6570 op-reserve operators ("{=path}",
//     "{!x}", "{|x*}") and a prefix modifier on any varspec ("{keys:1}").
//     Both are valid under the RFC's ABNF and fail only during expansion,
//     which a check over the template string cannot reach. The literals rule
//     follows errata ID 6937, so an apostrophe is a literal.
//   - email and idn-email assert the RFC 5321 Mailbox grammar plus the
//     §4.5.3.1 size limits (64 octets local, 253 domain, 254 total). The
//     254-octet limit covers the whole address, so a domain long enough to
//     break the 253-octet limit already breaks the total; only idn-email
//     reaches the domain limit, counting the domain in its longer A-label
//     form. The checkers reject the RFC 5322 comment, folding-whitespace, and
//     obsolete productions that Draft-07's cited §3.4.1 would permit.
//     Deliverability is never consulted.
//   - uri and uri-reference accept an IPvFuture authority ("http://[v7.x]/"),
//     which RFC 3986 §3.2.2 defines and Go's net/url cannot parse.
//   - hostname accepts a reserved-LDH label (a hyphen in positions 3 and 4,
//     as in "ab--cd.com") per RFC 1123 §2.1, while idn-hostname rejects it
//     per RFC 5890 §2.3.2.2, so hostname accepts names idn-hostname does not.
//     idn-email follows hostname here, because RFC 6531 widens the RFC 5321
//     domain grammar by admitting U-labels rather than importing IDNA's label
//     rules.
//
// # Traversal
//
// Helpers work on [Schema] values directly, independent of generation and
// validation.
//
// [SubschemaEntries] returns the direct sub-schemas of a schema and defines
// which Schema fields hold sub-schemas:
//
//   - Every non-nil schema held by one sub-schema-bearing keyword (applicators
//     such as items, properties, allOf, not, if/then/else, plus $defs and
//     definitions) is an entry, paired with the RFC 6901 JSON Pointer
//     addressing it from the parent ("/properties/a", "/allOf/0", "/items").
//     A sub-schema carried as raw JSON inside an unknown keyword is not.
//   - Children held in maps come in sorted-key order, so traversal is
//     deterministic.
//   - Appending each visited child's pointer while descending yields the
//     schema path the package's own errors report.
//   - Each entry carries its [Location], pairing the pointer with the same
//     location in typed form ([Location.Segments], one [Segment] per
//     reference token, mirroring [ValidationError.InstanceSegments]), so a
//     consumer need not re-parse the pointer string.
//
// [Walk] calls a [WalkFunc] for a schema and every schema transitively
// reachable through [SubschemaEntries]:
//
//   - The traversal is pre-order. The function runs on a schema before its
//     children are gathered, so it may replace or mutate sub-schema fields and
//     the traversal follows the updated children.
//   - Each distinct schema pointer is visited once, so aliased or cyclic
//     graphs terminate.
//   - Walk stops at and returns the first error from the function, except
//     [SkipChildren], which prunes the traversal at that schema and continues
//     with its siblings.
//   - The function receives each visited schema's [Location] from the root,
//     the JSON Pointer and the typed [Segment] slice in one value, built by
//     appending each descended child's [SubschemaEntry] location. A traversal
//     with no use for the location ignores the parameter, following
//     [io/fs.WalkDir].
//
// [Schemas] is the iterator form of Walk, yielding the same locations and
// schemas in the same pre-order to a range loop, for read-only traversals.
// Breaking out of the loop stops the iteration. Mutating traversals and
// [SkipChildren] pruning stay with Walk.
//
// Three predicates answer common shape questions:
//
//   - [CheckTypeNames] verifies that every type keyword reachable from a
//     schema names one of the seven JSON Schema type names. It returns nil or
//     an error wrapping [ErrInvalidType] that includes the schema path of the
//     first offending keyword. It is the standalone form of the check
//     [Compile] runs before resolution, for vetting structurally messy
//     schemas (cyclic graphs, unresolvable references) without compiling
//     them.
//   - [IsTrueSchema] reports whether a schema is the boolean true schema
//     form, a schema with no fields set, which marshals to JSON true and
//     accepts every instance. Annotation-only schemas (a description but no
//     constraints) return false, as do schemas whose only field is a non-nil
//     empty map or slice (Schema{Enum: []any{}} vacuously rejects every
//     instance).
//   - [IsFalseSchema] reports whether a schema is the boolean false schema
//     form {"not": {}} (the shape the upstream produces when unmarshaling the
//     JSON boolean false), which marshals to JSON false and rejects every
//     instance. Any sibling field next to the not, including annotations,
//     defeats the form.
//
// # Vocabularies
//
// Draft 2020-12 introduces $vocabulary, which appears in metaschemas and maps
// vocabulary URIs to booleans marking each vocabulary required (true) or
// optional (false). The validator honors $vocabulary to gate keyword groups,
// and a run silently skips the keywords of an inactive vocabulary. The
// contains keyword's at-least-one rule (its default minContains=1 floor)
// belongs to contains itself in the applicator vocabulary, so disabling the
// validation vocabulary skips only the explicit minContains/maxContains
// bounds, not the default floor.
//
// The boolean governs only implementations that do not understand the
// vocabulary and has no effect on ones that do (core §8.1.2). This
// implementation understands every standard 2020-12 vocabulary, so a
// recognized vocabulary is active whenever its URI appears in the map, true
// or false alike. A metaschema with validation: false still asserts type, and
// one with format-assertion: false still asserts format. A vocabulary is
// inactive only when its URI is absent from the map.
//
// When the format-assertion vocabulary drives assertion, a format name with
// no registered checker rejects every string instance, per the 2020-12
// requirement that implementations fail upon encountering unknown formats
// (validation section 7.2.3). Assertion enabled by [WithFormats] or by
// Draft-07's default instead treats an unknown format name as
// annotation-only, asserting nothing.
//
// Vocabulary support is a Draft 2020-12 feature. Under Draft 7 the full
// built-in vocabulary set is always in force, and [WithVocabularies] and
// [WithMetaSchemaResolver] have no effect on it. Under Draft 2020-12,
// vocabulary resolution takes the first source that answers:
//
//  1. [WithVocabularies], the direct override.
//  2. [WithMetaSchemaResolver], consulted once per compile with the root
//     schema's $schema URI; a miss ([ErrNotResolved]) falls through to the
//     default. A [SchemaMap] serves fixed metaschemas by exact $id, and
//     [ChainResolvers] composes resolvers.
//  3. The built-in standard vocabulary set, every group active except
//     format-assertion, so format is annotation-only by default.
//
// A schema that requires (marks true) a vocabulary URI this implementation
// does not recognize fails [Compile] with [ErrUnknownVocabulary]. So does a
// $vocabulary map that marks the 2020-12 core vocabulary optional (false) or
// omits it entirely, neither of which the spec permits.
//
// # Remote References
//
// Local fragment refs (those under #/$defs or #/definitions, plus #anchor
// forms) resolve within the document. Remote and absolute $ref URIs resolve
// through an optional [RefResolver] set with [WithRefResolver];
// [RefResolverFunc] adapts a bare function, so a one-off resolver needs no
// named type. A resolver reports a URI it does not serve by answering
// [ErrNotResolved], the not-resolved answer that passes the URI along (to the
// next [ChainResolvers] link, and ultimately to unresolvable-ref handling),
// following [io/fs.ErrNotExist].
//
// [Compile] fetches every document a reference reachable from the root
// names, and every document those documents name in turn, until no reference
// is left. Each fetched document registers under the URI it was fetched from,
// passes the checks below, and stays with the compiled [Validator], so no
// later reference or validation run asks the resolver for it. A miss or
// failure is remembered for the rest of the run that saw it, so an
// unresolvable URI costs one resolver call per run however many refs name it.
//
// A document the resolver cannot serve at compile time (a not-resolved
// answer, or any other resolver error) does not fail Compile, since the
// resolver may serve it later; a validation run reports the reference
// instead. A fragment that cannot resolve inside a document that is present,
// the root or a fetched document, fails Compile with an error wrapping
// [ErrNotResolved], since it can never resolve later.
//
// A validation run reports an unresolvable remote or absolute $ref as a
// [*ValidationError]. With no resolver (or a resolver answering
// ErrNotResolved) the message begins with "cannot resolve $ref" and includes
// the quoted ref, while a resolver that returns any other error yields one
// wrapping [ErrRefResolve].
//
// The run reports an unresolvable local fragment ref the same way when it sits
// inside a document first fetched during that run or inside a schema reached
// through an unknown keyword. Inside a document Compile checked, the run skips
// such a ref, since Compile already rejected the broken ones. The run detects
// circular refs and treats them as passing.
//
// One policy governs every document a reference reaches, from the moment it
// is reached. That covers a fetched document at its fetch, whether at compile
// time or during a validation run; a [SubstituteRef] schema at the
// substitution site; and a schema carried inside an unknown keyword or the
// internals of a non-applicator keyword, when a JSON Pointer reference first
// reaches it. Three checks run, in a fixed order: the cycle check, the
// identifier check, and the structural checks.
//
// The cycle check runs on a copy of the document, so nothing reads or mutates
// the resolver's return value again, and a schema the source reaches through
// two paths appears twice in the copy. The copy must hold no loop that crosses
// a schema, through a sub-schema keyword or a value field (Const, Enum,
// Examples, or Extra):
//
//   - A fetched document with a loop fails the referencing ref with an error
//     wrapping [ErrRefResolve] and [ErrSchemaCycle].
//   - A cyclic substitute fails with [ErrSchemaCycle] at the substitution
//     site.
//   - Either refusal names the pointer where the loop closes and the pointer
//     it returns to, rooted at the document checked.
//   - A container that loops without crossing a schema passes to
//     [encoding/json], which reports it as an ordinary error.
//   - A duplicated schema that carries a $id or an anchor fails with
//     [ErrIDCollision], since its two copies would claim one key.
//
// The identifier check requires that a fetched document claim no identifier
// another document already holds. A $id that resolves to a URI another
// document holds, or an $anchor or $dynamicAnchor key registered for a
// different schema, fails the referencing ref with an error wrapping
// [ErrRefResolve] and [ErrIDCollision], naming the identifier and both
// documents. The check judges a substitute on its $id alone, since it
// registers under the base of the reference it answers and an $anchor it
// carries reaches no reference. Three cases make no claim:
//
//   - A document registers under the URI it was fetched from whatever its $id
//     says, so the near-universal remote whose $id repeats its own retrieval
//     URI passes, as does the same document fetched again.
//   - A duplicate $id or anchor within one document resolves to the first one
//     reached.
//   - A schema inside an unknown keyword is a fragment of a document the run
//     already holds rather than a document of its own. Two such schemas
//     claiming one key resolve in the order references reach them, and such a
//     schema skips this check altogether, since the check needs a document
//     base no pointer target carries.
//
// The structural checks run last: field structure, the $id domain, type
// names, non-negative bounds, and under [Draft2020] the Draft-07 items array,
// the same checks Compile applies to the root. The failing check's sentinel is
// the refusal ([ErrInvalidType], [ErrNegativeBound],
// [ErrNonPositiveMultipleOf], [ErrItemsArrayUnderDraft2020],
// [ErrConflictingSchemaFields], [ErrNilSubschema],
// [ErrDuplicatePropertyOrder], or, for a fetched document, [ErrInvalidID] or
// [ErrMisplacedVocabulary]).
//
// A validation run and [Inline] both fail the referencing ref with an error
// wrapping [ErrRefResolve] and that sentinel. [Compile] reports the bare
// sentinel, framing a violation inside an unknown-keyword schema under the
// failing reference and a fetched document's under that document's own
// locator.
//
// The order is fixed, so a document carrying both an identifier collision and
// a structural violation fails with [ErrIDCollision]. Documents are fetched in
// a fixed order too, so a graph carrying faults in several documents fails on
// the first fault reached, and [Compile] and [Inline] name the same one.
//
// Non-local refs absolutize against the enclosing resource's base URI, which
// is its $id or the root base set with [WithBaseURI]. That base also
// registers the root document under its URI, so a ref absolutizing back to it
// resolves in-memory. The same [WithBaseURI] value serves [Inline] (see
// Inlining below), so one option configures both.
//
// The resolver receives a context with every resolution call: the [Compile]
// context for refs resolved while compiling, and the [Validator.Validate] (or
// other validation entry point) context for refs reached during that
// validation run, so a resolver that fetches over the network can honor
// cancellation and deadlines. A compiled [Validator] never retains a context
// (each run carries its own), and the Must* entry points pass
// [context.Background]. The package ships no network resolver; fetching
// remains the caller's concern.
//
// # Inlining
//
// [Inline] returns a fresh schema tree in which a copy of the target schema
// replaces every $ref (in the schema body, $defs, and definitions alike). The
// result is a single self-contained document for consumers that
// cannot follow references, such as code generators. Inline never mutates the
// input or any resolver-returned schema, and the output shares no schema with
// either. Every position in the result is its own *Schema, so the result
// compiles whatever the input's pointer graph looked like.
//
// Inline applies its options per call. [NewInliner] applies them once and
// returns a reusable [Inliner], completing the reusable trio with [Generator]
// and [Validator].
//
// Resolution mirrors the validator's:
//
//   - Fragment-only refs (#/pointer, #anchor) resolve within the enclosing
//     document, using the same $id and $anchor resolution the validator uses.
//     Every ref resolves against its document's original structure, so
//     expanding one ref never changes what a later ref's JSON Pointer or
//     anchor addresses.
//   - Other refs absolutize against the enclosing resource's base URI, the
//     resource's $id or the base given via [WithBaseURI]. Inline then fetches
//     the document through the [RefResolver] given via [WithRefResolver] and
//     evaluates any fragment against the fetched document.
//   - A schemeless base is normalized against file:///, so a back-reference
//     to the root document finds the in-memory copy.
//   - Inline expands fetched documents recursively under their own base URIs,
//     so a relative ref inside a fetched document resolves against that
//     document's URI and files can reference each other by relative path.
//     Each document is fetched at most once per call.
//   - [FileResolver] (constructed with [NewFileResolver]) adapts an [io/fs.FS]
//     to this interface, serving file-path and relative URIs from the fs root.
//     Each referenced file must contain a JSON schema document, and [io/fs]
//     confines resolution to the fs root, so a ref escaping above it returns
//     an error wrapping [ErrRefResolve]. Pair [os.DirFS] with [WithBaseURI]
//     to inline a directory of schemas. The same resolver also serves
//     file-path and relative refs during validation via [WithRefResolver].
//   - [StripPrefix] wraps any resolver to strip a published remote base from
//     each URI first, so refs absolutizing against an https $id can be served
//     from the fs.
//   - Inline passes its context to the resolver with every document fetch, so
//     a resolver that fetches over the network can honor cancellation and
//     deadlines.
//
// [WithRetrievalBase] makes refs resolve against each document's retrieval
// URI instead, treating $id as an inert annotation. Real-world schemas
// commonly declare a published remote $id while shipping the files their refs
// name alongside the schema; under the default RFC behavior those refs
// absolutize against the remote $id and cannot be served from disk. Under the
// option:
//
//   - $id neither establishes a base URI nor registers a resolution target,
//     in any document, including the Draft 7 fragment-only $id form that
//     otherwise acts as an anchor.
//   - $anchor and $dynamicAnchor still resolve within their document.
//   - $id keywords pass through to the output verbatim. An inert $id
//     addresses nothing, so Inline skips its domain check.
//   - The root document's refs absolutize against the base from
//     [WithBaseURI], and each fetched document's refs against the URI it was
//     fetched from.
//
// Sibling keywords beside $ref follow draft semantics. Inline detects the
// draft from the root schema's $schema exactly as the validator does, and a
// [WithDraft] option overrides the detection the same way. Fetched documents
// follow the root document's draft, matching how validation applies one draft
// throughout.
//
//   - Under Draft 2020-12 the referencing schema keeps its sibling keywords
//     and the target copy joins its allOf, preserving both the conjunction
//     and the annotation flow the unevaluated* keywords depend on.
//   - Under Draft 7 the target copy alone replaces the referencing schema,
//     since that draft ignores siblings of $ref.
//   - When $ref is the schema's only keyword, the target copy alone replaces
//     it under either draft.
//
// A spliced copy never carries a $schema keyword, and the returned root keeps
// the input's $schema. A spliced copy also carries no $id, $anchor, or
// $dynamicAnchor anywhere in its subtree. The names identify the target at
// its original position, and duplicating them at each splice would declare
// the same identifier several times in one document. The copy is
// self-contained, so the names have nothing left to resolve.
//
// Inline expands refs only in typed sub-schema positions (those
// [SubschemaEntries] covers). It leaves a $ref carried as raw JSON inside an
// unknown keyword as-is, although a ref pointing into such a position still
// resolves.
//
// Inline applies the checks Remote References (above) describes to every
// document a reference reaches, the moment it is reached: the root before any
// reference resolves, each fetched document at its fetch, each
// [SubstituteRef] schema at the substitution site, and a schema inside an
// unknown keyword when a JSON Pointer reference first reaches it. Where a
// check fails decides how Inline reports it:
//
//   - In the root, Inline returns the sentinel of the check that failed, in a
//     message naming the offending path. The same roots pass at both entry
//     points.
//   - In a fetched document, Inline returns an error wrapping [ErrRefResolve]
//     and that sentinel.
//   - In a substitute, Inline returns the sentinel in a message naming the
//     reference the substitute answered and the document and path where it
//     consulted the fallback.
//
// A loop that crosses a schema in any of them is [ErrSchemaCycle]. Under
// [WithRetrievalBase] Inline skips the $id domain check throughout the run,
// since an inert $id establishes no base and registers no target. A fetched
// document follows the root document's draft, so a Draft-07 run leaves a
// Draft-07 array-form items remote intact.
//
// Before expanding anything, Inline fetches every document the root's
// references name, and every document those name in turn, the same set
// [Compile] fetches. That fetch holds a document reachable only through
// another document's reference to the same checks as one the root names
// outright, so the two entry points refuse the same reference graphs.
//
// The fetch refuses a reference that resolves to nothing inside a document
// that is present, wherever it sits, including a branch no expansion would
// have copied, since such a reference can never resolve later. It tolerates a
// document the resolver cannot serve, as [Compile] does, and the expansion
// that reaches the reference reports it, so an unreachable remote in a branch
// no expansion visits surfaces no error.
//
// Expansion fails in three ways:
//
//   - A ref whose target is a schema the expansion is already inside closes a
//     reference cycle and returns an error wrapping [ErrRefCycle], since a
//     cyclic reference graph has no finite expansion. The root document
//     counts as entered, so a ref back into it closes the cycle at the same
//     depth however the expansion reached the target.
//   - A $dynamicRef under Draft 2020-12 returns an error wrapping
//     [ErrRefInline], since its target depends on the dynamic scope at
//     validation time and no single replacement preserves that. Draft 7
//     ignores the keyword, as the validator does.
//   - A non-local ref with no resolver configured, or any ref whose target
//     cannot be found, returns an error wrapping [ErrRefResolve].
//
// The fetch before expansion resolves a $dynamicRef to reach the document it
// names but never refuses on one. A $dynamicRef that resolves to nothing in a
// document no expansion reaches therefore leaves Inline silent where [Compile]
// refuses, and one whose JSON Pointer target fails the document checks
// answers [ErrRefInline] rather than the sentinel [Compile] reports. Under a
// [WithRefFallback] that drops the reference, Inline succeeds on that input,
// and the inlined document, carrying no $dynamicRef, compiles.
//
// [WithRefFallback] sets a per-reference failure policy (a [RefFallback])
// that Inline consults when expanding a reference fails for any of those
// reasons. The fallback receives a [RefFailure] carrying the URI of the
// containing document, the JSON Pointer path of the referencing schema within
// that document, the reference value, and the error, and runs under the
// Inline call's context. It answers with a [RefAction]:
//
//   - [PropagateRef] propagates the original error and ends the Inline call.
//   - [DropRef] drops the failing reference keyword while keeping the schema's
//     remaining keywords.
//   - [SubstituteRef] supplies a substitute schema the reference expands to
//     as if it had resolved there, with the usual draft sibling semantics.
//     Inline copies the substitute before splicing and inlines it
//     recursively, its refs resolving in the context of the document
//     containing the failing ref. A cycle the substitute introduces is an
//     ordinary [ErrRefCycle].
//
// Inline consults the fallback once per failure, at the reference that
// directly failed. A failure inside a nested expansion consults the innermost
// failing ref with its path in its containing document, and a declined
// consultation propagates outward without re-consulting at the enclosing
// refs. A cycle failure belongs to the expansion that closed it, and the same
// source ref consults again from a position whose expansion is inside a
// different set of schemas.
//
// Configuring a fallback also relaxes the fetch before expansion. A
// [RefFallback] answers one failing reference at a time, and a document
// reachable only through another document's reference has no reference in the
// expansion for a policy to answer. With a fallback set, the fetch therefore
// refuses only an identifier collision, and the expansion reports each other
// failure at the references it does reach. A run with no fallback refuses a
// violation anywhere in the fetched set, so adding a fallback that only
// propagates is not the same as adding none.
//
// [ErrIDCollision] is the one refusal a fallback does not suspend. A
// substitute stands in for one reference and cannot decide which of two
// documents owns a URI they both claim, so Inline refuses a colliding document
// wherever it sits.
package jsonschema
