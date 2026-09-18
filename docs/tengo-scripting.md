# Tengo Scripting Guide

This document covers Tengo scripting integration in openapi-mcp, including import extraction, module loading, and stdlib usage.

## Quick Reference

### Key Differences: Go vs Tengo JSON Module

| Operation | Go Package | Tengo Module |
|-----------|-----------|--------------|
| Parse JSON | `json.Unmarshal()` | `json.decode()` |
| Encode to JSON | `json.Marshal()` | `json.encode()` |
| Indent JSON | N/A | `json.indent()` |
| HTML Escape | N/A | `json.html_escape()` |

**Remember**: Use Tengo function names (`decode`/`encode`), NOT Go names (`Unmarshal`/`Marshal`).

## How Tengo Scripting Works in openapi-mcp

### 1. Script Front-Matter Format

All Tengo scripts (.tengo files) use YAML front-matter between `// ---` markers:

```tengo
// ---
// id: my_script
// kind: script
// api: myapi
// summary: What this script does
// params:
//     - name: input_data
//       type: string
//       required: true
//       description: Input JSON data
// ---

// Actual Tengo code below
json := import("json")
data := json.decode(input_data)
return "processed"
```

The front-matter is stripped before execution, leaving only the executable code.

### 2. Import Extraction (Critical Constraint)

**Tengo requires all imports at module scope** - they cannot be inside functions.

#### The Execution Pipeline

```
Raw Script
    ↓
parseTengoDoc() - strips front-matter
    ↓
wrapSource()    - extracts imports to module scope
    ↓
Compiled Tengo:
    json := import("json")
    fmt := import("fmt")
    __mcp_script_result__ := ""
    
    // user code with return statements converted to assignments
    users := json.decode(users_json)
    __mcp_script_result__ = "done"
```

#### Rules for wrapSource()

1. **Initialize result variable first**:
   ```tengo
   __mcp_script_result__ := ""
   ```

2. **Place all imports at module scope**:
   ```tengo
   json := import("json")
   math := import("math")
   ```

3. **Convert `return X` to `__mcp_script_result__ = X`**:
   ```tengo
   // Original
   return "result"
   
   // Becomes
   __mcp_script_result__ = "result"
   ```

4. **Handle multi-statement lines**:
   - Split by semicolons
   - Separate imports from other code
   - Keep order intact

### 3. Available Standard Library Modules

All modules are imported using the `import()` function at module scope:

```tengo
math := import("math")
fmt := import("fmt")
json := import("json")
text := import("text")
times := import("times")
rand := import("rand")
base64 := import("base64")
hex := import("hex")
enum := import("enum")
```

#### Module Reference

**math** - Mathematical operations
```tengo
math.abs(-5)        // 5
math.sqrt(9)        // 3
math.min(1, 2)      // 1
math.max(1, 2)      // 2
```

**fmt** - Formatting
```tengo
fmt.sprintf("hello %s", "world")     // "hello world"
fmt.println("output")
```

**json** - JSON encode/decode
```tengo
data := json.decode(`{"a": 1, "b": [2, 3]}`)   // Parse JSON
result := json.encode(data)                      // Create JSON
indented := json.indent(result, "", "  ")       // Format nicely
safe := json.html_escape(result)                // HTML-safe
```

**text** - String operations & regex
```tengo
text.compare("a", "b")           // -1, 0, or 1
text.contains("hello", "ell")    // true
text.index("hello", "l")         // 2
text.regex_match(".*@.*", "a@b") // true
```

**times** - Time functions
```tengo
times.now()              // Current time
times.unix(1234567890)   // Time from unix timestamp
```

**rand** - Random functions
```tengo
rand.float()             // Random float 0-1
rand.int_range(1, 10)    // Random int in range
```

**base64** - Base64 encoding/decoding
```tengo
base64.encode("hello")   // "aGVsbG8="
base64.decode("aGVsbG8=") // "hello"
```

**hex** - Hex encoding/decoding
```tengo
hex.encode("hello")      // "68656c6c6f"
hex.decode("68656c6c6f") // "hello"
```

**enum** - Enumeration functions
```tengo
enum.all([1, 2, 3], func(x) { return x > 0 })  // true if all match
enum.any([1, 2, 3], func(x) { return x > 2 })  // true if any match
enum.map([1, 2, 3], func(x) { return x * 2 })  // [2, 4, 6]
```

## Script Examples

### Example 1: Simple JSON Processing

```tengo
// ---
// id: parse_json
// kind: script
// api: myapi
// summary: Parse and validate JSON input
// params:
//     - name: json_data
//       type: string
//       required: true
// ---
json := import("json")

data := json.decode(json_data)
if !data {
  return "error: invalid JSON"
}

return "valid"
```

### Example 2: Complex Data Transformation

