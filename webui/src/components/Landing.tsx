import { useEffect, useState } from "react";
import { fetchManifest, type Manifest } from "../lib/session";

// Landing shows the session-aware registry context (APIs, scripts, tool count)
// fetched from /ui/manifest, giving the shell a reason to exist beyond chat.
export default function Landing({ token }: { token: string }) {
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    fetchManifest(token)
      .then((m) => alive && setManifest(m))
      .catch((e: unknown) => alive && setError(String(e)));
    return () => {
      alive = false;
    };
  }, [token]);

  return (
    <header className="landing">
      <div className="landing-row">
        <span className="brand">openapi-mcp</span>
        <span className="session">
          session <code>{manifest?.session.slice(0, 8) ?? "…"}</code>
        </span>
      </div>
      {error ? (
        <div className="landing-error">{error}</div>
      ) : !manifest ? (
        <div className="landing-loading">loading registry context…</div>
      ) : (
        <div className="landing-stats">
          <span>{manifest.apis.length} APIs</span>
          <span>{manifest.tools.length} tools</span>
          <span>{manifest.scripts.length} scripts</span>
          <span>
            knowledge {manifest.meta.knowledge?.enabled ? "on" : "off"}
          </span>
        </div>
      )}
    </header>
  );
}