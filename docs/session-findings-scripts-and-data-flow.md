# Session Findings: Scripts Exposure, Data Flow, and Ephemeral Results System

**Date:** 2026-09-16  
**Status:** Analysis and findings; implementation pending  
**Scope:** Script invocation workflows, large-data parameter passing, token efficiency  

## 1. Current State (Session Verification)

### 1.1 What Works

All 6 major features are functional and testable through MCP:

- ✅ **API Registration & Introspection** (311/59/136 operations across 3 test APIs)
- ✅ **API Exposure** (dynamic tool activation via `update_session_api_exposure`)
- ✅ **Knowledge Base** (141 documents per API, searchable, indexed)
- ✅ **Meta Knowledge Base** (`_meta` system active with capabilities)
- ✅ **Scripting Core** (Tengo scripts registered, `script_describe` works)
- ✅ **Web UI Shell** (manifest, chat bridge, session isolation)

### 1.2 The Gap: Scripts Are Not Directly Callable as MCP Tools

**Discovery:** Created Tengo script `format_user_list_data` in the finn knowledge base, registered successfully, but **cannot invoke it directly through MCP tools**.

**Current state:**
- ✅ Script is stored as `scripts/format_user_list_data.tengo` in knowledge base
- ✅ Script metadata accessible via `script_describe` management tool
- ✅ Script appears in `script_list` output
- ✅ Script marked `exposed: false` (not exposed as MCP tool)
- ❌ **No direct MCP tool to invoke scripts** (no `finn__format_user_list_data` tool)
- ❌ `run_task` only invokes **capabilities** (multi-step tasks), not raw scripts
- ❌ Scripts cannot be called directly by agents or other tools without embedding in a capability

**Root cause:** Phase 3 (Scripting core) registers scripts as knowledge docs and validates them, but **the script→tool exposure bridge is incomplete**. The `scriptRef` and registry plumbing exist (see `pkg/server/scripts_test.go`), but scripts are not surfaced in `tools/list`.

---

## 2. Workflow Limitations Discovered

### 2.1 The Token-Heavy Data Flow Problem

**Current workflow for reusable data formatting:**

```
┌─────────────────────────────────────────────────────────────────┐
│ AI Agent Session                                                │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. Agent calls: finn__get_all_users_users_get                 │
│     ↓                                                           │
│  2. API returns: 20 users × ~500 bytes each = ~10 KB JSON     │
│     ↓                                                           │
│  3. **Result travels in MCP response** → AI context            │ ← TOKEN COST 1
│     ↓                                                           │
│  4. Agent thinks: "Format this with script"                   │
│     ↓                                                           │
│  5. Agent must construct tool call:                           │
│     "users_json": "<entire JSON string in params>"            │
│     ↓                                                           │
│  6. **Full JSON travels in MCP request** → Server              │ ← TOKEN COST 2
│     ↓                                                           │
│  7. Script receives and parses JSON                           │
│     ↓                                                           │
│  8. Script output travels back → AI context                    │ ← TOKEN COST 3
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**Token cost:** The same data (10 KB user list) travels 3+ times through the AI context:
- Response payload (step 2→3)
- Request parameter (step 5→6)  
- Response result (step 7→8)

**Size limitations:**
- OpenAI/Claude message limits (~200 KB practical for structured data)
- Agent context window consumed by repeated data
- Cannot efficiently chain multiple transformations

### 2.2 Example: Why This Matters

Tested workflow with 20 users:
1. `finn__get_all_users_users_get(limit=20)` → 10 KB response
2. Would need to pass full JSON to `finn__format_user_list_data` as a string parameter
3. Script would parse, compute statistics, return formatted report
4. If next step needs to filter/transform further → repeat with another script or tool

Each step forces the entire dataset through the AI context.

---

## 3. Proposed Solution: Ephemeral Results System

### 3.1 Design Overview

**Goal:** Decouple data from the AI context. Pass **handles** (UUIDs) instead of raw data, store data temporarily on the server.

```
┌──────────────────────────────────────────────────────────────────┐
│ With Ephemeral Results System                                    │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Agent calls: finn__get_all_users_users_get                  │
│     ↓                                                            │
│  2. API returns: 20 users × ~500 bytes = ~10 KB JSON           │
│     ↓                                                            │
│  3. Server stores in /tmp/mcp-results/<uuid>.json              │
│     ↓                                                            │
│  4. **MCP response: {result_id: "uuid-abc123"}** → AI context   │ ← NO TOKEN COST
│     ↓                                                            │
│  5. Agent calls: finn__format_user_list_data                   │
│     Param: "$ref:uuid-abc123"                                  │
│     ↓                                                            │
│  6. Server resolves $ref → loads from disk                     │
│     ↓                                                            │
│  7. Script receives parsed data (not JSON string)              │
│     ↓                                                            │
│  8. **MCP response: formatted report** (smaller, summaries only) │ ← REDUCED TOKEN
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

