package knowledge

import (
	"sort"
	"strings"
	"unicode"
)

var accentReplacer = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
	"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N",
)

func normalize(s string) string {
	return accentReplacer.Replace(strings.ToLower(s))
}

// tokenize returns a set of normalized (lowercased, accent-stripped) tokens.
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(normalize(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(f) > 1 {
			out[f] = true
		}
	}
	return out
}

// Hit is one search result.
type Hit struct {
	Doc   *Doc
	Score int
}

// Search ranks the library documents by term overlap with query. It is a light,
// deliberate retrieval: matching is done over title, id, intents, tags, summary
// and body (in decreasing weight), so natural-language phrases resolve to the
// documents that reference the same concepts.
func (lib *Library) Search(query string, limit int) []Hit {
	if lib == nil {
		return nil
	}
	q := tokenize(query)
	if len(q) == 0 {
		return nil
	}

	type scored struct {
		doc   *Doc
		score int
	}
	out := make([]scored, 0, len(lib.Docs))
	for _, d := range lib.Docs {
		s := 0
		for tok := range q {
			if tokenize(d.Title)[tok] || tokenize(d.ID)[tok] {
				s += 4
			}
			if tokenize(strings.Join(d.Intents, " "))[tok] {
				s += 4
			}
			if tokenize(strings.Join(d.Tags, " "))[tok] {
				s += 3
			}
			if tokenize(d.Summary)[tok] {
				s += 2
			}
			if tokenize(d.Body)[tok] {
				s++
			}
		}
		if s > 0 {
			out = append(out, scored{doc: d, score: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score == out[j].score {
			return out[i].doc.Path < out[j].doc.Path
		}
		return out[i].score > out[j].score
	})
	if limit <= 0 || limit > len(out) {
		limit = len(out)
	}
	hits := make([]Hit, 0, limit)
	for _, s := range out[:limit] {
		hits = append(hits, Hit{Doc: s.doc, Score: s.score})
	}
	return hits
}
