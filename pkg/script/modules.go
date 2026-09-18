package script

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/d5/tengo/v2"
)

// --- mcp module ---

// mcpCall implements mcp.call(tool, args?) -> string, bound to the host bridge.
func (e *Executor) mcpCall(call CallFunc) tengo.CallableFunc {
	return func(args ...tengo.Object) (tengo.Object, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("mcp.call: tool name is required")
		}
		tool, ok := args[0].(*tengo.String)
		if !ok || strings.TrimSpace(tool.Value) == "" {
			return nil, fmt.Errorf("mcp.call: tool name must be a non-empty string")
		}
		in := map[string]interface{}{}
		if len(args) > 1 && args[1] != tengo.UndefinedValue {
			m, ok := args[1].(*tengo.Map)
			if !ok {
				return nil, fmt.Errorf("mcp.call: arguments must be a map")
			}
			for k, v := range m.Value {
				in[k] = tengo.ToInterface(v)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
		defer cancel()
		out, err := call(ctx, tool.Value, in)
		if err != nil {
			return nil, err
		}
		return &tengo.String{Value: out}, nil
	}
}

// mcpResolve implements mcp.resolve(tool) -> {name, api, ...} | undefined.
func (e *Executor) mcpResolve(resolve ResolveFunc) tengo.CallableFunc {
	return func(args ...tengo.Object) (tengo.Object, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("mcp.resolve: tool name is required")
		}
		tool, ok := args[0].(*tengo.String)
		if !ok {
			return nil, fmt.Errorf("mcp.resolve: tool name must be a string")
		}
		if resolve == nil {
			return tengo.UndefinedValue, nil
		}
		info, found := resolve(tool.Value)
		if !found {
			return tengo.UndefinedValue, nil
		}
		return tengo.FromInterface(info)
	}
}

// --- os module ---

func (e *Executor) osModule() map[string]tengo.Object {
	return map[string]tengo.Object{
		"getenv": &tengo.BuiltinFunction{Name: "getenv", Value: func(args ...tengo.Object) (tengo.Object, error) {
			name, err := stringArg("os.getenv", args, 0)
			if err != nil {
				return nil, err
			}
			return &tengo.String{Value: os.Getenv(name)}, nil
		}},
		"hostname": &tengo.BuiltinFunction{Name: "hostname", Value: func(args ...tengo.Object) (tengo.Object, error) {
			h, err := os.Hostname()
			if err != nil {
				return nil, err
			}
			return &tengo.String{Value: h}, nil
		}},
		"getwd": &tengo.BuiltinFunction{Name: "getwd", Value: func(args ...tengo.Object) (tengo.Object, error) {
			wd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			return &tengo.String{Value: wd}, nil
		}},
		"environ": &tengo.BuiltinFunction{Name: "environ", Value: func(args ...tengo.Object) (tengo.Object, error) {
			env := os.Environ()
			out := make([]interface{}, len(env))
			for i, v := range env {
				out[i] = v
			}
			return tengo.FromInterface(out)
		}},
	}
}

// --- exec module ---

func (e *Executor) execModule() map[string]tengo.Object {
	return map[string]tengo.Object{
		"run": &tengo.BuiltinFunction{Name: "run", Value: func(args ...tengo.Object) (tengo.Object, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("exec.run: command is required")
			}
			cmdName, ok := args[0].(*tengo.String)
			if !ok || strings.TrimSpace(cmdName.Value) == "" {
				return nil, fmt.Errorf("exec.run: command must be a non-empty string")
			}
			argv := make([]string, 0, len(args)-1)
			for i := 1; i < len(args); i++ {
				s, ok := args[i].(*tengo.String)
				if !ok {
					return nil, fmt.Errorf("exec.run: argument %d must be a string", i)
				}
				argv = append(argv, s.Value)
			}
			if err := e.checkExecAllowed(cmdName.Value); err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, cmdName.Value, argv...)
			out, err := cmd.CombinedOutput()
			if ctx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("exec.run: command %q timed out after %s", cmdName.Value, e.opts.Timeout)
			}
			if err != nil {
				return nil, fmt.Errorf("exec.run: %v: %s", err, strings.TrimSpace(string(out)))
			}
			return &tengo.String{Value: string(out)}, nil
		}},
	}
}

func (e *Executor) checkExecAllowed(cmd string) error {
	if len(e.opts.ExecAllowlist) == 0 {
		return fmt.Errorf("exec.run: no commands are allowed (configure meta.scripting.exec_allowlist)")
	}
	base := path.Base(filepath.ToSlash(cmd))
	for _, allowed := range e.opts.ExecAllowlist {
		if allowed == cmd || allowed == base {
			return nil
		}
	}
	return fmt.Errorf("exec.run: command %q is not in the allowlist", cmd)
}

// --- fs module ---

