#!/usr/bin/env python3
"""Frozen WGS84 -> EPSG:2180 (PL-1992) -> Unreal local ENU transform.

CONTRACT (W0, SCHEMA_VERSION 1.0.0) -- DO NOT CHANGE WITHOUT A SCHEMA BUMP
=========================================================================
1. Input CRS is EPSG:4326 (WGS84), axis order (lat, lon) in degrees.
2. Working CRS is EPSG:2180 (PL-1992, GRS80, Transverse Mercator,
   lat_0=0, lon_0=19, k_0=0.9993, FE=500000, FN=-5300000).
   Exposed as epsg2180_x = easting, epsg2180_y = northing (metres).
3. Engine CRS is a local ENU frame anchored at ORIGIN.
   UE_x_cm = +North,  UE_y_cm = +East,  UE_z_cm = +Up,  1 uu = 1 cm.
   Rationale: UE is left-handed (X forward, Y right, Z up). With X=North and
   Y=East a real map stays readable from above and is not mirrored.
   Switching to X=East is a coordinate-layout change -> SCHEMA_VERSION bump.
4. The transform is deterministic: same input always yields the same integers.
   No object may be nudged by hand; every correction goes through this file.
5. Cross-checked against pyproj to < 1 mm when pyproj is installed (see tests).

Usage:
    python3 Scripts/geo_to_ue.py 50.1003348 19.2542585
    python3 Scripts/geo_to_ue.py --self-check
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

# --- Frozen constants -------------------------------------------------------

ORIGIN_LAT = 50.1003348
ORIGIN_LON = 19.2542585

BBOX = {
    "lat_min": 50.0881157,
    "lat_max": 50.1341934,
    "lon_min": 19.2227522,
    "lon_max": 19.2773330,
}

# GRS80 / WGS84 differ only marginally; EPSG:2180 is defined on GRS80 and
# EPSG:4326 on WGS84. The difference (< 1 mm at city scale) is recorded here
# instead of being silently ignored.
GRS80_A = 6378137.0
GRS80_F = 1.0 / 298.257222101
WGS84_A = 6378137.0
WGS84_F = 1.0 / 298.257223563

# EPSG:2180 projection parameters
TM_LAT0 = 0.0
TM_LON0 = 19.0
TM_K0 = 0.9993
TM_FE = 500000.0
TM_FN = -5300000.0


def _e2(f: float) -> float:
    return 2.0 * f - f * f


def _e_prime2(f: float) -> float:
    e2 = _e2(f)
    return e2 / (1.0 - e2)


def _meridian_arc(phi: float, a: float, f: float) -> float:
    """Meridian distance from the equator (Snyder 3-21, series form)."""
    e2 = _e2(f)
    e4 = e2 * e2
    e6 = e4 * e2
    return a * (
        (1 - e2 / 4 - 3 * e4 / 64 - 5 * e6 / 256) * phi
        - (3 * e2 / 8 + 3 * e4 / 32 + 45 * e6 / 1024) * math.sin(2 * phi)
        + (15 * e4 / 256 + 45 * e6 / 1024) * math.sin(4 * phi)
        - (35 * e6 / 3072) * math.sin(6 * phi)
    )


def wgs84_to_epsg2180(lat: float, lon: float) -> tuple[float, float]:
    """WGS84 degrees -> EPSG:2180 (easting, northing) in metres."""
    phi = math.radians(lat)
    lam = math.radians(lon)
    lam0 = math.radians(TM_LON0)

    a, f = GRS80_A, GRS80_F
    e2 = _e2(f)
    ep2 = _e_prime2(f)

    sin_phi = math.sin(phi)
    cos_phi = math.cos(phi)
    tan_phi = math.tan(phi)

    n = a / math.sqrt(1.0 - e2 * sin_phi * sin_phi)
    t = tan_phi * tan_phi
    c = ep2 * cos_phi * cos_phi
    aa = (lam - lam0) * cos_phi
    m = _meridian_arc(phi, a, f)

    easting = TM_FE + TM_K0 * n * (
        aa
        + (1 - t + c) * aa**3 / 6
        + (5 - 18 * t + t * t + 72 * c - 58 * ep2) * aa**5 / 120
    )
    northing = TM_FN + TM_K0 * (
        m
        + n
        * tan_phi
        * (
            aa**2 / 2
            + (5 - t + 9 * c + 4 * c * c) * aa**4 / 24
            + (61 - 58 * t + t * t + 600 * c - 330 * ep2) * aa**6 / 720
        )
    )
    return easting, northing


def _geodetic_to_ecef(lat: float, lon: float, h: float = 0.0) -> tuple[float, float, float]:
    phi = math.radians(lat)
    lam = math.radians(lon)
    a, f = WGS84_A, WGS84_F
    e2 = _e2(f)
    n = a / math.sqrt(1.0 - e2 * math.sin(phi) ** 2)
    x = (n + h) * math.cos(phi) * math.cos(lam)
    y = (n + h) * math.cos(phi) * math.sin(lam)
    z = (n * (1.0 - e2) + h) * math.sin(phi)
    return x, y, z


def wgs84_to_enu(lat: float, lon: float) -> tuple[float, float, float]:
    """WGS84 degrees -> local ENU metres (east, north, up) about ORIGIN."""
    ox, oy, oz = _geodetic_to_ecef(ORIGIN_LAT, ORIGIN_LON)
    px, py, pz = _geodetic_to_ecef(lat, lon)
    dx, dy, dz = px - ox, py - oy, pz - oz

    phi = math.radians(ORIGIN_LAT)
    lam = math.radians(ORIGIN_LON)
    sin_phi, cos_phi = math.sin(phi), math.cos(phi)
    sin_lam, cos_lam = math.sin(lam), math.cos(lam)

    east = -sin_lam * dx + cos_lam * dy
    north = -sin_phi * cos_lam * dx - sin_phi * sin_lam * dy + cos_phi * dz
    up = cos_phi * cos_lam * dx + cos_phi * sin_lam * dy + sin_phi * dz
    return east, north, up


def wgs84_to_ue_cm(lat: float, lon: float, z_cm: int = 0) -> tuple[int, int, int]:
    """WGS84 degrees -> Unreal local coordinates in centimetres (frozen rule 3)."""
    east, north, up = wgs84_to_enu(lat, lon)
    ue_x = int(round(north * 100.0))
    ue_y = int(round(east * 100.0))
    ue_z = int(z_cm) + int(round(up * 100.0))
    return ue_x, ue_y, ue_z


def ue_cm_to_enu(ue_x_cm: int, ue_y_cm: int) -> tuple[float, float]:
    """Inverse of the planar part, in metres (east, north). Ignores up."""
    return ue_y_cm / 100.0, ue_x_cm / 100.0


def _ecef_to_geodetic(x: float, y: float, z: float) -> tuple[float, float, float]:
    """ECEF -> geodetic (lat, lon, h) by fixed-point iteration (mm accuracy)."""
    a, f = WGS84_A, WGS84_F
    e2 = _e2(f)
    lon = math.atan2(y, x)
    p = math.hypot(x, y)
    lat = math.atan2(z, p * (1.0 - e2))
    for _ in range(8):
        sin_lat = math.sin(lat)
        n = a / math.sqrt(1.0 - e2 * sin_lat * sin_lat)
        h = p / math.cos(lat) - n
        lat = math.atan2(z, p * (1.0 - e2 * n / (n + h)))
    return math.degrees(lat), math.degrees(lon), h


def local_metres_to_wgs84(east_m: float, north_m: float, up_m: float = 0.0) -> tuple[float, float]:
    """Local ENU metres -> WGS84 degrees (exact, via ECEF round-trip).

    Sub-millimetre inside the Chełmek bbox, so web seeds derived from legacy
    local coordinates are geometrically faithful. Authoritative GIS data still
    enters through EPSG:2180, never through this function.
    """
    phi = math.radians(ORIGIN_LAT)
    lam = math.radians(ORIGIN_LON)
    sin_phi, cos_phi = math.sin(phi), math.cos(phi)
    sin_lam, cos_lam = math.sin(lam), math.cos(lam)

    dx = -sin_lam * east_m - sin_phi * cos_lam * north_m + cos_phi * cos_lam * up_m
    dy = cos_lam * east_m - sin_phi * sin_lam * north_m + cos_phi * sin_lam * up_m
    dz = cos_phi * north_m + sin_phi * up_m

    ox, oy, oz = _geodetic_to_ecef(ORIGIN_LAT, ORIGIN_LON)
    lat, lon, _h = _ecef_to_geodetic(ox + dx, oy + dy, oz + dz)
    return lat, lon


def bearing_deg(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Initial great-circle bearing, degrees clockwise from north."""
    phi1, phi2 = math.radians(lat1), math.radians(lat2)
    dlam = math.radians(lon2 - lon1)
    y = math.sin(dlam) * math.cos(phi2)
    x = math.cos(phi1) * math.sin(phi2) - math.sin(phi1) * math.cos(phi2) * math.cos(dlam)
    return (math.degrees(math.atan2(y, x)) + 360.0) % 360.0


