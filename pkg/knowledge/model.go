// Package knowledge implements the semantic knowledge layer: a library of
// Markdown documents (glossary, endpoint/schema/field annotations and
// high-level "capability" tasks) that is indexed by the server so knowledge can
// be extended during a session and used to discover/execute high-level tasks.
//
// Each document combines YAML front-matter (the machine-readable part: kind,
// intents, tasks steps, relations) with a Markdown body (the human-readable part
// that uses relative Markdown links to relate the concepts).
package knowledge

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind identifies what a document describes.
type Kind string

const (
	KindIndex      Kind = "index"
	KindGlossary   Kind = "glossary"
	KindEndpoint   Kind = "endpoint"
	KindSchema     Kind = "schema"
	KindField      Kind = "field"
	KindCapability Kind = "capability"
	KindPattern    Kind = "pattern"   // reusable, API-agnostic procedure
	KindTool       Kind = "tool"      // documents a tool / integration / command
	KindIdea       Kind = "idea"      // free-form note; never executed
	KindScript     Kind = "script"    // executable tengo: surfaced as an MCP tool
	KindView       Kind = "view"      // what to render and how (web UI)
	KindDashboard  Kind = "dashboard" // a saved view or group of views + inputs
)

// Permissions declares which host modules a kind: script document may import.
// It accepts either a YAML list (["exec", "fs"]) or a YAML map
// ({exec: true, fs: false}); only truthy entries are kept. Module names are
// lower-cased, de-duplicated and sorted so the parsed doc is stable.
type Permissions []string

// UnmarshalYAML accepts both the list and map encodings.
func (p *Permissions) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*p = normalizePermissions(list)
	case yaml.MappingNode:
		var m map[string]bool
		if err := value.Decode(&m); err != nil {
			return err
		}
		list := make([]string, 0, len(m))
		for k, v := range m {
			if v {
				list = append(list, k)
			}
		}
		*p = normalizePermissions(list)
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*p = normalizePermissions(strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }))
	default:
		return fmt.Errorf("permissions: expected a list or map")
	}
	return nil
}

// Has reports whether the named module was granted.
func (p Permissions) Has(module string) bool {
	module = strings.ToLower(strings.TrimSpace(module))
	for _, m := range p {
		if m == module {
			return true
		}
	}
	return false
}

