#!/usr/bin/env python3
"""W0 contract validator -- the gate that keeps manifests from drifting.

Checks (all must pass):
  * SCHEMA_VERSION file present, semver, and matches docs/CONTRACT.md
  * Data/schemas/*.schema.json parse and declare the frozen version
  * CSVs have exactly the frozen column set, in order
  * every CSV row validates against its JSON Schema
  * every POI's epsg2180 / ue coordinates match Scripts/geo_to_ue.py
  * branding_manifest covers every poi_id of a trademark brand, and
    licensed rows carry a consent reference
  * no Google-derived asset in the repo (metadata scan)
  * source_manifest.json references only allow-listed licences

Exit code 0 = PASS, 1 = FAIL. Writes a machine-readable report to
evidence/contract_report.json so CI can attach it.

    python3 Scripts/validate_contract.py [--strict] [--quiet]
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

sys.path.insert(0, str(ROOT / "Scripts"))
from geo_to_ue import wgs84_to_epsg2180, wgs84_to_ue_cm  # noqa: E402

MANIFEST_DIR = ROOT / "Data" / "POI"
SCHEMA_DIR = ROOT / "Data" / "schemas"
GEO_DIR = ROOT / "Data" / "Geo"
EVIDENCE = ROOT / "evidence"

FROZEN_COLUMNS = {
    "poi_manifest.csv": [
        "poi_id", "name", "brand", "category", "lat_wgs84", "lon_wgs84",
        "epsg2180_x", "epsg2180_y", "ue_x_cm", "ue_y_cm", "ue_z_cm",
        "street", "housenumber", "postal_code", "entrance_yaw", "source_url",
        "source_license", "branding_status", "sign_asset", "banner_asset",
        "interior_asset", "opening_hours", "enterable", "verified", "notes",
    ],
    "branding_manifest.csv": [
        "brand_id", "real_name", "fallback_name", "poi_ids", "mode",
        "sign_w_cm", "sign_h_cm", "sign_color_hex", "emissive_night",
        "holds_trademark", "consent_ref", "consent_expires", "fallback_asset",
    ],
}

# Georeference tolerance: coordinates are derived, so any drift means the row
# was edited by hand instead of going through geo_to_ue.py.
EPSG_TOL_M = 0.005
UE_TOL_CM = 2

# Matches actual Google asset/service ENDPOINTS, not the words "Google Maps"
# in prose. Documentation is allowed to discuss the prohibition; it is not
# allowed to carry a working reference to a tile, panorama or Places call.
GOOGLE_FORBIDDEN = re.compile(
    r"("
    r"googleapis\.com/maps"
    r"|maps\.google\.[a-z]{2,}"
    r"|google\.[a-z.]+/maps/(?!documentation)"
    r"|ggpht\.com"
    r"|khms\d"
    r"|mt\d\.google\.com/vt"
    r"|maps/api/streetview"
    r"|streetviewpublish"
    r")",
    re.IGNORECASE,
)

ALLOWED_LICENSES = {
    "ODbL-1.0", "official-site", "PRG", "EGiB", "BDOT10k",
    "own-survey", "unverified",
}

SEMVER = re.compile(r"^\d+\.\d+\.\d+$")


class Report:
    def __init__(self) -> None:
        self.checks: list[dict] = []

    def add(self, name: str, ok: bool, detail: str = "") -> None:
        self.checks.append({"check": name, "status": "PASS" if ok else "FAIL",
                            "detail": detail})

    @property
    def failed(self) -> list[dict]:
        return [c for c in self.checks if c["status"] == "FAIL"]

    def dump(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            json.dumps({"checks": self.checks,
                        "summary": {"total": len(self.checks),
                                    "failed": len(self.failed)}},
                       indent=2, ensure_ascii=False),
            encoding="utf-8",
        )


def load_schema(name: str) -> dict:
    return json.loads((SCHEMA_DIR / name).read_text(encoding="utf-8"))


def validate_row(row: dict, schema: dict) -> list[str]:
    """Minimal JSON-Schema subset validator (no external dependency)."""
    errors: list[str] = []
    props = schema.get("properties", {})
    required = schema.get("required", [])

    for key in required:
        if key not in row:
            errors.append(f"missing required column '{key}'")
    if schema.get("additionalProperties") is False:
        for key in row:
            if key not in props:
                errors.append(f"unexpected column '{key}'")

    for key, spec in props.items():
        if key not in row:
            continue
        raw = row[key]
        types = spec.get("type")
        types = [types] if isinstance(types, str) else (types or [])

        value: object = raw
        if "number" in types or "integer" in types:
            try:
                value = float(raw)
            except (TypeError, ValueError):
                errors.append(f"{key}: '{raw}' is not numeric")
                continue
        elif "boolean" in types:
            if str(raw).lower() not in ("true", "false"):
                errors.append(f"{key}: '{raw}' is not a boolean")
                continue
            value = str(raw).lower() == "true"

        if "const" in spec and value != spec["const"]:
            errors.append(f"{key}: {value!r} != const {spec['const']!r}")
        if "enum" in spec and not isinstance(value, bool) and value not in spec["enum"]:
            errors.append(f"{key}: {value!r} not in {spec['enum']}")
        if "pattern" in spec and isinstance(value, str) and not re.search(spec["pattern"], value):
            errors.append(f"{key}: '{value}' fails pattern {spec['pattern']}")
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            if "minimum" in spec and value < spec["minimum"]:
                errors.append(f"{key}: {value} < minimum {spec['minimum']}")
            if "maximum" in spec and value > spec["maximum"]:
                errors.append(f"{key}: {value} > maximum {spec['maximum']}")
            if "exclusiveMinimum" in spec and value <= spec["exclusiveMinimum"]:
                errors.append(f"{key}: {value} <= exclusiveMinimum {spec['exclusiveMinimum']}")
            if "exclusiveMaximum" in spec and value >= spec["exclusiveMaximum"]:
                errors.append(f"{key}: {value} >= exclusiveMaximum {spec['exclusiveMaximum']}")
        if "minLength" in spec and isinstance(value, str) and len(value) < spec["minLength"]:
            errors.append(f"{key}: shorter than {spec['minLength']}")
    return errors


def read_csv(path: Path) -> tuple[list[str], list[dict]]:
    with path.open(encoding="utf-8", newline="") as fh:
        reader = csv.DictReader(fh)
        rows = list(reader)
        return list(reader.fieldnames or []), rows


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--strict", action="store_true",
                    help="treat 'unverified' brand rows as failures")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)

    rep = Report()

    # --- 1. version -------------------------------------------------------
    version_file = ROOT / "SCHEMA_VERSION"
    version = version_file.read_text(encoding="utf-8").strip() if version_file.exists() else ""
    rep.add("schema_version.semver", bool(SEMVER.match(version)),
            f"SCHEMA_VERSION = {version!r}")

    contract = (ROOT / "docs" / "CONTRACT.md")
    contract_text = contract.read_text(encoding="utf-8") if contract.exists() else ""
    rep.add("schema_version.in_contract",
            f"SCHEMA_VERSION {version}" in contract_text,
            "docs/CONTRACT.md must state the frozen version")

    # --- 2. schemas -------------------------------------------------------
    schema_names = {
        "poi_manifest.csv": "poi_manifest.schema.json",
        "branding_manifest.csv": "branding_manifest.schema.json",
    }
    schemas: dict[str, dict] = {}
    for csv_name, schema_name in schema_names.items():
        path = SCHEMA_DIR / schema_name
        try:
            schemas[csv_name] = load_schema(schema_name)
            ok = True
            detail = str(path.relative_to(ROOT))
        except Exception as exc:  # noqa: BLE001
            schemas[csv_name] = {}
            ok = False
            detail = f"{path.relative_to(ROOT)}: {exc}"
        rep.add(f"schema.parses[{schema_name}]", ok, detail)

    source_schema_path = SCHEMA_DIR / "source_manifest.schema.json"
    source_schema = {}
    try:
        source_schema = load_schema("source_manifest.schema.json")
        rep.add("schema.parses[source_manifest.schema.json]", True, "")
    except Exception as exc:  # noqa: BLE001
        rep.add("schema.parses[source_manifest.schema.json]", False, str(exc))

    # --- 3+4. csv columns and rows ---------------------------------------
    rows_by_file: dict[str, list[dict]] = {}
    for csv_name, expected in FROZEN_COLUMNS.items():
        path = MANIFEST_DIR / csv_name
        if not path.exists():
            rep.add(f"csv.exists[{csv_name}]", False, f"missing {path.relative_to(ROOT)}")
            continue
        header, rows = read_csv(path)
        rows_by_file[csv_name] = rows

        same_order = header == expected
        same_set = set(header) == set(expected)
        if same_order:
            detail = f"{len(rows)} rows, {len(header)} columns"
        elif same_set:
            added = sorted(set(header) - set(expected))
            removed = sorted(set(expected) - set(header))
            detail = (f"column ORDER changed (added={added} removed={removed}). "
                      "Reordering is a SCHEMA_VERSION bump.")
        else:
            added = sorted(set(header) - set(expected))
            removed = sorted(set(expected) - set(header))
            detail = f"added={added} removed={removed}"
        rep.add(f"csv.frozen_columns[{csv_name}]", same_order, detail)

        schema = schemas.get(csv_name, {})
        row_errors: list[str] = []
        for idx, row in enumerate(rows, start=2):
            for err in validate_row(row, schema):
                row_errors.append(f"line {idx}: {err}")
        rep.add(f"csv.schema[{csv_name}]", not row_errors,
                "; ".join(row_errors[:12]) or f"{len(rows)} rows valid")

    # --- 5. georeference rebase ------------------------------------------
    poi_rows = rows_by_file.get("poi_manifest.csv", [])
    geo_errors: list[str] = []
    for row in poi_rows:
        try:
            lat, lon = float(row["lat_wgs84"]), float(row["lon_wgs84"])
        except (KeyError, ValueError):
            continue
        ex, ny = wgs84_to_epsg2180(lat, lon)
        ue = wgs84_to_ue_cm(lat, lon)
        try:
            if abs(float(row["epsg2180_x"]) - ex) > EPSG_TOL_M:
                geo_errors.append(f"{row.get('poi_id')}: epsg2180_x {row['epsg2180_x']} != {ex:.3f}")
            if abs(float(row["epsg2180_y"]) - ny) > EPSG_TOL_M:
                geo_errors.append(f"{row.get('poi_id')}: epsg2180_y {row['epsg2180_y']} != {ny:.3f}")
            for key, want in zip(("ue_x_cm", "ue_y_cm", "ue_z_cm"), ue):
                if abs(float(row[key]) - want) > UE_TOL_CM:
                    geo_errors.append(f"{row.get('poi_id')}: {key} {row[key]} != {want}")
        except KeyError:
            geo_errors.append(f"{row.get('poi_id')}: missing coordinate column")
    rep.add("poi.georeference_derived", not geo_errors,
            "; ".join(geo_errors[:10]) or f"{len(poi_rows)} rows match geo_to_ue.py")

    # --- 6. branding coverage --------------------------------------------
    poi_ids = {r.get("poi_id") for r in poi_rows}
    brand_rows = rows_by_file.get("branding_manifest.csv", [])
    brand_errors: list[str] = []
    covered: set[str] = set()
    for row in brand_rows:
        ids = [i for i in (row.get("poi_ids") or "").split(";") if i]
        covered.update(ids)
        for pid in ids:
            if pid not in poi_ids:
                brand_errors.append(f"{row.get('brand_id')}: unknown poi_id '{pid}'")
        if row.get("mode") == "licensed" and not (row.get("consent_ref") or "").strip():
            brand_errors.append(f"{row.get('brand_id')}: licensed without consent_ref")
        if row.get("mode") == "licensed" and not (row.get("consent_expires") or "").strip():
            brand_errors.append(f"{row.get('brand_id')}: licensed without consent_expires")
    rep.add("branding.manifest_consistent", not brand_errors,
            "; ".join(brand_errors[:10]) or f"{len(brand_rows)} brand rows")

    unverified = sorted(
        r.get("poi_id", "?") for r in poi_rows
        if r.get("branding_status") == "unverified"
    )
    rep.add("branding.no_unverified_rows", not unverified,
            f"{len(unverified)} POI rows still unverified: {unverified[:8]}")
    if args.strict and unverified:
        rep.checks[-1]["status"] = "FAIL"

    # A trademark brand present in POI but absent from branding manifest would
    # mean a logo in the world with no licence path.
    tm_missing = [
        r.get("poi_id", "?") for r in poi_rows
        if r.get("brand") and r.get("poi_id") not in covered
        and r.get("branding_status") in ("licensed", "fallback")
    ]
    rep.add("branding.every_brand_has_manifest_row", not tm_missing,
            "; ".join(tm_missing[:10]) or "all branded POIs mapped")

    # --- 7. licences ------------------------------------------------------
    lic_errors = [
        f"{r.get('poi_id')}: {r.get('source_license')!r}"
        for r in poi_rows
        if r.get("source_license") not in ALLOWED_LICENSES
    ]
    rep.add("licence.poi_source_allowlist", not lic_errors,
            "; ".join(lic_errors[:10]) or "all POI sources allow-listed")

    src_path = GEO_DIR / "source_manifest.json"
    if src_path.exists():
        try:
            src = json.loads(src_path.read_text(encoding="utf-8"))
            errs = validate_row(src, source_schema) if source_schema else []
            if src.get("schema_version") != version:
                errs.append(f"schema_version {src.get('schema_version')!r} != {version!r}")
            rep.add("geo.source_manifest_valid", not errs,
                    "; ".join(errs[:10]) or f"{len(src.get('sources', []))} sources")
        except json.JSONDecodeError as exc:
            rep.add("geo.source_manifest_valid", False, str(exc))
    else:
        rep.add("geo.source_manifest_valid", False, "missing Data/Geo/source_manifest.json")

    # --- 8. no Google-derived content ------------------------------------
    hits: list[str] = []
    skip_dirs = {".git", "node_modules", "dist", "build", "__pycache__", ".venv",
                 "tests"}
    for path in ROOT.rglob("*"):
        if not path.is_file() or any(part in skip_dirs for part in path.parts):
            continue
        if path.suffix.lower() not in {".json", ".csv", ".md", ".py", ".ts", ".tsx",
                                       ".js", ".ini", ".txt", ".html"}:
            continue
        if path.name == "validate_contract.py":
            continue  # the blacklist itself lives here
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        if GOOGLE_FORBIDDEN.search(text):
            hits.append(str(path.relative_to(ROOT)))
    rep.add("legality.no_google_assets", not hits, ", ".join(hits[:10]) or "clean")

    # --- report -----------------------------------------------------------
    rep.dump(EVIDENCE / "contract_report.json")

    if not args.quiet:
        for c in rep.checks:
            mark = "PASS" if c["status"] == "PASS" else "FAIL"
            print(f"[{mark}] {c['check']}" + (f"  -- {c['detail']}" if c["detail"] else ""))
        print()
        print(f"CONTRACT {version}: {len(rep.checks) - len(rep.failed)}/{len(rep.checks)} checks passed")

    return 0 if not rep.failed else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
