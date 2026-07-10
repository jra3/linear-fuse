package marshal

import "fmt"

// FieldError describes a single field that could not be parsed or resolved.
// It lives here because the module that discovers a bad field mints the error
// that names it: the parse family below rejects malformed frontmatter values,
// and internal/fs's resolvers reject names that don't resolve — both speak the
// same error so the fs write-failure classifier (classifyMutationErr) maps
// either to EINVAL with the reason in .error. internal/fs aliases this type
// (`type FieldError = marshal.FieldError`), so errors.As matches across the
// package boundary. Detail renders the .error payload in the established
// "Field / Value / Error" format.
type FieldError struct {
	Field   string
	Value   string
	Message string
}

func (e *FieldError) Detail() string {
	if e.Value != "" {
		return fmt.Sprintf("Field: %s\nValue: %q\nError: %s", e.Field, e.Value, e.Message)
	}
	return fmt.Sprintf("Field: %s\nError: %s", e.Field, e.Message)
}

func (e *FieldError) Error() string { return e.Detail() }
