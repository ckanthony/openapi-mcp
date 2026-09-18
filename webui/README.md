# webui — generative UI

This directory holds the React app that is built and embedded into the Go server
at `/ui`. **The built bundle (`dist/`) is a git-ignored build artifact** —
`make webui` (or `npm run build`) produces it here, and the server embeds it via
`go:embed dist`. The `make build` target depends on `webui`, so the compiled
binary always embeds the current bundle; run `make webui` once after a fresh
checkout before `go build ./...`.

## What it is

A **result-centric** React 18 + Vite 5 + TypeScript front-end: the UI is a
generative display surface for the agent session, not a chat window. Every
task, dashboard and result produced during the session lands on the Workbench
board; chat is an *option* for talking to the agent, alongside launching a
dashboard straight from the Library.

- **Workbench board** (`src/components/Board.tsx`): the central stage.
  `EventSource /ui/events` streams the session's `view` notifications and each
  becomes a card (table/list/markdown/error via `ViewRender`) on a responsive
  grid — the primary surface of the app.
- **Dock** (`src/components/Dock.tsx`): the side panel with two tabs:
  - **Library** (`src/components/Library.tsx`): the dashboards/views/scripts the
    session can produce, read from `GET /ui/manifest` (`views`). Clicking *run*
    re-issues the backing `view` tool call through `/ui/chat` and the result
    appears on the board — the "AI-assisted data and interaction display" path
    that needs no chat.
  - **Ask AI** (`src/components/GenUI.tsx` + CopilotChat): the agent chat —
    `<CopilotKit agentId="default" runtimeUrl="/ui/copilotkit"
    headers={{ "X-Ui-Session": <token> }}>` drives the server's deterministic
    AG-UI runtime (`GET /info`, `POST /agent/default/run`, `/connect`,
    `/stop/{thread}`), which streams `TEXT_MESSAGE_*` and `TOOL_CALL_*` events
    plus a `CUSTOM "view"` event per tool call. `useComponent` renderers paint
    the `view`/`run_task` calls as compact cards in the timeline; the full data
    still lands on the board.
- Landing header (`src/components/Landing.tsx`): API/tool/script counts from
  `GET /ui/manifest`.

The manifest's `views` list is built server-side from each API's merged
knowledge library (persisted + per-session overlay) plus the `_meta` base
(`Registry.uiViewEntries`).

Vite `base` is `/ui/` so embedded assets resolve under `/ui`. Dev mode
(`npm run dev`) proxies `/ui` to a local server on :8080.

## How the session token flows

Each browser tab mints an opaque session token (kept in memory +
`sessionStorage`) and sends it as `X-Ui-Session` on every request. The server
maps it to a dedicated MCP `connID`, giving every tab isolated per-session
overlays, active targets and exposure — exactly like a headless MCP client.

## Layout

- `src/main.tsx`, `src/App.tsx` — bootstrap; board-first layout (Board + Dock).
- `src/components/` — Board, Dock (Library + Ask AI tabs), GenUI cards, landing,
  library tiles, view renderer.
- `src/lib/session.ts`, `src/state/results.tsx` — token/manifest helpers
  (incl. the `views` type) and the `/ui/events` results store backing the board.
- `dist/` — **git-ignored** build output (rebuild via `make webui`), embedded
  into the binary with `go:embed`.
- `embed.go` — `//go:embed dist` (compile-time embed of the generated bundle).

## Scripts

- `npm run dev` — Vite dev server (proxies `/ui` → `localhost:8080`).
- `npm run build` — typecheck + production build into `dist/`.
- `npx tsc --noEmit` — typecheck only.