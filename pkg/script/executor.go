// Package script executes user-authored scripts (kind: script knowledge docs)
// in a sandboxed tengo VM and exposes a host bridge so a script can call other
// MCP tools. It deliberately has no dependency on pkg/server or pkg/knowledge:
// callers inject the host bridge (Options.Call/Resolve) and the declared
// permissions, keeping the executor independent and testable.
//
// Security model
//
//   - A script runs in a fresh tengo VM with only the safe standard-library
//     modules (math/text/times/rand/base64/hex/json/fmt/enum) plus the modules
//     it explicitly declares in its on-disk front-matter (permissions). The
//     privileged modules (mcp/os/exec/fs/http) default to deny.
//   - exec/fs/http are further constrained by operator allowlists supplied in
//     Options; an empty allowlist denies every use.
//   - Every run has a hard time budget (default 10s) and an allocation budget
//     (tengo's max-allocs guard) so a runaway loop cannot pin the host.
//   - Permissions are read from the document by the caller; a script can never
//     grant itself a privilege at runtime.
package script

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/d5/tengo/v2"
	"github.com/d5/tengo/v2/stdlib"
)

// Host module names a script may declare permission for.
const (
	ModuleMCP  = "mcp"
	ModuleOS   = "os"
	ModuleExec = "exec"
	ModuleFS   = "fs"
	ModuleHTTP = "http"
)

// resultVar is the implicit global the wrapped script assigns its return value
// to. Wrapping the source in a function makes a top-level `return` legal (tengo
// forbids it at file scope) and gives the executor a single place to read.
const resultVar = "__mcp_script_result__"

// Defaults for the execution guards.
const (
	DefaultTimeout   = 10 * time.Second
	DefaultMaxAllocs = 1 << 20 // ~1M object allocations
	// DefaultCacheEntries bounds the compiled-program cache per executor.
	DefaultCacheEntries = 64
)

// safeStdlibModules are the pure tengo standard-library modules always imported.
// The privileged os/exec/fs/http modules are intentionally excluded and
// replaced by the gated implementations in modules.go.
var safeStdlibModules = []string{"math", "text", "times", "rand", "base64", "hex", "json", "fmt", "enum"}

// Permissions is the set of host modules a script declared (and is therefore
// allowed to import). A missing/zero value denies every privileged module.
type Permissions map[string]bool

// Allows reports whether the named module was granted.
func (p Permissions) Allows(module string) bool { return p[module] }

// CallFunc is the host bridge used by the "mcp" module: it executes a fully
// qualified MCP tool name with JSON-ish arguments and returns the textual
// result. The server supplies it, so pkg/script stays free of registry deps.
type CallFunc func(ctx context.Context, tool string, args map[string]interface{}) (string, error)

// ResolveFunc reports whether a fully qualified tool name exists and returns
// lightweight metadata about it (used by mcp.resolve). It may be nil.
type ResolveFunc func(tool string) (map[string]interface{}, bool)

// Host is the per-run host binding. Because the "mcp" module closes over it, a
// Host partitions the compiled-program cache: the same script run for two
// sessions (Key = connection id) compiles twice, once per binding.
type Host struct {
	// Key partitions the compile cache (e.g. a session/connection id). Empty is
	// the process-global partition.
	Key string
	// Call backs mcp.call. When nil, the mcp module is not importable.
	Call CallFunc
	// Resolve backs mcp.resolve (optional).
	Resolve ResolveFunc
}

// Options configures an Executor.
type Options struct {
	// Permissions are the modules the script declared. Nil denies all.
	Permissions Permissions
	// ParamNames are the script's declared parameters. They are registered as
	// VM globals (so scripts can reference them directly) and refreshed on every
	// run. Optional; a "params" map global is always injected.
	ParamNames []string
	// Timeout is the per-run wall-clock budget (default DefaultTimeout).
	Timeout time.Duration
	// MaxAllocs is the tengo allocation guard (default DefaultMaxAllocs). It is
	// the executor's runaway-loop/step guard, alongside Timeout.
	MaxAllocs int64
	// CacheSize bounds the compiled-program cache (default DefaultCacheEntries).
	// The least-recently-used program is evicted when the bound is exceeded.
	CacheSize int
	// ExecAllowlist is the set of command names exec.run may execute.
	ExecAllowlist []string
	// FSReadRoots are the directory prefixes fs may read from ("" = none).
	FSReadRoots []string
	// HTTPAllowlist are the url patterns (suffix "*" = prefix, else exact) http
	// may request.
	HTTPAllowlist []string
}

