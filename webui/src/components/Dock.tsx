import { useState } from "react";
import { CopilotChat, CopilotKit } from "@copilotkit/react-core/v2";
import { GenUIHost } from "./GenUI";
import Library from "./Library";
import Notifications from "./Notifications";
import { useResults } from "../state/results";

const AGENT = "default";

type Tab = "library" | "chat" | "notifications";

// Dock is the secondary panel: it offers the session Library (dashboards and
// scripts you can launch onto the board), the MCP notifications feed, and,
// behind the "Ask AI" tab, the agent chat. The board — not the chat — is the
// primary surface of the UI.
export default function Dock({ token }: { token: string }) {
  const [tab, setTab] = useState<Tab>("library");
  const { notifications } = useResults();

  return (
    <aside className="dock">
      <nav className="dock-tabs">
        <button
          className={tab === "library" ? "active" : ""}
          onClick={() => setTab("library")}
        >
          Library
        </button>
        <button
          className={tab === "notifications" ? "active" : ""}
          onClick={() => setTab("notifications")}
        >
          Notifications
          {notifications.length > 0 && (
            <span className="dock-badge">
              {notifications.length > 99 ? "99+" : notifications.length}
            </span>
          )}
        </button>
        <button
          className={tab === "chat" ? "active" : ""}
          onClick={() => setTab("chat")}
        >
          Ask AI
        </button>
      </nav>

      <div className={"dock-panel" + (tab === "library" ? "" : " hidden")}>
        <Library token={token} />
      </div>

      <div className={"dock-panel" + (tab === "notifications" ? "" : " hidden")}>
        <Notifications />
      </div>

      <div className={"dock-panel" + (tab === "chat" ? "" : " hidden")}>
        <CopilotKit
          agentId={AGENT}
          runtimeUrl="/ui/copilotkit"
          headers={{ "X-Ui-Session": token }}
        >
          <GenUIHost />
          <div className="chat-frame">
            <CopilotChat className="chat-root" />
          </div>
        </CopilotKit>
      </div>
    </aside>
  );
}