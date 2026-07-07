package marshal

// The placeholder policy: an entity with an empty body (an issue's
// description, a document's content) renders as a `# <Title>` heading so the
// file isn't blank — and the parse side must recognize that same placeholder
// so a read-then-save of such an entity stays a byte-stable no-op instead of
// pushing the fabricated heading back as real content. Render and guard are
// defined together here so an entity cannot adopt one half without the other
// (documents shipped with the render but not the guard, silently mutating
// every empty document a no-op rewrite touched).

// placeholderBody renders the stand-in body for an entity with empty content.
func placeholderBody(title string) string {
	return "# " + title + "\n"
}

// isPlaceholderNoop reports whether a parsed body is just the placeholder a
// render fabricated for an entity whose real content is empty — i.e. the body
// differs from the original only because the render invented it.
func isPlaceholderNoop(body, originalContent, title string) bool {
	return originalContent == "" && body == placeholderBody(title)
}
