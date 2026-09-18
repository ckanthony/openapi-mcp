// Package results implements an ephemeral results store: large tool/script
// outputs are written to disk and referenced by a compact UUID handle instead
// of being inlined into the MCP response, shrinking the agent prompt surface.
// Stored results expire after a TTL and are swept by a background goroutine.
package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Defaults used when Options leaves a field empty.
const (
	DefaultMaxInline = 64 * 1024 // results above 64 KiB are externalized
	DefaultTTL       = 30 * time.Minute
	DefaultSweep     = time.Minute
	handlePrefix     = "r_"
)

// Handle is the compact, client-visible reference to a stored result.
type Handle struct {
	ID        string    `json:"id"`                   // e.g. "r_3f2a..."
	Tool      string    `json:"tool,omitempty"`       // fully qualified tool name
	Kind      string    `json:"kind,omitempty"`       // operation | script | management | view
	SessionID string    `json:"session_id,omitempty"` // owning MCP session ("" = global)
	Bytes     int64     `json:"bytes"`                // stored content size
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Options configures a Store.
type Options struct {
	// Dir is the storage directory. When empty, a per-process temp dir is
	// created (cleaned up on Close).
	Dir string
	// TTL is how long a stored result lives. Default 30 minutes.
	TTL time.Duration
	// MaxInline is the size threshold above which results are externalized.
	// Callers use ShouldExternalize to decide. Default 64 KiB.
	MaxInline int
	// SweepEvery is the background cleanup interval. Default 1 minute.
	SweepEvery time.Duration
}

// Stats reports store state after a cleanup or on demand.
type Stats struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// meta is a result's persisted sidecar metadata.
type meta struct {
	Handle
	DataFile string `json:"data_file"`
}

// storeIndex is the in-memory view of all stored results, keyed by handle id.
// The authoritative record lives on disk (dir/<id>.json + dir/<id>.data); the
// index is rebuilt from the metadata files on New so restarts do not leak
// handles into limbo.
type storeIndex struct {
	byID map[string]*meta
}

// Store is an ephemeral, file-backed results store. It is safe for concurrent
// use. Spawn a background sweeper with Start; Close stops it.
type Store struct {
	opts Options
	dir  string

	mu    sync.RWMutex
	index storeIndex

	stop    chan struct{}
	stopped chan struct{}
}

// New creates a Store. When opts.Dir is empty a temp directory is used.
func New(opts Options) (*Store, error) {
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.MaxInline <= 0 {
		opts.MaxInline = DefaultMaxInline
	}
	if opts.SweepEvery <= 0 {
		opts.SweepEvery = DefaultSweep
	}
	dir := opts.Dir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "openapi-mcp-results-*")
		if err != nil {
			return nil, fmt.Errorf("results: create temp dir: %w", err)
		}
		dir = tmp
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("results: create dir %q: %w", dir, err)
	}

	s := &Store{
		opts:    opts,
		dir:     dir,
		index:   storeIndex{byID: map[string]*meta{}},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if err := s.reindex(); err != nil {
		return nil, err
	}
	s.sweepLocked(time.Now()) // drop anything that expired while we were away
	s.Start()
	return s, nil
}

// Start launches the background sweeper. Called by New; idempotent.
func (s *Store) Start() {
	go func() {
		defer close(s.stopped)
		ticker := time.NewTicker(s.opts.SweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sweep()
			case <-s.stop:
				return
			}
		}
	}()
}

// Close stops the background sweeper. Stored files are left on disk so a
// future Store pointing at the same dir may reuse them (before expiry).
func (s *Store) Close() error {
	select {
	case <-s.stop:
		return nil
	default:
	}
	close(s.stop)
	<-s.stopped
	return nil
}

// Dir returns the storage directory.
func (s *Store) Dir() string { return s.dir }

// TTL returns the configured expiration.
func (s *Store) TTL() time.Duration { return s.opts.TTL }

// MaxInline returns the configured inlining threshold.
func (s *Store) MaxInline() int { return s.opts.MaxInline }

// ShouldExternalize reports whether a payload of the given byte size should be
// stored out-of-line rather than inlined into the response.
func (s *Store) ShouldExternalize(n int) bool { return n > s.opts.MaxInline }

// Store writes data out-of-line and returns its handle. sessionID/tool/kind are
// provenance metadata recorded with the result. On store the data is written
// atomically (temp file + rename) after the sidecar, so a crash never leaves a
// half-written result referable.
func (s *Store) Store(sessionID, tool, kind string, data []byte) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	h := Handle{
		ID:        handlePrefix + uuid.NewString(),
		Tool:      tool,
		Kind:      kind,
		SessionID: sessionID,
		Bytes:     int64(len(data)),
		CreatedAt: now.UTC(),
		ExpiresAt: now.Add(s.opts.TTL).UTC(),
	}
	m := &meta{Handle: h, DataFile: h.ID + ".data"}

	if err := writeFileAtomic(filepath.Join(s.dir, m.DataFile), data); err != nil {
		return Handle{}, fmt.Errorf("results: write data: %w", err)
	}
	if err := s.writeMeta(m); err != nil {
		_ = os.Remove(filepath.Join(s.dir, m.DataFile))
		return Handle{}, err
	}
	s.index.byID[h.ID] = m
	return h, nil
}