func (e *Executor) fsModule() map[string]tengo.Object {
	return map[string]tengo.Object{
		"read": &tengo.BuiltinFunction{Name: "read", Value: func(args ...tengo.Object) (tengo.Object, error) {
			p, err := stringArg("fs.read", args, 0)
			if err != nil {
				return nil, err
			}
			full, err := e.resolveFSPath(p)
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			return &tengo.String{Value: string(data)}, nil
		}},
		"stat": &tengo.BuiltinFunction{Name: "stat", Value: func(args ...tengo.Object) (tengo.Object, error) {
			p, err := stringArg("fs.stat", args, 0)
			if err != nil {
				return nil, err
			}
			full, err := e.resolveFSPath(p)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(full)
			if err != nil {
				return nil, err
			}
			return tengo.FromInterface(map[string]interface{}{
				"name":     info.Name(),
				"size":     info.Size(),
				"is_dir":   info.IsDir(),
				"mode":     info.Mode().String(),
				"mod_time": info.ModTime().UTC().Format(time.RFC3339),
			})
		}},
		"list": &tengo.BuiltinFunction{Name: "list", Value: func(args ...tengo.Object) (tengo.Object, error) {
			p, err := stringArg("fs.list", args, 0)
			if err != nil {
				return nil, err
			}
			full, err := e.resolveFSPath(p)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(full)
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, 0, len(entries))
			for _, entry := range entries {
				out = append(out, entry.Name())
			}
			return tengo.FromInterface(out)
		}},
	}
}

func (e *Executor) resolveFSPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("fs: path is required")
	}
	if len(e.opts.FSReadRoots) == 0 {
		return "", fmt.Errorf("fs: no read roots are configured (configure meta.scripting.fs_read_roots)")
	}
	full, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// Resolve symlinks so a link inside an allowed root cannot escape it.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	}
	for _, root := range e.opts.FSReadRoots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, full)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return full, nil
		}
	}
	return "", fmt.Errorf("fs: path %q is outside the configured read roots", p)
}

// --- http module ---

func (e *Executor) httpModule() map[string]tengo.Object {
	do := func(method string, args []tengo.Object) (tengo.Object, error) {
		var url string
		var body string
		headers := map[string]interface{}{}
		var err error
		switch method {
		case http.MethodGet:
			url, err = stringArg("http.get", args, 0)
			if err != nil {
				return nil, err
			}
			if len(args) > 1 {
				headers, _ = mapArg(args[1])
			}
		default:
			url, err = stringArg("http.post", args, 0)
			if err != nil {
				return nil, err
			}
			if len(args) > 1 {
				if s, ok := args[1].(*tengo.String); ok {
					body = s.Value
				}
			}
			if len(args) > 2 {
				headers, _ = mapArg(args[2])
			}
		}
		if err := e.checkHTTPAllowed(url); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
		defer cancel()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
		// Re-validate every redirect target so an allowlisted URL cannot bounce
		// the request to a host outside the allowlist.
		client := &http.Client{
			CheckRedirect: func(next *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("http: stopped after 5 redirects")
				}
				return e.checkHTTPAllowed(next.URL.String())
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("http: %s %s returned %s: %s", method, url, resp.Status, strings.TrimSpace(string(data)))
		}
		return &tengo.String{Value: string(data)}, nil
	}
	return map[string]tengo.Object{
		"get": &tengo.BuiltinFunction{Name: "get", Value: func(args ...tengo.Object) (tengo.Object, error) {
			return do(http.MethodGet, args)
		}},
		"post": &tengo.BuiltinFunction{Name: "post", Value: func(args ...tengo.Object) (tengo.Object, error) {
			return do(http.MethodPost, args)
		}},
	}
}

func (e *Executor) checkHTTPAllowed(rawURL string) error {
	if len(e.opts.HTTPAllowlist) == 0 {
		return fmt.Errorf("http: no urls are allowed (configure meta.scripting.http_allowlist)")
	}
	for _, pattern := range e.opts.HTTPAllowlist {
		if pattern == rawURL {
			return nil
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(rawURL, strings.TrimSuffix(pattern, "*")) {
			return nil
		}
	}
	return fmt.Errorf("http: url %q is not in the allowlist", rawURL)
}

// --- argument helpers ---

func stringArg(fn string, args []tengo.Object, i int) (string, error) {
	if i >= len(args) {
		return "", fmt.Errorf("%s: missing string argument %d", fn, i+1)
	}
	s, ok := args[i].(*tengo.String)
	if !ok {
		return "", fmt.Errorf("%s: argument %d must be a string", fn, i+1)
	}
	return s.Value, nil
}

func mapArg(obj tengo.Object) (map[string]interface{}, bool) {
	m, ok := obj.(*tengo.Map)
	if !ok {
		if im, ok2 := obj.(*tengo.ImmutableMap); ok2 {
			out := make(map[string]interface{}, len(im.Value))
			for k, v := range im.Value {
				out[k] = tengo.ToInterface(v)
			}
			return out, true
		}
		return nil, false
	}
	out := make(map[string]interface{}, len(m.Value))
	for k, v := range m.Value {
		out[k] = tengo.ToInterface(v)
	}
	return out, true
}
