package results

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreDefaultOptions(t *testing.T) {
	s, err := New(Options{})
	require.NoError(t, err)
	defer s.Close()

	require.Equal(t, DefaultTTL, s.TTL())
	require.Equal(t, DefaultMaxInline, s.MaxInline())
	require.False(t, s.ShouldExternalize(1024))
	require.True(t, s.ShouldExternalize(DefaultMaxInline+1))
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := New(Options{Dir: t.TempDir(), SweepEvery: time.Hour})
	require.NoError(t, err)
	defer s.Close()

	h, err := s.Store("sess-1", "finn__get_users", "operation", []byte("x"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(h.ID, handlePrefix))
	require.Equal(t, int64(1), h.Bytes)
	require.False(t, h.ExpiresAt.Before(time.Now().Add(DefaultTTL-time.Minute)))

	got, err := s.Get("sess-1", h.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("x"), got)

	// a different session must not read it
	_, err = s.Get("sess-2", h.ID)
	require.ErrorIs(t, err, ErrForbidden)

	// unknown handle
	_, err = s.Get("sess-1", handlePrefix+"does-not-exist")
	require.ErrorIs(t, err, ErrNotFound)

	// meta access
	m, err := s.Meta("sess-1", h.ID)
	require.NoError(t, err)
	require.Equal(t, h.ID, m.ID)
	require.Equal(t, "finn__get_users", m.Tool)

	// list shows it, session-scoped and global
	require.Len(t, s.List("sess-1"), 1)
	require.Len(t, s.List("sess-2"), 0)
	require.Len(t, s.List(""), 1)

	// delete
	require.NoError(t, s.Delete("sess-1", h.ID))
	_, err = s.Get("sess-1", h.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.Len(t, s.List(""), 0)
}

func TestStoreTTLExpiry(t *testing.T) {
	s, err := New(Options{
		Dir:        t.TempDir(),
		TTL:        100 * time.Millisecond,
		SweepEvery: time.Hour, // manual cleanup below
	})
	require.NoError(t, err)
	defer s.Close()

	h, err := s.Store("sess", "t", "operation", []byte("data"))
	require.NoError(t, err)

	// fresh: readable
	require.Len(t, s.List(""), 1)
	_, err = s.Get("sess", h.ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := s.Get("sess", h.ID)
		return err == ErrExpired
	}, time.Second, 20*time.Millisecond)

	// expired: Meta reports ErrExpired too
	_, err = s.Meta("sess", h.ID)
	require.ErrorIs(t, err, ErrExpired)
}

func TestStoreCleanupRemovesExpired(t *testing.T) {
	s, err := New(Options{
		Dir:        t.TempDir(),
		TTL:        100 * time.Millisecond,
		SweepEvery: time.Hour,
	})
	require.NoError(t, err)
	defer s.Close()

	h1, err := s.Store("s", "op1", "operation", []byte("aaaaaaa"))
	require.NoError(t, err)
	st := s.Stats()
	require.Equal(t, 1, st.Count)
	require.Equal(t, int64(7), st.Bytes)

	require.Eventually(t, func() bool {
		_, err := s.Get("s", h1.ID)
		return err == ErrExpired
	}, time.Second, 20*time.Millisecond)

	st, err = s.Cleanup()
	require.NoError(t, err)
	require.Equal(t, 1, st.Count)
	require.Equal(t, int64(7), st.Bytes)

	require.Len(t, s.List(""), 0)
	_, err = s.Get("s", h1.ID)
	require.ErrorIs(t, err, ErrNotFound)

	// files are gone from disk
	require.Empty(t, s.indexByIDForTest())
}

func TestStorePersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(Options{Dir: dir, SweepEvery: time.Hour})
	require.NoError(t, err)
	h, err := s1.Store("sess", "t", "script", []byte("persisted"))
	require.NoError(t, err)
	s1.Close()

	// a fresh store on the same dir re-loads the index
	s2, err := New(Options{Dir: dir, SweepEvery: time.Hour})
	require.NoError(t, err)
	defer s2.Close()

	got, err := s2.Get("sess", h.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("persisted"), got)
}

func TestHandlePayload(t *testing.T) {
	s, _ := New(Options{Dir: t.TempDir(), SweepEvery: time.Hour})
	defer s.Close()

	h, err := s.Store("sess", "finn__x", "operation", []byte("12345"))
	require.NoError(t, err)

	text := s.HandlePayloadText(h)
	require.Contains(t, text, `"handle":"`+h.ID+`"`)
	require.Contains(t, text, `"bytes":5`)
	require.Contains(t, text, `"truncated":true`)

	p, ok := ParseHandlePayload(text)
	require.True(t, ok)
	require.Equal(t, h.ID, p.HandleID)
	require.Equal(t, "finn__x", p.Tool)
	require.Equal(t, "operation", p.Kind)
	require.True(t, p.ExpiresIn > 0)

	// non-handle text is not a handle payload
	_, ok = ParseHandlePayload(`{"a":1}`)
	require.False(t, ok)
	_, ok = ParseHandlePayload(`plain text`)
	require.False(t, ok)
}

func TestStoreConcurrentAccess(t *testing.T) {
	s, err := New(Options{Dir: t.TempDir(), SweepEvery: time.Millisecond * 10})
	require.NoError(t, err)
	defer s.Close()

	const n = 50
	ids := make(chan string, n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			h, err := s.Store("s", "t", "operation", []byte(strings.Repeat("x", i)))
			if err == nil {
				ids <- h.ID
			}
			_, _ = s.Get("s", h.ID)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	close(ids)
	count := 0
	for range ids {
		count++
	}
	require.Equal(t, n, count)
}

// indexByIDForTest returns the current in-memory ids (test-only introspection).
func (s *Store) indexByIDForTest() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id := range s.index.byID {
		out = append(out, id)
	}
	return out
}
