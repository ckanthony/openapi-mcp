# Web launcher — Carbon Design System integration

`chelmek-explore/web/` is the launcher/landing frontend. It renders the **real
W0 contract data** and nothing else. It is a static site: no backend, no
`localhost` calls, deployable to Vercel as-is.

---

## What was connected, and how

| Repository | What it actually is | How it is connected | Verified |
|---|---|---|---|
| `carbon-design-system/carbon` | IBM's design system (React components, Apache-2.0) | **npm dependency** — `@carbon/react@1.120.0` | ✅ installed, built, typed, tested |
| `carbon-app/carbon` | "Create and share beautiful images of your source code" (MIT) | **not** an npm dependency — see below | ⚠️ |

### `carbon-design-system/carbon` → npm ✅

```
"@carbon/react": "1.120.0"
```

Pinned exactly, not with a caret: the launcher's visual output should not shift
because IBM cut a release. Component CSS is imported per component
(`styles.scss`) so the bundle carries ~25 kB gzip of CSS instead of the whole
library.

Peer requirements are satisfied: `react@19.2.0`, `sass@1.93.3`.

### `carbon-app/carbon` → not installable from npm ⚠️

The choice was "npm dependencies", but this repository **is not published to
npm**. What exists on the registry is:

| Package | Real origin | Verdict |
|---|---|---|
| `carbon-app` | — | ❌ does not exist |
| `carbon-cli@1.0.3` | `github.com/itaditya/carbon-cli`, published 2018 | ❌ **different project**, unmaintained |
| `carbon-now-cli@2.1.0` | `github.com/mixn/carbon-now-cli`, MIT, 2024 | ⚠️ maintained third-party wrapper, not the official repo |
| `@carbon/cli@11.53.0` | IBM Carbon build tooling | ❌ unrelated (design system, not screenshots) |

So there are three honest options. **No option was implemented**, because
choosing for you here would mean shipping code you did not ask for:

1. **`carbon-now-cli`** — the maintained CLI wrapper around carbon-app. Gives a
   `carbon-now` command for producing documentation screenshots.
   *Trade-off:* it prints `carbon-app/carbon` in its own credits; its licence is
   MIT, the upstream's too, so there is no legal issue — only a provenance one.
2. **Local clone, not a dependency** — clone `carbon-app/carbon` next to the
   repo. It is a Next.js **application** (a web app you open in a browser), not a
   library; adding an app as a library dependency is not meaningful.
3. **Skip it** — the launcher's docs screenshots can be taken with any tool.

Say which one and it takes one commit.

---

## Commands (all verified in this environment)

```bash
cd chelmek-explore/web
npm install          # 138 packages, ~42 s
npm run typecheck    # tsc --noEmit            -> clean
npm test             # 8/8 CSV contract tests  -> OK
npm run build        # vite build              -> 934 modules, 6.9 s
npm run dev          # http://0.0.0.0:8080     -> serves the launcher
```

Build output:

| Asset | Raw | gzip |
|---|---|---|
| `index.html` | 0.61 kB | 0.37 kB |
| `assets/index-*.css` | 241.52 kB | 24.78 kB |
| `assets/index-*.js` | 301.75 kB | 96.02 kB |

---

## The launcher is bound to the contract

`src/data.ts` imports the manifests at build time:

```ts
import poiCsv from "../../Data/POI/poi_manifest.csv?raw";
import brandingCsv from "../../Data/POI/branding_manifest.csv?raw";
import roadmapMd from "../../ROADMAP_STATUS.md?raw";
import contractVersion from "../../SCHEMA_VERSION?raw";
```

Consequences, all intentional:

* the launcher **cannot** show a POI the contract does not contain;
* editing a CSV changes the site on the next build — no duplicated data;
* the roadmap table on the page is parsed out of `ROADMAP_STATUS.md`, so the
  page cannot claim a step is done when the file says otherwise;
* the `SCHEMA {version}` badge in the header reads `SCHEMA_VERSION` directly.

`src/csv.ts` is a dependency-free RFC 4180 parser (quoted fields, embedded
commas, doubled quotes, CRLF, Polish diacritics), unit-tested against the real
manifest — including a bbox check so a bad row cannot leak through.

```
$ npm test
ok - parses the real poi_manifest.csv with the frozen column set
ok - POI coordinates stay inside the contract bbox
# tests 8  # pass 8  # fail 0
```

---

## Rules this launcher obeys

| Rule | How |
|---|---|
| Vercel never renders UE5 | The page says so explicitly; download/stream buttons are `disabled` with the B1 reason shown |
| Frontend must not call `localhost` | Verified: `grep localhost dist/assets/*` → no matches |
| No Google assets | Verified: `grep -riE 'googleapis|maps\\.google|streetview' src/` returns nothing |
| Honest status | Data comes from `ROADMAP_STATUS.md`; blockers B1/B2/B3 rendered from the same contract |
| Attribution | OSM (ODbL), GUGiK, Gmina Chełmek, IBM Carbon Apache-2.0, UE5 EULA — in the footer |

---

## Vercel

`web/vercel.json` sets `outputDirectory: dist`, `framework: vite`, and immutable
caching for hashed assets. Root directory in the Vercel project should be
`chelmek-explore/web`.

Deploy is **not** done here: it needs your Vercel project scope and a build with
your environment. The command is `vercel --prod` from that directory.

### One architecture note

If Vercel's root directory is this repo, the project should point at
`chelmek-explore/web` rather than the repository root — the repo root is a Go
MCP server, not this launcher.
