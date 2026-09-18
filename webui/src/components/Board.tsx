import { useState } from "react";
import { useResults } from "../state/results";
import { postBridge, type ViewPayload } from "../lib/session";
import { ViewRender } from "./ViewRender";

// Board is the primary surface of the UI, split into two tabs:
//
//   Dashboard  — the main view UI the user composes: results the user pinned
//                to the board stay here as their working dashboard.
//   Results    — the accumulating feed of every result produced during the
//                session that has NOT been pinned to the dashboard. Cards here
//                can be pinned to the Dashboard with the corner button.
//
// Cards that were replayed from the session's ephemeral cache wear a "stale"
// marker: they are a snapshot that may be refreshed. When the card is a
// render from the view tool the refresh re-runs it on the shared session and
// the updated result is broadcast back to both the agent and this board.
// firstRunOf identifies how a stale card can be refreshed: either it is a
// render from the view tool (carries api + view_id) or the backing call can be
// re-run directly (carries the tool name). Returns null when neither applies.
function firstRunOf(v: ViewPayload): { viewId?: string; api?: string; tool?: string } | null {
  const render = v.view as Record<string, unknown> | undefined;
  const viewId = typeof render?.view_id === "string" ? render.view_id : "";
  const api = typeof render?.api === "string" ? render.api : "";
  const tool = typeof v.tool === "string" && v.tool !== "view" ? v.tool : "";
  if (viewId) return { viewId, api };
  if (tool) return { tool };
  return null;
}

type Tab = "results" | "dashboard";

export default function Board({ token }: { token: string }) {
  const { unpinned, dashboard, clear, pin, unpin, clearPins } = useResults();
  const [tab, setTab] = useState<Tab>("results");
  const [busy, setBusy] = useState<string | null>(null);

  const items = tab === "dashboard" ? dashboard : unpinned;

  async function refresh(v: ViewPayload, key: string) {
    const first = firstRunOf(v);
    if (!first) return;
    setBusy(key);
    try {
      if (first.viewId) {
        await postBridge(token, "view", { api: first.api ?? "", view_id: first.viewId });
      } else if (first.tool) {
        // Fresh data = a direct re-run of the backing call on the shared
        // session; the result broadcasts back to the agent and this board.
        await postBridge(token, first.tool, v.args ?? {});
      }
    } catch {
      // the board updates when the updated view arrives
    } finally {
      setBusy(null);
    }
  }

  return (
    <section className="board">
      <div className="board-head">
        <div className="board-tabs">
          <button
            className={tab === "results" ? "active" : ""}
            onClick={() => setTab("results")}
          >
            Results
            {unpinned.length > 0 && <span className="board-tab-count">{unpinned.length}</span>}
          </button>
          <button
            className={tab === "dashboard" ? "active" : ""}
            onClick={() => setTab("dashboard")}
          >
            Dashboard
            {dashboard.length > 0 && <span className="board-tab-count">{dashboard.length}</span>}
          </button>
        </div>
        <div className="board-head-actions">
          {tab === "dashboard" && dashboard.length > 0 && (
            <button onClick={clearPins} title="Unpin everything from the dashboard">
              clear pins
            </button>
          )}
          <button onClick={clear} title="Clear all results and notifications">
            clear
          </button>
        </div>
      </div>
      <div className="board-grid">
        {items.length === 0 && tab === "dashboard" && (
          <div className="board-empty">
            <p>
              Your dashboard is empty. This is where the views you pin from the{" "}
              <b>Results</b> tab live — the main UI you compose from the session.
            </p>
            <p>
              Ask the agent for data, then hit the pin button on a result card to
              keep it here.
            </p>
          </div>
        )}
        {items.length === 0 && tab === "results" && (
          <div className="board-empty">
            <p>
              This session&rsquo;s results are empty — everything the agent
              produces (dashboards, task results, data tables) shows up here
              live.
            </p>
            <p>
              Pick a dashboard in the <b>Library</b>, describe what you need in
              the <b>Ask AI</b> panel, or call a tool directly. Pin what matters
              and it stays on your <b>Dashboard</b>.
            </p>
          </div>
        )}
        {items.map((v) => {
          const seq = v._seq ?? -1;
          const key = `${v.tool ?? "view"}-${seq}`;
          const pinnedHere = v._seq != null && dashboard.some((d) => d._seq === seq);
          return (
            <article className="board-card" key={key}>
              <button
                className="card-pin"
                onClick={() => (pinnedHere ? unpin(seq) : pin(seq))}
                title={pinnedHere ? "Unpin from Dashboard" : "Pin to the Dashboard"}
              >
                {pinnedHere ? "⛌" : "⌖"}
              </button>
              {v.stale && (
                <div className="card-stale">
                  <span title="This card is a cached snapshot of the session; the agent may have produced new data since.">
                    cached data
                  </span>
                  <button
                    disabled={busy === key || !firstRunOf(v)}
                    onClick={() => refresh(v, key)}
                    title={
                      firstRunOf(v)
                        ? "Re-run the call on the session to refresh"
                        : "This card cannot be refreshed"
                    }
                  >
                    {busy === key ? "refreshing…" : "refresh"}
                  </button>
                </div>
              )}
              <ViewRender view={v} />
            </article>
          );
        })}
      </div>
    </section>
  );
}