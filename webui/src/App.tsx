import { useState } from "react";
import Board from "./components/Board";
import Dock from "./components/Dock";
import Landing from "./components/Landing";
import { ResultsProvider } from "./state/results";
import { getSessionToken, sessionIdFromURL } from "./lib/session";

// App lays out the result-centric UI: the Workbench board is the stage where
// every session result (dashboards, tables, task outputs) is displayed, and
// the Dock hosts the session Library plus the "Ask AI" chat as an option — not
// as the primary surface.
//
// When the page was opened at /ui?sessionId=<id>, the whole tab binds to that
// MCP agent session: the board mirrors it live and the chat drives it.
export default function App() {
  const [token] = useState(() => getSessionToken());
  const bound = sessionIdFromURL();

  return (
    <div className="app">
      <Landing token={token} />
      <ResultsProvider token={token}>
        {bound && (
          <div className="session-banner">
            <span title="This tab is bound to the MCP agent session and mirrors it live.">
              mirroring session
            </span>
            <code>{bound}</code>
          </div>
        )}
        <div className="main">
          <Board token={token} />
          <Dock token={token} />
        </div>
      </ResultsProvider>
    </div>
  );
}