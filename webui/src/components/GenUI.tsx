import { useComponent } from "@copilotkit/react-core/v2";
import { z } from "zod";

// GenUI hosts the generative-UI registrations that render inside the chat
// timeline while an agent call runs:
//  - tool calls named `view` render a small view/dashboard card
//  - tool calls named `run_task` render a compact run card (mode + task)
// The rich payloads themselves land on the workbench board (a CUSTOM "view"
// AG-UI event is emitted on every call; the board shows it via /ui/events).

const viewArgs = z.object({
  api: z.string().optional(),
  view_id: z.string().optional(),
});

const runTaskArgs = z.object({
  api: z.string().optional(),
  task: z.string().optional(),
  params: z.record(z.any()).optional(),
  target: z.string().optional(),
  mode: z.enum(["dry-run", "ask", "auto"]).optional(),
});

function ViewCard({ api, view_id }: { api?: string; view_id?: string }) {
  return (
    <div className="genui-card genui-view">
      <div className="genui-head">
        <span className="genui-tool">view</span>
        <span className="genui-tag">dashboard</span>
        {view_id ? <code>{view_id}</code> : <em>all views</em>}
        {api && <span>{api}</span>}
      </div>
      <div className="genui-note">Payload rendered in the results pane.</div>
    </div>
  );
}

function RunTaskCard({
  api,
  task,
  mode,
}: {
  api?: string;
  task?: string;
  mode?: "dry-run" | "ask" | "auto";
}) {
  return (
    <div className="genui-card genui-runtask">
      <div className="genui-head">
        <span className="genui-tool">run_task</span>
        <span className="genui-tag">{mode ?? "dry-run"}</span>
        {api && <span>{api}</span>}
      </div>
      {task ? (
        <div className="genui-note">{task}</div>
      ) : (
        <div className="genui-note">Capability task execution.</div>
      )}
    </div>
  );
}

// GenUIHost must be mounted inside <CopilotKit>. It only registers renderers.
export function GenUIHost() {
  useComponent({
    name: "view",
    description: "Renders the result of a view / dashboard tool call",
    parameters: viewArgs,
    render: ViewCard,
  });

  useComponent({
    name: "run_task",
    description: "Renders a knowledge capability task execution card",
    parameters: runTaskArgs,
    render: RunTaskCard,
  });

  return null;
}