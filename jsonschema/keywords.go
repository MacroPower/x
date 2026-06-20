package jsonschema

import "go.jacobcolvin.com/x/jsonschema/internal/keyword"

// JSON Schema keyword name constants, so a consumer branching on
// [ValidationError.Keyword] or building schema locations from
// [Location.Segments] needs no raw keyword strings. The generator
// recognizes a subset as jsonschema struct tag keys; the validator reports
// the failing keyword on [ValidationError.Keyword]. The values live in
// [go.jacobcolvin.com/x/jsonschema/internal/keyword] so internal packages can
// share them without an import cycle; these are re-exports.
const (
	KeywordAdditionalItems       = keyword.AdditionalItems
	KeywordAdditionalProperties  = keyword.AdditionalProperties
	KeywordAllOf                 = keyword.AllOf
	KeywordAnyOf                 = keyword.AnyOf
	KeywordConst                 = keyword.Const
	KeywordContains              = keyword.Contains
	KeywordContentEncoding       = keyword.ContentEncoding
	KeywordContentMediaType      = keyword.ContentMediaType
	KeywordContentSchema         = keyword.ContentSchema
	KeywordDefault               = keyword.Default
	KeywordDefinitions           = keyword.Definitions
	KeywordDefs                  = keyword.Defs
	KeywordDependencies          = keyword.Dependencies
	KeywordDependentRequired     = keyword.DependentRequired
	KeywordDependentSchemas      = keyword.DependentSchemas
	KeywordDeprecated            = keyword.Deprecated
	KeywordDescription           = keyword.Description
	KeywordDynamicRef            = keyword.DynamicRef
	KeywordElse                  = keyword.Else
	KeywordEnum                  = keyword.Enum
	KeywordExamples              = keyword.Examples
	KeywordExclusiveMaximum      = keyword.ExclusiveMaximum
	KeywordExclusiveMinimum      = keyword.ExclusiveMinimum
	KeywordFormat                = keyword.Format
	KeywordIf                    = keyword.If
	KeywordItems                 = keyword.Items
	KeywordMaxContains           = keyword.MaxContains
	KeywordMaximum               = keyword.Maximum
	KeywordMaxItems              = keyword.MaxItems
	KeywordMaxLength             = keyword.MaxLength
	KeywordMaxProperties         = keyword.MaxProperties
	KeywordMinContains           = keyword.MinContains
	KeywordMinimum               = keyword.Minimum
	KeywordMinItems              = keyword.MinItems
	KeywordMinLength             = keyword.MinLength
	KeywordMinProperties         = keyword.MinProperties
	KeywordMultipleOf            = keyword.MultipleOf
	KeywordNot                   = keyword.Not
	KeywordOneOf                 = keyword.OneOf
	KeywordPattern               = keyword.Pattern
	KeywordPatternProperties     = keyword.PatternProperties
	KeywordPrefixItems           = keyword.PrefixItems
	KeywordProperties            = keyword.Properties
	KeywordPropertyNames         = keyword.PropertyNames
	KeywordReadOnly              = keyword.ReadOnly
	KeywordRef                   = keyword.Ref
	KeywordRequired              = keyword.Required
	KeywordThen                  = keyword.Then
	KeywordTitle                 = keyword.Title
	KeywordType                  = keyword.Type
	KeywordUnevaluatedItems      = keyword.UnevaluatedItems
	KeywordUnevaluatedProperties = keyword.UnevaluatedProperties
	KeywordUniqueItems           = keyword.UniqueItems
	KeywordWriteOnly             = keyword.WriteOnly
)

// formatDateTime is the format value the generator emits and the validator
// asserts. (The base64 contentEncoding value lives in internal/content, which
// owns the content assertion.)
const formatDateTime = "date-time"
