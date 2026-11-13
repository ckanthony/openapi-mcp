/**
 * World geometry for Chełmek EXPLORE (web build).
 *
 * Built from the OpenStreetMap extract inherited from the legacy web game:
 * 2731 building footprints, 903 road centrelines, 85 POIs, 116 green patches.
 *
 * Coordinate convention (local, metres, matches Scripts/geo_to_ue.py):
 *   local x = +East, local z = +South   (i.e. north = -z)
 *   three.js world x = local x, world z = local z, world y = up.
 * So the map is not mirrored and the north arrow on the HUD is truthful.
 */
import cityJson from "./city.json";
import {
  toRing,
  centroid,
  buildIndex,
  insideBuilding,
  nearestRoad as nearestRoadFrom,
  pointInRing,
  nearbyBuildings,
  type Building,
  type Ring,
  type Road,
} from "./geometry.ts";

export { insideBuilding, pointInRing, nearbyBuildings };

/** Nearest named road at a point (used by the HUD). */
export function nearestRoad(x: number, z: number, includePaths = true) {
  return nearestRoadFrom(x, z, roads, includePaths);
}
export type { Building, Ring, Road };

export type Poi = {
  x: number;
  z: number;
  name: string;
  kind: string;
};

type RawCity = {
  origin: { lat: number; lon: number };
  spawn: { x: number; z: number; name: string };
  attribution: string;
  buildings: { p: number[]; h: number; k: string }[];
  roads: { p: number[]; w: number; k: string; name?: string | null }[];
  water: { p: number[]; w?: number; name?: string | null }[];
  lakes: { p: number[]; name?: string | null }[];
  greens: { p: number[]; k: string }[];
  rails: { p: number[] }[];
  pois: { x: number; z: number; name: string; kind: string }[];
};

const city = cityJson as unknown as RawCity;

export const origin = city.origin;
export const spawn = city.spawn;
export const attribution = city.attribution;

export const buildings: Building[] = city.buildings.map((b) => {
  const ring = toRing(b.p);
  const [cx, cz] = centroid(ring);
  let radius = 0;
  for (const [x, z] of ring) {
    radius = Math.max(radius, Math.hypot(x - cx, z - cz));
  }
  return { ring, height: b.h, kind: b.k, cx, cz, radius };
});

export const roads: Road[] = city.roads.map((r) => ({
  points: toRing(r.p),
  width: r.w,
  kind: r.k,
  name: r.name ?? null,
}));

export const greens = city.greens.map((g) => ({ ring: toRing(g.p), kind: g.k }));
export const lakes = city.lakes.map((l) => toRing(l.p));
export const water = city.water.map((w) => ({ points: toRing(w.p), name: w.name ?? null }));
export const rails = city.rails.map((r) => toRing(r.p));
export const pois: Poi[] = city.pois;

// --- spatial index ---------------------------------------------------------
// The collision helpers live in geometry.ts (pure, unit-tested); this module
// just feeds them the real OSM data.
buildIndex(buildings);

/** Overall world extent, used to size the ground plane and the minimap. */
export const extent = (() => {
  let minX = Infinity;
  let maxX = -Infinity;
  let minZ = Infinity;
  let maxZ = -Infinity;
  const visit = (ring: Ring) => {
    for (const [x, z] of ring) {
      if (x < minX) minX = x;
      if (x > maxX) maxX = x;
      if (z < minZ) minZ = z;
      if (z > maxZ) maxZ = z;
    }
  };
  buildings.forEach((b) => visit(b.ring));
  roads.forEach((r) => visit(r.points));
  pois.forEach((p) => visit([[p.x, p.z]]));
  return { minX, maxX, minZ, maxZ };
})();

export const stats = {
  buildings: buildings.length,
  roads: roads.length,
  pois: pois.length,
  greens: greens.length,
  lakes: lakes.length,
  namedRoads: new Set(roads.map((r) => r.name).filter(Boolean)).size,
};