**Key benefits:**
- AI context only receives **handles** (UUID + metadata)
- Large datasets never travel through the AI prompt
- Chaining multiple transformations costs zero tokens per intermediate step
- Tools/scripts receive real data structures, not JSON strings

### 3.2 API Contract

#### 3.2.1 Tool Output Format

Tools have optional new parameter to control output format:

```yaml
# Config option (per API, optional)
server:
  results:
    enabled: true
    format: handle  # raw | handle (default: raw for backward compat)
    ttl_seconds: 3600
    temp_dir: /tmp/mcp-results
```

#### 3.2.2 Tool Response Structure

**Before (raw data):**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "type": "tool_result",
    "content": [
      {
        "type": "text",
        "text": "{\"users\": [{...large json...}]}"
      }
    ]
  }
}
```

**After (with results system enabled):**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "type": "tool_result",
    "content": [
      {
        "type": "text",
        "text": "20 users retrieved"
      },
      {
        "type": "resource",
        "resource": {
          "handle": "result:f47ac10b-58cc-4372-a567-0e02b2c3d479",
          "mime_type": "application/json",
          "size_bytes": 10240,
          "ttl_seconds": 3600,
          "expires_at": "2026-09-16T14:35:22Z"
        }
      }
    ]
  }
}
```

#### 3.2.3 Tool Parameter Resolution

Tools accept both raw data and result references:

```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "finn__format_user_list_data",
    "arguments": {
      "users_json": "$ref:f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "show_first_n": 5
    }
  }
}
```

The MCP server recognizes `$ref:` prefix and:
1. Loads data from `/tmp/mcp-results/<uuid>.json`
2. Parses JSON
3. Passes parsed data to the tool/script
4. Tool receives actual data structure (not JSON string)

#### 3.2.4 Backward Compatibility

- ✅ **Enabled by default: `false`** — existing tools/clients see no change
- ✅ **Optional output format** — when enabled, tools can choose to return handles
- ✅ **Optional input resolution** — tools can accept `$ref:` in string parameters
- ✅ **Schema advertised** — introspection tools report if results system is active

### 3.3 File Layout

```
/tmp/mcp-results/
├── f47ac10b-58cc-4372-a567-0e02b2c3d479.json     (data)
├── f47ac10b-58cc-4372-a567-0e02b2c3d479.meta     (ttl, created, api, tool)
├── [cleaned up after ttl expires]
└── [max 1 GB total by default, configurable]
```

### 3.4 Lifecycle

1. **Creation:** When a tool returns data and `results.format: handle` is set
   - Data written to UUID-named file
   - Metadata file created with TTL and source tool name
   - Handle returned in response

2. **Resolution:** When a tool receives `$ref:` parameter
   - Server intercepts parameter
   - Looks up UUID file
   - Validates TTL (not expired)
   - Parses and injects into tool call

3. **Cleanup:** Background goroutine
   - Scans results directory every 60s
   - Deletes expired files
   - Reports disk usage stats

4. **Expiry:** Default 1 hour after creation, configurable per API

---

## 4. Implementation Plan

### 4.1 Phase 1: Expose Scripts as MCP Tools (Immediate)

**Goal:** Make scripts immediately invokable through MCP.

**Changes:**
1. `pkg/server/registry.go`:
   - Ensure scripts are included in `tools/list` output
   - Verify `toolFullName` generation for scripts
   - Expose scripts for testing

2. `pkg/server/management.go`:
   - Add to the exposed tools check

3. Tests:
   - `pkg/server/scripts_test.go`: script tool in `tools/list`
   - Script callable with real parameters
   - Result properly serialized

**Effort:** 2-4 hours  
**Blocker:** None — code already exists, just needs exposure

### 4.2 Phase 2: Ephemeral Results System (Primary Implementation)

**Changes:**

1. **Configuration** (`pkg/config`):
   - Add `ResultsConfig` to `ServerConfig`
   - Fields: `enabled`, `format` (raw/handle), `ttl_seconds`, `temp_dir`, `max_size_bytes`
   - Validation and defaults

2. **Results Storage** (`pkg/results/results.go` — new package):
   - `Store` interface with methods:
     - `Create(data []byte, source ToolRef) (id string, err error)`
     - `Get(id string) (data []byte, expired bool, err error)`
     - `Cleanup()` (background goroutine)
   - Implementations:
     - `FileStore`: writes to `temp_dir`, tracks TTL in `.meta` files
     - `MemoryStore`: in-process for testing

