/**
 * Third-person walker over the real Chełmek footprints.
 *
 * Deliberately small and dependency-light: three.js + the OSM extract.
 * Everything the player sees is either a real OSM footprint, a real road
 * centreline, or a real park/water polygon -- no invented streets.
 */
import * as THREE from "three";
import {
  buildings,
  roads,
  greens,
  lakes,
  water,
  rails,
  pois,
  insideBuilding,
  nearestRoad,
  extent,
  spawn,
  type Poi,
} from "./world";

export type GameStats = {
  fps: number;
  x: number;
  z: number;
  street: string;
  heading: number;
};

export type GameHandle = {
  dispose: () => void;
  setInput: (input: Input) => void;
  teleportToPoi: (poi: Poi) => void;
  getStats: () => GameStats;
  isReady: () => boolean;
};

export type Input = {
  forward: number; // -1..1
  strafe: number; // -1..1
  run: boolean;
  look: number; // radians per frame from drag
};

const EYE_HEIGHT = 1.7;
const WALK_SPEED = 4.2;
const RUN_SPEED = 8.5;
const RADIUS = 0.45;

function ringShape(ring: [number, number][], target: THREE.Shape | THREE.Path) {
  ring.forEach(([x, z], i) => {
    if (i === 0) target.moveTo(x, z);
    else target.lineTo(x, z);
  });
  target.closePath();
  return target;
}

// A phone at devicePixelRatio 3 on a full-screen canvas asks the GPU to fill
// up to ~13 million pixels per frame. That is what turns a working scene into
// a black screen on mobile, so the ratio is capped hard and phones get less.
function pixelRatioCap(): number {
  const dpr = window.devicePixelRatio || 1;
  const small = Math.min(window.innerWidth, window.innerHeight) < 820;
  return Math.min(dpr, small ? 1.25 : 1.75);
}

