# CONTRACT — frozen data contract for `ChelmekExploreUE5`

**SCHEMA_VERSION 1.0.0** · frozen 2026-11-13 · milestone **W0**

This file is the single source of truth for *how data is shaped*. No later step
(W1–W15) may add, remove, reorder or retype a column, nor switch a coordinate
convention, without a `SCHEMA_VERSION` bump and a matching change here.

If `python3 Scripts/validate_contract.py` fails, the change is not allowed in.

---

## 1. What is frozen

| # | Frozen thing | Where it lives | Enforced by |
|---|---|---|---|
| 1 | `poi_manifest.csv` — 25 columns, exact order | `Data/POI/poi_manifest.csv` | `validate_contract.py` `csv.frozen_columns` |
| 2 | `branding_manifest.csv` — 13 columns, exact order | `Data/POI/branding_manifest.csv` | idem |
| 3 | `Data/Geo/source_manifest.json` shape | `Data/Geo/` | `schema.parses` + `geo.source_manifest_valid` |
| 4 | WGS84 → EPSG:2180 → UE transform + origin | `Scripts/geo_to_ue.py` | `poi.georeference_derived` |
| 5 | Data Layer names | this document, §5 | review |
| 6 | Asset naming convention | this document, §6 | review |
| 7 | `[BRANDING_MODE]` rules | this document, §7 | `branding.*` checks |
| 8 | Source licence allow-list | this document, §8 | `licence.poi_source_allowlist` |

Any diff to items 1–4 must (a) bump `SCHEMA_VERSION`, (b) update the matching
file in `Data/schemas/`, (c) update this document, (d) regenerate manifests with
`Scripts/generate_poi_manifest.py`, (e) land as one commit
`feat(contract): SCHEMA_VERSION X.Y.Z — <reason>`.

---

## 2. Coordinate chain

```
OSM / PRG / EGiB / BDOT10k          (EPSG:4326, lat/lon degrees)
        │
        ├── working CRS ──────────► EPSG:2180 / PL-1992   (metres, GRS80 TM)
        │                            epsg2180_x = easting
        │                            epsg2180_y = northing
        │
        └── engine CRS ───────────► local ENU about ORIGIN
                                     UE_x_cm = +North  × 100
                                     UE_y_cm = +East   × 100
                                     UE_z_cm = +Up     × 100
                                     1 uu = 1 cm  (frozen)
```

**ORIGIN:** `50.1003348 N, 19.2542585 E` (WGS84)
→ `epsg2180 (518178.544, 248151.823)`
→ `UE (0, 0, 0)`

**BBOX:** `50.0881157–50.1341934 N`, `19.2227522–19.2773330 E`
Roughly 5.1 km N–S × 3.9 km E–W. Every POI must fall inside it; the schema
enforces this with `minimum`/`maximum` on `lat_wgs84` / `lon_wgs84`.

### Why X = North and Y = East

Unreal is left-handed with X forward, Y right, Z up. Putting **North on X** and
**East on Y** gives a plan view that is not mirrored: you read the map from
above exactly as the real town looks. The tempting alternative (X = East) makes
every CAD/GeoTIFF import come in mirrored, which is the single most expensive
mistake to unwind later. Inverting this decision is a **coordinate-layout
change** → `SCHEMA_VERSION` bump.

### Accuracy

`Scripts/geo_to_ue.py` is a pure-Python Transverse Mercator implementation for
EPSG:2180. Verified against `pyproj` on 6 control points across the bbox:
**< 1 mm agreement**. Self-check:

```
$ python3 Scripts/geo_to_ue.py --self-check
ok   origin -> (0, 0, 0) cm
ok   chirality: east(300 m) -> y=30000 cm, north(300 m) -> x=30000 cm
ok   pyproj cross-check: 6/6 points < 1 mm
ok   round-trip error 0.0 mm
SELF-CHECK PASS
```

A target of ≤ 2 m absolute georeference error (prompt §2) is therefore limited
purely by the *source data*, not by the transform.

---

## 3. `poi_manifest.csv` — 25 columns

| Column | Type | Rule |
|---|---|---|
| `poi_id` | string | `^poi_[a-z0-9_]+$`, unique |
| `name` | string | real name, Polish diacritics preserved |
| `brand` | string | empty, or a `real_name` present in `branding_manifest.csv` |
| `category` | enum | see `poi_manifest.schema.json` |
| `lat_wgs84` | number | inside BBOX |
| `lon_wgs84` | number | inside BBOX |
| `epsg2180_x` | number | **derived** |
| `epsg2180_y` | number | **derived** |
| `ue_x_cm` | integer | **derived** |
| `ue_y_cm` | integer | **derived** |
| `ue_z_cm` | integer | **derived** (0 until terrain exists) |
| `street` | string | from PRG/OSM, not from Google |
| `housenumber` | string | PRG; empty allowed until verified |
| `postal_code` | string | `32-660` or empty |
| `entrance_yaw` | number | `0 ≤ yaw < 360`, clockwise from north; the **sign faces the street** |
| `source_url` | string | must be one of the allow-listed sources |
| `source_license` | enum | `ODbL-1.0`, `official-site`, `PRG`, `EGiB`, `BDOT10k`, `own-survey`, `unverified` |
| `branding_status` | enum | `licensed`, `fallback`, `unverified`, `not-applicable` |
| `sign_asset` | string | UE content path, empty for non-branded POI |
| `banner_asset` | string | idem |
| `interior_asset` | string | empty when `enterable=false` |
| `opening_hours` | string | OSM `opening_hours` syntax |
| `enterable` | boolean | drives the W3 interior list |
| `verified` | boolean | `false` = seed value, human must confirm |
| `notes` | string | free text, use `"` escaping as needed |