// compiledEntry is one cached tengo program plus its cache key.
type compiledEntry struct {
	key  string
	prog *tengo.Compiled
}

// Executor runs scripts with a fixed permission set and host bridge. It caches
// compiled bytecode per (source hash, host key) in an LRU bounded by
// Options.CacheSize. Executors are safe for concurrent use.
type Executor struct {
	opts       Options
	mu         sync.Mutex
	cache      map[string]*list.Element // key -> element holding *compiledEntry
	order      *list.List               // front = most recently used
	maxEntries int
}

// NewExecutor builds an executor from opts, filling defaults.
func NewExecutor(opts Options) *Executor {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxAllocs <= 0 {
		opts.MaxAllocs = DefaultMaxAllocs
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = DefaultCacheEntries
	}
	return &Executor{
		opts:       opts,
		cache:      map[string]*list.Element{},
		order:      list.New(),
		maxEntries: opts.CacheSize,
	}
}

// Permissions returns the executor's declared permissions (defensive copy).
func (e *Executor) Permissions() Permissions {
	out := make(Permissions, len(e.opts.Permissions))
	for k, v := range e.opts.Permissions {
		out[k] = v
	}
	return out
}

// Timeout returns the executor's per-run time budget.
func (e *Executor) Timeout() time.Duration { return e.opts.Timeout }

// Run compiles (or reuses compiled) src and executes it with args injected as
// globals. The return value is normalized: nil/undefined -> "", string ->
// verbatim, everything else -> JSON.
func (e *Executor) Run(ctx context.Context, src string, args map[string]interface{}, host Host) (string, error) {
	compiled, err := e.compile(src, host)
	if err != nil {
		return "", err
	}
	run := compiled.Clone()
	for _, name := range e.opts.ParamNames {
		v, ok := args[name]
		if !ok {
			v = nil
		}
		if err := run.Set(name, v); err != nil {
			return "", fmt.Errorf("script parameter %q: %w", name, err)
		}
	}
	if err := run.Set("params", args); err != nil {
		return "", fmt.Errorf("injecting params: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	if err := run.RunContext(runCtx); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("script exceeded its %s time budget", e.opts.Timeout)
		}
		return "", err
	}
	return normalize(run.Get(resultVar))
}

// compile returns a cached compiled program for (src, host.Key), wrapping the
// source so a top-level `return` is valid and its value lands in resultVar.
func (e *Executor) compile(src string, host Host) (*tengo.Compiled, error) {
	key := sourceHash(src) + "\x00" + host.Key
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.cache[key]; ok {
		e.order.MoveToFront(el)
		return el.Value.(*compiledEntry).prog, nil
	}
	s := tengo.NewScript([]byte(wrapSource(src)))
	s.SetImports(e.buildModules(host))
	s.SetMaxAllocs(e.opts.MaxAllocs)
	// Define the params map and every declared parameter before compiling, so
	// each run can refresh them on a cloned program.
	if err := s.Add("params", map[string]interface{}{}); err != nil {
		return nil, fmt.Errorf("injecting params global: %w", err)
	}
	for _, name := range e.opts.ParamNames {
		if name == "" || name == "params" || name == resultVar {
			return nil, fmt.Errorf("invalid script parameter name %q", name)
		}
		if err := s.Add(name, nil); err != nil {
			return nil, fmt.Errorf("injecting parameter %q: %w", name, err)
		}
	}
	c, err := s.Compile()
	if err != nil {
		return nil, err
	}
	el := e.order.PushFront(&compiledEntry{key: key, prog: c})
	e.cache[key] = el
	for len(e.cache) > e.maxEntries {
		back := e.order.Back()
		if back == nil {
			break
		}
		e.order.Remove(back)
		delete(e.cache, back.Value.(*compiledEntry).key)
	}
	return c, nil
}

