// Compila la SPA con esbuild: JSX -> un solo bundle minificado + CSS en dist/.
import { build } from "esbuild";
import { copyFileSync, mkdirSync, rmSync } from "node:fs";

rmSync("dist", { recursive: true, force: true });
mkdirSync("dist/assets", { recursive: true });
await build({
  entryPoints: ["src/main.jsx"],
  bundle: true,
  minify: true,
  sourcemap: false,
  target: ["es2020"],
  jsx: "automatic",
  outfile: "dist/assets/app.js",
  define: { "process.env.NODE_ENV": '"production"' },
  logLevel: "info",
});
copyFileSync("index.html", "dist/index.html");
copyFileSync("favicon.svg", "dist/favicon.svg");
console.log("SPA compilada en dist/");
