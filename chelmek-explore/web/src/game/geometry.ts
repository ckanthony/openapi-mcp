/**
 * Pure geometry helpers for the Chełmek world. No JSON, no three.js -- so it
 * can be unit-tested directly from Node (`npm test`).
 *
 * Coordinate convention: local metres, x = +East, z = +South.
 * These are the numbers a player's collision actually depends on, so they are
 * covered by tests rather than trusted by eye.
 */

export type Ring = [number, number][];

export type Building = {
  ring: Ring;
  height: number;
  kind: string;
  cx: number;
  cz: number;
  radius: number;
};

export function toRing(flat: number[]): Ring {
  const ring: Ring = [];
  for (let i = 0; i + 1 < flat.length; i += 2) {
    ring.push([flat[i], flat[i + 1]]);
  }
  return ring;
}

export function centroid(ring: Ring): [number, number] {
  let sx = 0;
  let sz = 0;
  for (const [x, z] of ring) {
    sx += x;
    sz += z;
  }
  return [sx / ring.length, sz / ring.length];
}

/** Point-in-polygon (ray casting). Handles ordinary OSM footprint rings. */
export function pointInRing(x: number, z: number, ring: Ring): boolean {
  let inside = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i, i += 1) {
    const [xi, zi] = ring[i];
    const [xj, zj] = ring[j];
    const intersects =
      zi > z !== zj > z && x < ((xj - xi) * (z - zi)) / (zj - zi) + xi;
    if (intersects) inside = !inside;
  }
  return inside;
}

const GRID = 100; // metres per cell
const grid = new Map<string, Building[]>();

function key(cx: number, cz: number): string {
  return `${Math.floor(cx / GRID)}:${Math.floor(cz / GRID)}`;
}

let maxRadius = 0;

export function buildIndex(list: Building[]): void {
  grid.clear();
  maxRadius = 0;
  for (const b of list) {
    const k = key(b.cx, b.cz);
    const existing = grid.get(k);
    if (existing) existing.push(b);
    else grid.set(k, [b]);
    if (b.radius > maxRadius) maxRadius = b.radius;
  }
}


/** Buildings whose footprint may touch a circle -- for cheap collision. */
export function nearbyBuildings(x: number, z: number, r: number): Building[] {
  const out: Building[] = [];
  // Buildings are bucketed by CENTROID cell but can extend up to maxRadius
  // past it. Searching only the cells covered by `r` would miss a footprint
  // that overhangs the cell boundary -- which is exactly how a player walks
  // through a wall. Expand the window by maxRadius; the exact radius test
  // below stays authoritative.
  const reach = r + maxRadius;
  const minI = Math.floor((x - reach) / GRID);
  const maxI = Math.floor((x + reach) / GRID);
  const minJ = Math.floor((z - reach) / GRID);
  const maxJ = Math.floor((z + reach) / GRID);
  for (let i = minI; i <= maxI; i += 1) {
    for (let j = minJ; j <= maxJ; j += 1) {
      const cell = grid.get(`${i}:${j}`);
      if (!cell) continue;
      for (const b of cell) {
        if (Math.hypot(b.cx - x, b.cz - z) <= b.radius + r) out.push(b);
      }
    }
  }
  return out;
}

/** True when the point falls inside any building footprint. */
export function insideBuilding(x: number, z: number): Building | null {
  for (const b of nearbyBuildings(x, z, 0)) {
    if (pointInRing(x, z, b.ring)) return b;
  }
  return null;
}

export type Road = {
  points: Ring;
  width: number;
  kind: string;
  name: string | null;
};


export type RoadHit = { road: Road; distance: number };

/** Distance from a point to the nearest road centreline, ignoring `path`. */
export function nearestRoad(
  x: number,
  z: number,
  candidates: Road[],
  includePaths = true,
): RoadHit | null {
  let best: RoadHit | null = null;
  for (const road of candidates) {
    if (!includePaths && road.kind === "path") continue;
    const half = road.width / 2;
    for (let i = 0; i + 1 < road.points.length; i += 1) {
      const [ax, az] = road.points[i];
      const [bx, bz] = road.points[i + 1];
      const dx = bx - ax;
      const dz = bz - az;
      const len2 = dx * dx + dz * dz;
      let t = len2 === 0 ? 0 : ((x - ax) * dx + (z - az) * dz) / len2;
      t = Math.max(0, Math.min(1, t));
      const px = ax + t * dx;
      const pz = az + t * dz;
      const d = Math.hypot(x - px, z - pz) - half;
      if (!best || d < best.distance) best = { road, distance: d };
    }
  }
  return best;
}
