# ROADMAP_STATUS — Chełmek EXPLORE

**SCHEMA_VERSION 1.0.0** · last updated 2026-11-13

Legend: ✅ done and verified · 🟡 partial, evidence attached · ⛔ blocked by a
human · ⬜ not started

| Step | Name | Status | Evidence / blocker |
|---|---|---|---|
| **W0** | Freeze data contract | ✅ | `docs/CONTRACT.md`, `Scripts/validate_contract.py` → 16/16 PASS, `evidence/contract_report.json`, 21/21 tests |
| W1 | UE 5.8 upgrade (Mesh Terrain, PVE, MegaLights, Legacy_Fallback) | ⛔ | B1 — no UE_ROOT |
| W2 | Photogrammetry, 25 hero objects | ⛔ | B1 + B3 — needs field capture |
| W3 | Enterable interiors, 12 POIs | ⛔ | B1 |
| W4 | Living city / day schedule | ⛔ | B1 |
| W5 | POI sync + re-derive all POIs | ⛔ | B3 — needs PRG/EGiB/OSM download; B4 describes the work |
| W6 | Light, weather, audio at 50.10 °N | ⛔ | B1 |
| W7 | Physics + micro-interactions | ⛔ | B1 |
| W8 | 12 original quests | ⛔ | B1 + B5 |
| W9 | UI / HUD / photo mode | ⛔ | B1 |
| W10 | Playtest with residents | ⛔ | needs a build (B1) |
| W11 | CI/CD, one-command release | 🟡 | contract CI exists (`.github/workflows/contract.yml`); UE build steps need B1 |
| W12 | Pixel Streaming on GPU host | ⛔ | B1 — needs GPU host |
| W13 | Legal clearance + release kit | 🟡 | licence allow-list + `LEGAL_CLEARANCE.md` scaffolded; all 6 brands `fallback` (B2) |
| W14 | Creator mode / UGC | ⬜ | after W10 |
| W15 | Co-op | ⬜ | after W10 |

---

## W0 — what was actually delivered

**Committed, tested, and re-runnable today without Unreal:**

| Artifact | Purpose |
|---|---|
| `SCHEMA_VERSION` | `1.0.0`, the number every later change must bump |
| `docs/CONTRACT.md` | the freeze: columns, coordinate chain, layers, asset names, branding rules, licence allow-list |
| `Data/schemas/*.schema.json` | 3 JSON Schemas, versioned inside the same file set |
| `Scripts/geo_to_ue.py` | pure-Python WGS84 → EPSG:2180 → UE (cm); 0 mm against pyproj on 6 control points; `--self-check` |
| `Scripts/generate_poi_manifest.py` | the **only** writer of the two CSVs; no hand-typed coordinates |
| `Scripts/validate_contract.py` | 16 gates; fails on column drift, hand-nudged coords, unlicensed logos, Google references, bad licences, version drift |
| `Scripts/tests/test_contract.py` | 21 tests, 8 of them negative — they break the repo on purpose and assert the gate fires |
| `Data/POI/poi_manifest.csv` | 15 seeded POIs with derived EPSG:2180 + UE coordinates |
| `Data/POI/branding_manifest.csv` | 6 brands, all `fallback`, geometry frozen for a later `licensed` swap |
| `Data/Geo/source_manifest.json` | 8 sources with honest `present` / `not-downloaded` status |
| `.github/workflows/contract.yml` | CI runs self-check, validator, tests, and a **drift check** (regenerate → `git diff --exit-code`) |
| `docs/W0_BLOCKERS.md` | 6 blockers, each with a concrete request |

### Acceptance for W0 (from the roadmap)

| Criterion | Result |
|---|---|
| `validate_contract.py` passes | ✅ 16/16 |
| No later step can change the schema without a new version | ✅ CI + `contract.yml` drift check + `--strict` mode |
| One clean commit `docs(contract): freeze W0` | ✅ |

### Verification commands

```bash
cd chelmek-explore
python3 Scripts/geo_to_ue.py --self-check        # SELF-CHECK PASS
python3 Scripts/validate_contract.py             # CONTRACT 1.0.0: 16/16
python3 -m unittest discover -s Scripts/tests -t . -v   # Ran 21 tests ... OK
```

---

## Next move

**Recommended order unchanged: W0 → W1 → W2.** W0 is done. W1 needs B1 and B3;
the cheapest real progress before those unblock is:

1. **B3 first** — one download run on a machine with internet produces
   BDOT10k + NMT + PRG for the bbox. That single action unblocks W1, W2 and W5
   at once and lets `validate_distances.py` be written against real data.
2. **B2 in parallel** — the licence table is a paper task, no GPU needed.
3. Then W5's re-derivation (fixes B4) before any interior modelling, so W3 is
   built on confirmed entrances rather than seed coordinates.
