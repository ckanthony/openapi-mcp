package knowledge

import (
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseScriptDoc parses a kind: script document. rel (the library-relative
// path) selects the encoding:
//
//   - *.tengo: the tengo source is the whole file; the leading "// ---" comment
//     block, when present, is YAML front-matter (id/kind/summary/permissions/
//     params).
//   - *.md (or anything else): standard "---" front-matter plus a fenced
//     ```tengo (a.k.a. ```js) code block holding the executable source; Body
//     keeps the surrounding prose.
func ParseScriptDoc(rel string, data []byte) (*Doc, error) {
	if strings.EqualFold(path.Ext(rel), ".tengo") {
		return parseTengoDoc(data)
	}
	doc, err := ParseDoc(data)
	if err != nil {
		return nil, err
	}
	doc.Kind = KindScript
	src := extractTengoFence(doc.Body)
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("script %s: no fenced tengo code block found", rel)
	}
	doc.Source = src
	return doc, nil
}

// parseTengoDoc parses a raw .tengo file with an optional comment front-matter.
func parseTengoDoc(data []byte) (*Doc, error) {
	text := string(data)
	doc := &Doc{Kind: KindScript, Body: text, Source: text}
	lines := strings.Split(text, "\n")

	start := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if isFMMarker(t) {
			start = i
		}
		break // first non-empty line decides
	}
	if start < 0 {
		return doc, nil
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if isFMMarker(strings.TrimSpace(lines[i])) {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("script: unterminated comment front-matter (missing closing // ---)")
	}

	fm := make([]string, 0, end-start-1)
	for _, l := range lines[start+1 : end] {
		fm = append(fm, uncomment(l))
	}
	if err := yaml.Unmarshal([]byte(strings.Join(fm, "\n")), doc); err != nil {
		return nil, fmt.Errorf("script: invalid comment front-matter: %w", err)
	}
	if doc.Kind == "" {
		doc.Kind = KindScript
	}
	// Strip front-matter from Source: keep only lines after closing // ---
	if end+1 < len(lines) {
		doc.Source = strings.Join(lines[end+1:], "\n")
	} else {
		doc.Source = ""
	}
	return doc, nil
}

// isFMMarker reports whether a trimmed line is a comment front-matter fence.
func isFMMarker(t string) bool {
	t = strings.ReplaceAll(t, " ", "")
	t = strings.ReplaceAll(t, "\t", "")
	return t == "//---"
}

// uncomment strips a leading "//" (and one space) from a comment line.
func uncomment(l string) string {
	l = strings.TrimLeft(l, " \t")
	l = strings.TrimPrefix(l, "//")
	return strings.TrimPrefix(l, " ")
}

// knownScriptModule reports whether module is a recognized host module a
// kind: script may declare. Kept in sync with pkg/script's Module* constants.
func knownScriptModule(module string) bool {
	switch strings.ToLower(strings.TrimSpace(module)) {
	case "mcp", "os", "exec", "fs", "http":
		return true
	}
	return false
}

// extractTengoFence returns the body of the first fenced code block whose info
// string is empty or mentions tengo/js/javascript.
func extractTengoFence(body string) string {
	lines := strings.Split(body, "\n")
	inFence := false
	lang := ""
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !inFence {
			if strings.HasPrefix(t, "```") {
				info := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "```")))
				if info == "" || strings.Contains(info, "tengo") || strings.Contains(info, "javascript") || info == "js" {
					inFence = true
					lang = info
					continue
				}
			}
			continue
		}
		if strings.HasPrefix(t, "```") {
			return strings.Join(out, "\n")
		}
		out = append(out, l)
	}
	_ = lang
	return ""
}