```tengo
// ---
// id: transform_users
// kind: script
// api: finn
// summary: Transform and analyze user data
// params:
//     - name: users_json
//       type: string
//       description: JSON array of users
//     - name: show_count
//       type: int
//       description: Number of users to show
// ---
json := import("json")
fmt := import("fmt")
math := import("math")

// Validate input
if !users_json {
  return "error: users_json required"
}

if !show_count {
  show_count = 5
}

// Parse and process
users := json.decode(users_json)
if !is_array(users) {
  return "error: must be array"
}

total := len(users)
active_count := 0

for user in users {
  if user["IsActive"] {
    active_count++
  }
}

percentage := (active_count / total) * 100
shown := math.min(show_count, total)

result := fmt.sprintf(
  "Total: %d, Active: %d (%.1f%%), Showing: %d",
  total, active_count, percentage, shown
)

return result
```

### Example 3: Multiple Imports on One Line

```tengo
// ---
// id: multi_import
// kind: script
// api: test
// summary: Test multiple imports on same line
// ---
json := import("json"); math := import("math")
x := json.decode(`[1,2,3]`)
y := math.sqrt(9)
return string(len(x)) + " and " + string(y)
```

The extraction logic automatically handles this by splitting on semicolons.

## Testing Your Scripts

### Running Tests

```bash
curl -s http://localhost:8086/mcp -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "finn__my_script",
      "arguments": {
        "param1": "value1",
        "param2": "value2"
      }
    }
  }' | jq '.result.content[0].text'
```

### Common Issues & Solutions

#### Issue: `not callable: undefined`
**Cause**: Using wrong function name (e.g., `json.unmarshal` instead of `json.decode`)
**Solution**: Check Tengo stdlib docs, use correct names

#### Issue: `return not allowed outside function`
**Cause**: Script has no imports but return statement is at module scope
**Solution**: Add at least one import, OR wrap code differently

#### Issue: `unresolved reference`
**Cause**: Variable/module used before definition
**Solution**: Ensure imports come before usage, initialize variables

#### Issue: Import doesn't work in code
**Cause**: Import placed inside function instead of module scope
**Solution**: Verify extraction placed it at top level (this is handled automatically)

## Implementation Details

### File Locations

- **Script executor**: `pkg/script/executor.go` - `wrapSource()` function (lines 246-327)
- **Script parser**: `pkg/knowledge/script.go` - `parseTengoDoc()` function (lines 37-86)
- **Tengo VM setup**: `pkg/script/executor.go` - `compile()` and `buildModules()` functions

### Key Code Sections

#### Front-Matter Stripping (script.go:79-84)
```go
// Strip front-matter from Source: keep only lines after closing // ---
if end+1 < len(lines) {
    doc.Source = strings.Join(lines[end+1:], "\n")
} else {
    doc.Source = ""
}
```

#### Import Extraction (executor.go:246-327)
```go
// Regex matches: identifier := import("module")
importStmtRegex := regexp.MustCompile(`(\w+\s*:=\s*import\s*\([^)]*\))`)

// Split by semicolons to handle multiple statements per line
statements := strings.Split(line, ";")

// Separate imports from other code
for _, stmt := range statements {
    if importStmtRegex.MatchString(stmt) {
        imports = append(imports, stmt)
    } else {
        otherStatements = append(otherStatements, stmt)
    }
}
```

### Module Registration

Modules are registered in `buildModules()` (executor.go:383-406):

```go
// Safe stdlib modules always available
safeStdlibModules := []string{
    "math", "text", "times", "rand", 
    "base64", "hex", "json", "fmt", "enum"
}

mods := stdlib.GetModuleMap(safeStdlibModules...)
```

## Best Practices

1. **Always declare imports at the start**:
   ```tengo
   // GOOD
   json := import("json")
   fmt := import("fmt")
   // ... rest of code
   
   // BAD - mixed with other code
   if condition {
     json := import("json")
   }
   ```

2. **Use `json.decode()` not `json.unmarshal()`**:
   ```tengo
   // CORRECT
   data := json.decode(json_string)
   
   // WRONG (will fail)
   data := json.unmarshal(json_string)
   ```

3. **Validate input before processing**:
   ```tengo
   if !input_param {
     return "error: input required"
   }
   ```

4. **Use type-agnostic functions for robustness**:
   ```tengo
   // Check if value exists
   if !value {
     return "not found"
   }
   
   // Use is_* functions
   if is_array(data) {
     // process array
   }
   ```

5. **Format output clearly**:
   ```tengo
   fmt := import("fmt")
   return fmt.sprintf("Result: %d items processed", count)
   ```

## References

- **Tengo GitHub**: https://github.com/d5/tengo
- **Tengo Stdlib Docs**: https://github.com/d5/tengo/blob/master/docs/stdlib.md
- **Tengo Language Tutorial**: https://github.com/d5/tengo/blob/master/docs/tutorial.md
- **Tengo Playground**: https://tengolang.com

## Version History

- **2026-09-16**: Fixed json module issue - documented correct function names (decode/encode)
- **2026-09-16**: Completed import extraction system for module-scope constraints
- **2026-09-16**: All 15 test scripts passing, production-ready
