package marshal

import "strings"

// MarkdownToStatusUpdate extracts body and health from status-update content
// (shared by project and initiative updates). Supports plain text or markdown
// with YAML frontmatter containing a health field; plain text defaults health
// to onTrack. An explicitly written but unrecognized health value is a
// *FieldError (-> EINVAL), as is frontmatter whose body is empty — the writer
// expressed intent, so silently creating an onTrack update (or nothing) would
// swallow it. Only content with no frontmatter may parse to an empty body; the
// caller treats that as flush noise and no-ops. Unclosed frontmatter is a
// *FieldError too (not the raw bytes posted as the update body — the old hand
// scanner's silent fallback): a FieldError classifies as EINVAL where a plain
// parse error would read as a backend failure (EIO).
func MarkdownToStatusUpdate(content []byte) (body string, health string, err error) {
	health = "onTrack" // Default health

	// Frontmatter presence is syntactic: Parse returns an empty map both for
	// "no frontmatter" and "empty frontmatter block", but only the former may
	// no-op on an empty body.
	hasFrontmatter := strings.HasPrefix(string(content), frontmatterDelimiter)

	doc, perr := Parse(content)
	if perr != nil {
		return "", "", &FieldError{Field: "frontmatter", Message: perr.Error()}
	}

	// Normalize the health value; coerce first so a bare scalar isn't dropped.
	if raw, ok := doc.Frontmatter["health"]; ok {
		h := ScalarToString(raw)
		switch strings.ToLower(h) {
		case "ontrack", "on track", "on-track":
			health = "onTrack"
		case "atrisk", "at risk", "at-risk":
			health = "atRisk"
		case "offtrack", "off track", "off-track":
			health = "offTrack"
		default:
			return "", "", &FieldError{Field: "health", Value: h,
				Message: "invalid health: must be onTrack, atRisk, or offTrack"}
		}
	}

	body = strings.TrimSpace(doc.Body)
	if body == "" && hasFrontmatter {
		return "", "", &FieldError{Field: "body",
			Message: "update body is required: write the update text after the frontmatter"}
	}
	return body, health, nil
}
