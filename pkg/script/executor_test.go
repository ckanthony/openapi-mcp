package script

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, opts Options, src string, args map[string]interface{}) (string, error) {
	t.Helper()
	return NewExecutor(opts).Run(context.Background(), src, args, Host{})
}

func TestExecutorReturns(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"string", `return "hello"`, "hello"},
		{"map-json", `return {a: 1, b: [2, 3]}`, `{"a":1,"b":[2,3]}`},
		{"array-json", `return [1, 2]`, `[1,2]`},
		{"nil-empty", `x := undefined; return x`, ""},
		{"implicit-undefined", `x := 1`, ""},
		{"computed", "a := 2\nb := 3\nreturn a * b", "6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(t, Options{}, tc.src, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestExecutorParams(t *testing.T) {
	opts := Options{ParamNames: []string{"name", "count"}}
	got, err := run(t, opts, `return name + ":" + string(count)`, map[string]interface{}{"name": "w", "count": int64(3)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "w:3" {
		t.Fatalf("got %q", got)
	}
	got, err = run(t, opts, `return params.name`, map[string]interface{}{"name": "map"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "map" {
		t.Fatalf("params map got %q", got)
	}
}

func TestExecutorSafeStdlib(t *testing.T) {
	got, err := run(t, Options{}, `m := import("math"); return m.sqrt(16)`, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "4" {
		t.Fatalf("got %q", got)
	}
}

func TestExecutorDeniedModule(t *testing.T) {
	_, err := run(t, Options{}, `m := import("exec"); return "x"`, nil)
	if err == nil {
		t.Fatal("expected compile error for denied module")
	}
}

func TestExecutorMCPModule(t *testing.T) {
	var called string
	opts := Options{Permissions: Permissions{ModuleMCP: true}}
	host := Host{
		Key: "sess-1",
		Call: func(ctx context.Context, tool string, args map[string]interface{}) (string, error) {
			called = tool
			return "pong", nil
		},
	}
	got, err := NewExecutor(opts).Run(context.Background(), `m := import("mcp"); return m.call("weather__getForecast", {city: "x"})`, nil, host)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "pong" || called != "weather__getForecast" {
		t.Fatalf("got %q called %q", got, called)
	}
}

func TestExecutorTimeout(t *testing.T) {
	start := time.Now()
	_, err := run(t, Options{Timeout: 100 * time.Millisecond}, `for { x := 1 }`, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "time budget") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestExecutorExecAllowlist(t *testing.T) {
	opts := Options{Permissions: Permissions{ModuleExec: true}, ExecAllowlist: []string{"echo"}}
	got, err := run(t, opts, `e := import("exec"); return e.run("echo", "hi")`, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(got) != "hi" {
		t.Fatalf("got %q", got)
	}
	_, err = run(t, opts, `e := import("exec"); return e.run("rm", "-rf", "/")`, nil)
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected allowlist error, got %v", err)
	}
	_, err = run(t, Options{Permissions: Permissions{ModuleExec: true}}, `e := import("exec"); return e.run("echo")`, nil)
	if err == nil || !strings.Contains(err.Error(), "no commands") {
		t.Fatalf("expected empty-allowlist error, got %v", err)
	}
}

func TestExecutorFSRoots(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Permissions: Permissions{ModuleFS: true}, FSReadRoots: []string{dir}}
	got, err := run(t, opts, `f := import("fs"); f.read("/etc/passwd")`, nil)
	_ = got
	if err == nil || !strings.Contains(err.Error(), "outside the configured read roots") {
		t.Fatalf("expected path-escape error, got %v", err)
	}
}

func TestExecutorHTTPAllowlist(t *testing.T) {
	opts := Options{Permissions: Permissions{ModuleHTTP: true}, HTTPAllowlist: []string{"https://example.com/*"}}
	_, err := run(t, opts, `h := import("http"); return h.get("https://evil.test/")`, nil)
	if err == nil || !strings.Contains(err.Error(), "not in the allowlist") {
		t.Fatalf("expected allowlist error, got %v", err)
	}
}

func TestExecutorHTTPRedirectBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.test/steal", http.StatusFound)
	}))
	defer server.Close()
	opts := Options{
		Permissions:   Permissions{ModuleHTTP: true},
		HTTPAllowlist: []string{server.URL + "/*"},
	}
	_, err := run(t, opts, `h := import("http"); return h.get("`+server.URL+`/start")`, nil)
	if err == nil {
		t.Fatal("expected redirect to be blocked")
	}
}

func TestExecutorCacheBounded(t *testing.T) {
	e := NewExecutor(Options{CacheSize: 2})
	ctx := context.Background()
	runOne := func(src string) {
		t.Helper()
		if _, err := e.Run(ctx, src, nil, Host{}); err != nil {
			t.Fatalf("run %q: %v", src, err)
		}
	}
	runOne(`return "a"`)
	runOne(`return "b"`)
	e.mu.Lock()
	got := len(e.cache)
	e.mu.Unlock()
	if got != 2 {
		t.Fatalf("cache len = %d, want 2", got)
	}
	runOne(`return "c"`)
	e.mu.Lock()
	got = len(e.cache)
	e.mu.Unlock()
	if got != 2 {
		t.Fatalf("cache len = %d, want 2 after eviction", got)
	}
	// The least-recently-used program was evicted; re-running it recompiles.
	out, err := e.Run(ctx, `return "a"`, nil, Host{})
	if err != nil || out != "a" {
		t.Fatalf("re-run evicted program: %q %v", out, err)
	}
}
