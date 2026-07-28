package marshal

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const frontmatterDelimiter = "---"

type Document struct {
	Frontmatter map[string]any
	Body        string
}

// Parse splits a markdown document into frontmatter and body
func Parse(content []byte) (*Document, error) {
	str := string(content)

	// Check for frontmatter delimiter
	if !strings.HasPrefix(str, frontmatterDelimiter) {
		return &Document{
			Frontmatter: make(map[string]any),
			Body:        str,
		}, nil
	}

	// Find the closing delimiter
	rest := str[len(frontmatterDelimiter):]
	idx := strings.Index(rest, "\n"+frontmatterDelimiter)
	if idx == -1 {
		return nil, fmt.Errorf("unclosed frontmatter")
	}

	// Extract frontmatter YAML
	fmYAML := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n"+frontmatterDelimiter):], "\n")

	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &frontmatter); err != nil {
		if hint := quotingHint(fmYAML); hint != "" {
			return nil, fmt.Errorf("failed to parse frontmatter: %w (%s)", err, hint)
		}
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	if frontmatter == nil {
		frontmatter = make(map[string]any)
	}

	return &Document{
		Frontmatter: frontmatter,
		Body:        body,
	}, nil
}

// yamlIndicatorChars are characters that begin YAML structure — flow
// collections, aliases/anchors, tags, block scalars, directives, comments. An
// unquoted scalar starting with one of these is read as syntax rather than a
// string; the classic trap is a title like `[1] Do the thing`, which YAML
// parses as a flow sequence and rejects.
const yamlIndicatorChars = "[]{}*&!|>%@`#,-"

// quotingHint inspects raw frontmatter YAML after a parse failure and, when a
// top-level value begins with a YAML indicator character, returns a short hint
// telling the user to quote it. It returns "" when no such value is found, so
// callers only append the hint to genuinely indicator-triggered failures.
func quotingHint(fmYAML string) string {
	for line := range strings.SplitSeq(fmYAML, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(trimmed, ": ")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		first := value[0]
		if strings.IndexByte(yamlIndicatorChars, first) < 0 {
			continue
		}
		// A balanced flow collection ([a, b] or {a: b}) is valid YAML — don't
		// flag it; the failure is elsewhere in the document.
		last := value[len(value)-1]
		if (first == '[' && last == ']') || (first == '{' && last == '}') {
			continue
		}
		return fmt.Sprintf(
			"hint: quote the value for %q — it starts with %q, which YAML reads as syntax; e.g. %s: %q",
			strings.TrimSpace(key), string(first), strings.TrimSpace(key), value,
		)
	}
	return ""
}

// Render combines frontmatter and body into a markdown document
func Render(doc *Document) ([]byte, error) {
	var buf bytes.Buffer

	if len(doc.Frontmatter) > 0 {
		buf.WriteString(frontmatterDelimiter)
		buf.WriteString("\n")

		fmBytes, err := yaml.Marshal(doc.Frontmatter)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal frontmatter: %w", err)
		}
		buf.Write(fmBytes)

		buf.WriteString(frontmatterDelimiter)
		buf.WriteString("\n")
	}

	buf.WriteString(doc.Body)

	return buf.Bytes(), nil
}
