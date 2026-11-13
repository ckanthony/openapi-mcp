// Minimal static server for the built game (dist/).
//
// Why this exists: the Vite dev server re-transforms ~970 modules on every
// request, which is slow and fragile on a phone over a proxied preview.
// This serves the already-built, minified bundle with correct MIME types,
// long-lived caching for hashed assets, and SPA fallback. No dependencies,
// no host allowlist (so the preview proxy origin is always accepted).

import { createServer } from "node:http";
import { createReadStream, statSync } from "node:fs";
import { extname, join, normalize, resolve } from "node:path";

const ROOT = resolve(import.meta.dirname, "dist");
const PORT = Number(process.env.PORT ?? 8080);
const HOST = process.env.HOST ?? "0.0.0.0";

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".wasm": "application/wasm",
};

function resolveTarget(urlPath) {
  const clean = decodeURIComponent(urlPath.split("?")[0].split("#")[0]);
  // normalize() collapses ".." so a request cannot escape ROOT.
  const candidate = join(ROOT, normalize(clean));
  if (!candidate.startsWith(ROOT)) return null;
  try {
    const st = statSync(candidate);
    if (st.isFile()) return candidate;
  } catch {
    /* not a file — fall through */
  }
  return null;
}

const server = createServer((req, res) => {
  const method = req.method ?? "GET";
  if (method !== "GET" && method !== "HEAD") {
    res.writeHead(405, { Allow: "GET, HEAD" });
    res.end();
    return;
  }

  const requested = resolveTarget(req.url ?? "/");
  // SPA fallback: unknown paths get index.html with a 200 so deep links work.
  const file = requested ?? join(ROOT, "index.html");
  const isHtml = extname(file) === ".html";

  let size = 0;
  try {
    size = statSync(file).size;
  } catch {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
    res.end("404 — brak pliku. Najpierw uruchom: npm run build\n");
    return;
  }

  res.writeHead(200, {
    "Content-Type": MIME[extname(file)] ?? "application/octet-stream",
    "Content-Length": size,
    // Hashed asset names are immutable; index.html must never be cached or a
    // stale bundle would pin the old build after a redeploy.
    "Cache-Control": isHtml ? "no-cache" : "public, max-age=31536000, immutable",
    "X-Content-Type-Options": "nosniff",
  });

  if (method === "HEAD") {
    res.end();
    return;
  }
  createReadStream(file).pipe(res);
});

server.listen(PORT, HOST, () => {
  console.log(`game on http://${HOST}:${PORT} (static build from ${ROOT})`);
});
