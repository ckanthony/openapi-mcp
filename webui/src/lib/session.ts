// Browser session plumbing: per-tab session token (server "X-Ui-Session"
// header) and the /ui/manifest + /ui/chat bridge clients that do not go
// through CopilotKit.

export const SESSION_KEY = "openapi-mcp.session";
export const SESSION_HEADER = "X-Ui-Session";

// sessionIdFromURL reads the session-specific URL the agent hands to the user
// (/ui?sessionId=<mcp session>). When present, the whole tab binds to that MCP
// session as an observer: the board mirrors the agent session's stream and the
// chat drives the same registry session.
export function sessionIdFromURL(): string | null {
  return new URLSearchParams(window.location.search).get("sessionId");
}

export function getSessionToken(): string {
  const bound = sessionIdFromURL();
  try {
    const existing = window.sessionStorage.getItem(SESSION_KEY);
    if (bound && existing !== bound) {
      window.sessionStorage.setItem(SESSION_KEY, bound);
      return bound;
    }
    if (existing) return existing;
    const tok = crypto.randomUUID();
    window.sessionStorage.setItem(SESSION_KEY, tok);
    return tok;
  } catch {
    return bound ?? crypto.randomUUID();
  }
}

export interface ManifestApi {
  name: string;
  targets: string[];
  active: string;
  tools: number;
  exposed: number;
  mode: string;
  session_override: boolean;
  knowledge?: { enabled?: boolean; language?: string };
}

export interface ViewPayload {
  tool?: string;
  kind?: string;
  error?: boolean;
  text?: string;
  result?: boolean; // true: data gathered by a tool/task call; false: MCP feedback
  status_code?: number;
  layout?: string;
  columns?: string[];
  rows?: Record<string, unknown>[];
  count?: number;
  view?: Record<string, unknown>;
  args?: Record<string, unknown>; // original tool arguments, for refresh
  stale?: boolean; // replayed from the session's ephemeral cache, refreshable
  _seq?: number; // client-assigned monotonically increasing id, for stable identity
}

export interface ManifestView {
  id: string;
  api: string;
  kind: string; // "view" | "dashboard"
  title: string;
  summary?: string;
  layout?: string;
  auto_show?: boolean;
}

export interface Manifest {
  session: string;
  apis: ManifestApi[];
  meta: { knowledge?: { enabled?: boolean } };
  views: ManifestView[];
  scripts: { name: string; api: string; summary: string; exposed: boolean }[];
  tools: string[];
}

export async function fetchManifest(token: string): Promise<Manifest> {
  const res = await fetch("/ui/manifest", { headers: { [SESSION_HEADER]: token } });
  if (!res.ok) throw new Error(`manifest ${res.status}`);
  return (await res.json()) as Manifest;
}

export interface BridgeResult {
  text?: string;
  view?: ViewPayload;
  ok?: boolean;
  error?: string;
}

export async function postBridge(
  token: string,
  tool: string,
  args: Record<string, unknown>,
): Promise<BridgeResult> {
  const res = await fetch("/ui/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json", [SESSION_HEADER]: token },
    body: JSON.stringify({ tool, arguments: args }),
  });
  const j = (await res.json().catch(() => ({}))) as BridgeResult;
  return j;
}