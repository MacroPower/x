package validate

import (
	"slices"

	"go.jacobcolvin.com/jsonschema"
)

// base64Encoding is the content validator tag and the contentEncoding keyword
// value for base64-encoded string content.
const base64Encoding = "base64"

// isStringKeywordTag reports whether key names a format, pattern, or content
// validator. These all map to string-only JSON Schema keywords, so the caller
// only applies them when the field's generated schema can hold a string
// instance (see [schemaPermitsString]), which includes []byte fields whose Go
// kind is not string but whose schema is a base64-encoded string.
func isStringKeywordTag(key string) bool {
	return formatFor(key) != "" || patternFor(key) != "" || isContentTag(key)
}

// schemaPermitsString reports whether the generated schema can hold a string
// instance. It accepts both the single Type form and the Types array form (as
// produced for nullable and []byte fields), so a string-only keyword such as
// base64 is allowed on a field whose schema is a string even when the Go kind is
// not (e.g. a []byte field, which generates a base64-encoded string schema).
func schemaPermitsString(s *jsonschema.Schema) bool {
	if s.Type == "string" {
		return true
	}

	return slices.Contains(s.Types, "string")
}

// applyStringKeywordTag applies the format, pattern, or content keyword named by
// key to a string schema. An explicit jsonschema struct tag value is preserved:
// each keyword is only set when not already present. Key must be recognized by
// [isStringKeywordTag].
func applyStringKeywordTag(key string, s *jsonschema.Schema) {
	if format := formatFor(key); format != "" {
		if s.Format == "" {
			s.Format = format
		}

		return
	}
	if pattern := patternFor(key); pattern != "" {
		if s.Pattern == "" {
			s.Pattern = pattern
		}

		return
	}

	applyContentTag(key, s)
}

// formatFor maps a format validator key to its JSON Schema "format" value, or
// "" if key is not a format tag.
func formatFor(key string) string {
	switch key {
	case "email":
		return "email"
	case "url":
		return "uri"
	case "uri":
		return "uri-reference"
	case "uuid":
		return "uuid"
	case "ipv4":
		return "ipv4"
	case "ipv6":
		return "ipv6"
	case "hostname":
		return "hostname"
	default:
		return ""
	}
}

// patternFor maps a pattern validator key to its JSON Schema "pattern" value, or
// "" if key is not a pattern tag.
func patternFor(key string) string {
	switch key {
	case "alpha":
		return `^[a-zA-Z]+$`
	case "alphanum":
		return `^[a-zA-Z0-9]+$`
	case "numeric":
		return `^[-+]?[0-9]+(?:\.[0-9]+)?$`
	case "number":
		return `^[0-9]+$`
	case "ascii":
		return `^[\x00-\x7F]*$`
	default:
		return ""
	}
}

// isContentTag reports whether key names a content validator.
func isContentTag(key string) bool {
	switch key {
	case "json", base64Encoding:
		return true
	default:
		return false
	}
}

// applyContentTag applies content validator tags to a string schema, preserving
// any value already set by an explicit jsonschema struct tag.
func applyContentTag(key string, s *jsonschema.Schema) {
	switch key {
	case "json":
		if s.ContentMediaType == "" {
			s.ContentMediaType = "application/json"
		}

	case base64Encoding:
		if s.ContentEncoding == "" {
			s.ContentEncoding = base64Encoding
		}
	}
}
