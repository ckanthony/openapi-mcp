package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTengoDoc(t *testing.T) {
	src := `// ---
// id: free_disk_space
// kind: script
// summary: Free disk space
// permissions: {exec: true, fs: false}
// params:
//   - {name: mount, required: false}
// ---
func := import("os/exec")
return func("df", mount)
`
	doc, err := ParseScriptDoc("scripts/free-disk-space.tengo", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.ID != "free_disk_space" || doc.Kind != KindScript {
		t.Fatalf("unexpected id/kind: %q %q", doc.ID, doc.Kind)
	}
	if !doc.Permissions.Has("exec") {
		t.Fatalf("expected exec permission, got %v", doc.Permissions)
	}
	if doc.Permissions.Has("fs") {
		t.Fatalf("fs should be false: %v", doc.Permissions)
	}
	if len(doc.Params) != 1 || doc.Params[0].Name != "mount" {
		t.Fatalf("unexpected params: %+v", doc.Params)
	}
	if doc.Source == "" || doc.Source != "func := import(\"os/exec\")\nreturn func(\"df\", mount)\n" {
		t.Fatalf("tengo source should be the code after front-matter, got %q", doc.Source)
	}
	if doc.Body != string(src) {
		t.Fatalf("tengo Body should mirror the whole file")
	}
}

func TestParseTengoDocPermissionsList(t *testing.T) {
	src := `// ---
// permissions: [mcp, exec]
// ---
return "x"
`
	doc, err := ParseScriptDoc("a.tengo", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !doc.Permissions.Has("mcp") || !doc.Permissions.Has("exec") {
		t.Fatalf("unexpected permissions: %v", doc.Permissions)
	}
}

func TestParseMarkdownScriptDoc(t *testing.T) {
	src := `---
id: ping
kind: script
summary: Ping
permissions: [mcp]
---
# Ping

Some prose.

` + "```tengo\nreturn \"pong\"\n```\n"
	doc, err := ParseScriptDoc("scripts/ping.md", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Source != `return "pong"` {
		t.Fatalf("unexpected source: %q", doc.Source)
	}
	if doc.Body == doc.Source {
		t.Fatalf("markdown script should keep prose in Body")
	}
}

func TestMarkdownScriptMissingFence(t *testing.T) {
	src := "---\nkind: script\n---\n# no code\n"
	if _, err := ParseScriptDoc("s.md", []byte(src)); err == nil {
		t.Fatal("expected error for missing fence")
	}
}

func TestLoadLocalIndexesScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "ping.tengo"), []byte("// ---\n// kind: script\n// ---\nreturn \"pong\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := LoadLocal(dir, LoadOptions{API: "acme"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	scripts := lib.Scripts()
	if len(scripts) != 1 {
		t.Fatalf("expected 1 script, got %d (warnings: %v)", len(scripts), lib.Warnings)
	}
	if scripts[0].ID != "ping" || scripts[0].API != "acme" {
		t.Fatalf("unexpected script doc: %+v", scripts[0])
	}
}
