package knowledge

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	ghttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// ErrConflict reports that a pull could not be completed because the local and
// remote histories diverged under the configured conflict policy (ff_only), or
// the rebase replay could not apply cleanly. The checkout is left untouched and
// Status().Conflict is set until a later sync succeeds.
var ErrConflict = errors.New("knowledge: git sync conflict")

// ErrPendingPush reports that a knowledge write was committed locally but the
// follow-up push to the remote failed. Local changes are preserved and the
// next push (knowledge_sync push / update) retries them.
var ErrPendingPush = errors.New("knowledge: changes committed locally; remote push pending")

// pendingPushError carries the underlying push failure alongside ErrPendingPush.
type pendingPushError struct {
	cause error
}

func (e *pendingPushError) Error() string {
	return fmt.Sprintf("%v: %v", ErrPendingPush, e.cause)
}

func (e *pendingPushError) Unwrap() error { return e.cause }

// IsPendingPush reports whether err is a pending-push condition (local commit
// succeeded, remote push did not).
func IsPendingPush(err error) bool {
	var p *pendingPushError
	return errors.As(err, &p)
}

// IsConflict reports whether err is a git sync conflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

// GitConfig carries the resolved (env-expanded) settings for a git knowledge
// backend. Credentials are only ever supplied from the process environment.
type GitConfig struct {
	Repository  string // URL of the remote; empty builds a local-only repository
	Branch      string // branch checked out (default "main")
	Sync        string // "auto" (pull on load, push after edits) or "manual"
	Conflict    string // divergence policy on pull/push: "rebase" or "ff_only"
	AuthToken   string // https token
	SSHKey      string // path to a private key for ssh remotes
	AuthorName  string // commit author (env-resolved)
	AuthorEmail string
}

// SyncStatus is a client-safe snapshot of the git sync state of a backend.
type SyncStatus struct {
	Repo        string
	Branch      string
	Commit      string // short HEAD hash; empty while unborn
	Upstream    string // short origin/<branch> hash known locally; empty if absent
	Dirty       bool   // uncommitted worktree/index changes
	PendingPush bool   // local branch is ahead of the remote-tracking branch
	Conflict    bool   // last sync hit a conflict
}

// String renders the status as a compact human-readable form.
func (s SyncStatus) String() string {
	var parts []string
	if s.Repo != "" {
		parts = append(parts, "repo="+s.Repo)
	}
	parts = append(parts, "branch="+orDefault(s.Branch, "main"))
	if s.Commit != "" {
		parts = append(parts, "commit="+s.Commit)
	}
	if s.Dirty {
		parts = append(parts, "dirty")
	}
	if s.PendingPush {
		parts = append(parts, "pending_push")
	}
	if s.Conflict {
		parts = append(parts, "conflict")
	}
	if len(parts) == 0 {
		parts = append(parts, "clean")
	}
	return strings.Join(parts, ", ")
}

// GitBackend stores the knowledge library in a git checkout. It wraps go-git
// (pure Go, no git binary required): the first load clones the repository (or
// initializes a local-only repo when no remote is configured), pulls follow the
// configured conflict policy, and writes are committed with a selective add and
// pushed with a pull-rebase retry on divergence.
type GitBackend struct {
	mu       sync.Mutex
	root     string
	cfg      GitConfig
	fs       *LocalBackend
	repo     *git.Repository
	conflict bool
}

// NewGitBackend returns a git-backed knowledge backend rooted at root and
// configured from cfg.
func NewGitBackend(root string, cfg GitConfig) *GitBackend {
	b := &GitBackend{root: root, fs: NewLocalBackend(root)}
	b.cfg = cfg
	if b.cfg.Branch == "" {
		b.cfg.Branch = "main"
	}
	if b.cfg.Sync == "" {
		b.cfg.Sync = "auto"
	}
	if b.cfg.Conflict == "" {
		b.cfg.Conflict = "rebase"
	}
	return b
}

func (b *GitBackend) Type() string { return "git" }

func (b *GitBackend) Root() string { return b.root }

// SyncPolicy exposes the configured sync policy ("auto" | "manual").
func (b *GitBackend) SyncPolicy() string { return b.cfg.Sync }

