package validate

import (
	"errors"
	"fmt"
	"strings"

	"go.jacobcolvin.com/x/jsonschema"
)

// isConflict reports whether an error from the shared model is a value-constraint
// clash, which this dialect re-reports through its own sentinel.
func isConflict(err error) bool {
	return errors.Is(err, jsonschema.ErrConstraintConflict)
}

// conflictDetail phrases a clash in this dialect's terms. The model reports what
// happened -- a different value is already pinned, an enumeration is already set
// -- and this names the tag that tried to add one, so the message says which
// rule to look at without every rule needing its own conflict check.
func conflictDetail(key, value string, err error) string {
	if value == "" {
		return fmt.Sprintf("%s conflicts with a constraint already in force: %s", key, unwrapConflict(err))
	}

	return fmt.Sprintf("%s=%s conflicts with a constraint already in force: %s", key, value, unwrapConflict(err))
}

// unwrapConflict strips the shared sentinel's own prefix, leaving the specific
// reason the model gave, so the composed message reads as one sentence rather
// than repeating "conflicting value constraints" twice.
func unwrapConflict(err error) string {
	msg := err.Error()

	prefix := jsonschema.ErrConstraintConflict.Error() + ": "

	_, reason, found := strings.Cut(msg, prefix)
	if !found {
		return msg
	}

	return reason
}
