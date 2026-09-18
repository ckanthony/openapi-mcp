package knowledge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// seedRemote initializes a bare repository at remotePath and pushes a first
// commit (the given files) onto refs/heads/main.
func seedRemote(t *testing.T, remotePath string, withTime time.Time, files map[string]string) {
	t.Helper()
	_, err := git.PlainInit(remotePath, true)
	require.NoError(t, err)

	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sig := &object.Signature{Name: "seed", Email: "seed@example.com", When: withTime}
	for _, name := range names {
		writeFileIn(t, work, name, files[name])
		_, err := wt.Add(name)
		require.NoError(t, err)
	}
	if _, err := wt.Commit("seed knowledge base", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}})
	if err != nil && err != git.ErrRemoteExists {
		t.Fatalf("seed remote: %v", err)
	}
	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/master:refs/heads/main"},
	}); err != nil {
		t.Fatalf("seed push: %v", err)
	}
}

func writeFileIn(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// remoteFiles returns the file contents of the HEAD tree of the branch on the
// bare remote.
func remoteFiles(t *testing.T, remotePath, branch string) map[string]string {
	t.Helper()
	if branch == "" {
		branch = "main"
	}
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:           remotePath,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
	})
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	out := map[string]string{}
	iter := tree.Files()
	defer iter.Close()
	for {
		f, err := iter.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		c, err := f.Contents()
		require.NoError(t, err)
		out[f.Name] = c
	}
	return out
}

func newGitTestBackend(t *testing.T, remote, conflict string) *GitBackend {
	t.Helper()
	root := filepath.Join(t.TempDir(), "kb")
	return NewGitBackend(root, GitConfig{
		Repository: remote,
		Branch:     "main",
		Sync:       "auto",
		Conflict:   conflict,
		AuthorName: "test", AuthorEmail: "test@example.com",
	})
}

func TestGitBackendLocalOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	b := NewGitBackend(root, GitConfig{Sync: "auto", Conflict: "rebase", AuthorName: "t", AuthorEmail: "t@e"})

	require.NoError(t, b.WriteFile("_index.md", []byte("# Manual\n")))
	require.NoError(t, b.WriteFile("glossary/incidencia.md", []byte("---\nid: incidencia\nkind: glossary\n---\n# Incidencia\n")))
	// No push yet: the staged writes are uncommitted, so the tree is dirty.
	st, err := b.Status()
	require.NoError(t, err)
	require.True(t, st.Dirty, "files are staged, not yet committed")

	require.NoError(t, b.Push())
	st, err = b.Status()
	require.NoError(t, err)
	require.Empty(t, st.Repo)
	require.NotEmpty(t, st.Commit)
	require.False(t, st.Dirty, "push committed everything")
	require.False(t, st.PendingPush)

	files, err := b.ListFiles()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"_index.md", "glossary/incidencia.md"}, files)

	data, err := b.ReadFile("glossary/incidencia.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "# Incidencia")

	// A second write without push leaves a dirty tree.
	require.NoError(t, b.WriteFile("capabilities/nueva.md", []byte("# Nueva\n")))
	st, err = b.Status()
	require.NoError(t, err)
	require.True(t, st.Dirty)
}