3. **Parameter Resolution** (`pkg/server/dispatcher.go`):
   - Before dispatching tool call, scan parameters for `$ref:` patterns
   - Resolve references to actual data using `results.Store`
   - Inject parsed data into tool arguments
   - Log resolution for debugging

4. **Response Wrapping** (`pkg/server/views.go`):
   - Tool results converted to handles when `results.format: handle`
   - Metadata attached (size, TTL, created_at)
   - Fallback to raw data if results store is disabled or fails

5. **Introspection** (`pkg/server/management.go`):
   - `system_status` tool gains `results` block showing enabled/active/disk usage
   - `api_exposure` notes whether API has results enabled

6. **Tests**:
   - `pkg/results/store_test.go`: Create, Get, TTL expiry, concurrent cleanup
   - `pkg/server/results_integration_test.go`: End-to-end parameter resolution
   - Existing tool tests continue to work (backward compat)

**Effort:** 6-10 hours  
**Risk:** File I/O in hot path — mitigation: disk cache, in-memory LRU

### 4.3 Phase 3: UI and UX (Secondary Polish)

**Changes:**
1. Web UI displays handles with "cached data" badge
2. Refresh button re-runs backing call
3. Results dashboard shows active handles, sizes, TTL

**Effort:** 3-5 hours (optional for now)

---

## 5. Interplay with Scripts and Capabilities

### 5.1 When Scripts Have Access to Results

Once Phase 1 (expose scripts) + Phase 2 (results system) are done:

```
┌─ API Tool Result (handle) ──────┐
│  $ref:uuid-123                  │
└─ Parameter to Script ────────────┘
                     ↓
            ┌─ Script Receives ┐
            │ Parsed JSON data │
            │ (not JSON string)│
            └──────────────────┘
                     ↓
            Script chains to next API call
            or transforms data
                     ↓
            ┌─ Script Result ──┐
            │ Handle (if large)│
            │ or raw (if small)│
            └──────────────────┘
```

**Example: Multi-step workflow**
```tengo
// format_user_data.tengo
// Step 1: Receive API result as handle
users_data := param("users_json", "[]")  // $ref:uuid resolved server-side

// Step 2: Parse (already parsed by server resolution)
users := json.unmarshal(users_data)

// Step 3: Transform
filtered := [user for user in users if user.IsActive]

// Step 4: Chain to another tool
supervisor_roles := mcp.call("auth__GetRoleAssignments", {
  user_ids: [user.Id for user in filtered]
})  // Returns another handle

// Step 5: Return
return {
  formatted_users: len(filtered),
  roles_pending: supervisor_roles  // Handle passed through
}
```

### 5.2 Scripts Can Also Return Handles

If a script result is large (>1 KB):
- Server can automatically store output as handle
- Return handle + summary in response
- Agent doesn't need the raw data in context

---

## 6. Edge Cases and Design Decisions

### 6.1 Backward Compatibility

- ✅ **Disabled by default** — existing deployments see no change
- ✅ **Opt-in** — operators enable with `server.results.enabled: true`
- ✅ **Graceful fallback** — if `$ref:` resolution fails, tool gets error (not silent pass-through)

### 6.2 Security

- ✅ **No remote access** — handles are UUIDs only visible in responses, not guessable
- ✅ **TTL enforcement** — expired handles rejected immediately
- ✅ **Operator control** — `temp_dir` and `max_size_bytes` limit disk impact
- ✅ **Per-session isolation** — results are global but only the calling session can access

### 6.3 Observability

- `result_created` events on MCP stream (optional)
- Metrics: count, sizes, cleanup frequency
- Logs: resolution failures, TTL expirations, disk errors

### 6.4 Tool Parameter Typing

**Question:** Should Tengo script parameters change?

**Answer:** No. Parameters remain typed in front-matter. Server resolution happens before script invocation:
- Parameter declared as `string` → receives raw string
- **If** that string starts with `$ref:` → server resolves to actual data
- Script receives **parsed data**, not JSON string
- Script doesn't need to know about the resolution mechanism

Example:
```yaml
params:
  - name: users_json
    type: string
    description: "User list (can be raw JSON or $ref:uuid)"
```

Script code:
```tengo
users := json.unmarshal(param("users_json", "[]"))
// Server pre-resolves $ref, so this is the parsed data already
```

---

## 7. Related Work and Standards

- **MCP Resource Protocol**: Similar concept (named resources + URIs)
- **JSON-LD Dereferencing**: `@id` and `@context`
- **HTTP Range Requests**: Pagination model could be adapted for large result sets
- **OpenAPI Links Object**: Parameter binding pattern similar to `$ref`

---

## 8. Success Criteria

### Phase 1 (Scripts Exposure)
- [ ] `tools/list` includes at least one script tool
- [ ] Script callable with parameters
- [ ] Result serialized correctly
- [ ] Integration test passes

