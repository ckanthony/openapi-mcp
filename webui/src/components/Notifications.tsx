import { useState } from "react";
import { useResults } from "../state/results";

const FEED_LIMIT = 100;

// Notifications renders the MCP feedback feed: status messages, confirmations
// and errors that are not data results. The workbench is reserved for results.
export default function Notifications() {
  const { notifications } = useResults();
  const [limit, setLimit] = useState(FEED_LIMIT);
  const shown = notifications.slice(-limit);

  return (
    <div className="notif-list">
      {shown.length === 0 ? (
        <div className="notif-empty">No notifications yet. MCP feedback lands here.</div>
      ) : (
        <>
          {shown.map((v, i) => (
            <div key={i} className={`notif-item${v.error ? " error" : ""}`}>
              <div className="notif-head">
                <span className="notif-tool" title={v.tool}>
                  {v.tool ?? "mcp"}
                </span>
                <span className="notif-kind">{v.error ? "error" : v.kind ?? "feedback"}</span>
              </div>
              {v.text && <div className="notif-text">{v.text}</div>}
            </div>
          ))}
          {notifications.length > shown.length && (
            <button
              className="btn"
              onClick={() => setLimit((l) => l + FEED_LIMIT)}
            >
              show {notifications.length - shown.length} older
            </button>
          )}
        </>
      )}
    </div>
  );
}