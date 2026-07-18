import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("panorama is the default route and topology remains available", () => {
  const routes = read("../src/routes.tsx");
  const client = read("../src/api/client.ts");
  assert.match(routes, /Navigate to="\/panorama"/);
  assert.match(routes, /path="panorama"/);
  assert.match(client, /\/api\/topology/);
});