### Phase 2 (Ephemeral Results)
- [ ] Result creation + storage works
- [ ] Parameter resolution works
- [ ] TTL cleanup runs without errors
- [ ] Large data (>1 MB) efficiently handled
- [ ] Two tools chaining via handles produces correct output
- [ ] Backward compat: no `$ref:` still works
- [ ] Disk space limits enforced

### Phase 3 (UI Polish)
- [ ] Handles displayed with metadata badges
- [ ] Refresh action works
- [ ] Results dashboard shows statistics

---

## 9. Files Affected (Approximate)

**New:**
- `pkg/results/results.go` (350 lines)
- `pkg/results/store_test.go` (300 lines)
- `docs/session-findings-scripts-and-data-flow.md` (this file)

**Modified:**
- `pkg/config/config.go` (+30 lines for ResultsConfig)
- `pkg/server/registry.go` (+10 for script exposure)
- `pkg/server/server.go` (+50 for parameter resolution)
- `pkg/server/dispatcher.go` (+40 for $ref handling)
- `pkg/server/views.go` (+30 for handle wrapping)
- `pkg/server/management.go` (+20 for introspection)
- `docs/feature-spec-meta-scripting-ui.md` (update Phase 6 notes)

**Tests:**
- `pkg/server/results_integration_test.go` (300 lines)
- `pkg/server/scripts_test.go` (+50 lines for exposure)

---

## 10. Example: Full End-to-End Flow

### Scenario: Format user list with statistics and export

**Config:**
```yaml
server:
  results:
    enabled: true
    format: handle
    ttl_seconds: 3600
    temp_dir: /tmp/mcp-results
```

**Agent interaction:**

```
Agent: "Show me the first 5 and last 2 users, with activity stats"

1. tools/call: finn__get_all_users_users_get
   args: {paginationLimit: 20, paginationOffset: 0}
   
   Response:
   {
     "content": [
       {"type": "text", "text": "Retrieved 20 users"},
       {"type": "resource", 
        "resource": {
          "handle": "result:f47ac10b-58cc-4372-a567-0e02b2c3d479",
          "size_bytes": 10240,
          "expires_at": "2026-09-16T14:35:22Z"
        }}
     ]
   }
   
2. tools/call: finn__format_user_list_data
   args: {
     users_json: "$ref:f47ac10b-58cc-4372-a567-0e02b2c3d479",
     show_first_n: 5,
     show_last_n: 2
   }
   
   Server resolution:
   - Sees "$ref:..." → loads /tmp/mcp-results/f47ac10b-58cc-4372-a567-0e02b2c3d479.json
   - Parses 10 KB JSON
   - Passes parsed array to script
   - Script executes with real data (not JSON string)
   
   Script result: 2 KB formatted report + statistics
   
   Response:
   {
     "content": [
       {"type": "text", "text": "=== USER LIST SUMMARY ===\nTotal: 20\nActive: 18...[formatted report]"}
     ]
   }

Agent: Perfect! This is the dashboard I want for future sessions.

3. tools/call: knowledge_upsert
   args: {
     api: "finn",
     content: "---\nid: user_activity_dashboard\nkind: dashboard\nsource: finn__get_all_users_users_get\n---\n[rendered dashboard definition]"
   }
   
   → Dashboard saved and reusable

4. Agent calls: view
   args: {
     api: "finn",
     view_id: "user_activity_dashboard"
   }
   
   Response: Rendered dashboard displayed in web UI
```

---

## 11. Next Steps

1. **Documentation review** — Present this to maintainers
2. **Phase 1 implementation** — Expose scripts (quick win)
3. **Phase 2 implementation** — Ephemeral results system (main work)
4. **Integration testing** — Verify multi-step workflows
5. **Update feature plan** — Note as Phase 6+ continuation

---

## Appendix: Why This Matters for AI Agents

**Token efficiency:** Reduces AI prompt bloat by 50-80% for data-heavy operations.

**Example savings (100 users, 500 bytes each = 50 KB):**

| Operation | Without Results | With Results | Savings |
|-----------|-----------------|--------------|---------|
| Fetch users | 50 KB in context | "result:uuid" in context | 49.9 KB |
| Pass to script | 50 KB param + 50 KB response | Handle resolution | 100 KB |
| Chain to 2nd tool | 50 KB in context + 50 KB param | Handle → Handle | 100 KB |
| **Total for 3 tools** | **150 KB** | **~500 bytes handles** | **99.7% reduction** |

**Practical impact:**
- Agents can work with 10x larger datasets
- Complex multi-step workflows fit in smaller context windows
- Cost reduction for prompt-based APIs (OpenAI, Claude, Anthropic)

