package knowledge

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Backend abstracts where the knowledge library lives and how it is kept in
// sync. A local backend reads/writes the manual directly; a git backend
// (Phase 2) mirrors a remote repository into a local checkout.
type Backend interface {
	// Type is "local" or "git".
	Type() string
	// Root is the local directory the library is read from / written to.
	Root() string
	// ListFiles returns library-relative paths of all Markdown documents.
	ListFiles() ([]string, error)
	// ReadFile returns the content of a library-relative path.
	ReadFile(rel string) ([]byte, error)
	// WriteFile writes a library-relative path (creating parent dirs).
	WriteFile(rel string, data []byte) error
	// DeleteFile removes a library-relative path.
	DeleteFile(rel string) error
	// Pull synchronizes the working copy with the remote (no-op on local).
	Pull() error
	// Push commits and uploads local changes (no-op on local).
	Push() error
}

// LocalBackend is a plain directory with Markdown files.
type LocalBackend struct {
	root string
}

// NewLocalBackend returns a backend rooted at root.
func NewLocalBackend(root string) *LocalBackend {
	return &LocalBackend{root: root}
}

func (l *LocalBackend) Type() string { return "local" }
func (l *LocalBackend) Root() string { return l.root }

// ListFiles walks the root for Markdown files, returning slash-relative paths.
func (l *LocalBackend) ListFiles() ([]string, error) {
	var files []string
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if !strings.HasSuffix(lower, ".md") && !strings.HasSuffix(lower, ".tengo") {
			return nil
		}
		rel, err := filepath.Rel(l.root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: listing library %q: %w", l.root, err)
	}
	return files, nil
}

func (l *LocalBackend) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.root, filepath.FromSlash(rel)))
}

func (l *LocalBackend) WriteFile(rel string, data []byte) error {
	full := filepath.Join(l.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (l *LocalBackend) DeleteFile(rel string) error {
	full := filepath.Join(l.root, filepath.FromSlash(rel))
	return os.Remove(full)
}

func (l *LocalBackend) Pull() error { return nil }
func (l *LocalBackend) Push() error { return nil }

// Serialize flattens a Doc back into a Markdown document with its front-matter
// and body, ready for Store/WriteFile.
func Serialize(doc *Doc) ([]byte, error) {
	// The Doc struct carries yaml tags; a dedicated snippet keeps body separate.
	type fm struct {
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
		Permissions Permissions `yaml:"permissions,omitempty"`
		View        *View       `yaml:"view,omitempty"`
		Draft       bool        `yaml:"draft,omitempty"`
		TimeoutS    int         `yaml:"timeout,omitempty"`
	}
	f := fm{doc.ID, doc.Kind, doc.API, doc.Language, doc.Summary, doc.Anchor, doc.Tags, doc.Intents, doc.Params, doc.Steps, doc.Related, doc.Permissions, doc.View, doc.Draft, doc.TimeoutS}
	out, err := yaml.Marshal(f)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(doc.Body)
	if body == "" {
		body = doc.Title
	}
	if !strings.HasPrefix(strings.TrimSpace(doc.Body), "#") && doc.Title != "" {
		body = "# " + doc.Title + "\n\n" + body
	}
	res := append([]byte("---\n"), out...)
	res = append(res, []byte("---\n")...)
	res = append(res, []byte(body)...)
	res = append(res, '\n')
	return res, nil
}
