#!/usr/bin/env python3
"""Generate Data/POI/poi_manifest.csv from the seed + branding declarations.

This is the ONLY writer of poi_manifest.csv. Every coordinate column is
derived here with Scripts/geo_to_ue.py; nothing is typed by hand. That keeps
`validate_contract.py` meaningful: if the CSV stops matching the transform, a
human edited it.

    python3 Scripts/generate_poi_manifest.py            # write
    python3 Scripts/generate_poi_manifest.py --dry-run  # print only
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "Scripts"))

from geo_to_ue import local_metres_to_wgs84, wgs84_to_epsg2180, wgs84_to_ue_cm  # noqa: E402

SEED = ROOT / "Data" / "POI" / "seed" / "poi_seed.json"
BRANDS = ROOT / "Data" / "POI" / "seed" / "brands_seed.json"
OUT_POI = ROOT / "Data" / "POI" / "poi_manifest.csv"
OUT_BRAND = ROOT / "Data" / "POI" / "branding_manifest.csv"

POI_COLUMNS = [
    "poi_id", "name", "brand", "category", "lat_wgs84", "lon_wgs84",
    "epsg2180_x", "epsg2180_y", "ue_x_cm", "ue_y_cm", "ue_z_cm",
    "street", "housenumber", "postal_code", "entrance_yaw", "source_url",
    "source_license", "branding_status", "sign_asset", "banner_asset",
    "interior_asset", "opening_hours", "enterable", "verified", "notes",
]

BRAND_COLUMNS = [
    "brand_id", "real_name", "fallback_name", "poi_ids", "mode",
    "sign_w_cm", "sign_h_cm", "sign_color_hex", "emissive_night",
    "holds_trademark", "consent_ref", "consent_expires", "fallback_asset",
]


def build_rows(seed: dict, brands: dict) -> tuple[list[dict], list[dict]]:
    brand_by_name = {b["real_name"]: b for b in brands["brands"]}
    sources = seed["source"]
    default_license = sources["license"]

    rows: list[dict] = []
    for p in seed["pois"]:
        # legacy web frame: east = x, north = -z
        lat, lon = local_metres_to_wgs84(float(p["x_m"]), -float(p["z_m"]))
        ex, ny = wgs84_to_epsg2180(lat, lon)
        ue_x, ue_y, ue_z = wgs84_to_ue_cm(lat, lon)

        brand = brand_by_name.get(p.get("brand") or "")
        branding_status = brand["mode"] if brand else "not-applicable"

        rows.append({
            "poi_id": p["poi_id"],
            "name": p["name"],
            "brand": p.get("brand", ""),
            "category": p["category"],
            "lat_wgs84": f"{lat:.9f}",
            "lon_wgs84": f"{lon:.9f}",
            "epsg2180_x": f"{ex:.3f}",
            "epsg2180_y": f"{ny:.3f}",
            "ue_x_cm": ue_x,
            "ue_y_cm": ue_y,
            "ue_z_cm": ue_z,
            "street": p.get("street", ""),
            "housenumber": p.get("housenumber", ""),
            "postal_code": p.get("postal_code", "32-660"),
            "entrance_yaw": f"{float(p.get('entrance_yaw', 0.0)):.1f}",
            "source_url": p.get("source_url", sources["url"]),
            "source_license": p.get("source_license", default_license),
            "branding_status": branding_status,
            "sign_asset": f"/Game/Chelmek/POI/Signs/{p['poi_id']}" if brand else "",
            "banner_asset": f"/Game/Chelmek/POI/Banners/{p['poi_id']}" if brand else "",
            "interior_asset": (
                f"/Game/Chelmek/POI/Interiors/{p['poi_id']}"
                if p.get("enterable") else ""
            ),
            "opening_hours": p.get("opening_hours", "Mo-Su 00:00-24:00"),
            "enterable": str(bool(p.get("enterable", False))).lower(),
            "verified": str(bool(p.get("verified", True))).lower(),
            "notes": p.get("notes", ""),
        })

    brand_rows: list[dict] = []
    poi_by_brand: dict[str, list[str]] = {}
    for p in seed["pois"]:
        if p.get("brand"):
            poi_by_brand.setdefault(p["brand"], []).append(p["poi_id"])

    for b in brands["brands"]:
        ids = poi_by_brand.get(b["real_name"], [])
        if not ids:
            continue
        brand_rows.append({
            "brand_id": b["brand_id"],
            "real_name": b["real_name"],
            "fallback_name": b["fallback_name"],
            "poi_ids": ";".join(sorted(ids)),
            "mode": b["mode"],
            "sign_w_cm": b["sign_w_cm"],
            "sign_h_cm": b["sign_h_cm"],
            "sign_color_hex": b["sign_color_hex"],
            "emissive_night": str(bool(b["emissive_night"])).lower(),
            "holds_trademark": str(bool(b["holds_trademark"])).lower(),
            "consent_ref": b.get("consent_ref", ""),
            "consent_expires": b.get("consent_expires", ""),
            "fallback_asset": b["fallback_asset"],
        })
    return rows, brand_rows


def write_csv(path: Path, columns: list[str], rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=columns, lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args(argv)

    seed = json.loads(SEED.read_text(encoding="utf-8"))
    brands = json.loads(BRANDS.read_text(encoding="utf-8"))
    poi_rows, brand_rows = build_rows(seed, brands)

    if args.dry_run:
        for r in poi_rows:
            print(f"{r['poi_id']:28s} {r['lat_wgs84']} {r['lon_wgs84']} "
                  f"ue=({r['ue_x_cm']},{r['ue_y_cm']}) {r['branding_status']}")
        print(f"\n{len(poi_rows)} POI, {len(brand_rows)} brand rows")
        return 0

    write_csv(OUT_POI, POI_COLUMNS, poi_rows)
    write_csv(OUT_BRAND, BRAND_COLUMNS, brand_rows)
    print(f"wrote {OUT_POI.relative_to(ROOT)} ({len(poi_rows)} rows)")
    print(f"wrote {OUT_BRAND.relative_to(ROOT)} ({len(brand_rows)} rows)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
