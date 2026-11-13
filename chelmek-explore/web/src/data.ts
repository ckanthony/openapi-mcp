/**
 * Build-time data loader for the launcher.
 *
 * Source of truth: the frozen W0 contract in this repository.
 *   ../../Data/POI/poi_manifest.csv      -> the POI rows
 *   ../../SCHEMA_VERSION                -> the contract version badge
 *
 * Nothing here is hand-typed: if the CSV changes, this module changes, and
 * Vite rebuilds. That keeps the launcher honest -- it cannot show a POI that
 * the contract does not contain.
 */
import poiCsv from "../../Data/POI/poi_manifest.csv?raw";
import brandingCsv from "../../Data/POI/branding_manifest.csv?raw";
import roadmapMd from "../../ROADMAP_STATUS.md?raw";
import contractVersion from "../../SCHEMA_VERSION?raw";
import { parseCsv } from "./csv";

export type Poi = {
  poiId: string;
  name: string;
  brand: string;
  category: string;
  street: string;
  housenumber: string;
  lat: number;
  lon: number;
  ueX: number;
  ueY: number;
  entranceYaw: number;
  branding: string;
  enterable: boolean;
  verified: boolean;
  openingHours: string;
};

export type RoadmapRow = {
  step: string;
  name: string;
  status: "done" | "partial" | "blocked" | "todo";
};

export const schemaVersion = contractVersion.trim();

export const pois: Poi[] = parseCsv(poiCsv).map((r) => ({
  poiId: r.poi_id,
  name: r.name,
  brand: r.brand,
  category: r.category,
  street: r.street,
  housenumber: r.housenumber,
  lat: Number(r.lat_wgs84),
  lon: Number(r.lon_wgs84),
  ueX: Number(r.ue_x_cm),
  ueY: Number(r.ue_y_cm),
  entranceYaw: Number(r.entrance_yaw),
  branding: r.branding_status,
  enterable: r.enterable === "true",
  verified: r.verified === "true",
  openingHours: r.opening_hours,
}));

export const brandRows = parseCsv(brandingCsv);

export const stats = {
  poiCount: pois.length,
  brandCount: brandRows.length,
  enterableCount: pois.filter((p) => p.enterable).length,
  verifiedCount: pois.filter((p) => p.verified).length,
  fallbackCount: brandRows.filter((b) => b.mode === "fallback").length,
  licensedCount: brandRows.filter((b) => b.mode === "licensed").length,
  categories: Array.from(new Set(pois.map((p) => p.category))).sort(),
};

const STATUS_MAP: Record<string, RoadmapRow["status"]> = {
  "✅": "done",
  "🟡": "partial",
  "⛔": "blocked",
  "⬜": "todo",
};

/** Parse the W0-W15 status table out of ROADMAP_STATUS.md. */
export const roadmap: RoadmapRow[] = roadmapMd
  .split("\n")
  .filter((line) => /^\|\s*\*{0,2}W\d+/.test(line))
  .map((line) => {
    const cells = line.split("|").map((c) => c.trim());
    const step = cells[1].replace(/\*/g, "");
    const name = cells[2];
    const statusCell = cells[3];
    const glyph = Object.keys(STATUS_MAP).find((g) => statusCell.includes(g));
    return {
      step,
      name,
      status: glyph ? STATUS_MAP[glyph] : "todo",
    };
  });

export const blockerCount = (
  roadmapMd.match(/^\|\s*B\d+\s*\|/gm) ?? []
).length;
