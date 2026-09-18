package knowledge

import (
	"regexp"
	"strings"
)

// linkRe matches inline Markdown links: [text](target).
var linkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)

// KBTargetPrefix introduces a cross-library link target: a link that points
// into the knowledge base of a *different* API (or the "_meta" global base).
// The form is kb:<api>:<rel>, where rel is the library-relative path of the
// target document. "kb:<api>" alone refers to the target library's index.
const KBTargetPrefix = "kb:"

// LinkRef is one Markdown link found in a document body.
type LinkRef struct {
	Text   string
	Target string
}

// ExtractLinks returns every inline markdown link in body.
func ExtractLinks(body string) []LinkRef {
	matches := linkRe.FindAllStringSubmatch(body, -1)
	out := make([]LinkRef, 0, len(matches))
	for _, m := range matches {
		out = append(out, LinkRef{Text: m[1], Target: m[2]})
	}
	return out
}

// ParseKBTarget splits a "kb:api:rel" link target into its API name and
// library-relative path. ok is false when target is not a kb: link. When rel is
// omitted, the target document is the library index ("_index.md").
func ParseKBTarget(target string) (api, rel string, ok bool) {
	if len(target) < len(KBTargetPrefix) || target[:len(KBTargetPrefix)] != KBTargetPrefix {
		return "", "", false
	}
	rest := target[len(KBTargetPrefix):]
	if rest == "" {
		return "", "", true
	}
	api, rel, _ = strings.Cut(rest, ":")
	if rel == "" {
		rel = "_index.md"
	}
	return api, rel, true
}
