import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const exists = (path) => existsSync(new URL(path, import.meta.url));

const collectionRoutes = [
  "../src/features/assets/AssetsView.tsx",
  "../src/features/scans/ScansView.tsx",
  "../src/features/cleanup/CleanupView.tsx",
  "../src/features/executions/ExecutionsView.tsx",
  "../src/features/findings/FindingsView.tsx",
  "../src/features/audits/AuditsView.tsx",
];

test("panorama owns the canvas while data routes use full-width layouts", () => {
  const panorama = read("../src/features/panorama/PanoramaView.tsx");
  const pageLayout = read("../src/components/patterns/PageLayout.tsx");
  assert.match(panorama, /<PageLayout mode="canvas"/);
  assert.doesNotMatch(pageLayout, /max-w-\[/);
  assert.match(panorama, /<Toolbar/);
  assert.match(panorama, /className="relative min-h-0 flex-1"/);
  assert.doesNotMatch(panorama, /relative min-h-\[32rem\]/);
  assert.doesNotMatch(panorama, /<Card className="relative h-/);
  assert.doesNotMatch(panorama, /<PageHeader/);
  assert.match(panorama, /absolute right-20 bottom-6 left-6/);
  assert.match(panorama, /lg:right-6/);
});

test("every collection route uses the shared full-width list layout", () => {
  for (const path of collectionRoutes) {
    const source = read(path);
    assert.match(source, /<PageLayout mode="list"/, path);
    assert.doesNotMatch(source, /className="flex min-h-full flex-col"/, path);
    assert.doesNotMatch(source, /<div className="p-5 sm:p-7">/, path);
  }
});

test("collection routes do not repeat the application title", () => {
  for (const path of collectionRoutes) {
    const source = read(path);
    assert.doesNotMatch(source, /PageHeader/, path);
    assert.doesNotMatch(source, /<h1/, path);
  }
  assert.doesNotMatch(
    read("../src/features/settings/SettingsView.tsx"),
    /description=\{t\("settings\.detail"\)\}/,
  );
});

test("the authenticated shell owns the only route-level h1", () => {
  assert.equal(exists("../src/components/domain/PageHeader.tsx"), false);
  assert.match(read("../src/app/AppHeader.tsx"), /<h1/);
  for (const path of [
    "../src/app/AppShell.tsx",
    "../src/app/ConnectionGate.tsx",
    "../src/app/RouteErrorBoundary.tsx",
  ]) {
    const source = read(path);
    assert.doesNotMatch(source, /<h1/, path);
    assert.match(source, /<h2/, path);
  }
  assert.match(read("../src/auth/LoginView.tsx"), /<h1/);
});

test("interactive table rows keep native row semantics and cell controls", () => {
  const table = read("../src/components/ui/table.tsx");
  assert.match(table, /aria-selected:bg-muted/);

  for (const path of [
    "../src/features/executions/ExecutionsView.tsx",
    "../src/features/findings/FindingsView.tsx",
  ]) {
    const source = read(path);
    assert.match(source, /className="group cursor-pointer"/, path);
  }
  for (const path of [
    "../src/features/executions/ExecutionsView.tsx",
    "../src/features/findings/FindingsView.tsx",
    "../src/features/audits/AuditsView.tsx",
  ]) {
    const source = read(path);
    assert.doesNotMatch(source, /tabIndex=\{0\}/, path);
    assert.doesNotMatch(source, /onKeyDown=/, path);
  }

  const assets = read("../src/features/assets/AssetsView.tsx");
  assert.doesNotMatch(assets, /className="group cursor-pointer"/);
  assert.doesNotMatch(assets, /onOpen\(\)/);
  assert.match(assets, /font-medium text-info underline-offset-4/);
  assert.doesNotMatch(assets, /data-row-inspector-trigger/);
  assert.match(assets, /<Checkbox/);
  assert.match(assets, /onClick=\{\(event\) => event\.stopPropagation\(\)\}/);

  const tasks = read("../src/features/cleanup/CleanupView.tsx");
  assert.match(tasks, /<TableRow key=\{task\.id\}>/);
  assert.match(
    tasks,
    /to=\{`\/cleanup\/\$\{encodeURIComponent\(task\.id\)\}`\}/,
  );
  const taskRow = tasks.match(
    /<TableRow key=\{task\.id\}>[\s\S]*?<\/TableRow>/,
  )?.[0];
  assert.ok(taskRow);
  assert.doesNotMatch(taskRow, /onClick=/);
  for (const path of [
    "../src/features/executions/ExecutionsView.tsx",
    "../src/features/findings/FindingsView.tsx",
    "../src/features/audits/AuditsView.tsx",
  ]) {
    assert.match(read(path), /data-row-inspector-trigger/, path);
  }
});

test("details and cleanup workflow use reading or list layouts", () => {
  const expected = new Map([
    ["../src/features/assets/AssetDetail.tsx", "reading"],
    ["../src/features/cleanup/CleanupTaskDetail.tsx", "reading"],
    ["../src/features/executions/ExecutionDetail.tsx", "list"],
  ]);
  for (const [path, mode] of expected) {
    const source = read(path);
    assert.match(source, new RegExp('<PageLayout mode="' + mode + '"'), path);
    assert.doesNotMatch(source, /className="flex min-h-full flex-col"/, path);
  }
});

test("detail routes register titles while task creation uses a shared dialog", () => {
  const detailRoutes = [
    "../src/features/assets/AssetDetail.tsx",
    "../src/features/cleanup/CleanupTaskDetail.tsx",
    "../src/features/executions/ExecutionDetail.tsx",
  ];
  for (const path of detailRoutes) {
    const source = read(path);
    assert.match(source, /<PageTitle/, path);
    assert.match(source, /parent=\{\{/, path);
    assert.doesNotMatch(source, /PageHeader|ArrowLeft/, path);
  }
  for (const path of detailRoutes.filter(
    (path) => !path.endsWith("AssetDetail.tsx"),
  )) {
    assert.match(read(path), /<PageToolbar/, path);
  }

  const assetDetail = read("../src/features/assets/AssetDetail.tsx");
  assert.doesNotMatch(
    assetDetail,
    /PageToolbar|technicalInfo|max-w-6xl/,
  );
  assert.match(assetDetail, /resourcePropertyRows/);
  assert.match(assetDetail, /md:grid-cols-2/);
  assert.match(assetDetail, /cloudConsoleURL/);
  assert.match(
    assetDetail,
    /regionId: assetConsoleRegionID\(value\.location\)/,
  );
  assert.match(
    assetDetail,
    /consoleLinkTemplate: kind\?\.console_link_template/,
  );
  assert.match(assetDetail, /ResourcePropertyValue/);
  const propertyValue = read(
    "../src/features/panorama/ResourcePropertyValue.tsx",
  );
  assert.match(propertyValue, /displayBrowserTime/);
  assert.match(propertyValue, /tagValues/);
  assert.doesNotMatch(assetDetail, /t\("common\.scope"\)/);

  const builder = read("../src/features/cleanup/CleanupTaskBuilder.tsx");
  assert.match(builder, /<TaskCreationDialog/);
  assert.match(builder, /<ul className="mt-3 divide-y"/);
  assert.match(builder, /<ResourceKindPicker/);
  assert.doesNotMatch(builder, /<Collapsible|advancedOptions|request-options/);
  assert.doesNotMatch(
    builder,
    /<PlanStages|PageHeader|PageTitle|PageLayout|ArrowLeft|<Sheet/,
  );
});

test("task builder keeps a simple one-column resource list without advanced options", () => {
  const source = read("../src/features/cleanup/CleanupTaskBuilder.tsx");
  assert.match(source, /<TaskCreationDialog/);
  assert.match(source, /bodyClassName="space-y-4"/);
  assert.match(source, /<ul className="mt-3 divide-y"/);
  assert.match(source, /allLabel=\{t\("assets\.allResourceKinds"\)\}/);
  assert.doesNotMatch(source, /<Card|<Collapsible|optionsOpen/);
  assert.doesNotMatch(source, /<aside/);
  assert.doesNotMatch(source, /sticky bottom-0/);
});

test("task resource results reuse the flat collection-table pattern", () => {
  const source = read("../src/features/cleanup/CleanupTaskDetail.tsx");
  const resourceContent = source.match(
    /<TabsContent value="resources">[\s\S]*?<\/TabsContent>/,
  )?.[0];
  assert.ok(resourceContent);
  assert.match(resourceContent, /<DataTableShell/);
  assert.match(resourceContent, /<CursorPagination/);
  assert.match(resourceContent, /<Table>/);
  assert.doesNotMatch(resourceContent, /overflow-hidden rounded-xl border/);
  assert.match(
    resourceContent,
    /aria-label=\{t\("cleanup\.searchResources"\)\}/,
  );
});

test("cleanup task targets use a drill-down list instead of cards", () => {
  const source = read("../src/features/cleanup/CleanupTaskDetail.tsx");
  const targetContent = source.match(
    /<TabsContent value="review">[\s\S]*?<\/TabsContent>/,
  )?.[0];
  assert.ok(targetContent);
  assert.match(targetContent, /<ul className="divide-y">/);
  assert.match(targetContent, /cleanup\.viewTargetResources/);
  assert.match(targetContent, /<ChevronRight/);
  assert.doesNotMatch(targetContent, /rounded-lg border/);
  assert.match(source, /function CleanupTargetResources/);
});

test("cleanup target resources reuse the resource-result table pattern", () => {
  const source = read("../src/features/cleanup/CleanupTaskDetail.tsx");
  const targetResources = source.match(
    /function CleanupTargetResources[\s\S]*?function cleanupTargetAssetIDs/,
  )?.[0];
  assert.ok(targetResources);
  assert.match(targetResources, /<DataTableShell/);
  assert.match(targetResources, /<Table>/);
  assert.match(targetResources, /<TableHeader>/);
  assert.match(targetResources, /<TableBody>/);
  assert.doesNotMatch(targetResources, /<ul className="divide-y">/);
});

test("entry and error states avoid dashboard cards and oversized branding", () => {
  for (const path of [
    "../src/auth/LoginView.tsx",
    "../src/app/ConnectionGate.tsx",
    "../src/app/RouteErrorBoundary.tsx",
  ]) {
    const source = read(path);
    assert.doesNotMatch(
      source,
      /shadow-xl|rounded-xl border bg-card p-8/,
      path,
    );
  }
});

test("responsive overlays use bounded task-specific widths and short motion", () => {
  const sidebar = read("../src/components/ui/sidebar.tsx");
  const inspector = read("../src/components/patterns/InspectorSheet.tsx");
  const scanForm = read("../src/features/scans/CreateScanDialog.tsx");
  const taskDialog = read("../src/components/patterns/TaskCreationDialog.tsx");
  const connectionForm = read("../src/features/settings/ConnectionEditor.tsx");
  const dialog = read("../src/components/ui/dialog.tsx");
  const sheet = read("../src/components/ui/sheet.tsx");
  const drawer = read("../src/components/ui/drawer.tsx");

  assert.match(sidebar, /w-\[min\(16\.25rem,88vw\)\]/);
  assert.match(inspector, /w-screen[^"\n]*sm:max-w-md/);
  assert.match(scanForm, /<TaskCreationDialog/);
  assert.doesNotMatch(scanForm, /<Sheet/);
  assert.match(taskDialog, /w-\[calc\(100%-2rem\)\][^"\n]*sm:max-w-3xl/);
  assert.match(taskDialog, /max-h-\[calc\(100dvh-2rem\)\]/);
  assert.match(taskDialog, /overflow-y-auto/);
  assert.match(connectionForm, /w-screen[^"\n]*sm:max-w-lg/);
  assert.match(dialog, /max-w-\[calc\(100%-2rem\)\]/);
  assert.match(sheet, /duration-\[180ms\]/);
  assert.match(drawer, /max-h-\[85svh\]/);
  assert.match(drawer, /data-slot="drawer-scroll-area"/);
});

test("task-dialog selectors portal their menus and respect viewport collisions", () => {
  for (const path of [
    "../src/components/domain/ResourceKindPicker.tsx",
    "../src/components/ui/combobox.tsx",
    "../src/components/ui/multi-combobox.tsx",
  ]) {
    const source = read(path);
    assert.doesNotMatch(source, /portalled=\{false\}/, path);
    assert.match(source, /collisionPadding=\{12\}/, path);
    assert.match(source, /--radix-popover-content-available-height/, path);
  }
});
