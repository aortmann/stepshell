// Builds the SPA into dist/ for Go embedding. esbuild bundles JS/CSS; the
// xterm stylesheet and index.html are copied through.
import * as esbuild from "esbuild";
import { copyFileSync, mkdirSync, rmSync } from "node:fs";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const dist = `${root}/dist`;
const assets = `${dist}/assets`;

rmSync(dist, { recursive: true, force: true });
mkdirSync(assets, { recursive: true });

const watch = process.argv.includes("--watch");

const ctx = await esbuild.context({
  entryPoints: {
    app: `${root}/src/main.ts`,
  },
  bundle: true,
  format: "esm",
  target: "es2020",
  sourcemap: watch,
  minify: !watch,
  outdir: assets,
  entryNames: "[name]",
  loader: { ".css": "css" },
  logLevel: "info",
});

async function copyStatic() {
  copyFileSync(`${root}/index.html`, `${dist}/index.html`);
  copyFileSync(`${root}/src/app.css`, `${assets}/app.css`);
  copyFileSync(`${root}/node_modules/@xterm/xterm/css/xterm.css`, `${assets}/xterm.css`);
}

await ctx.rebuild();
await copyStatic();

if (watch) {
  await ctx.watch();
  console.log("watching…");
} else {
  await ctx.dispose();
}
