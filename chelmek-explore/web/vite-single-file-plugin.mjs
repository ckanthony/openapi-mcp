// Inlines the single output chunk and stylesheet into dist-single/index.html.
//
// Used only by vite.singlefile.config.ts. Kept as a plain .mjs so it needs no
// build step of its own.

import { readFileSync, writeFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";

export function viteSingleFile() {
  let outDir = "dist-single";
  let root = process.cwd();

  return {
    name: "inline-single-file",
    apply: "build",
    configResolved(config) {
      outDir = config.build.outDir;
      root = config.root;
    },
    // Runs after the bundle is written, which is when the files exist on disk.
    closeBundle() {
      const dir = resolve(root, outDir);
      const htmlPath = join(dir, "index.html");
      let html = readFileSync(htmlPath, "utf8");
      const files = readdirSync(dir);

      // Stylesheets first: they are plain <link> tags.
      for (const css of files.filter((f) => f.endsWith(".css"))) {
        const code = readFileSync(join(dir, css), "utf8");
        html = html.replace(
          new RegExp(`<link[^>]*href="[^"]*${escape(css)}"[^>]*>`),
          // A FUNCTION replacement, never a string: the bundle is full of `$`
          // sequences ($&, $', $1) that String.replace would expand as capture
          // groups and silently corrupt the code.
          () => `<style>\n${code}\n</style>`,
        );
      }

      // Then the single JS chunk, as a module script.
      for (const js of files.filter((f) => f.endsWith(".js"))) {
        const code = readFileSync(join(dir, js), "utf8");
        // A literal </script> inside the bundle would close the tag early.
        const safe = code.replace(/<\/script>/gi, "<\\/script>");
        html = html.replace(
          new RegExp(`<script[^>]*src="[^"]*${escape(js)}"[^>]*></script>`),
          () => `<script type="module">\n${safe}\n</script>`,
        );
      }

      writeFileSync(htmlPath, html, "utf8");
      console.log(`inlined ${files.length} assets into ${htmlPath}`);
    },
  };
}

function escape(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
