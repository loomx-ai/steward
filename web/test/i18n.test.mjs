import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("locale selection normalizes browser languages and honors manual preference", async () => {
  const { normalizePreference, resolveLocale } =
    await import("../src/i18n/locales.ts");
  assert.equal(resolveLocale("auto", ["zh_Hans_CN", "en-US"]), "zh-CN");
  assert.equal(resolveLocale("auto", ["zh-TW", "en-US"]), "en-US");
  assert.equal(resolveLocale("auto", ["fr-FR"]), "en-US");
  assert.equal(resolveLocale("en-US", ["zh-CN"]), "en-US");
  assert.equal(normalizePreference("zh-CN"), "zh-CN");
  assert.equal(normalizePreference("invalid"), "auto");
  assert.equal(normalizePreference(null), "auto");
});

test("Chinese and English dictionaries have identical keys and interpolate values", async () => {
  const { messages, translate, translateCode } =
    await import("../src/i18n/messages.ts");
  assert.deepEqual(
    Object.keys(messages["zh-CN"]).sort(),
    Object.keys(messages["en-US"]).sort(),
  );
  assert.equal(
    translate(messages["zh-CN"], "common.count", { count: 12 }),
    "共 12 项",
  );
  assert.equal(
    translate(messages["en-US"], "common.count", { count: 12 }),
    "12 items",
  );
  assert.equal(
    translateCode("zh-CN", "scan_coverage_incomplete", "fallback"),
    "扫描覆盖不完整，不能安全清理所选范围。",
  );
  assert.equal(
    translateCode("en-US", "unknown.code", "provider diagnostic"),
    "provider diagnostic",
  );
  for (const value of [
    "account",
    "region",
    "global",
    "connection",
    "scope",
    "group",
    "asset",
    "controller",
    "critical",
    "open",
    "complete",
    "incomplete",
    "delegated_delete",
    "delete_with_ros_stack",
    "retain_shared",
    "deleted_by_controller",
    "still_present",
    "scan_coverage_incomplete",
    "cross_scope_dependency",
  ]) {
    assert.ok(`domain.${value}` in messages["zh-CN"], value);
    assert.notEqual(messages["zh-CN"][`domain.${value}`], value, value);
  }
});

test("application shell and settings use the locale provider", () => {
  const main = read("../src/main.tsx");
  const sidebar = read("../src/app/AppSidebar.tsx");
  const routes = read("../src/routes.tsx");
  const settings = read("../src/features/settings/SettingsView.tsx");
  const routeError = read("../src/app/RouteErrorBoundary.tsx");
  const planDetail = read("../src/features/cleanup/CleanupTaskDetail.tsx");
  const topologyCanvas = read("../src/features/panorama/TopologyCanvas.tsx");
  const graph = read("../src/features/graph/GraphView.tsx");
  const lifecycle = read("../src/features/lifecycle/LifecyclePanel.tsx");

  assert.match(main, /LocaleProvider/);
  assert.match(sidebar, /useLocale/);
  assert.match(sidebar, /\/settings/);
  assert.match(routes, /path="settings"/);
  assert.match(settings, /setPreference/);
  assert.match(settings, /createConnection/);
  assert.match(settings, /replaceConnectionCredential/);
  assert.match(routeError, /formatError/);
  assert.match(planDetail, /messageForCode/);
  assert.match(planDetail, /label\((?:row\.action|displayAction)\)/);
  assert.match(topologyCanvas, /useLocale/);
  assert.match(graph, /label\(relationship\.type\)/);
  assert.match(lifecycle, /label\(expected\)/);
  assert.doesNotMatch(
    sidebar,
    /label:\s*"(?:Assets|Connections|Scans|Cleanup|Executions|Findings|Audit)"/,
  );
});