func (b *GitBackend) ListFiles() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return nil, err
	}
	return b.fs.ListFiles()
}

func (b *GitBackend) ReadFile(rel string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return nil, err
	}
	return b.fs.ReadFile(rel)
}

// WriteFile writes the file to the working tree and stages it (selective add,
// never add -A). Nothing is committed until Push, so a burst of writes (e.g.
// knowledge_init scratchpad) coalesces into a single commit.
func (b *GitBackend) WriteFile(rel string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return err
	}
	if err := b.fs.WriteFile(rel, data); err != nil {
		return err
	}
	wt, err := b.repo.Worktree()
	if err != nil {
		return err
	}
	if _, err := wt.Add(rel); err != nil {
		return fmt.Errorf("knowledge: staging %q: %w", rel, err)
	}
	return nil
}

func (b *GitBackend) DeleteFile(rel string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return err
	}
	if err := b.fs.DeleteFile(rel); err != nil && !os.IsNotExist(err) {
		return err
	}
	wt, err := b.repo.Worktree()
	if err != nil {
		return err
	}
	if _, err := wt.Add(rel); err != nil {
		return fmt.Errorf("knowledge: staging deletion of %q: %w", rel, err)
	}
	return nil
}

// Pull synchronizes the working copy with the remote: it commits any staged
// (uncommitted) local writes, fetches origin and applies the configured
// conflict policy (fast-forward, or rebase of local commits). On a conflict the
// checkout is left untouched and ErrConflict is returned.
func (b *GitBackend) Pull() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return err
	}
	if b.cfg.Repository == "" {
		return nil // local-only repository
	}
	if err := b.commitLocal(); err != nil {
		return err
	}
	auth, err := b.authMethod()
	if err != nil {
		return err
	}
	err = b.repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/*:refs/remotes/origin/*")},
		Auth:       auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("knowledge: fetch %q: %w", b.cfg.Repository, err)
	}

	up, err := b.repo.Reference(plumbing.NewRemoteReferenceName("origin", b.cfg.Branch), false)
	if err != nil {
		return nil // remote has no branch yet; nothing to pull
	}
	upCommit, err := b.repo.CommitObject(up.Hash())
	if err != nil {
		return nil
	}
	head, err := b.repo.Head()
	if err != nil {
		// Local branch is unborn: adopt the remote branch as our own.
		st := b.repo.Storer
		if err := st.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(b.cfg.Branch), upCommit.Hash)); err != nil {
			return err
		}
		wt, err := b.repo.Worktree()
		if err != nil {
			return err
		}
		b.conflict = false
		return wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: upCommit.Hash})
	}
	headCommit, err := b.repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}

	switch {
	case headCommit.Hash == upCommit.Hash:
		b.conflict = false
		return nil
	case isAncestor(upCommit, headCommit):
		return nil // we are ahead of the remote (pending push)
	}

	wt, err := b.repo.Worktree()
	if err != nil {
		return err
	}
	if isAncestor(headCommit, upCommit) {
		// Remote is strictly ahead: fast-forward.
		if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: upCommit.Hash}); err != nil {
			return fmt.Errorf("knowledge: fast-forward: %w", err)
		}
		b.conflict = false
		return nil
	}

	// Diverged.
	if b.cfg.Conflict == "ff_only" {
		b.conflict = true
		return ErrConflict
	}
	if err := b.rebaseOnto(wt, headCommit, upCommit); err != nil {
		b.conflict = true
		return ErrConflict
	}
	b.conflict = false
	return nil
}

// Push commits every staged local write (coalesced into one commit) and uploads
// it. On divergence it retries once with a pull-rebase; if the push still fails
// the local state is kept and ErrPendingPush is returned.
func (b *GitBackend) Push() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return err
	}
	if err := b.commitLocal(); err != nil {
		return err
	}
	if b.cfg.Repository == "" {
		return nil // local-only repository: a commit is the whole story
	}
	auth, err := b.authMethod()
	if err != nil {
		return err
	}
	err = b.push(auth)
	if err == nil {
		b.conflict = false
		return nil
	}
	if !isNonFastForward(err) {
		return &pendingPushError{cause: err}
	}
	// Diverged: pull-rebase once, then push again.
	if pullErr := b.pullRebaseForPush(auth); pullErr != nil {
		if IsConflict(pullErr) {
			b.conflict = true
			return pullErr
		}
		return &pendingPushError{cause: pullErr}
	}
	if err := b.push(auth); err != nil {
		return &pendingPushError{cause: err}
	}
	b.conflict = false
	return nil
}

func (b *GitBackend) push(auth transport.AuthMethod) error {
	branch := "refs/heads/" + b.cfg.Branch
	err := b.repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(branch + ":" + branch)},
		Auth:       auth,
	})
	if err == nil || err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
}

func isNonFastForward(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "rejected")
}

// pullRebaseForPush re-fetches origin and rebases the local branch onto it. It
// exists only to resolve a rejected push, so unlike Pull it assumes the local
// branch already exists.
func (b *GitBackend) pullRebaseForPush(auth transport.AuthMethod) error {
	err := b.repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/*:refs/remotes/origin/*")},
		Auth:       auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return err
	}
	up, err := b.repo.Reference(plumbing.NewRemoteReferenceName("origin", b.cfg.Branch), false)
	if err != nil {
		return nil
	}
	upCommit, err := b.repo.CommitObject(up.Hash())
	if err != nil {
		return err
	}
	head, err := b.repo.Head()
	if err != nil {
		return nil
	}
	headCommit, err := b.repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	if headCommit.Hash == upCommit.Hash || isAncestor(upCommit, headCommit) {
		return nil
	}
	if isAncestor(headCommit, upCommit) {
		wt, err := b.repo.Worktree()
		if err != nil {
			return err
		}
		return wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: upCommit.Hash})
	}
	if b.cfg.Conflict == "ff_only" {
		return ErrConflict
	}
	wt, err := b.repo.Worktree()
	if err != nil {
		return err
	}
	return b.rebaseOnto(wt, headCommit, upCommit)
}

// commitLocal folds staged worktree changes (if any) into a single commit.
func (b *GitBackend) commitLocal() error {
	wt, err := b.repo.Worktree()
	if err != nil {
		return err
	}
	st, err := wt.Status()
	if err != nil {
		return err
	}
	if st.IsClean() {
		return nil
	}
	_, err = wt.Commit(b.commitMessage(st), &git.CommitOptions{Author: b.signature(nil)})
	if err != nil {
		return fmt.Errorf("knowledge: commit: %w", err)
	}
	return nil
}

func (b *GitBackend) commitMessage(st git.Status) string {
	paths := make([]string, 0, len(st))
	for p, f := range st {
		if f.Staging != git.Unmodified {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		for p := range st {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	suffix := "document(s)"
	if len(paths) == 1 {
		suffix = "document"
	}
	return fmt.Sprintf("knowledge: update %d %s", len(paths), suffix)
}

func (b *GitBackend) signature(commit *object.Commit) *object.Signature {
	name, email := b.cfg.AuthorName, b.cfg.AuthorEmail
	when := time.Now()
	if commit != nil && commit.Author.When.Unix() != 0 {
		when = commit.Author.When
	}
	if name == "" {
		name = "openapi-mcp"
	}
	if email == "" {
		email = "openapi-mcp@localhost"
	}
	return &object.Signature{Name: name, Email: email, When: when}
}

// rebaseOnto replaces the divergent local commits with a replay of the same
// file changes committed on top of the upstream branch. On any failure the
// worktree is restored to the pre-operations HEAD and ErrConflict is returned.
func (b *GitBackend) rebaseOnto(wt *git.Worktree, head, upstream *object.Commit) error {
	merges, err := head.MergeBase(upstream)
	if err != nil {
		return fmt.Errorf("no merge base: %w", err)
	}
	if len(merges) == 0 {
		return fmt.Errorf("no merge base")
	}
	merged := merges[0]
	local, err := b.localCommitsSince(merged, head)
	if err != nil {
		return err
	}
	wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: upstream.Hash})

	defer func() {
		if err != nil {
			// Restore the checkout so a failed rebase leaves it untouched.
			wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: head.Hash})
		}
	}()
	for _, c := range local {
		if err = b.replayCommit(wt, c); err != nil {
			return err
		}
	}
	return nil
}

// localCommitsSince returns the commits from base (exclusive) to head,
// oldest first.
func (b *GitBackend) localCommitsSince(base, head *object.Commit) ([]*object.Commit, error) {
	iter, err := b.repo.Log(&git.LogOptions{From: head.Hash})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var revs []*object.Commit
	for {
		c, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if c.Hash == base.Hash {
			break
		}
		ancestor, err := base.IsAncestor(c)
		if err == nil && ancestor {
			revs = append(revs, c)
		}
	}
	sort.Slice(revs, func(i, j int) bool {
		return revs[i].Committer.When.Before(revs[j].Committer.When)
	})
	return revs, nil
}

// replayCommit applies the file-level changes of one local commit onto the
// (already reset) worktree and commits them.
func (b *GitBackend) replayCommit(wt *git.Worktree, c *object.Commit) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	parent, err := c.Parent(0)
	if err != nil {
		// Root commit: replay every file of the commit as an insertion.
		return b.replayFullTree(wt, tree)
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return err
	}
	changes, err := parentTree.Diff(tree)
	if err != nil {
		return err
	}
	for _, ch := range changes {
		name := changeName(ch)
		if err := b.applyChange(ch); err != nil {
			return fmt.Errorf("rebasing %s: %w", name, err)
		}
		if _, err := wt.Add(name); err != nil {
			return fmt.Errorf("staging %s during rebase: %w", name, err)
		}
	}
	msg := strings.TrimSpace(c.Message)
	msg = "rebase: knowledge(" + b.cfg.Branch + ") " + msg
	if _, err := wt.Commit(msg, &git.CommitOptions{Author: b.signature(c)}); err != nil {
		return fmt.Errorf("rebase commit: %w", err)
	}
	return nil
}

// replayFullTree writes every regular file of a tree into the worktree and
// stages it. It is the root-commit case of a rebase (no parent to diff).
func (b *GitBackend) replayFullTree(wt *git.Worktree, tree *object.Tree) error {
	iter := tree.Files()
	defer iter.Close()
	for {
		f, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !isFileMode(f.Mode) {
			continue
		}
		content, err := f.Contents()
		if err != nil {
			return err
		}
		full := filepath.Join(b.root, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
		if _, err := wt.Add(f.Name); err != nil {
			return err
		}
	}
	return nil
}

// changeName returns the affected path of a tree change.
func changeName(ch *object.Change) string {
	if ch.From.Name != "" {
		return ch.From.Name
	}
	return ch.To.Name
}

// applyChange writes (insert/modify) or removes (delete) a single file change
// into the knowledge root directory.
func (b *GitBackend) applyChange(ch *object.Change) error {
	action, err := ch.Action()
	if err != nil {
		return err
	}
	switch action {
	case merkletrie.Delete:
		return os.Remove(filepath.Join(b.root, filepath.FromSlash(ch.From.Name)))
	default:
		f, ferr := ch.To.Tree.TreeEntryFile(&ch.To.TreeEntry)
		if ferr != nil {
			return ferr
		}
		if !isFileMode(f.Mode) {
			return nil // not a regular file (e.g. a directory marker)
		}
		content, cerr := f.Contents()
		if cerr != nil {
			return cerr
		}
		full := filepath.Join(b.root, filepath.FromSlash(changeName(ch)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, []byte(content), 0o644)
	}
}

// Status returns a snapshot of the git sync state.
func (b *GitBackend) Status() (SyncStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.open(); err != nil {
		return SyncStatus{}, err
	}
	st := SyncStatus{Branch: b.cfg.Branch, Repo: b.cfg.Repository}
	if b.cfg.Repository != "" {
		if r, err := b.repo.Remote("origin"); err == nil && len(r.Config().URLs) > 0 {
			st.Repo = r.Config().URLs[0]
		}
	}
	if wt, err := b.repo.Worktree(); err == nil {
		if fs, err := wt.Status(); err == nil {
			st.Dirty = !fs.IsClean()
		}
	}
	head, err := b.repo.Head()
	if err == nil {
		st.Commit = shortHash(head.Hash().String())
		if up, err2 := b.repo.Reference(plumbing.NewRemoteReferenceName("origin", b.cfg.Branch), false); err2 == nil {
			st.Upstream = shortHash(up.Hash().String())
			if up.Hash() != head.Hash() {
				if hc, e1 := b.repo.CommitObject(head.Hash()); e1 == nil {
					if uc, e2 := b.repo.CommitObject(up.Hash()); e2 == nil && isAncestor(uc, hc) {
						st.PendingPush = true
					}
				}
			}
		}
	}
	st.Conflict = b.conflict
	return st, nil
}

// open ensures the repository exists (cloning or initializing as needed) and
// that an "origin" remote points at the configured URL.
func (b *GitBackend) open() error {
	if b.repo != nil {
		return nil
	}
	gitDir := filepath.Join(b.root, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		repo, err := git.PlainOpen(b.root)
		if err != nil {
			return fmt.Errorf("knowledge: open git checkout %q: %w", b.root, err)
		}
		b.repo = repo
		if err := b.ensureOrigin(); err != nil {
			return err
		}
		return nil
	}

	if b.cfg.Repository != "" {
		if err := os.MkdirAll(filepath.Dir(b.root), 0o755); err != nil {
			return err
		}
		auth, err := b.authMethod()
		if err != nil {
			return err
		}
		repo, err := git.PlainClone(b.root, false, &git.CloneOptions{
			URL:             b.cfg.Repository,
			Auth:            auth,
			RemoteName:      "origin",
			ReferenceName:   plumbing.NewBranchReferenceName(b.cfg.Branch),
			SingleBranch:    true,
			InsecureSkipTLS: false,
		})
		if err != nil {
			return fmt.Errorf("knowledge: cloning %q: %w", b.cfg.Repository, err)
		}
		b.repo = repo
		return nil
	}

	// Local-only repository.
	if err := os.MkdirAll(b.root, 0o755); err != nil {
		return err
	}
	repo, err := git.PlainInit(b.root, false)
	if err != nil {
		return fmt.Errorf("knowledge: initializing local git repository at %q: %w", b.root, err)
	}
	if b.cfg.Branch != "master" {
		head := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(b.cfg.Branch))
		_ = repo.Storer.SetReference(head)
	}
	b.repo = repo
	return nil
}

// ensureOrigin creates or updates the "origin" remote when a repository URL is
// configured. It is what lets a locally-initialized knowledge base later gain a
// remote (e.g. via update_api_knowledge).
func (b *GitBackend) ensureOrigin() error {
	if b.cfg.Repository == "" {
		return nil
	}
	rc := &config.RemoteConfig{Name: "origin", URLs: []string{b.cfg.Repository}}
	if _, err := b.repo.Remote("origin"); err != nil {
		_, err := b.repo.CreateRemote(rc)
		return err
	}
	cfg, err := b.repo.Config()
	if err != nil {
		return err
	}
	cfg.Remotes[rc.Name] = rc
	return b.repo.Storer.SetConfig(cfg)
}

func (b *GitBackend) authMethod() (transport.AuthMethod, error) {
	if b.cfg.SSHKey != "" {
		pem, err := os.ReadFile(b.cfg.SSHKey)
		if err != nil {
			return nil, fmt.Errorf("knowledge: reading ssh key %q: %w", b.cfg.SSHKey, err)
		}
		return gssh.NewPublicKeys("git", pem, "")
	}
	if b.cfg.AuthToken != "" {
		return &ghttp.BasicAuth{Username: "x-access-token", Password: b.cfg.AuthToken}, nil
	}
	return nil, nil
}

// isAncestor reports whether commit a is an ancestor of b, ignoring traversal
// errors.
func isAncestor(a, b *object.Commit) bool {
	ok, _ := a.IsAncestor(b)
	return ok
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// isFileMode reports whether a tree entry is a regular file.
func isFileMode(m filemode.FileMode) bool {
	return m.IsFile()
}
