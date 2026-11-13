/**
 * Geometry tests for the walkable world.
 *   node --experimental-strip-types --test src/game/geometry.test.ts
 *
 * These cover the numbers that decide whether a player walks through a wall,
 * so they run in Node against the real OSM extract -- not eyeballed in a 3D
 * viewport.
 */
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import {
  toRing,
  centroid,
  pointInRing,
  buildIndex,
  insideBuilding,
  nearbyBuildings,
  nearestRoad,
  type Building,
  type Ring,
  type Road,
} from "./geometry.ts";

const here = dirname(fileURLToPath(import.meta.url));
const cityPath = resolve(here, "city.json");
const city = JSON.parse(readFileSync(cityPath, "utf8"));

const buildings: Building[] = city.buildings.map(
  (b: { p: number[]; h: number; k: string }) => {
    const ring = toRing(b.p);
    const [cx, cz] = centroid(ring);
    let radius = 0;
    for (const [x, z] of ring) radius = Math.max(radius, Math.hypot(x - cx, z - cz));
    return { ring, height: b.h, kind: b.k, cx, cz, radius };
  },
);
buildIndex(buildings);

const roads: Road[] = city.roads.map(
  (r: { p: number[]; w: number; k: string; name?: string | null }) => ({
    points: toRing(r.p),
    width: r.w,
    kind: r.k,
    name: r.name ?? null,
  }),
);

// --- primitives ------------------------------------------------------------

test("toRing pairs flat coordinates", () => {
  assert.deepEqual(toRing([1, 2, 3, 4]), [
    [1, 2],
    [3, 4],
  ]);
});

test("toRing ignores a dangling coordinate", () => {
  assert.deepEqual(toRing([1, 2, 3]), [[1, 2]]);
});

test("centroid of a square is its middle", () => {
  const [cx, cz] = centroid([
    [0, 0],
    [10, 0],
    [10, 10],
    [0, 10],
  ]);
  assert.equal(cx, 5);
  assert.equal(cz, 5);
});

test("pointInRing: inside, outside, and on the edge", () => {
  const square: Ring = [
    [0, 0],
    [10, 0],
    [10, 10],
    [0, 10],
  ];
  assert.equal(pointInRing(5, 5, square), true);
  assert.equal(pointInRing(-1, 5, square), false);
  assert.equal(pointInRing(15, 5, square), false);
  // a vertex is a degenerate case: ray casting may report either, but it must
  // not throw and must stay boolean
  assert.equal(typeof pointInRing(0, 0, square), "boolean");
});

// --- real data ------------------------------------------------------------

test("city.json parses and carries the expected breadth", () => {
  assert.ok(buildings.length > 2500, `buildings: ${buildings.length}`);
  assert.ok(roads.length > 800, `roads: ${roads.length}`);
  assert.ok(city.pois.length > 80, `pois: ${city.pois.length}`);
});

test("centroid-radius disk always contains the whole footprint", () => {
  // L-shaped buildings have their vertex-average centroid OUTSIDE the ring, so
  // "centroid is inside" is the wrong invariant. The one collision relies on
  // is stronger and correct: every vertex lies within `radius` of the centroid,
  // therefore the disk contains the polygon.
  let concave = 0;
  for (const b of buildings) {
    for (const [x, z] of b.ring) {
      const d = Math.hypot(x - b.cx, z - b.cz);
      assert.ok(d <= b.radius + 1e-6, `vertex outside radius at ${b.cx},${b.cz}`);
    }
    if (!pointInRing(b.cx, b.cz, b.ring)) concave += 1;
  }
  assert.ok(concave > 0, "expected at least one concave footprint in a real town");
});

test("insideBuilding finds a point known to be inside a footprint", () => {
  let checked = 0;
  for (const b of buildings.slice(0, 400)) {
    // a point 10% of the way from a vertex toward the centroid is inside for
    // both convex and concave simple polygons
    const [vx, vz] = b.ring[0];
    const px = vx + (b.cx - vx) * 0.1;
    const pz = vz + (b.cz - vz) * 0.1;
    if (!pointInRing(px, pz, b.ring)) continue;
    assert.notEqual(insideBuilding(px, pz), null, `missed interior point of building at ${b.cx},${b.cz}`);
    checked += 1;
  }
  assert.ok(checked > 100, `checked only ${checked}`);
});

test("points far outside any footprint are walkable", () => {
  // a handful of points 400 m from every centroid must be free
  const probes: [number, number][] = [
    [5000, 5000],
    [-5000, -5000],
    [8000, -3000],
  ];
  for (const [x, z] of probes) {
    assert.equal(insideBuilding(x, z), null, `blocked at ${x},${z}`);
  }
});

test("nearbyBuildings never misses a building that contains the point", () => {
  // sample real centroids; the coarse grid must still return their building
  for (const b of buildings.slice(0, 300)) {
    const hits = nearbyBuildings(b.cx, b.cz, 0);
    assert.ok(
      hits.some((h) => Math.abs(h.cx - b.cx) < 1e-9 && Math.abs(h.cz - b.cz) < 1e-9),
      `missed building at ${b.cx},${b.cz}`,
    );
  }
});

test("nearestRoad finds a road within 40 m of the map spawn", () => {
  const hit = nearestRoad(city.spawn.x, city.spawn.z, roads);
  assert.ok(hit, "spawn should be near a road");
  assert.ok(hit.distance < 40, `spawn is ${hit.distance.toFixed(1)} m off-road`);
});

test("spawn point is not inside a building", () => {
  assert.equal(
    insideBuilding(city.spawn.x, city.spawn.z),
    null,
    "player would spawn inside a wall",
  );
});

test("nearestRoad reports a real street name at the spawn", () => {
  const hit = nearestRoad(city.spawn.x, city.spawn.z, roads);
  assert.ok(hit?.road.name, "the square should resolve to a named street");
});

test("path filtering works", () => {
  const withPaths = nearestRoad(city.spawn.x, city.spawn.z, roads, true);
  const withoutPaths = nearestRoad(city.spawn.x, city.spawn.z, roads, false);
  assert.ok(withPaths);
  assert.ok(withoutPaths);
  assert.ok(
    withoutPaths!.distance >= withPaths!.distance - 1e-9,
    "excluding paths cannot bring a road closer",
  );
});

test("no building footprint is empty or degenerate", () => {
  for (const b of buildings) {
    assert.ok(b.ring.length >= 3, "footprint has fewer than 3 corners");
    assert.ok(b.radius > 0, "footprint has zero radius");
    assert.ok(b.height > 0, "footprint has no height");
  }
});