**Anchor rule:** a POI is anchored on the **entrance**, not the building centre.
`entrance_yaw` must agree with the sign orientation; W9's `Chelmek.POI.Theme`
test checks it visually.

**Derived means derived.** `epsg2180_*` and `ue_*_cm` are written only by
`Scripts/generate_poi_manifest.py`. Hand-editing them is a contract violation
and `validate_contract.py` will catch it (tolerance 5 mm / 2 cm).

---

## 4. `branding_manifest.csv` — 13 columns

| Column | Rule |
|---|---|
| `brand_id` | `^brand_[a-z0-9_]+$` |
| `real_name` | matches `poi_manifest.brand` |
| `fallback_name` | neutral Polish generic — never a look-alike of the real mark |
| `poi_ids` | `;`-separated, every id must exist in `poi_manifest.csv` |
| `mode` | `licensed` or `fallback` |
| `sign_w_cm`, `sign_h_cm` | frozen physical size, identical in both modes |
| `sign_color_hex` | `#RRGGBB`, semantic colour kept in `fallback` |
| `emissive_night` | boolean |
| `holds_trademark` | boolean |
| `consent_ref` | **required** when `mode=licensed` |
| `consent_expires` | **required** when `mode=licensed` (`YYYY-MM-DD`) |
| `fallback_asset` | replacement material used when `mode=fallback` |

Switching `licensed ↔ fallback` swaps materials only. It must never move a
sign, resize it or change the sign's lit area — that is what makes the flag
useful.

---

## 5. Frozen Data Layer names

```
Terrain        Terrain
Roads          Roads
Buildings      Buildings
Foliage        Foliage
POI            POI
Gameplay       Gameplay
Debug          Debug
Legacy_Fallback Legacy_Fallback      (W1, toggleable, never deleted)
```

No new layer without a contract bump: W1's `Legacy_Fallback` switch and W5's
partial rebuild both key on these names.

---

## 6. Frozen asset naming

```
/Game/Chelmek/Geo/MeshTerrain/<Layer>_<Tile>_<LLLL>
/Game/Chelmek/Architecture/<typology>/SM_<typology>_<element>_<nn>
/Game/Chelmek/Foliage/<species_latin_snake>/SM_<species>_<LOD>
/Game/Chelmek/POI/Signs/<poi_id>
/Game/Chelmek/POI/Banners/<poi_id>
/Game/Chelmek/POI/Brands/Fallback/<BrandGeneric>
/Game/Chelmek/POI/Interiors/<poi_id>
/Game/Chelmek/Vista/<element>
```

Rules: `SM_` static mesh, `M_` material, `MI_` instance, `T_` texture,
`PCG_` graph, `BP_` blueprint, `DT_` data table. Lowercase snake_case after the
prefix. No spaces, no Polish diacritics **in asset paths** (content authoring
language is English; in-world text is Polish).

---

## 7. `[BRANDING_MODE]` rules

* Exactly one mode per build: `licensed` or `fallback`. Mixed builds are
  forbidden unless the Credits screen says so explicitly.
* `licensed` requires a `consent_ref` + `consent_expires` for **every** brand
  row, otherwise `validate_contract.py` fails and CI blocks the build.
* `fallback` keeps: footprint, sign size, sign position, lit area, semantic
  colour. It changes: the wordmark and the logo only.
* Trademark names may be used as **text** (an inscription on a real building),
  but the logo glyph may not be reproduced without a licence.
* A `brand` value with no matching `branding_manifest.csv` row is a hard error —
  it would mean a logo in the world with no licence path.

Current state: **all 6 brands are `fallback`** (no licences obtained yet). See
`docs/W0_BLOCKERS.md` B2.

---

## 8. Source licence allow-list

| `source_license` | Meaning | Attribution |
|---|---|---|
| `ODbL-1.0` | OpenStreetMap | "© OpenStreetMap contributors, ODbL 1.0" |
| `BDOT10k` | GUGiK topographic database | GUGiK/Geoportal |
| `PRG` | GUGiK registers | GUGiK/Geoportal |
| `EGiB` | land & building register | Powiat oświęcimski |
| `official-site` | institution / company page | link in `ATTRIBUTION.md` |
| `own-survey` | our own field capture | none (we own it) |
| `unverified` | placeholder, blocks release | — |

**Forbidden as a data source, absolutely:** Google Maps, Street View, Places
API, Google elevation, any scraped tile or panorama. `validate_contract.py`
greps the repository for Google map endpoints (`legality.no_google_assets`) and
the scan covers `.json .csv .md .py .ts .tsx .js .ini .txt .html`.

---

## 9. Validation gates

```
python3 Scripts/geo_to_ue.py --self-check      # transform correctness
python3 Scripts/generate_poi_manifest.py       # the only writer of the CSVs
python3 Scripts/validate_contract.py           # 16 contract checks
python3 Scripts/validate_contract.py --strict  # also fails on unverified rows
```

`evidence/contract_report.json` is the machine-readable artifact for CI.

CI (`.github/workflows/contract.yml`) runs exactly these three commands on every
push and pull request touching `chelmek-explore/**`.

---

## 10. Schema change log

| SCHEMA_VERSION | Date | Change |
|---|---|---|
| 1.0.0 | 2026-11-13 | Initial freeze (W0). 25 POI columns, 13 brand columns, source manifest, EPSG:2180 + ENU cm transform, 8 Data Layers, asset naming, branding rules, licence allow-list. |
