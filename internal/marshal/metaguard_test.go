package marshal

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryEditableRenderHasMetaTwin is the completeness guard for the
// editable-only split (CONTEXT.md "Entity render" / "Collection meta split"):
// every editable entity's XToMarkdown must ship with an XMetaToMarkdown, so an
// eighth editable entity cannot land without its .meta sidecar render. The
// scan reads this package's source (the fixture-parity precedent of scanning
// the applied schema), so a new render is caught the moment it is written —
// not when someone remembers to extend a hand-kept list.
func TestEveryEditableRenderHasMetaTwin(t *testing.T) {
	t.Parallel()

	// Read-only generated renders with no editable file — no .meta twin exists
	// or should. Extending this list is a deliberate act with a reason.
	readOnly := map[string]string{
		"History": "history.md is a read-only generated file (renderFile), not an editable entity",
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?m)^func (\w+?)(Meta)?ToMarkdown\(`)

	renders := map[string]bool{} // base name -> has plain render
	metas := map[string]bool{}   // base name -> has meta render
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range fn.FindAllStringSubmatch(string(src), -1) {
			if m[2] == "Meta" {
				metas[m[1]] = true
			} else {
				renders[m[1]] = true
			}
		}
	}

	if len(renders) == 0 {
		t.Fatal("scan found no XToMarkdown functions — the regex or the working directory is wrong")
	}

	for base := range renders {
		if readOnly[base] != "" {
			if metas[base] {
				t.Errorf("%sToMarkdown is on the read-only exclusion list (%s) but has a Meta twin — remove it from the list", base, readOnly[base])
			}
			continue
		}
		if !metas[base] {
			t.Errorf("%sToMarkdown has no %sMetaToMarkdown twin: every editable entity render needs its .meta sidecar render (the editable-only split). If %s is genuinely read-only, add it to this test's exclusion list with a reason.", base, base, base)
		}
	}
	for base := range metas {
		if !renders[base] {
			t.Errorf("%sMetaToMarkdown has no %sToMarkdown counterpart — a meta sidecar with no editable file makes no sense", base, base)
		}
	}
}
