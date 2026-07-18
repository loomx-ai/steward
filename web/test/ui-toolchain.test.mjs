import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("web toolchain is configured for shadcn ui, Tailwind 4, and Vitest", () => {
  const packageJSON = JSON.parse(read("../package.json"));
  const tsconfig = JSON.parse(read("../tsconfig.json"));
  const vite = read("../vite.config.ts");
  const componentsURL = new URL("../components.json", import.meta.url);

  assert.equal(packageJSON.scripts["test:contracts"], "node --test test/*.test.mjs");
  assert.equal(packageJSON.scripts["test:ui"], "vitest run");
  assert.equal(
    packageJSON.scripts.test,
    "npm run test:contracts && npm run test:ui",
  );
  assert.match(packageJSON.dependencies.tailwindcss, /^\^?4\./);
  assert.ok(packageJSON.dependencies["@tailwindcss/vite"]);
  assert.ok(packageJSON.devDependencies.vitest);
  assert.deepEqual(tsconfig.compilerOptions.paths, { "@/*": ["./src/*"] });
  assert.match(vite, /tailwindcss\(\)/);
  assert.match(vite, /path\.resolve\(__dirname, "\.\/src"\)/);
  assert.match(vite, /include:\s*\["src\/\*\*\/\*\.test\.\{ts,tsx\}"\]/);
  assert.match(vite, /environment:\s*"jsdom"/);
  assert.equal(existsSync(componentsURL), true, "components.json must exist");
});
