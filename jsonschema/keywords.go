package jsonschema

// JSON Schema keyword name constants, so a consumer branching on
// [ValidationError.Keyword] or building schema locations from
// [Location.Segments] needs no raw keyword strings. The generator
// recognizes a subset as jsonschema struct tag keys; the validator reports
// the failing keyword on [ValidationError.Keyword].
const (
	KeywordAdditionalItems       = "additionalItems"
	KeywordAdditionalProperties  = "additionalProperties"
	KeywordAllOf                 = "allOf"
	KeywordAnyOf                 = "anyOf"
	KeywordConst                 = "const"
	KeywordContains              = "contains"
	KeywordContentEncoding       = "contentEncoding"
	KeywordContentMediaType      = "contentMediaType"
	KeywordContentSchema         = "contentSchema"
	KeywordDefault               = "default"
	KeywordDefinitions           = "definitions"
	KeywordDefs                  = "$defs"
	KeywordDependencies          = "dependencies"
	KeywordDependentRequired     = "dependentRequired"
	KeywordDependentSchemas      = "dependentSchemas"
	KeywordDeprecated            = "deprecated"
	KeywordDescription           = "description"
	KeywordDynamicRef            = "$dynamicRef"
	KeywordElse                  = "else"
	KeywordEnum                  = "enum"
	KeywordExamples              = "examples"
	KeywordExclusiveMaximum      = "exclusiveMaximum"
	KeywordExclusiveMinimum      = "exclusiveMinimum"
	KeywordFormat                = "format"
	KeywordIf                    = "if"
	KeywordItems                 = "items"
	KeywordMaxContains           = "maxContains"
	KeywordMaximum               = "maximum"
	KeywordMaxItems              = "maxItems"
	KeywordMaxLength             = "maxLength"
	KeywordMaxProperties         = "maxProperties"
	KeywordMinContains           = "minContains"
	KeywordMinimum               = "minimum"
	KeywordMinItems              = "minItems"
	KeywordMinLength             = "minLength"
	KeywordMinProperties         = "minProperties"
	KeywordMultipleOf            = "multipleOf"
	KeywordNot                   = "not"
	KeywordOneOf                 = "oneOf"
	KeywordPattern               = "pattern"
	KeywordPatternProperties     = "patternProperties"
	KeywordPrefixItems           = "prefixItems"
	KeywordProperties            = "properties"
	KeywordPropertyNames         = "propertyNames"
	KeywordReadOnly              = "readOnly"
	KeywordRef                   = "$ref"
	KeywordRequired              = "required"
	KeywordThen                  = "then"
	KeywordTitle                 = "title"
	KeywordType                  = "type"
	KeywordUnevaluatedItems      = "unevaluatedItems"
	KeywordUnevaluatedProperties = "unevaluatedProperties"
	KeywordUniqueItems           = "uniqueItems"
	KeywordWriteOnly             = "writeOnly"
)

// Content-encoding and format value constants for the values the generator
// emits and the validator asserts.
const (
	contentEncodingBase64 = "base64"
	formatDateTime        = "date-time"
)
