import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "./vite-single-file-plugin.mjs";

// Separate config for the offline single-file build.
//
// The normal build splits the game into a lazy chunk, which is right for a
// served site (the launcher stays small). In a single .html file there is no
// origin to fetch a chunk from, so code splitting has to be off entirely --
// regex-patching the hashed chunk imports after the fact does not work,
// because the chunk also imports back into the main bundle.
export default defineConfig({
  plugins: [react(), viteSingleFile()],
  base: "./",
  build: {
    outDir: "dist-single",
    sourcemap: false,
    cssCodeSplit: false,
    // One file, one chunk: no dynamic imports left to resolve at runtime.
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
        entryFileNames: "all.js",
        assetFileNames: "all.[ext]",
      },
    },
    chunkSizeWarningLimit: 4000,
  },
  css: {
    preprocessorOptions: {
      scss: {
        silenceDeprecations: ["legacy-js-api", "import", "global-builtin"],
        quietDeps: true,
      },
    },
  },
});
