#!/usr/bin/env python3
"""W0 contract tests. Run: python3 -m unittest discover -s Scripts/tests -v

Covers:
  * transform correctness and determinism
  * bbox enforcement
  * the validator actually REJECTS violations (negative tests) -- a gate that
    never fires is worse than no gate.
"""

from __future__ import annotations

import csv
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(ROOT / "Scripts"))

import geo_to_ue as geo  # noqa: E402

PY = sys.executable


class TestTransform(unittest.TestCase):
    def test_origin_is_zero(self):
        self.assertEqual(geo.wgs84_to_ue_cm(geo.ORIGIN_LAT, geo.ORIGIN_LON), (0, 0, 0))

    def test_determinism(self):
        a = geo.wgs84_to_ue_cm(50.11, 19.26)
        b = geo.wgs84_to_ue_cm(50.11, 19.26)
        self.assertEqual(a, b)

    def test_north_is_plus_x(self):
        lat, lon = geo.local_metres_to_wgs84(0.0, 500.0)
        x, y, _ = geo.wgs84_to_ue_cm(lat, lon)
        self.assertAlmostEqual(x, 50000, delta=5)
        self.assertAlmostEqual(y, 0, delta=5)

    def test_east_is_plus_y(self):
        lat, lon = geo.local_metres_to_wgs84(500.0, 0.0)
        x, y, _ = geo.wgs84_to_ue_cm(lat, lon)
        self.assertAlmostEqual(y, 50000, delta=5)
        self.assertAlmostEqual(x, 0, delta=5)

    def test_epsg2180_matches_pyproj(self):
        try:
            from pyproj import Transformer
        except ImportError:
            self.skipTest("pyproj not installed")
        t = Transformer.from_crs("EPSG:4326", "EPSG:2180", always_xy=True)
        for lat, lon in geo.SELF_CHECK_POINTS:
            ref = t.transform(lon, lat)
            mine = geo.wgs84_to_epsg2180(lat, lon)
            self.assertAlmostEqual(mine[0], ref[0], delta=1e-3)
            self.assertAlmostEqual(mine[1], ref[1], delta=1e-3)

    def test_bbox_corners_inside(self):
        for key in ("lat_min", "lat_max"):
            for other in ("lon_min", "lon_max"):
                lat = geo.BBOX[key]
                lon = geo.BBOX[other]
                ex, ny = geo.wgs84_to_epsg2180(lat, lon)
                # sanity: Polish national grid range for Małopolska
                self.assertTrue(515000 < ex < 521000, (lat, lon, ex))
                self.assertTrue(246000 < ny < 252000, (lat, lon, ny))

    def test_self_check_passes(self):
        self.assertEqual(geo.self_check(verbose=False), 0)

    def test_cli_reports_outside_bbox(self):
        out = subprocess.run(
            [PY, str(ROOT / "Scripts" / "geo_to_ue.py"), "52.2297", "21.0122", "--json"],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(out.returncode, 2, out.stderr)
        self.assertIn('"inside_bbox": false', out.stdout)


class TestContractGate(unittest.TestCase):
    """Copies the repo to a temp dir, breaks it on purpose, expects FAIL."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.root = self.tmp / "chelmek-explore"
        shutil.copytree(
            ROOT,
            self.root,
            ignore=shutil.ignore_patterns(
                ".git", "__pycache__", "evidence", "node_modules", "dist",
            ),
        )

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _validate(self) -> subprocess.CompletedProcess:
        return subprocess.run(
            [PY, str(self.root / "Scripts" / "validate_contract.py"), "--quiet"],
            capture_output=True, text=True, check=False,
        )

    def test_baseline_passes(self):
        self.assertEqual(self._validate().returncode, 0)

    def test_detects_hand_edited_coordinate(self):
        csv_path = self.root / "Data" / "POI" / "poi_manifest.csv"
        rows = list(csv.DictReader(csv_path.open(encoding="utf-8")))
        rows[0]["ue_x_cm"] = str(int(rows[0]["ue_x_cm"]) + 500)  # nudged by 5 m
        with csv_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()), lineterminator="\n")
            w.writeheader()
            w.writerows(rows)
        result = self._validate()
        self.assertEqual(result.returncode, 1, "validator accepted a hand-nudged POI")

    def test_detects_extra_column(self):
        csv_path = self.root / "Data" / "POI" / "poi_manifest.csv"
        rows = list(csv.DictReader(csv_path.open(encoding="utf-8")))
        cols = list(rows[0].keys()) + ["sneaky_new_column"]
        for r in rows:
            r["sneaky_new_column"] = "x"
        with csv_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=cols, lineterminator="\n")
            w.writeheader()
            w.writerows(rows)
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_removed_column(self):
        csv_path = self.root / "Data" / "POI" / "poi_manifest.csv"
        rows = list(csv.DictReader(csv_path.open(encoding="utf-8")))
        cols = [c for c in rows[0] if c != "entrance_yaw"]
        with csv_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=cols, lineterminator="\n", extrasaction="ignore")
            w.writeheader()
            w.writerows(rows)
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_licensed_without_consent(self):
        csv_path = self.root / "Data" / "POI" / "branding_manifest.csv"
        rows = list(csv.DictReader(csv_path.open(encoding="utf-8")))
        rows[0]["mode"] = "licensed"  # no consent_ref, no expiry
        with csv_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()), lineterminator="\n")
            w.writeheader()
            w.writerows(rows)
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_trademark_without_manifest_row(self):
        poi_path = self.root / "Data" / "POI" / "poi_manifest.csv"
        rows = list(csv.DictReader(poi_path.open(encoding="utf-8")))
        rows[1]["brand"] = "Biedronka"
        rows[1]["branding_status"] = "fallback"
        # drop Biedronka's brand row so the POI has a logo with no licence path
        brand_path = self.root / "Data" / "POI" / "branding_manifest.csv"
        brows = [r for r in csv.DictReader(brand_path.open(encoding="utf-8"))
                 if r["real_name"] != "Biedronka"]
        with brand_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(brows[0].keys()), lineterminator="\n")
            w.writeheader()
            w.writerows(brows)
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_google_reference(self):
        doc = self.root / "docs" / "CONTRACT.md"
        doc.write_text(
            doc.read_text(encoding="utf-8")
            + "\nsee https://maps.googleapis.com/maps/api/streetview for reference\n",
            encoding="utf-8",
        )
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_bad_licence(self):
        csv_path = self.root / "Data" / "POI" / "poi_manifest.csv"
        rows = list(csv.DictReader(csv_path.open(encoding="utf-8")))
        rows[0]["source_license"] = "GooglePlaces"
        with csv_path.open("w", encoding="utf-8", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()), lineterminator="\n")
            w.writeheader()
            w.writerows(rows)
        self.assertEqual(self._validate().returncode, 1)

    def test_detects_schema_version_drift(self):
        (self.root / "SCHEMA_VERSION").write_text("1.0.1\n", encoding="utf-8")
        self.assertEqual(self._validate().returncode, 1)


class TestManifests(unittest.TestCase):
    def test_regeneration_is_idempotent(self):
        """generate -> validate must be a no-op fixpoint."""
        before = (ROOT / "Data" / "POI" / "poi_manifest.csv").read_text(encoding="utf-8")
        out = subprocess.run(
            [PY, str(ROOT / "Scripts" / "generate_poi_manifest.py")],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        after = (ROOT / "Data" / "POI" / "poi_manifest.csv").read_text(encoding="utf-8")
        self.assertEqual(before, after, "generator is not deterministic")

    def test_all_pois_inside_bbox(self):
        rows = list(csv.DictReader(
            (ROOT / "Data" / "POI" / "poi_manifest.csv").open(encoding="utf-8")))
        self.assertGreater(len(rows), 0)
        for r in rows:
            lat, lon = float(r["lat_wgs84"]), float(r["lon_wgs84"])
            self.assertTrue(geo.BBOX["lat_min"] <= lat <= geo.BBOX["lat_max"], r["poi_id"])
            self.assertTrue(geo.BBOX["lon_min"] <= lon <= geo.BBOX["lon_max"], r["poi_id"])

    def test_branding_covers_branded_pois(self):
        poi = list(csv.DictReader(
            (ROOT / "Data" / "POI" / "poi_manifest.csv").open(encoding="utf-8")))
        brand = list(csv.DictReader(
            (ROOT / "Data" / "POI" / "branding_manifest.csv").open(encoding="utf-8")))
        covered = {i for b in brand for i in b["poi_ids"].split(";") if i}
        for r in poi:
            if r["brand"]:
                self.assertIn(r["poi_id"], covered, r["poi_id"])

    def test_source_manifest_version_matches(self):
        version = (ROOT / "SCHEMA_VERSION").read_text(encoding="utf-8").strip()
        src = json.loads((ROOT / "Data" / "Geo" / "source_manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(src["schema_version"], version)


if __name__ == "__main__":
    unittest.main(verbosity=2)
