package logx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	valid := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
		"INFO": slog.LevelInfo, "  debug  ": slog.LevelDebug,
	}
	for in, want := range valid {
		got, err := ParseLevel(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := ParseLevel("bogus")
	assert.Error(t, err)
}

func TestModuleAndFormat(t *testing.T) {
	var buf bytes.Buffer
	Configure(&buf, slog.LevelDebug)

	lg := Module("server")
	lg.Info("Starting MCP server", "addr", ":8086", "apis", 2)
	lg.Debug("found param", "key", "page", "val", "2")
	lg.Warn("dropped notification", "conn", "abc")

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3)

	assert.Contains(t, lines[0], "[INFO][server]")
	assert.Contains(t, lines[0], "logx_test.go:")
	assert.Contains(t, lines[0], "Starting MCP server")
	assert.Contains(t, lines[0], "addr=:8086")
	assert.Contains(t, lines[0], "apis=2")

	assert.Contains(t, lines[1], "[DEBUG][server]")
	assert.Contains(t, lines[1], "found param key=page val=2")

	assert.Contains(t, lines[2], "[WARN][server]")
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	Configure(&buf, slog.LevelWarn)

	lg := Module("server")
	lg.Info("should be filtered")
	lg.Debug("also filtered")
	lg.Warn("visible warn")
	lg.Error("visible error")

	out := buf.String()
	assert.NotContains(t, out, "should be filtered")
	assert.NotContains(t, out, "also filtered")
	assert.Contains(t, out, "visible warn")
	assert.Contains(t, out, "visible error")
}

func TestSetLevelChangesOnTheFly(t *testing.T) {
	var buf bytes.Buffer
	Configure(&buf, slog.LevelInfo)
	lg := Module("server")

	lg.Debug("hidden debug")
	assert.Empty(t, buf.String())

	// Raise verbosity at runtime; the very next call is emitted.
	SetLevelString("debug")
	lg.Debug("now visible")
	assert.Contains(t, buf.String(), "now visible")

	// And the current level is readable.
	assert.Equal(t, slog.LevelDebug, Level())

	// An invalid level name is rejected and leaves the level unchanged.
	require.Error(t, SetLevelString("loud"))
	assert.Equal(t, slog.LevelDebug, Level())
}

func TestModuleAttributeAndUntagged(t *testing.T) {
	var buf bytes.Buffer
	Configure(&buf, slog.LevelInfo)

	// An untagged logger prints [-].
	plain := Module("")
	plain.Info("no module")
	assert.Contains(t, buf.String(), "[INFO][-]")
	buf.Reset()

	// The module attribute is consumed into the header, not repeated as a field.
	named := Module("registry")
	named.Info("work", "item", 7)
	assert.NotContains(t, buf.String(), "module=registry")
	assert.Contains(t, buf.String(), "item=7")
}