def distance_m(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Haversine distance in metres."""
    r = 6371008.8
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dp = p2 - p1
    dl = math.radians(lon2 - lon1)
    h = math.sin(dp / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    return 2 * r * math.asin(math.sqrt(h))


# --- Cross-checks -----------------------------------------------------------

SELF_CHECK_POINTS = [
    (ORIGIN_LAT, ORIGIN_LON),
    (BBOX["lat_min"], BBOX["lon_min"]),
    (BBOX["lat_max"], BBOX["lon_max"]),
    (50.10189, 19.24023),   # Grunwaldzka (vista reference)
    (50.09952, 19.24107),   # Na Skałce
    (50.10378, 19.26244),   # Krakowska retail strip (seed)
]


def _pyproj_epsg2180(lat: float, lon: float):
    try:
        from pyproj import Transformer
    except ImportError:
        return None
    t = Transformer.from_crs("EPSG:4326", "EPSG:2180", always_xy=True)
    return t.transform(lon, lat)


def self_check(verbose: bool = True) -> int:
    """Verify the pure-Python transform against pyproj and internal invariants."""
    failures = 0

    # 1. Origin must map to (0,0,0) in UE space.
    x, y, z = wgs84_to_ue_cm(ORIGIN_LAT, ORIGIN_LON)
    if (abs(x) > 2 or abs(y) > 2 or abs(z) > 2) and verbose:
        print(f"FAIL origin maps to {(x, y, z)} cm, expected ~(0,0,0)")
        failures += 1
    elif verbose:
        print(f"ok   origin -> ({x}, {y}, {z}) cm")

    # 2. Purist chirality: east of origin has +y, north of origin has +x.
    lat_e, lon_e = local_metres_to_wgs84(300.0, 0.0)
    lat_n, lon_n = local_metres_to_wgs84(0.0, 300.0)
    xe, ye, _ = wgs84_to_ue_cm(lat_e, lon_e)
    xn, yn, _ = wgs84_to_ue_cm(lat_n, lon_n)
    ok = ye > 29000 and abs(xe) < 2000 and xn > 29000 and abs(yn) < 2000
    if verbose:
        print(f"{'ok  ' if ok else 'FAIL'} chirality: east(300 m) -> y={ye} cm, "
              f"north(300 m) -> x={xn} cm")
    failures += 0 if ok else 1

    # 3. pyproj agreement
    checked = 0
    for lat, lon in SELF_CHECK_POINTS:
        ref = _pyproj_epsg2180(lat, lon)
        if ref is None:
            break
        mine = wgs84_to_epsg2180(lat, lon)
        dx, dy = mine[0] - ref[0], mine[1] - ref[1]
        err = math.hypot(dx, dy)
        checked += 1
        if err > 1e-3:
            failures += 1
            if verbose:
                print(f"FAIL EPSG:2180 {lat:.7f},{lon:.7f}: err {err * 1000:.3f} mm "
                      f"(mine {mine}, pyproj {ref})")
    if verbose:
        if checked == 0:
            print("skip pyproj cross-check (pyproj not installed)")
        elif failures == 0:
            print(f"ok   pyproj cross-check: {checked}/{checked} points < 1 mm")

    # 4. Round-trip through local metres
    lat, lon = local_metres_to_wgs84(1234.5, -987.6)
    e, n, _ = wgs84_to_enu(lat, lon)
    rt = math.hypot(e - 1234.5, n + 987.6)
    if rt > 1.0:
        failures += 1
        if verbose:
            print(f"FAIL round-trip error {rt:.3f} m")
    elif verbose:
        print(f"ok   round-trip error {rt * 1000:.1f} mm")

    if verbose:
        print("\nSELF-CHECK", "PASS" if failures == 0 else f"FAIL ({failures})")
    return 0 if failures == 0 else 1


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("lat", nargs="?", type=float, help="WGS84 latitude")
    p.add_argument("lon", nargs="?", type=float, help="WGS84 longitude")
    p.add_argument("--self-check", action="store_true", help="run built-in checks")
    p.add_argument("--json", action="store_true", help="JSON output")
    args = p.parse_args(argv)

    if args.self_check or args.lat is None:
        return self_check()

    if args.lon is None:
        p.error("lon is required")

    easting, northing = wgs84_to_epsg2180(args.lat, args.lon)
    ue_x, ue_y, ue_z = wgs84_to_ue_cm(args.lat, args.lon)
    result = {
        "lat_wgs84": args.lat,
        "lon_wgs84": args.lon,
        "epsg2180_x": round(easting, 3),
        "epsg2180_y": round(northing, 3),
        "ue_x_cm": ue_x,
        "ue_y_cm": ue_y,
        "ue_z_cm": ue_z,
        "inside_bbox": (
            BBOX["lat_min"] <= args.lat <= BBOX["lat_max"]
            and BBOX["lon_min"] <= args.lon <= BBOX["lon_max"]
        ),
    }
    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
    else:
        for k, v in result.items():
            print(f"{k:14s} {v}")
    return 0 if result["inside_bbox"] else 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
