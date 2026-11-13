# W0 blockers — what a human has to unblock

Written honestly at the end of W0. Nothing below is "in progress": each item is
**not started and cannot be started from this environment.**

---

## B1 — No Unreal Engine, no GPU (blocks W1–W13)

There is no `UE_ROOT` and no GPU here. `ChelmekExploreUE5` therefore exists only
as a planned structure in `docs/ARCHITECTURE_PLAN.md`; not one `.uasset`,
`.umap` or `.uproject` has been created, because a hand-written binary asset
would be a fake.

**What I need:** `[UE_ROOT]` on a Windows/Linux workstation with UE 5.8, and a
GPU host for Pixel Streaming (W12). Until then W1 (Mesh Terrain), W2
(photogrammetry), W3 (interiors) and everything downstream are blocked.

**What is ready for that machine:** the frozen contract (this milestone), the
validator, the manifest generator, the CI gate. They run today, headless,
without Unreal.

---

## B2 — No trademark licences (blocks `[BRANDING_MODE]=licensed`)

All six brands (Lidl, Biedronka, Rossmann, Żabka, Ptyś, Ciżemka) are recorded as
`fallback`. `validate_contract.py` refuses a `licensed` row without a
`consent_ref` **and** a `consent_expires`, so a licensed build literally cannot
be produced by accident.

**What I need:** either written permissions with an expiry date, or an explicit
decision to ship `fallback` only. Do **not** ask me to "just use the logos" —
that is the one blocker with legal consequences.

---

## B3 — Geoportal / Overpass / Nominatim unreachable (blocks W1, W2, W5)

The sandbox can only reach `github.com`, `codeload.github.com`,
`api.github.com`, `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`.
Verified failures during W0:

```
$ curl https://overpass-api.de/api/interpreter   -> no route
$ curl https://nominatim.openstreetmap.org/      -> 000
$ curl https://www.geoportal.gov.pl/             -> no route
$ curl https://www.szybkiplik.pl/...             -> SSL_ERROR_SYSCALL
```

So `Data/Geo/source_manifest.json` lists 7 of 8 sources as `not-downloaded`. The
only `present` source is the OSM extract already committed inside the web game
repository, which W0 reused as a seed.

**What I need:** a machine with normal internet to run the Geoportal downloads
(BDOT10k, NMT/NMPT, ORTO) and one Overpass extract. `Scripts/` will then do the
rest. That download step is the single highest-value unblock: it turns
`not-downloaded` into real terrain and footprints for W1.

---

## B4 — The web game's coordinates are approximate (affects W5, not W0)

W0 seeded POIs from `Che-mek-EXPLORE/src/game/city.json`. That frame was
hand-built and has a measurable bias:

* its origin `50.1018, 19.2515` sits **256 m at bearing 129.5°** from the
  contract origin `50.1003348, 19.2542585`;
* after re-projection, Lidl lands at `50.09837, 19.26518`, i.e. ~40 m west and
  ~120 m south of the real Krakowska 20A frontage;
* the reservoir in that file sits north of the origin, the real Zbiornik
  Dziećkowice sits south — so the legacy frame is not only shifted but at least
  partly flipped.

Using it unrectified in UE5 would put buildings tens of metres off and make
`validate_distances.py` impossible to pass. Therefore:

* every seeded POI carries `verified` accordingly and a `notes` entry saying it
  is a seed;
* `poi_seed.json` explicitly forbids hand-editing into the manifest;
* W5 must re-derive every row from PRG/EGiB/OSM. **Seeds are scaffolding, not
  truth.**

---

## B5 — The vista (Wzgórze Skała) interpretation is unconfirmed

Prompt §8.1 asks for the viewing tower on Wzgórze Skała with the Grunwald
monument, in the Grunwaldzka / Sadowa / Na Skałce area. The prompt itself flags
that sources disagree on the tower (30.8 m vs 36 m, platform ~26 m). It also
notes the monument node in the legacy extract sits at local `(-814, 242)`,
which re-projects to ~`50.0996, 19.2401` — roughly 1 km west of that street
cluster.

**What I need:** one sentence confirming the location, plus the authoritative
tower dimensions (design documentation, not a blog). Until then no tower, no
monument and no 360° horizon is modelled — `validate_horizon.py` cannot be
written meaningfully without a confirmed viewpoint.

---

## B6 — The uploaded ZIP was unreadable

`Che-mek-EXPLORE-main.zip` (10.31 MB, uploaded 2026-11-13 05:19) was on
`szybkiplik.pl`, which is unreachable from this sandbox. I used the public
GitHub repository `amadii90210-dev/Che-mek-EXPLORE` instead
(`grok-workspace.zip`, `src/game/*`). If the ZIP contains something the
repository does not, re-upload it here as an attachment.

---

## Summary

| Blocker | Blocks | Owner |
|---|---|---|
| B1 no UE / no GPU | W1–W13 | you (workstation + GPU host) |
| B2 no trademark licences | `licensed` mode | you (legal) |
| B3 no Geoportal/OSM network | W1, W2, W5 | you (one download run) |
| B4 legacy frame bias | W5 accuracy | me, after B3 |
| B5 vista unconfirmed | W8 section, horizon | you (one sentence) |
| B6 ZIP unreadable | context only | you (re-upload) |

None of these block W0, which is why W0 was the right first milestone: it is
pure, testable, and every later step depends on it.
