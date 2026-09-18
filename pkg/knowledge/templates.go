package knowledge

import (
	"fmt"
	"sort"
	"strings"
)

// sectionTitles holds the human section headings used by knowledge_init
// skeletons, per language. Unknown languages fall back to English.
var sectionTitles = map[string]map[string]string{
	"es": {
		"description": "Descripcion",
		"when":        "Cuando usarlo",
		"params":      "Parametros",
		"examples":    "Ejemplos",
		"rules":       "Reglas y notas",
		"related":     "Relacionado",
	},
	"en": {
		"description": "Description",
		"when":        "When to use",
		"params":      "Parameters",
		"examples":    "Examples",
		"rules":       "Rules and notes",
		"related":     "Related",
	},
}

func title(lang, key string) string {
	if m := sectionTitles[strings.ToLower(lang)]; m != nil {
		if s := m[key]; s != "" {
			return s
		}
	}
	return sectionTitles["en"][key]
}

// EndpointSkeleton returns a skeleton Markdown doc (front-matter + body) for an
// endpoint, with the parameters and the language-appropriate section headings.
func EndpointSkeleton(apiName, operationID, method, path, lang string, params []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "kind: endpoint\n")
	fmt.Fprintf(&b, "api: %s\n", apiName)
	fmt.Fprintf(&b, "language: %s\n", lang)
	fmt.Fprintf(&b, "anchor: %s\n", operationID)
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "# %s %s\n\n", strings.ToUpper(method), path)
	fmt.Fprintf(&b, "## %s\n\n<!-- Que hace este endpoint y para que sirve -->\n\n", title(lang, "description"))
	if len(params) > 0 {
		fmt.Fprintf(&b, "## %s\n\n", title(lang, "params"))
		fmt.Fprintf(&b, "| Parametro | Significado |\n|---|---|\n")
		for _, p := range params {
			fmt.Fprintf(&b, "| `%s` |  |\n", p)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## %s\n\n<!-- Cuando conviene llamarlo, prerequisitos, resultados -->\n", title(lang, "when"))
	return b.String()
}

// SchemaSkeleton returns a skeleton doc for a schema/component.
func SchemaSkeleton(apiName, name, lang string, props []string) string {
	sort.Strings(props)
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "kind: schema\n")
	fmt.Fprintf(&b, "api: %s\n", apiName)
	fmt.Fprintf(&b, "language: %s\n", lang)
	fmt.Fprintf(&b, "anchor: %s\n", name)
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "# %s\n\n", name)
	fmt.Fprintf(&b, "## %s\n\n<!-- Que representa este DTO, donde aparece -->\n", title(lang, "description"))
	if len(props) > 0 {
		fmt.Fprintf(&b, "\nCampos: %s\n", strings.Join(props, ", "))
	}
	return b.String()
}

// GlossarySkeleton returns a skeleton doc for a domain term.
func GlossarySkeleton(apiName, term, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "kind: glossary\n")
	fmt.Fprintf(&b, "api: %s\n", apiName)
	fmt.Fprintf(&b, "language: %s\n", lang)
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "# %s\n\n", term)
	fmt.Fprintf(&b, "## %s\n\n<!-- Definicion del termino en el dominio del producto -->\n", title(lang, "description"))
	return b.String()
}

// CapabilitySkeleton returns a skeleton doc for a high-level task, with an
// empty steps list ready to fill in.
func CapabilitySkeleton(apiName, name, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "kind: capability\n")
	fmt.Fprintf(&b, "api: %s\n", apiName)
	fmt.Fprintf(&b, "language: %s\n", lang)
	fmt.Fprintf(&b, "intents: []\n")
	fmt.Fprintf(&b, "params: []\n")
	fmt.Fprintf(&b, "steps: []\n")
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "# %s\n\n", name)
	fmt.Fprintf(&b, "## %s\n\n<!-- Que tarea de alto nivel resuelve (en lenguaje natural) -->\n", title(lang, "description"))
	return b.String()
}

// IndexSkeleton returns the library index (manual cover) document.
func IndexSkeleton(apiName, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "kind: index\n")
	fmt.Fprintf(&b, "api: %s\n", apiName)
	fmt.Fprintf(&b, "language: %s\n", lang)
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "# Manual de %s\n\n", apiName)
	fmt.Fprintf(&b, "## Glossary\n\n- [incidencias](glossary/incidencias.md)\n")
	fmt.Fprintf(&b, "\n## Capabilities\n\n- [Nueva tarea](capabilities/nueva-tarea.md)\n")
	fmt.Fprintf(&b, "\n## Scripts\n\n")
	fmt.Fprintf(&b, "Solve a recurring procedure once interactively, then promote it to a reusable tool:\n\n")
	fmt.Fprintf(&b, "1. run the steps manually (or with run_task)\n")
	fmt.Fprintf(&b, "2. capture the recipe with knowledge_remember_sequence\n")
	fmt.Fprintf(&b, "3. write it as a kind: script document under `scripts/` (tengo); it becomes an MCP tool `%s__<id>`\n", apiName)
	return b.String()
}

// ScriptSkeleton returns a skeleton kind: script document: a comment
// front-matter (id, summary, params, permissions) plus a tengo body.
func ScriptSkeleton(apiName, id string) string {
	return "// ---\n" +
		"// kind: script\n" +
		"// id: " + id + "\n" +
		"// summary: What this script does\n" +
		"// permissions: []   # mcp, os, exec, fs, http (privileged modules default deny)\n" +
		"// params:\n" +
		"//   - {name: example, required: false, type: string}\n" +
		"// ---\n" +
		"// Tengo source below. This recipe was first solved interactively, then\n" +
		"// promoted into a reusable script tool (" + apiName + "__" + id + ").\n" +
		"return \"hello from " + id + "\"\n"
}

// ViewSkeleton returns a skeleton doc for a web UI view/dashboard: a declared
// rendering of a tool/capability/script result, with optional inputs.
func ViewSkeleton(apiName, id string) string {
	return "---\n" +
		"id: " + id + "\n" +
		"kind: " + string(KindView) + "\n" +
		"api: " + apiName + "\n" +
		"language: en\n" +
		"summary: How a result is rendered in the web UI\n" +
		"view:\n" +
		"  source: <tool name or capability id>\n" +
		"  layout: table | list | cards | chart\n" +
		"  inputs: []\n" +
		"  auto_show: false\n" +
		"---\n" +
		"# " + id + "\n\n" +
		"## Description\n\n<!-- What does this view show, and which source feeds it -->\n"
}
