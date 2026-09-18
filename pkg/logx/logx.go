// Package logx provides the MCP server's structured logging. It is built on
// log/slog but renders a compact, human-readable line per record:
//
//	2026-09-09 12:00:00.123 [INFO][server] server.go:96 Starting MCP server addr=:8086
//
// where [MODULE] names the component emitting the line, the source location is
// the file:line of the log call, and any structured fields follow the message
// as key=value pairs.
//
// Log levels follow the application policy:
//   - debug: detailed internals (request bodies, parameter processing, ...)
//   - info:  general information about actions performed by the MCP
//   - warn:  possible but non-critical issues
//   - error: critical, unrecoverable failures
//
// Configure() is called once at startup to point logging at stdout with the
// desired minimum level. Module loggers created earlier (or later) pick those
// settings up because they share the underlying handler state.
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// state is the mutable, handler-shared configuration: the writer and the
// minimum level. Derived handlers (WithAttrs/WithGroup) share one *state, so
// Configure()/SetLevel() affect every module logger.
//
// The minimum level is a slog.LevelVar: an atomic value that let the level be
// varied dynamically at runtime (see log/slog docs: "Setting HandlerOptions.Level
// to a LevelVar allows the level to be varied dynamically ... programLevel.Set(
// slog.LevelDebug)"). Every Enabled check reads it, so changing it takes effect
// on the very next log statement.
type state struct {
	mu    sync.RWMutex
	w     io.Writer
	level slog.LevelVar

	// writeMu serializes line writes so concurrent loggers never interleave
	// bytes mid-line.
	writeMu sync.Mutex
}

// Handler renders slog records in the human-readable format above.
type Handler struct {
	state  *state
	attrs  []slog.Attr
	groups []string
}

// defaultState is used until Configure is called.
var defaultState = &state{
	w: io.Discard,
}

// standard is the shared root handler used by Module.
var standard = &Handler{state: defaultState}

// Configure points the application logger at w with the given minimum level.
// It affects every logger returned by Module. The level can still be changed
// afterwards with SetLevel.
func Configure(w io.Writer, level slog.Level) {
	defaultState.mu.Lock()
	defaultState.w = w
	defaultState.mu.Unlock()
	defaultState.level.Set(level)
}

// SetLevel changes the minimum log level on the fly. It is safe to call from
// any goroutine and takes effect immediately; the fmt-level names are
// "debug", "info", "warn" and "error".
func SetLevel(level slog.Level) {
	defaultState.level.Set(level)
}

// Level returns the current minimum log level.
func Level() slog.Level {
	return defaultState.level.Level()
}

// SetLevelString parses a level name ("debug", "info", "warn"/"warning",
// "error") and applies it. It returns a descriptive error for unknown names.
func SetLevelString(s string) error {
	level, err := ParseLevel(s)
	if err != nil {
		return err
	}
	SetLevel(level)
	return nil
}

// Module returns a logger tagged with module, so every emitted line is prefixed
// with [module]. Output and minimum level follow the shared configuration.
func Module(name string) *slog.Logger {
	return slog.New(standard).With("module", name)
}

// ParseLevel converts a string log level ("debug", "info", "warn"/"warning",
// "error") into an slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: must be debug, info, warn or error", s)
	}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.state.level.Level()
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &Handler{state: h.state, attrs: merged, groups: h.groups}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := append([]string{}, h.groups...)
	groups = append(groups, name)
	return &Handler{state: h.state, attrs: h.attrs, groups: groups}
}

// Handle implements slog.Handler, rendering the record in the human-readable
// format.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("2006-01-02 15:04:05.000"))
	b.WriteString(" [")
	b.WriteString(strings.ToUpper(r.Level.String()))
	b.WriteString("][")

	module := ""
	prefix := strings.Join(h.groups, ".")
	var fields []string

	var collect func(gprefix string, attrs []slog.Attr)
	collect = func(gprefix string, attrs []slog.Attr) {
		for _, a := range attrs {
			a.Value = a.Value.Resolve()
			if a.Value.Kind() == slog.KindGroup {
				child := gprefix
				if a.Key != "" {
					if child == "" {
						child = a.Key
					} else {
						child = child + "." + a.Key
					}
				}
				collect(child, a.Value.Group())
				continue
			}
			key := a.Key
			if gprefix != "" {
				if key == "" {
					key = gprefix
				} else {
					key = gprefix + "." + key
				}
			}
			if key == "module" {
				module = a.Value.String()
				continue
			}
			if key == "" {
				continue
			}
			fields = append(fields, key+"="+valueString(a.Value))
		}
	}
	collect(prefix, h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		collect(prefix, []slog.Attr{a})
		return true
	})

	if module != "" {
		b.WriteString(module)
	} else {
		b.WriteString("-")
	}
	b.WriteString("] ")
	if file, line := sourceFromPC(r.PC); file != "" {
		b.WriteString(filepath.Base(file))
		b.WriteString(":")
		b.WriteString(strconv.Itoa(line))
		b.WriteByte(' ')
	}
	b.WriteString(r.Message)
	if len(fields) > 0 {
		b.WriteByte(' ')
		b.WriteString(strings.Join(fields, " "))
	}
	b.WriteByte('\n')

	st := h.state
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	st.mu.RLock()
	w := st.w
	st.mu.RUnlock()
	_, err := io.WriteString(w, b.String())
	return err
}

// sourceFromPC resolves a program counter (from slog.Record.PC) into the file
// and line of the log call site. It returns ("", 0) when no PC is available.
func sourceFromPC(pc uintptr) (string, int) {
	if pc == 0 {
		return "", 0
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	f, _ := frames.Next()
	if f.File == "" {
		return "", 0
	}
	return f.File, f.Line
}

// valueString renders a resolved attr value without quotes unless needed.
func valueString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return quoteIfNeeded(v.String())
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(v.Bool())
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return x.Error()
		case fmt.Stringer:
			return x.String()
		default:
			return quoteIfNeeded(fmt.Sprintf("%v", x))
		}
	default:
		return fmt.Sprintf("<%s>", v.Kind())
	}
}

// quoteIfNeeded quotes a string value when it contains characters that would
// blur the key=value formatting.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\r\"=") {
		return strconv.Quote(s)
	}
	return s
}
