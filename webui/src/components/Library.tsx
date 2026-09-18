import { useEffect, useState } from "react";
import { fetchManifest, postBridge, type Manifest, type ManifestView } from "../lib/session";

// Library is the "interaction" half of the display: the dashboards/views the
// session can render and the registered scripts. Launching one re-issues the
// backing call through /ui/chat; the resulting view payload lands on the board
// via /ui/events — AI-assisted, but driven from the display rather than chat.
export default function Library({ token }: { token: string }) {
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    fetchManifest(token)
      .then((m) => alive && setManifest(m))
      .catch((e: unknown) => alive && setError(String(e)));
    return () => {
      alive = false;
    };
  }, [token]);

  const run = async (tool: string, args: Record<string, unknown>, key: string) => {
    setBusy(key);
    setNotice(null);
    try {
      const res = await postBridge(token, tool, args);
      if (!res.ok && res.error) {
        setNotice(`run failed: ${res.error}`);
      }
    } catch (e: unknown) {
      setNotice(`run failed: ${String(e)}`);
    } finally {
      setBusy(null);
    }
  };

  if (error) return <div className="lib-error">{error}</div>;
  if (!manifest) return <div className="lib-loading">loading library…</div>;

  const dashboards = manifest.views.filter((v) => v.kind === "dashboard");
  const views = manifest.views.filter((v) => v.kind !== "dashboard");
  const scripts = manifest.scripts.filter((s) => s.exposed);

  return (
    <div className="lib">
      {notice && <div className="lib-notice">{notice}</div>}

      <div className="lib-group">
        <h4>Dashboards</h4>
        {dashboards.length === 0 && (
          <div className="lib-none">no dashboards in the knowledge base</div>
        )}
        {dashboards.map((v) => (
          <DashboardTile
            key={v.api + "/" + v.id}
            view={v}
            busy={busy === v.api + "/" + v.id}
            onRun={() =>
              run("view", { api: v.api, view_id: v.id }, v.api + "/" + v.id)
            }
          />
        ))}
      </div>

      <div className="lib-group">
        <h4>Views</h4>
        {views.length === 0 && (
          <div className="lib-none">no single views defined</div>
        )}
        {views.map((v) => (
          <DashboardTile
            key={v.api + "/" + v.id}
            view={v}
            busy={busy === v.api + "/" + v.id}
            onRun={() =>
              run("view", { api: v.api, view_id: v.id }, v.api + "/" + v.id)
            }
          />
        ))}
      </div>

      <div className="lib-group">
        <h4>Scripts</h4>
        {scripts.length === 0 && (
          <div className="lib-none">no exposed scripts</div>
        )}
        {scripts.map((s) => (
          <div className="lib-row" key={s.api + "/" + s.name}>
            <div className="lib-row-main">
              <strong>{s.name}</strong>
              <span>{s.summary || s.api}</span>
            </div>
            <button
              disabled={busy === s.name}
              onClick={() => run(s.name, {}, s.name)}
            >
              {busy === s.name ? "…" : "run"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

function DashboardTile({
  view,
  busy,
  onRun,
}: {
  view: ManifestView;
  busy: boolean;
  onRun: () => void;
}) {
  return (
    <div className="lib-row">
      <div className="lib-row-main">
        <strong>{view.title}</strong>
        <span>
          {view.id} · {view.api}
          {view.layout ? ` · ${view.layout}` : ""}
        </span>
      </div>
      <button disabled={busy} onClick={onRun}>
        {busy ? "…" : "run"}
      </button>
    </div>
  );
}