func normalizePermissions(in []string) Permissions {
	seen := map[string]bool{}
	out := make(Permissions, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Doc is a single knowledge document. The YAML front-matter holds the machine
// part; Body holds the human prose with relative Markdown links.
type Doc struct {
	ID          string      `yaml:"id,omitempty"`
	Kind        Kind        `yaml:"kind,omitempty"`
	API         string      `yaml:"api,omitempty"`
	Language    string      `yaml:"language,omitempty"`
	Summary     string      `yaml:"summary,omitempty"`
	Anchor      string      `yaml:"anchor,omitempty"`
	Tags        []string    `yaml:"tags,omitempty"`
	Intents     []string    `yaml:"intents,omitempty"`
	Params      []Param     `yaml:"params,omitempty"`
	Steps       []Step      `yaml:"steps,omitempty"`
	Related     []Rel       `yaml:"related,omitempty"`
	Permissions Permissions `yaml:"permissions,omitempty"` // host modules a kind: script may import
	View        *View       `yaml:"view,omitempty"`        // rendering spec for kind: view/dashboard
	Draft       bool        `yaml:"draft,omitempty"`

	// TimeoutS overrides the scripting default run budget, in seconds, for a
	// kind: script document (front-matter "timeout").
	TimeoutS int `yaml:"timeout,omitempty"`

	Body string `yaml:"-"`

	// Source is the executable tengo source of a kind: script document. For a
	// .tengo file it mirrors Body (the whole file, comment front-matter and
	// all); for a .md document it is the content of the first tengo fenced code
	// block, while Body keeps the human prose.
	Source string `yaml:"-"`

	// Path is the library-relative path of the document (e.g.
	// "endpoints/create-user.md"). Links use this as the stable target base.
	Path string `yaml:"-"`
	// Title is the first "# Heading" of the body, or the ID when absent.
	Title string `yaml:"-"`
}

// Param is a free-form parameter a capability task or script accepts. For
// scripts the optional Type/Description/Default refine the generated tool
// schema; capabilities typically only use Name/Required.
type Param struct {
	Name        string      `yaml:"name"`
	Required    bool        `yaml:"required,omitempty"`
	Type        string      `yaml:"type,omitempty"`        // string|integer|number|boolean|array|object
	Description string      `yaml:"description,omitempty"` // shown in the tool schema
	Default     interface{} `yaml:"default,omitempty"`     // default value when omitted
}

// Step is one executable step of a capability task. Inputs bind task parameters
// or earlier outputs into the step's tool arguments; Outputs record where a
// value produced by the response is to be captured for later steps.
type Step struct {
	Tool    string                  `yaml:"tool"`
	Inputs  map[string]InputBinding `yaml:"inputs"`
	Outputs map[string]string       `yaml:"outputs"`
}

// InputBinding describes where the value for a tool argument comes from:
// "param.<name>" (a task parameter) or "step.<N>.<jsonpath>" (output of a
// previous step). A plain value is used verbatim as a literal.
type InputBinding struct {
	From string `yaml:"from"`
}

// UnmarshalYAML accepts either a mapping {"from": "..."} or a bare scalar. A
// bare scalar (or a non-string "from" value) is materialized as a typed
// literal: strings pass through verbatim, numbers/booleans keep their JSON
// encoding so the resolver can recover the original type.
func (b *InputBinding) UnmarshalYAML(value *yaml.Node) error {
	var raw interface{}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	switch t := raw.(type) {
	case map[string]interface{}:
		v, ok := t["from"]
		if !ok {
			return fmt.Errorf("input binding must provide a string \"from\" field, got %v", raw)
		}
		b.From = bindingValueText(v)
	case nil:
		return nil
	default:
		b.From = bindingValueText(raw)
	}
	return nil
}

// bindingValueText renders a bound/literal value as the "from" text: strings
// pass through verbatim; other scalars use their JSON encoding.
func bindingValueText(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(buf)
}

// Rel is a typed relationship from this document to another one.
type Rel struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"` // library-relative path of the target doc
}

// View describes how a result is rendered in the web UI (kind: view /
// kind: dashboard). Source is the tool/capability/script feeding the view;
// Inputs declares the interactive controls (search, pagination, sort,
// filters) bound to the backing call's parameters; Layout picks the rendering;
// AutoShow makes the UI display the view automatically when Source completes.
type View struct {
	Source   string      `yaml:"source,omitempty"`    // tool/capability/script feeding the view
	Inputs   []ViewInput `yaml:"inputs,omitempty"`    // interactive controls (search, page, sort, filter)
	Layout   string      `yaml:"layout,omitempty"`    // table | list | cards | chart
	AutoShow bool        `yaml:"auto_show,omitempty"` // show when Source completes (run_task memo)
}

// ViewInput declares one interactive control of a view/dashboard and how it
// maps onto the backing call (see InputBinding for the binding grammar).
type ViewInput struct {
	Name     string   `yaml:"name"`
	Label    string   `yaml:"label,omitempty"`
	Type     string   `yaml:"type,omitempty"`    // text | number | select | ...
	Options  []string `yaml:"options,omitempty"` // for select
	Required bool     `yaml:"required,omitempty"`
	Default  string   `yaml:"default,omitempty"`
	Binding  string   `yaml:"binding,omitempty"` // param.<name> | step.<n>.<jsonpath> on Source
}

// headingRe matches the first Markdown heading.
var headingRe = regexp.MustCompile(`(?m)^\s*#\s+(.+)$`)

// ExtractTitle returns the first "# heading" of the body, trimmed.
func ExtractTitle(body string) string {
	if m := headingRe.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// Fields returns the ordered list of the document's parameter names.
func (d *Doc) ParamNames() []string {
	out := make([]string, 0, len(d.Params))
	for _, p := range d.Params {
		out = append(out, p.Name)
	}
	return out
}

// NormalizedPath joins a base library path with a (possibly "../"-relative)
// link target and cleans it, so links can be resolved to canonical paths.
func NormalizedPath(base, target string) string {
	if target == "" {
		return ""
	}
	// Strip an optional #anchor suffix; heading anchors live within the target
	// file and are not part of the file path.
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target = target[:i]
	}
	dir := path.Dir(base)
	if dir == "." {
		dir = ""
	}
	return path.Clean(path.Join(dir, target))
}