func TestGitBackendClonePullPush(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	seedRemote(t, remote, time.Now().Add(-10*time.Minute), map[string]string{
		"_index.md": "# Manual\n",
	})

	b := newGitTestBackend(t, remote, "rebase")
	files, err := b.ListFiles()
	require.NoError(t, err)
	require.Contains(t, files, "_index.md")

	require.NoError(t, b.WriteFile("capabilities/alta.md", []byte("---\nid: alta\nkind: capability\nintents: [crear empleado]\n---\n# Alta\n")))
	require.NoError(t, b.Push())

	rf := remoteFiles(t, remote, "main")
	require.Contains(t, rf, "capabilities/alta.md")
	require.Contains(t, rf["capabilities/alta.md"], "crear empleado")

	// A second backend re-clones and sees the pushed change.
	b2 := newGitTestBackend(t, remote, "rebase")
	data, err := b2.ReadFile("capabilities/alta.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "# Alta")
}

func TestGitBackendRebaseOnDivergence(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	base := time.Now().Add(-30 * time.Minute)
	seedRemote(t, remote, base, map[string]string{"_index.md": "# Manual\n"})

	a := newGitTestBackend(t, remote, "rebase")
	b := newGitTestBackend(t, remote, "rebase")

	// Both clones are in sync with the seed.
	filesA, err := a.ListFiles()
	require.NoError(t, err)
	require.Contains(t, filesA, "_index.md")
	filesB, err := b.ListFiles()
	require.NoError(t, err)
	require.Contains(t, filesB, "_index.md")

	// A pushes a change.
	require.NoError(t, a.WriteFile("glossary/a.md", []byte("---\nid: a\nkind: glossary\n---\n# A\n")))
	require.NoError(t, a.Push())

	// B has NOT pulled A's change; pushing B's own change diverges and must
	// succeed via a pull-rebase retry.
	require.NoError(t, b.WriteFile("glossary/b.md", []byte("---\nid: b\nkind: glossary\n---\n# B\n")))
	require.NoError(t, b.Push())

	rf := remoteFiles(t, remote, "main")
	require.Contains(t, rf, "glossary/a.md")
	require.Contains(t, rf, "glossary/b.md")

	// B's checkout now contains both changed files.
	for _, f := range []string{"glossary/a.md", "glossary/b.md"} {
		data, err := b.ReadFile(f)
		require.NoError(t, err, "file %s must exist in B's checkout", f)
		require.NotEmpty(t, data)
	}
	st, err := b.Status()
	require.NoError(t, err)
	require.False(t, st.Conflict)
	require.False(t, st.PendingPush)
}

func TestGitBackendFFOnlyConflict(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	seedRemote(t, remote, time.Now().Add(-10*time.Minute), map[string]string{"_index.md": "# Manual\n"})

	a := newGitTestBackend(t, remote, "rebase")
	b := newGitTestBackend(t, remote, "ff_only")

	// Force both clones to initialise from the seed, before A pushes.
	_, err := a.ListFiles()
	require.NoError(t, err)
	_, err = b.ListFiles()
	require.NoError(t, err)

	require.NoError(t, a.WriteFile("glossary/a.md", []byte("---\nid: a\nkind: glossary\n---\n# A\n")))
	require.NoError(t, a.Push())

	// B diverges and its ff_only policy must report a conflict, keeping the
	// local change intact.
	require.NoError(t, b.WriteFile("glossary/b.md", []byte("---\nid: b\nkind: glossary\n---\n# B\n")))
	err = b.Push()
	require.Error(t, err)
	require.True(t, IsConflict(err), "expected conflict, got: %v", err)

	st, err := b.Status()
	require.NoError(t, err)
	require.True(t, st.Conflict, "status should report the conflict")

	// The remote got A's change but not B's.
	rf := remoteFiles(t, remote, "main")
	require.Contains(t, rf, "glossary/a.md")
	_, hasB := rf["glossary/b.md"]
	require.False(t, hasB, "B's local change must not be on the remote")

	// B's local worktree still has its file.
	data, err := b.ReadFile("glossary/b.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "# B")
}

func TestGitBackendPendingPushOnFailedPush(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	seedRemote(t, remote, time.Now().Add(-10*time.Minute), map[string]string{"_index.md": "# Manual\n"})

	b := NewGitBackend(filepath.Join(t.TempDir(), "kb"), GitConfig{
		Repository: remote,
		Branch:     "main", Sync: "auto", Conflict: "rebase",
		AuthorName: "t", AuthorEmail: "t@e",
	})
	// Sync once through the file remote to establish the repo and upstream.
	require.NoError(t, b.WriteFile("glossary/uno.md", []byte("---\nid: uno\nkind: glossary\n---\n# Uno\n")))
	require.NoError(t, b.Push())

	// Swap the origin remote to a dead HTTP server: the push cannot reach any
	// git service, so the local commit must survive as a pending push.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		// Minimal pkt-line v2 capabilities advertisement with no refs → the
		// client sees an empty ref list and the push succeeds locally but the
		// protocol-level error from a 500 on the receive-pack POST surfaces
		// as a non-exit/non-path error that becomes a pending-push.
		http.Error(w, "no git service", http.StatusInternalServerError)
	}))
	defer dead.Close()
	updateOriginURL(t, b, dead.URL)

	require.NoError(t, b.WriteFile("glossary/dos.md", []byte("---\nid: dos\nkind: glossary\n---\n# Dos\n")))
	err := b.Push()
	require.Error(t, err)
	if !IsPendingPush(err) {
		t.Fatalf("expected pending-push error, got: %v", err)
	}
	st, err := b.Status()
	require.NoError(t, err)
	require.NotEmpty(t, st.Commit, "local commit must exist")
	require.False(t, st.Dirty, "worktree committed locally")
	require.True(t, st.PendingPush, "local commit is ahead of the last known remote state")

	// The local file must be present in the checkout.
	data, err := b.ReadFile("glossary/dos.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "# Dos")
}

// updateOriginURL rewrites the origin remote URL inside a backend's git repo
// so that subsequent push/fetch calls target the new URL.
func updateOriginURL(t *testing.T, b *GitBackend, newURL string) {
	t.Helper()
	cfg, err := b.repo.Config()
	require.NoError(t, err)
	cfg.Remotes["origin"].URLs = []string{newURL}
	require.NoError(t, b.repo.Storer.SetConfig(cfg))
}