export function createGame(container: HTMLElement): GameHandle {
  const renderer = new THREE.WebGLRenderer({
    // MSAA on a high-DPI phone screen is expensive and barely visible; the
    // pixel ratio already delivers most of the perceived sharpness.
    antialias: (window.devicePixelRatio || 1) < 2,
    powerPreference: "high-performance",
    // Do not fail hard when the browser hands out a weaker context.
    failIfMajorPerformanceCaveat: false,
  });
  renderer.setPixelRatio(pixelRatioCap());
  renderer.setSize(container.clientWidth, container.clientHeight);
  renderer.shadowMap.enabled = false;
  container.appendChild(renderer.domElement);

  const scene = new THREE.Scene();
  const SKY = new THREE.Color("#0b1220");
  scene.background = SKY;
  scene.fog = new THREE.Fog(SKY, 180, 620);

  const camera = new THREE.PerspectiveCamera(
    62,
    container.clientWidth / container.clientHeight,
    0.1,
    1400,
  );

  // --- lighting: a low, warm late-afternoon sun over Małopolska ----------
  const hemi = new THREE.HemisphereLight("#9fc4ff", "#2a2418", 1.15);
  scene.add(hemi);

  const sun = new THREE.DirectionalLight("#ffd9a8", 2.1);
  sun.position.set(-260, 320, -180);
  scene.add(sun);

  // --- ground -----------------------------------------------------------
  const spanX = extent.maxX - extent.minX;
  const spanZ = extent.maxZ - extent.minZ;
  const groundGeo = new THREE.PlaneGeometry(spanX + 2400, spanZ + 2400, 1, 1);
  const groundMat = new THREE.MeshStandardMaterial({
    color: "#1d2a20",
    roughness: 1,
    metalness: 0,
  });
  const ground = new THREE.Mesh(groundGeo, groundMat);
  ground.rotation.x = -Math.PI / 2;
  ground.position.set(
    (extent.minX + extent.maxX) / 2,
    -0.02,
    (extent.minZ + extent.maxZ) / 2,
  );
  ground.receiveShadow = false;
  scene.add(ground);

  // --- ground detail: ONE merged vertex-coloured buffer -----------------
  // Everything flat that is not a building goes into a single geometry.
  // This is the difference between the game running and a black screen on a
  // phone: drawing each of the 6945 road segments as its own mesh meant 7293
  // draw calls per frame, and mobile GPUs choke long before that.
  const flatPositions: number[] = [];
  const flatColors: number[] = [];
  const flatPush = (x: number, y: number, z: number, c: THREE.Color) => {
    flatPositions.push(x, y, z);
    flatColors.push(c.r, c.g, c.b);
  };

  const C_GARDEN = new THREE.Color("#33452c");
  const C_FOREST = new THREE.Color("#243424");
  const C_PITCH = new THREE.Color("#2f4a33");
  const C_LAKE = new THREE.Color("#173247");
  const C_RIVER = new THREE.Color("#1b3a52");
  const C_MAIN = new THREE.Color("#3b3f44");
  const C_STREET = new THREE.Color("#33373c");
  const C_SERVICE = new THREE.Color("#2b2f33");
  const C_PATH = new THREE.Color("#3a3527");
  const C_RAIL = new THREE.Color("#5a5348");

  const ROAD_COLOR: Record<string, THREE.Color> = {
    main: C_MAIN,
    street: C_STREET,
    service: C_SERVICE,
    path: C_PATH,
  };

  // Triangulate a flat ring with THREE.Shape, then bake it into the buffer.
  // Y is deliberately different per layer so nothing z-fights.
  const bakeRing = (ring: [number, number][], y: number, colour: THREE.Color) => {
    if (ring.length < 3) return;
    const shape = new THREE.Shape();
    ringShape(ring, shape);
    const geo = new THREE.ShapeGeometry(shape);
    const pos = geo.getAttribute("position");
    // ShapeGeometry uses (x, y); our plane is (x, z) with the ring already in
    // map coordinates, so y from the shape becomes world z directly.
    for (let i = 0; i < pos.count; i += 1) {
      flatPush(pos.getX(i), y, pos.getY(i), colour);
    }
    geo.dispose();
  };

  // A quad between two points, `width` wide, laid flat on the ground.
  const bakeSegment = (ax: number, az: number, bx: number, bz: number, width: number, y: number, colour: THREE.Color) => {
    const dx = bx - ax;
    const dz = bz - az;
    const len = Math.hypot(dx, dz);
    if (len < 0.05) return;
    const nx = (dz / len) * (width / 2);
    const nz = (-dx / len) * (width / 2);
    const x1 = ax + nx, z1 = az + nz;
    const x2 = ax - nx, z2 = az - nz;
    const x3 = bx - nx, z3 = bz - nz;
    const x4 = bx + nx, z4 = bz + nz;
    flatPush(x1, y, z1, colour);
    flatPush(x2, y, z2, colour);
    flatPush(x3, y, z3, colour);
    flatPush(x1, y, z1, colour);
    flatPush(x3, y, z3, colour);
    flatPush(x4, y, z4, colour);
  };

  for (const g of greens) {
    bakeRing(g.ring, -0.01, g.kind === "forest" ? C_FOREST : g.kind === "pitch" ? C_PITCH : C_GARDEN);
  }
  for (const lake of lakes) {
    bakeRing(lake, -0.005, C_LAKE);
  }
  for (const river of water) {
    for (let i = 0; i + 1 < river.points.length; i += 1) {
      const [ax, az] = river.points[i];
      const [bx, bz] = river.points[i + 1];
      bakeSegment(ax, az, bx, bz, 6, 0.005, C_RIVER);
    }
  }
  for (const road of roads) {
    const colour = ROAD_COLOR[road.kind] ?? C_STREET;
    const width = Math.max(road.width, 1);
    for (let i = 0; i + 1 < road.points.length; i += 1) {
      const [ax, az] = road.points[i];
      const [bx, bz] = road.points[i + 1];
      bakeSegment(ax, az, bx, bz, width, 0.012, colour);
    }
  }
  for (const rail of rails) {
    for (let i = 0; i + 1 < rail.length; i += 1) {
      const [ax, az] = rail[i];
      const [bx, bz] = rail[i + 1];
      bakeSegment(ax, az, bx, bz, 2.6, 0.02, C_RAIL);
    }
  }

  const flatGeo = new THREE.BufferGeometry();
  flatGeo.setAttribute("position", new THREE.Float32BufferAttribute(flatPositions, 3));
  flatGeo.setAttribute("color", new THREE.Float32BufferAttribute(flatColors, 3));
  flatGeo.computeBoundingSphere();
  // One material, no lighting: these are flat ground decals, all detail comes
  // from vertex colour. Basic is also much cheaper than Standard on mobile.
  const flatMat = new THREE.MeshBasicMaterial({ vertexColors: true });
  const flatMesh = new THREE.Mesh(flatGeo, flatMat);
  flatMesh.frustumCulled = true;
  scene.add(flatMesh);

  // --- buildings: one merged buffer per material, 3 palettes ------------
  // Merging matters: 2731 separate meshes would cost thousands of draw calls.
  const palettes = [
    { color: "#8d8175", roof: "#4a4038" }, // plaster / old brick
    { color: "#6f6a63", roof: "#3a352f" }, // concrete
    { color: "#96806a", roof: "#503f30" }, // brick
  ];

  const positions: number[] = [];
  const normals: number[] = [];
  const colorValues: number[] = [];
  const tmpColor = new THREE.Color();

  for (const b of buildings) {
    const h = Math.max(b.height, 3);
    const palette = palettes[Math.abs(Math.floor(b.cx + b.cz)) % palettes.length];

    // walls
    for (let i = 0; i < b.ring.length; i += 1) {
      const [x1, z1] = b.ring[i];
      const [x2, z2] = b.ring[(i + 1) % b.ring.length];
      const dx = x2 - x1;
      const dz = z2 - z1;
      const len = Math.hypot(dx, dz);
      if (len < 0.01) continue;
      const nx = dz / len;
      const nz = -dx / len;

      // two triangles, wound so the normal points outward
      positions.push(x1, 0, z1, x2, 0, z2, x2, h, z2);
      positions.push(x1, 0, z1, x2, h, z2, x1, h, z1);
      for (let k = 0; k < 6; k += 1) normals.push(nx, 0, nz);

      tmpColor.set(palette.color);
      // slight per-building variation so the city is not one flat tone
      const wobble = ((Math.abs(b.cx * 7 + b.cz * 13) % 100) / 100 - 0.5) * 0.16;
      tmpColor.offsetHSL(0, 0, wobble);
      for (let k = 0; k < 6; k += 1) colorValues.push(tmpColor.r, tmpColor.g, tmpColor.b);
    }

    // roof
    const roof = tmpColor.clone().set(palette.roof);
    const ring = b.ring;
    if (ring.length >= 3) {
      for (let i = 1; i + 1 < ring.length; i += 1) {
        const [x0, z0] = ring[0];
        const [x1, z1] = ring[i];
        const [x2, z2] = ring[i + 1];
        positions.push(x0, h, z0, x1, h, z1, x2, h, z2);
        for (let k = 0; k < 3; k += 1) normals.push(0, 1, 0);
        for (let k = 0; k < 3; k += 1) colorValues.push(roof.r, roof.g, roof.b);
      }
    }
  }

  const cityGeo = new THREE.BufferGeometry();
  cityGeo.setAttribute("position", new THREE.Float32BufferAttribute(positions, 3));
  cityGeo.setAttribute("normal", new THREE.Float32BufferAttribute(normals, 3));
  cityGeo.setAttribute("color", new THREE.Float32BufferAttribute(colorValues, 3));
  cityGeo.computeBoundingSphere();

  const cityMat = new THREE.MeshStandardMaterial({
    vertexColors: true,
    roughness: 0.88,
    metalness: 0.02,
  });
  scene.add(new THREE.Mesh(cityGeo, cityMat));

  // --- POI markers: a slim emissive pillar per real POI ------------------
  const poiPositions: number[] = [];
  const poiColors: number[] = [];
  const poiPalette: Record<string, THREE.Color> = {
    shop: new THREE.Color("#f2a33c"),
    convenience: new THREE.Color("#f2a33c"),
    church: new THREE.Color("#c9b8ff"),
    civic: new THREE.Color("#5fd0e8"),
    school: new THREE.Color("#8fd96a"),
    kindergarten: new THREE.Color("#8fd96a"),
    doctors: new THREE.Color("#ff7b8a"),
    pharmacy: new THREE.Color("#ff7b8a"),
    restaurant: new THREE.Color("#ffb26b"),
    cafe: new THREE.Color("#ffb26b"),
    stop: new THREE.Color("#b8c4d0"),
    monument: new THREE.Color("#ffe08a"),
    attraction: new THREE.Color("#ffe08a"),
    memorial: new THREE.Color("#ffe08a"),
    industrial: new THREE.Color("#7c8ba1"),
    station: new THREE.Color("#b8c4d0"),
  };
  const defaultPoiColor = new THREE.Color("#9fb2c4");

  for (const p of pois) {
    const colour = poiPalette[p.kind] ?? defaultPoiColor;
    const w = 0.7;
    const h = 11;
    // two crossed quads so the marker reads from any angle
    const quads: number[][] = [
      [p.x - w, 0, p.z, p.x + w, 0, p.z, p.x + w, h, p.z, p.x - w, 0, p.z, p.x + w, h, p.z, p.x - w, h, p.z],
      [p.x, 0, p.z - w, p.x, 0, p.z + w, p.x, h, p.z + w, p.x, 0, p.z - w, p.x, h, p.z + w, p.x, h, p.z - w],
    ];
    for (const q of quads) {
      for (let i = 0; i < q.length; i += 3) {
        poiPositions.push(q[i], q[i + 1], q[i + 2]);
        poiColors.push(colour.r, colour.g, colour.b);
      }
    }
  }

  const poiGeo = new THREE.BufferGeometry();
  poiGeo.setAttribute("position", new THREE.Float32BufferAttribute(poiPositions, 3));
  poiGeo.setAttribute("color", new THREE.Float32BufferAttribute(poiColors, 3));
  const poiMat = new THREE.MeshBasicMaterial({
    vertexColors: true,
    transparent: true,
    opacity: 0.62,
    side: THREE.DoubleSide,
    depthWrite: false,
  });
  scene.add(new THREE.Mesh(poiGeo, poiMat));

  // --- player state -----------------------------------------------------
  const state = {
    x: spawn.x,
    z: spawn.z,
    heading: Math.PI, // facing "into" the town from the square
    bob: 0,
  };

  let input: Input = { forward: 0, strafe: 0, run: false, look: 0 };
  let disposed = false;
  let ready = true;

  const stats: GameStats = { fps: 0, x: state.x, z: state.z, street: "", heading: 0 };
  let frames = 0;
  let lastFpsAt = performance.now();

  function collide(nx: number, nz: number) {
    const hit = insideBuilding(nx, nz);
    if (hit) return;
    state.x = nx;
    state.z = nz;
  }

  function step(dt: number) {
    state.heading += input.look;

    const speed = input.run ? RUN_SPEED : WALK_SPEED;
    const sin = Math.sin(state.heading);
    const cos = Math.cos(state.heading);

    // forward vector for heading=0 is -z (north)
    const fx = -sin;
    const fz = -cos;
    const rx = cos;
    const rz = -sin;

    const moveX = (fx * input.forward + rx * input.strafe) * speed * dt;
    const moveZ = (fz * input.forward + rz * input.strafe) * speed * dt;

    if (moveX !== 0) collide(state.x + moveX, state.z);
    if (moveZ !== 0) collide(state.x, state.z + moveZ);

    const moving = Math.hypot(moveX, moveZ) > 0.0005;
    if (moving) state.bob += dt * (input.run ? 15 : 9);

    const bob = Math.sin(state.bob) * (moving ? 0.055 : 0);

    camera.position.set(state.x, EYE_HEIGHT + bob, state.z);
    const dir = new THREE.Vector3(
      Math.sin(state.heading) * Math.cos(0),
      0,
      Math.cos(state.heading) * Math.cos(0),
    );
    camera.lookAt(
      state.x - dir.x * 10,
      EYE_HEIGHT + bob - 0.25,
      state.z - dir.z * 10,
    );

    // stats
    frames += 1;
    const now = performance.now();
    if (now - lastFpsAt > 500) {
      stats.fps = Math.round((frames * 1000) / (now - lastFpsAt));
      frames = 0;
      lastFpsAt = now;
    }
    stats.x = state.x;
    stats.z = state.z;
    stats.heading = state.heading;
    const roadHit = nearestRoad(state.x, state.z);
    stats.street = roadHit?.road.name ?? "poza jezdnią";
  }

  let lastTime = performance.now();
  let raf = 0;

  function loop() {
    if (disposed) return;
    const now = performance.now();
    const dt = Math.min((now - lastTime) / 1000, 0.05);
    lastTime = now;

    step(dt);
    renderer.render(scene, camera);
    raf = requestAnimationFrame(loop);
  }
  raf = requestAnimationFrame(loop);

  function onResize() {
    const w = container.clientWidth;
    const h = container.clientHeight;
    if (w === 0 || h === 0) return;
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    renderer.setPixelRatio(pixelRatioCap());
    renderer.setSize(w, h);
  }
  const ro = new ResizeObserver(onResize);
  ro.observe(container);

  return {
    dispose() {
      disposed = true;
      cancelAnimationFrame(raf);
      ro.disconnect();
      cityGeo.dispose();
      cityMat.dispose();
      flatGeo.dispose();
      flatMat.dispose();
      poiGeo.dispose();
      poiMat.dispose();
      groundGeo.dispose();
      groundMat.dispose();
      renderer.dispose();
      if (renderer.domElement.parentElement === container) {
        container.removeChild(renderer.domElement);
      }
    },
    setInput(next: Input) {
      input = next;
    },
    teleportToPoi(poi: Poi) {
      // land on the street outside the footprint, never inside a wall
      let ox = 0;
      let oz = 0;
      for (let i = 0; i < 24; i += 1) {
        const a = (i / 24) * Math.PI * 2;
        const cx = poi.x + Math.cos(a) * 9;
        const cz = poi.z + Math.sin(a) * 9;
        if (!insideBuilding(cx, cz)) {
          ox = cx;
          oz = cz;
          break;
        }
      }
      state.x = poi.x + ox;
      state.z = poi.z + oz;
      state.heading = Math.atan2(-(poi.x - state.x), -(poi.z - state.z));
    },
    getStats() {
      return stats;
    },
    isReady() {
      return ready;
    },
  };
}
