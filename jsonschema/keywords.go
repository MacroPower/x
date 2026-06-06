package jsonschema

// JSON Schema type name constants, shared by the generator (schema type
// fields) and the validator (instance type checks).
const (
	typeNameNull    = "null"
	typeNameBoolean = "boolean"
	typeNameString  = "string"
	typeNameInteger = "integer"
	typeNameNumber  = "number"
	typeNameObject  = "object"
	typeNameArray   = "array"
)

// JSON Schema keyword name constants. The generator recognizes a subset as
// jsonschema struct tag keys; the validator reports assertion keywords on
// [ValidationError.Keyword].
const (
	keywordAllOf                 = "allOf"
	keywordAnyOf                 = "anyOf"
	keywordConst                 = "const"
	keywordContains              = "contains"
	keywordContentEncoding       = "contentEncoding"
	keywordContentMediaType      = "contentMediaType"
	keywordDefault               = "default"
	keywordDependencies          = "dependencies"
	keywordDependentRequired     = "dependentRequired"
	keywordDeprecated            = "deprecated"
	keywordDescription           = "description"
	keywordElse                  = "else"
	keywordEnum                  = "enum"
	keywordExamples              = "examples"
	keywordExclusiveMaximum      = "exclusiveMaximum"
	keywordExclusiveMinimum      = "exclusiveMinimum"
	keywordFormat                = "format"
	keywordMaxContains           = "maxContains"
	keywordMaximum               = "maximum"
	keywordMaxItems              = "maxItems"
	keywordMaxLength             = "maxLength"
	keywordMaxProperties         = "maxProperties"
	keywordMinContains           = "minContains"
	keywordMinimum               = "minimum"
	keywordMinItems              = "minItems"
	keywordMinLength             = "minLength"
	keywordMinProperties         = "minProperties"
	keywordMultipleOf            = "multipleOf"
	keywordNot                   = "not"
	keywordOneOf                 = "oneOf"
	keywordPattern               = "pattern"
	keywordPatternProperties     = "patternProperties"
	keywordReadOnly              = "readOnly"
	keywordRequired              = "required"
	keywordThen                  = "then"
	keywordTitle                 = "title"
	keywordType                  = "type"
	keywordUnevaluatedItems      = "unevaluatedItems"
	keywordUnevaluatedProperties = "unevaluatedProperties"
	keywordUniqueItems           = "uniqueItems"
	keywordWriteOnly             = "writeOnly"
)

// Content-encoding and format value constants for the values the generator
// emits and the validator asserts.
const (
	contentEncodingBase64 = "base64"
	formatDateTime        = "date-time"
)
