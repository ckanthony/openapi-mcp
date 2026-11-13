# Web build — playable Chełmek (browser, no install)

A third-person walk through the real town, running in the browser on a phone.
This is **not** the Unreal Engine 5 game — it is the honest web version, built
from the OSM extract that already existed in the legacy repository.

```
cd chelmek-explore/web
npm install
npm run dev      # http://<host>:8080  ->  przycisk "Uruchom grę"
```

---

## Zagraj teraz — link

**https://8080-i7vhpsgygm4iixbnzavtn.e2b.app**

To jest podgląd sandboxu Arena (dev server Vite na `0.0.0.0:8080`). Trwa tak
długo, jak działa proces `chelmek-explore-launcher`. Po restarcie sandboxu
adres zmieni się, bo zawiera identyfikator sandboxu.

Sprawdzone przez proxy (nagłówek `Host` ustawiony na domenę podglądu), nie
zgadywane:

```
200  /
200  /src/main.tsx
200  /src/App.tsx
200  /src/game/GameView.tsx
200  /src/game/engine.ts
200  /src/game/world.ts
200  /src/game/city.json   (425 350 B)
200  /src/styles.scss
```

Osobno sprawdzony build produkcyjny (`dist/`) serwowany statycznie:
`index.html` 200, `index-onpFgXfu.js` 200, `index-tSGv_bLM.css` 200.

Stały adres publiczny (Vercel / GitHub Pages) **nie powstał** — patrz sekcja
o blokadzie wdrożenia niżej.


## What is real in it

| Element | Count | Source |
|---|---|---|
| Building footprints (walls + roofs) | **2731** | `src/game/city.json` ← OSM |
| Road centrelines | **903** (93 named streets + paths + service) | OSM |
| POIs (markers, HUD proximity, fast travel) | **85** | OSM |
| Green areas (forest / park / grass / pitch) | 116 | OSM |
| Lakes and rivers | 22 lakes, 38 water lines | OSM |
| Rail line | 20 segments | OSM |

Nothing is procedurally invented: every wall you collide with is a real
footprint, and the street name in the HUD is a real OSM street name resolved
from your position.

**Honest limitation:** heights are the OSM `building:levels`-derived value from
the legacy extract, roofs are flat, and there is no terrain relief. A 1:1
vertical world needs NMT/NMPT from Geoportal — that is blocker **B3**, and
without it the UE5 build cannot start either.

---

## Controls

| Input | Action |
|---|---|
| `W A S D` / arrows | walk |
| `Shift` | run |
| drag with a finger or mouse | look around |
| on-screen pad (bottom-left) | walk on a phone |
| `Esc` | leave the game |
| `H` | toggle the help card |
| "Podejdź" in the HUD | step to the nearest POI outside its wall |

Rendering is three.js with a merged city buffer (one draw call for all 2731
buildings) and a coarse spatial grid for collision — 0.005 ms per query
against a 16.6 ms frame budget, measured in `scripts`-free form below.

---

## Verified here (not assumed)

```
$ npm run typecheck      # tsc --noEmit                     -> clean
$ npm test               # 22 tests                         -> 22 pass
$ npm run build          # vite build                       -> ok
$ curl localhost:8080    # dev server                       -> 200
```

Bundle after code-splitting:

| Chunk | gzip | Loaded |
|---|---|---|
| launcher (`index`) | 97.4 kB | immediately |
| game (`GameView`) | 277.7 kB | only after "Uruchom grę" |
| CSS | 24.8 kB + 0.9 kB | — |

The game chunk carries three.js and the 416 kB `city.json`, so it must not
block first paint — hence the `lazy()` split.

### Two real bugs the tests caught

Both would have shipped silently without unit tests, because there is no
browser binary in this sandbox to click through the game:

1. **Wall collision had a hole at grid boundaries.** Buildings are bucketed by
   centroid cell, but a footprint can extend ~100 m past its own cell
   (`maxRadius` = 100.6 m). The query window covered only the probe radius, so
   at certain coordinates `insideBuilding()` returned `null` *inside* a
   building — the player would walk through the wall. Fixed by expanding the
   search window by `maxRadius`; the exact radius test remains authoritative.
   Regression test: *"insideBuilding finds a point known to be inside a footprint"*.
2. **A wrong invariant in the first test.** "A building's centroid is inside its
   ring" is false for L-shaped footprints — and a real town has plenty. The
   correct invariant, which is what collision actually relies on, is *every
   vertex lies within `radius` of the centroid*, so the disk contains the
   polygon. Replaced, and the suite now asserts at least one concave footprint
   exists.

Performance of the fix, measured over 2000 queries across the whole map:

```
buildings: 2731   maxRadius: 100.6 m   index build: 27.6 ms
nearbyBuildings avg: 0.0053 ms   worst: 1.196 ms
=> per frame (2 collision calls) ~ 0.011 ms of the 16.6 ms budget
```

---

## What this is not

* **Not the UE5 world.** It cannot be: no engine, no GPU (blocker B1).
* **Not streamed from a server.** It is a static site; Vercel serves files only.
* **Not the final art direction.** Flat roofs, no foliage, no NPCs, no traffic.
  Those belong to W1–W4 of the roadmap and need real terrain first.

It exists because "a game you can open in a link today" and "a 1:1 UE5 world"
are two different deliverables, and only the first one is achievable without
the blocked inputs.

---

## Deployment — blocked

The Vercel connector authenticates as `amadii90210-dev`
(default team `team_KlAqfjPFmsilt0oYwspzoj1B`) but the token is `limited: true`
and every project/deployment call returns **Access denied**:

```
validate_credential          -> success  (token IS valid)
get_authenticated_user       -> success  (amadii90210-dev)
list_projects   (user scope) -> Access denied
list_projects   (team scope) -> Access denied
create_deployment (git)      -> Access denied
create_deployment (files)    -> Access denied
create_project               -> Access denied
```

Every write path was tried, including uploading the built `dist/` directly as
files (a different API surface from git deploys) and creating the project first.
All blocked at the token scope, so **no public URL could be produced from
here.** To get one:

1. Reconnect Vercel in Arena with a token that has project + deployment write
   scope, or
2. On a machine with the Vercel CLI:
   ```bash
   cd chelmek-explore/web
   npx vercel --prod
   ```
   `vercel.json` is already configured (`outputDirectory: dist`, framework
   `vite`, immutable asset caching).

Meanwhile the live preview on port **8080** is the runnable build.
