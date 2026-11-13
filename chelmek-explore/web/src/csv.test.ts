/**
 * Unit tests for the CSV parser. Run with:
 *   node --experimental-strip-types --test src/csv.test.ts
 *
 * The launcher renders real contract data, so a parser bug would silently
 * show wrong coordinates. These tests exist to make that impossible.
 */
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { parseCsv } from "./csv.ts";

const here = dirname(fileURLToPath(import.meta.url));
const manifest = resolve(here, "../../Data/POI/poi_manifest.csv");

test("parses simple rows", () => {
  const rows = parseCsv("a,b\n1,2\n3,4\n");
  assert.equal(rows.length, 2);
  assert.deepEqual(rows[0], { a: "1", b: "2" });
  assert.deepEqual(rows[1], { a: "3", b: "4" });
});

test("handles CRLF line endings", () => {
  const rows = parseCsv("a,b\r\n1,2\r\n");
  assert.deepEqual(rows, [{ a: "1", b: "2" }]);
});

test("handles quoted fields with commas", () => {
  const rows = parseCsv('a,b\n"x, y",2\n');
  assert.equal(rows[0].a, "x, y");
});

test("handles escaped quotes", () => {
  const rows = parseCsv('a\n"say ""hi"""\n');
  assert.equal(rows[0].a, 'say "hi"');
});

test("keeps empty trailing fields", () => {
  const rows = parseCsv("a,b,c\n1,,\n");
  assert.deepEqual(rows[0], { a: "1", b: "", c: "" });
});

test("preserves Polish diacritics", () => {
  const rows = parseCsv("name\nŻabka\n");
  assert.equal(rows[0].name, "Żabka");
});

test("parses the real poi_manifest.csv with the frozen column set", () => {
  const rows = parseCsv(readFileSync(manifest, "utf8"));
  assert.ok(rows.length >= 15, `expected >= 15 POIs, got ${rows.length}`);

  const expected = [
    "poi_id", "name", "brand", "category", "lat_wgs84", "lon_wgs84",
    "epsg2180_x", "epsg2180_y", "ue_x_cm", "ue_y_cm", "ue_z_cm",
    "street", "housenumber", "postal_code", "entrance_yaw", "source_url",
    "source_license", "branding_status", "sign_asset", "banner_asset",
    "interior_asset", "opening_hours", "enterable", "verified", "notes",
  ];
  assert.deepEqual(Object.keys(rows[0]), expected);

  for (const row of rows) {
    assert.ok(Number.isFinite(Number(row.lat_wgs84)), row.poi_id);
    assert.ok(Number.isFinite(Number(row.ue_x_cm)), row.poi_id);
    assert.ok(["true", "false"].includes(row.enterable), row.poi_id);
  }
});

test("POI coordinates stay inside the contract bbox", () => {
  const rows = parseCsv(readFileSync(manifest, "utf8"));
  for (const row of rows) {
    const lat = Number(row.lat_wgs84);
    const lon = Number(row.lon_wgs84);
    assert.ok(lat >= 50.0881157 && lat <= 50.1341934, `${row.poi_id} lat ${lat}`);
    assert.ok(lon >= 19.2227522 && lon <= 19.277333, `${row.poi_id} lon ${lon}`);
  }
});
