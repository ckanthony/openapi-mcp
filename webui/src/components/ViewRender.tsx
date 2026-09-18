import type { ViewPayload } from "../lib/session";

// ViewRender renders a server-produced view payload: a table or key/value list
// projected from a JSON tool result, an explicit `view` tool render payload
// (rows/lists), or a markdown fallback. Unknown shapes degrade to a <pre>.

function pretty(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

export function viewLayout(view: ViewPayload): string {
  const inner = view.view && typeof view.view === "object" ? view.view : undefined;
  return (inner?.layout as string | undefined) ?? view.layout ?? "markdown";
}

export function viewColumns(view: ViewPayload): string[] {
  const inner = view.view && typeof view.view === "object" ? view.view : undefined;
  return ((inner?.columns as string[] | undefined) ?? view.columns) ?? [];
}

export function viewRows(view: ViewPayload): Record<string, unknown>[] {
  const inner = view.view && typeof view.view === "object" ? view.view : undefined;
  return ((inner?.rows as Record<string, unknown>[] | undefined) ?? view.rows) ?? [];
}

export function ViewRender({ view }: { view: ViewPayload }) {
  const layout = viewLayout(view);
  const columns = viewColumns(view);
  const rows = viewRows(view);
  const inner =
    view.view && typeof view.view === "object"
      ? JSON.stringify(view.view, null, 2)
      : "";
  const title = `${view.tool ?? "result"}${view.error ? " · error" : ""}`;

  return (
    <div className={`view view-${layout}${view.error ? " view-error" : ""}`}>
      <div className="view-title">{title}</div>
      {layout === "table" && columns.length > 0 ? (
        <div className="view-table-scroll">
          <table className="view-table">
            <thead>
              <tr>
                {columns.map((c) => (
                  <th key={c}>{c}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i}>
                  {columns.map((c) => (
                    <td key={c}>{pretty(row[c])}</td>
                  ))}
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={columns.length}>no rows</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      ) : layout === "rows" ? (
        <div className="view-cards">
          {rows.map((row, i) => (
            <div className="view-card" key={i}>
              {(columns.length ? columns : Object.keys(row)).map((c) => (
                <div className="view-card-field" key={c}>
                  <span>{c}</span>
                  <strong>{pretty(row[c])}</strong>
                </div>
              ))}
            </div>
          ))}
        </div>
      ) : (
        <pre className="view-pre">{inner || view.text}</pre>
      )}
    </div>
  );
}