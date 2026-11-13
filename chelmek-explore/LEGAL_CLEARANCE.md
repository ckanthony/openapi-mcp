# LEGAL_CLEARANCE — status table (W13 scaffold)

**SCHEMA_VERSION 1.0.0** · opened 2026-11-13 · **not releasable yet**

A build with `[BRANDING_MODE]=licensed` may only be produced when this table has
**zero `MISSING` rows**. Today every commercial brand is `MISSING`, which is why
all six are set to `fallback`.

## Trademarks and brand marks

| Brand | Signs in world | Consent status | Scope | Expires | Fallback asset |
|---|---|---|---|---|---|
| Lidl | `poi_lidl_krakowska` | ❌ MISSING | — | — | `/Game/Chelmek/POI/Brands/Fallback/MarketSpoleczy` |
| Biedronka | `poi_biedronka_krakowska` | ❌ MISSING | — | — | `.../SklepDyskontowy` |
| Rossmann | `poi_rossmann_krakowska` | ❌ MISSING | — | — | `.../Drogeria` |
| Żabka | `poi_zabka_piastowska`, `poi_zabka_krakowska` | ❌ MISSING | — | — | `.../SklepOsiedlowy` |
| Ptyś | `poi_ptys_baty` | ❌ MISSING | — | — | `.../Cukiernia` |
| Ciżemka | `poi_cizemka` | ❌ MISSING | — | — | `.../Pizzeria` |

**Note on text vs. logo:** writing the real name on a modelled building
(`szyld` text) is not the same act as reproducing the registered wordmark.
The conservative position taken here: `fallback` swaps both the glyph and the
stylised wordmark, keeps the sign's size, position, lit area and semantic colour.

## Public symbols

| Symbol | Status | Note |
|---|---|---|
| Herb / flaga Gminy Chełmek | ⚠️ UNREVIEWED | Public symbols have their own usage rules; modelled only as a neutral plaque until confirmed |
| Patron names on school plaques | ✅ OK | Facts, not trademarks |
| Religious symbols (cross, stained glass) | ✅ OK | Modelled with restraint, no invented dedications |

## Data and content licences

| Source | Licence | Attribution present |
|---|---|---|
| OpenStreetMap | ODbL 1.0 | ✅ `ATTRIBUTION.md` |
| GUGiK BDOT10k / NMT / NMPT / ORTO | open, attribution required | ✅ listed (data not yet downloaded) |
| Gmina Chełmek MPZP / studium | official-site | ✅ listed |
| Own photogrammetry (W2) | own-survey | ⬜ not started |
| Fonts, music, voice | — | ⬜ **no asset chosen yet** |

## Hard prohibitions (verified by CI)

| Rule | Enforcement |
|---|---|
| No Google Maps / Street View / Places assets or derived content | `validate_contract.py` → `legality.no_google_assets` |
| No Rockstar / GTA assets, HUD, missions, music | review + `docs/CONTRACT.md` §8 |
| No real people's faces or plates | not started (W2/W13) |
| No telemetry without opt-in (RODO) | not started (W10) |

## Release checklist

- [ ] 0 `MISSING` rows above
- [ ] `ATTRIBUTION.md` + `LICENSES.md` final
- [ ] in-game Credits screen matching them
- [ ] PEGI self-assessment
- [ ] privacy policy (only if telemetry is ever enabled)