// wrapSource transforms the script source to extract imports (required at
// module scope) and capture the last expression as a return value.
// In Tengo, if there are imports, we extract them to module scope and then
// convert return statements to assignments to the result variable.
// If there are no imports, we wrap in a function (for backwards compatibility).
func wrapSource(src string) string {
	// Regex to match individual import statements: identifier := import(...)
	importStmtRegex := regexp.MustCompile(`(\w+\s*:=\s*import\s*\([^)]*\))`)
	
	lines := strings.Split(src, "\n")
	var imports []string
	var nonImports []string
	
	// Process each line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		
		// Skip empty lines and comments
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			nonImports = append(nonImports, line)
			continue
		}
		
		// Find all import statements in this line
		matches := importStmtRegex.FindAllStringIndex(line, -1)
		if len(matches) == 0 {
			// No imports on this line
			nonImports = append(nonImports, line)
			continue
		}
		
		// If there are imports on this line, we need to carefully separate them
		// from any other code. Split by semicolons and process each statement.
		statements := strings.Split(line, ";")
		var lineImports []string
		var otherStatements []string
		
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			
			// Check if this statement is an import
			if importStmtRegex.MatchString(stmt) {
				lineImports = append(lineImports, stmt)
			} else {
				otherStatements = append(otherStatements, stmt)
			}
		}
		
		// Add all imports for this line
		imports = append(imports, lineImports...)
		
		// Add other statements as non-import code
		if len(otherStatements) > 0 {
			nonImports = append(nonImports, strings.Join(otherStatements, "; "))
		}
	}
	
	// If there are imports, put them at module scope and don't wrap in a function.
	// Convert "return X" statements to "resultVar = X" assignments.
	if len(imports) > 0 {
		var result strings.Builder
		
		// Initialize the result variable at the start, before any code that might use it
		// Use empty string as the default value
		result.WriteString(resultVar)
		result.WriteString(" := \"\"\n")
		
		// Write imports at module scope
		for _, imp := range imports {
			result.WriteString(imp)
			result.WriteString("\n")
		}
		
		// Process non-import lines and convert return statements
		for _, line := range nonImports {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "return ") {
				// Replace "return X" with "resultVar = X"
				rest := trimmed[7:] // Remove "return "
				result.WriteString(resultVar)
				result.WriteString(" = ")
				result.WriteString(rest)
				result.WriteString("\n")
			} else {
				result.WriteString(line)
				result.WriteString("\n")
			}
		}
		
		return result.String()
	}
	
	// No imports: use the original function wrapper approach
	var result strings.Builder
	result.WriteString(resultVar)
	result.WriteString(" := func() {\n")
	result.WriteString(strings.Join(nonImports, "\n"))
	result.WriteString("\n}()\n")
	
	return result.String()
}

func sourceHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])
}

// normalize converts a tengo variable into the textual tool result.
func normalize(v *tengo.Variable) (string, error) {
	if v == nil || v.IsUndefined() {
		return "", nil
	}
	switch val := v.Value().(type) {
	case nil:
		return "", nil
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return "", fmt.Errorf("script result is not serializable: %w", err)
		}
		return string(b), nil
	}
}

// buildModules assembles the import map for a script: the safe stdlib modules
// plus the gated host modules the script declared. The mcp module is only
// importable when the run supplied a Host.Call bridge.
func (e *Executor) buildModules(host Host) *tengo.ModuleMap {
	mods := stdlib.GetModuleMap(safeStdlibModules...)
	if e.opts.Permissions.Allows(ModuleMCP) && host.Call != nil {
		attrs := map[string]tengo.Object{
			"call": &tengo.BuiltinFunction{Name: "call", Value: e.mcpCall(host.Call)},
		}
		if host.Resolve != nil {
			attrs["resolve"] = &tengo.BuiltinFunction{Name: "resolve", Value: e.mcpResolve(host.Resolve)}
		}
		mods.AddBuiltinModule(ModuleMCP, attrs)
	}
	if e.opts.Permissions.Allows(ModuleOS) {
		mods.AddBuiltinModule(ModuleOS, e.osModule())
	}
	if e.opts.Permissions.Allows(ModuleExec) {
		mods.AddBuiltinModule(ModuleExec, e.execModule())
	}
	if e.opts.Permissions.Allows(ModuleFS) {
		mods.AddBuiltinModule(ModuleFS, e.fsModule())
	}
	if e.opts.Permissions.Allows(ModuleHTTP) {
		mods.AddBuiltinModule(ModuleHTTP, e.httpModule())
	}
	return mods
}
