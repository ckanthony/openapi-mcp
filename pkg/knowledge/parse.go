package knowledge

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontMatterSep is the document separator used to delimit YAML front-matter.
const frontMatterSep = "---"

// ErrNoFrontMatter indicates a document without YAML front-matter. Such
// documents are kept as human-only content: they cannot hold machine metadata
// (intents, steps, relations) but are still indexed and searchable.
var ErrNoFrontMatter = fmt.Errorf("no front-matter")

// ParseDoc parses a Markdown knowledge document with optional YAML front-matter
// delimited by "---" lines. It returns the parsed Doc (Body and Title filled).
func ParseDoc(data []byte) (*Doc, error) {
	text := string(data)
	lines := strings.Split(text, "\n")

	d := &Doc{}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontMatterSep {
		d.Body = text
		d.Title = ExtractTitle(text)
		return d, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontMatterSep {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("unterminated front-matter: missing closing %q", frontMatterSep)
	}

	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), d); err != nil {
		return nil, fmt.Errorf("invalid front-matter: %w", err)
	}
	d.Body = strings.Join(lines[end+1:], "\n")
	d.Body = strings.TrimLeft(d.Body, "\n\r")
	if d.ID == "" && d.Body != "" {
		// keep ID empty; the loader assigns the file stem.
	}
	d.Title = ExtractTitle(d.Body)
	return d, nil
}
