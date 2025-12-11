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
