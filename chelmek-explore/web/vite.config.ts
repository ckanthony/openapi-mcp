import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The launcher is a plain static site: no backend calls, so there is no
// localhost reference anywhere in the bundle (Vercel requirement, W12).
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "dist",
    sourcemap: true,
    // A single budgeted chunk: the launcher is a landing page, and splitting
    // Carbon's CSS/Sass output across chunks only adds requests.
    chunkSizeWarningLimit: 1200,
  },
  css: {
    preprocessorOptions: {
      scss: {
        // Carbon ships Sass with modern @use semantics; silence the legacy
        // @import deprecation noise from its own internals only.
        silenceDeprecations: ["legacy-js-api", "import", "global-builtin"],
        quietDeps: true,
      },
    },
  },
  server: {
    host: "0.0.0.0",
    port: 8080,
    // The Arena preview proxy serves the app from a *.e2b.app origin.
    allowedHosts: true,
  },
  preview: {
    host: "0.0.0.0",
    port: 8080,
    allowedHosts: true,
  },
});