// Get returns the stored payload for a handle. A handle from another session
// (when sessionID is non-empty) is rejected so results do not leak across
// sessions; expired handles return ErrExpired (unless the sweeper has already
// reaped them, in which case ErrNotFound).
func (s *Store) Get(sessionID, id string) ([]byte, error) {
	s.mu.RLock()
	m, ok := s.index.byID[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if s.expired(m) {
		return nil, ErrExpired
	}
	if sessionID != "" && m.SessionID != "" && m.SessionID != sessionID {
		return nil, ErrForbidden
	}
	data, err := os.ReadFile(filepath.Join(s.dir, m.DataFile))
	if err != nil {
		s.deleteOne(id)
		return nil, fmt.Errorf("results: read data: %w", err)
	}
	return data, nil
}

// Meta returns the handle metadata without reading the payload.
func (s *Store) Meta(sessionID, id string) (Handle, error) {
	s.mu.RLock()
	m, ok := s.index.byID[id]
	s.mu.RUnlock()
	if !ok {
		return Handle{}, ErrNotFound
	}
	if s.expired(m) {
		return Handle{}, ErrExpired
	}
	if sessionID != "" && m.SessionID != "" && m.SessionID != sessionID {
		return Handle{}, ErrForbidden
	}
	return m.Handle, nil
}

// Delete removes a stored result by handle.
func (s *Store) Delete(sessionID, id string) error {
	s.mu.RLock()
	m, ok := s.index.byID[id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if sessionID != "" && m.SessionID != "" && m.SessionID != sessionID {
		return ErrForbidden
	}
	s.deleteOne(id)
	return nil
}

// List returns the (non-expired) handles recorded for a session (or all handle,
// origin-sessions filtered when sessionID is non-empty), newest first.
func (s *Store) List(sessionID string) []Handle {
	s.mu.Lock()
	now := time.Now()
	out := make([]Handle, 0, len(s.index.byID))
	for _, m := range s.index.byID {
		if !m.ExpiresAt.After(now) {
			continue
		}
		if sessionID != "" && m.SessionID != sessionID {
			continue
		}
		out = append(out, m.Handle)
	}
	s.mu.Unlock()
	// newest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Cleanup removes expired results and reports how much was reclaimed. It is
// also the exported entry point for a manual cleanup tool.
func (s *Store) Cleanup() (Stats, error) {
	return s.sweep(), nil
}

// Stats reports current live results.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statsLocked()
}

func (s *Store) statsLocked() Stats {
	st := Stats{}
	for _, m := range s.index.byID {
		st.Count++
		st.Bytes += m.Bytes
	}
	return st
}

func (s *Store) sweep() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(time.Now())
}

func (s *Store) sweepLocked(now time.Time) Stats {
	var dead []string
	for id, m := range s.index.byID {
		if !m.ExpiresAt.After(now) {
			dead = append(dead, id)
		}
	}
	stats := Stats{}
	for _, id := range dead {
		if m, ok := s.index.byID[id]; ok {
			stats.Count++
			stats.Bytes += m.Bytes
			removeFiles(filepath.Join(s.dir, id+".json"), filepath.Join(s.dir, m.DataFile))
			delete(s.index.byID, id)
		}
	}
	return stats
}

func (s *Store) deleteOne(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.index.byID[id]; ok {
		removeFiles(filepath.Join(s.dir, id+".json"), filepath.Join(s.dir, m.DataFile))
		delete(s.index.byID, id)
	}
}

func (s *Store) expired(m *meta) bool {
	return !m.ExpiresAt.After(time.Now())
}

func (s *Store) writeMeta(m *meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("results: marshal meta: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(s.dir, m.ID+".json"), b); err != nil {
		return fmt.Errorf("results: write meta: %w", err)
	}
	return nil
}

// reindex rebuilds the in-memory index from the metadata sidecars on disk.
func (s *Store) reindex() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("results: read dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var m meta
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if m.ID == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.dir, m.DataFile)); err != nil {
			continue // orphan sidecar, skip
		}
		s.index.byID[m.ID] = &m
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".results-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func removeFiles(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// Errors returned by the Store.
var (
	ErrNotFound  = errors.New("results: handle not found")
	ErrExpired   = errors.New("results: handle expired")
	ErrForbidden = errors.New("results: handle not accessible from this session")
)
