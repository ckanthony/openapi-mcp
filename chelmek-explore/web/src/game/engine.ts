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

export function createGame(container: HTMLElement): GameHandle {
  const renderer = new THREE.WebGLRenderer({
    antialias: true,
    powerPreference: "high-performance",
  });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
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

  // --- greens (parks, forest, grass) -----------------------------------
  const gardenMat = new THREE.MeshStandardMaterial({ color: "#33452c", roughness: 1 });
  const forestMat = new THREE.MeshStandardMaterial({ color: "#243424", roughness: 1 });
  const pitchMat = new THREE.MeshStandardMaterial({ color: "#2f4a33", roughness: 1 });

  for (const g of greens) {
    if (g.ring.length < 3) continue;
    const shape = new THREE.Shape();
    ringShape(g.ring, shape);
    const geo = new THREE.ShapeGeometry(shape);
    const mat = g.kind === "forest" ? forestMat : g.kind === "pitch" ? pitchMat : gardenMat;
    const mesh = new THREE.Mesh(geo, mat);
    mesh.rotation.x = Math.PI / 2;
    mesh.position.y = -0.01;
    scene.add(mesh);
  }

  // --- water: lakes filled, rivers as ribbons ---------------------------
  const waterMat = new THREE.MeshStandardMaterial({
    color: "#173247",
    roughness: 0.18,
    metalness: 0.35,
  });
  for (const lake of lakes) {
    if (lake.length < 3) continue;
    const shape = new THREE.Shape();
    ringShape(lake, shape);
    const mesh = new THREE.Mesh(new THREE.ShapeGeometry(shape), waterMat);
    mesh.rotation.x = Math.PI / 2;
    mesh.position.y = -0.005;
    scene.add(mesh);
  }
  for (const river of water) {
    if (river.points.length < 2) continue;
    const curve = new THREE.CatmullRomCurve3(
      river.points.map(([x, z]) => new THREE.Vector3(x, 0, z)),
      false,
      "catmullrom",
      0.2,
    );
    const geo = new THREE.TubeGeometry(curve, river.points.length * 2, 6, 3, false);
    const mesh = new THREE.Mesh(geo, waterMat);
    mesh.scale.y = 0.02;
    mesh.position.y = 0.1;
    scene.add(mesh);
  }

  // --- roads ------------------------------------------------------------
  const roadMats: Record<string, THREE.MeshStandardMaterial> = {
    main: new THREE.MeshStandardMaterial({ color: "#3b3f44", roughness: 0.95 }),
    street: new THREE.MeshStandardMaterial({ color: "#33373c", roughness: 0.95 }),
    service: new THREE.MeshStandardMaterial({ color: "#2b2f33", roughness: 1 }),
    path: new THREE.MeshStandardMaterial({ color: "#3a3527", roughness: 1 }),
  };

  for (const road of roads) {
    if (road.points.length < 2) continue;
    const mat = roadMats[road.kind] ?? roadMats.street;
    const half = Math.max(road.width, 1) / 2;

    for (let i = 0; i + 1 < road.points.length; i += 1) {
      const [ax, az] = road.points[i];
      const [bx, bz] = road.points[i + 1];
      const len = Math.hypot(bx - ax, bz - az);
      if (len < 0.05) continue;
      const geo = new THREE.PlaneGeometry(half * 2, len);
      const mesh = new THREE.Mesh(geo, mat);
      mesh.rotation.x = -Math.PI / 2;
      mesh.rotation.z = Math.atan2(bx - ax, bz - az);
      mesh.position.set((ax + bx) / 2, 0.012, (az + bz) / 2);
      scene.add(mesh);
    }
  }

  // --- rail line --------------------------------------------------------
  const railMat = new THREE.MeshStandardMaterial({ color: "#5a5348", roughness: 0.8 });
  for (const rail of rails) {
    for (let i = 0; i + 1 < rail.length; i += 1) {
      const [ax, az] = rail[i];
      const [bx, bz] = rail[i + 1];
      const len = Math.hypot(bx - ax, bz - az);
      if (len < 0.05) continue;
      const mesh = new THREE.Mesh(new THREE.PlaneGeometry(2.6, len), railMat);
      mesh.rotation.x = -Math.PI / 2;
      mesh.rotation.z = Math.atan2(bx - ax, bz - az);
      mesh.position.set((ax + bx) / 2, 0.02, (az + bz) / 2);
      scene.add(mesh);
    }
  }

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
