import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import test from "node:test";

const exists = (path) => existsSync(new URL(path, import.meta.url));
const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("semantic themes and shadcn primitives are wired into the application", () => {
  assert.equal(exists("../src/styles/globals.css"), true);
  assert.equal(exists("../src/styles/tokens.css"), true);
  assert.equal(exists("../src/app/ThemeProvider.tsx"), true);
  assert.equal(exists("../src/app/ThemeToggle.tsx"), true);
  assert.equal(exists("../src/components/ui/button.tsx"), true);

  const tokens = read("../src/styles/tokens.css");
  const main = read("../src/main.tsx");
  assert.match(tokens, /--sidebar:/);
  assert.match(tokens, /--success:/);
  assert.match(tokens, /--graph-lifecycle:/);
  assert.match(tokens, /\.dark\s*\{/);
  assert.match(main, /ThemeProvider/);
  assert.match(main, /styles\/globals\.css/);
  assert.doesNotMatch(main, /styles\.css/);
});

test("the Codex workspace has no legacy presentation system", () => {
  for (const path of [
    "../src/components/AppShell.tsx",
    "../src/components/Sidebar.tsx",
    "../src/components/Domain.tsx",
    "../src/components/ui/DropdownMenu.tsx",
    "../src/components/ui/LegacyDrawer.tsx",
    "../src/components/ui/LegacySelect.tsx",
    "../src/components/ui/LegacyAdapters.test.tsx",
    "../src/styles.css",
  ]) {
    assert.equal(exists(path), false, `${path} must be removed`);
  }

  const sourceRoot = new URL("../src/", import.meta.url);
  const files = readdirSync(sourceRoot, {
    recursive: true,
    withFileTypes: true,
  });
  const legacyReferences = [];
  const hardCodedColors = [];
  for (const entry of files) {
    if (!entry.isFile() || !/\.(?:tsx?|css)$/.test(entry.name)) continue;
    const path = `${entry.parentPath}/${entry.name}`;
    const content = readFileSync(path, "utf8");
    if (
      /components\/(?:AppShell|Sidebar|Domain)|components\/ui\/(?:DropdownMenu|LegacyDrawer|LegacySelect)|styles\.css/.test(
        content,
      )
    ) {
      legacyReferences.push(path);
    }
    if (entry.name !== "tokens.css" && /#[0-9a-f]{3,8}\b/i.test(content)) {
      hardCodedColors.push(path);
    }
  }
  assert.deepEqual(legacyReferences, []);
  assert.deepEqual(hardCodedColors, []);
});

test("ChatGPT-style tokens and primitives stay neutral and quiet", () => {
  const tokens = read("../src/styles/tokens.css");
  const button = read("../src/components/ui/button.tsx");
  const dropdown = read("../src/components/ui/dropdown-menu.tsx");
  const sheet = read("../src/components/ui/sheet.tsx");
  const sidebar = read("../src/components/ui/sidebar.tsx");

  assert.match(tokens, /--background:\s*oklch\(1 0 0\)/);
  assert.match(tokens, /--primary:\s*oklch\(0\.205 0 0\)/);
  assert.match(tokens, /--sidebar:\s*oklch\(0\.97 0 0\)/);
  assert.match(tokens, /--ring:\s*oklch\(0\.623 0\.188 259\)/);
  assert.match(tokens, /--info:\s*oklch\(0\.56 0\.188 259\)/);
  assert.match(tokens, /--destructive:\s*oklch\(0\.704 0\.191 22\.216\)/);
  assert.doesNotMatch(tokens, /--primary:[^;]*165/);
  assert.match(button, /rounded-lg/);
  assert.doesNotMatch(button, /shadow-xs/);
  assert.match(button, /dark:bg-destructive\/50/);
  assert.match(button, /dark:hover:bg-destructive\/55/);
  assert.match(dropdown, /rounded-xl/);
  assert.match(dropdown, /slide-in-from-top-1/);
  assert.match(sheet, /duration-\[180ms\]/);
  assert.doesNotMatch(sheet, /duration-500/);
  assert.match(sidebar, /delayDuration=\{500\}/);
});

test("semantic status foregrounds stay readable in light and dark themes", () => {
  const tokens = read("../src/styles/tokens.css");

  for (const name of ["success", "warning", "info"]) {
    const values = [
      ...tokens.matchAll(
        new RegExp(`--${name}-foreground:\\s*oklch\\(([0-9.]+)`, "g"),
      ),
    ].map((match) => Number(match[1]));

    assert.equal(values.length, 2, `${name} needs light and dark foregrounds`);
    assert.ok(values[0] < 0.6, `${name} must be dark enough on light tints`);
    assert.ok(values[1] > 0.7, `${name} must be light enough on dark tints`);
  }
});

test("the application shell is edge-to-edge instead of an inset dashboard", () => {
  const shell = read("../src/app/AppShell.tsx");
  const sidebar = read("../src/components/ui/sidebar.tsx");
  assert.doesNotMatch(shell, /variant="inset"/);
  assert.doesNotMatch(shell, /md:my-2|md:mr-2|md:rounded-xl|md:shadow-sm/);
  assert.match(sidebar, /const SIDEBAR_WIDTH = "16\.25rem"/);
  assert.match(sidebar, /const SIDEBAR_WIDTH_ICON = "3\.25rem"/);
});

test("the sidebar account identity keeps readable contrast", () => {
  const tokens = read("../src/styles/tokens.css");
  const userMenu = read("../src/app/SidebarUserMenu.tsx");

  const tokenLightness = (name) => {
    const match = tokens.match(
      new RegExp(`--${name}:\\s*oklch\\(([0-9.]+) 0 0\\)`),
    );
    assert.ok(match, `missing neutral ${name} token`);
    return Number(match[1]);
  };
  const neutralOklchToSrgb = (lightness) => {
    const linear = lightness ** 3;
    return linear <= 0.0031308
      ? 12.92 * linear
      : 1.055 * linear ** (1 / 2.4) - 0.055;
  };
  const srgbToLinear = (channel) =>
    channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  const contrast = (foreground, background) => {
    const lighter = Math.max(foreground, background);
    const darker = Math.min(foreground, background);
    return (lighter + 0.05) / (darker + 0.05);
  };
  const compositeContrast = (foreground, background, alpha) => {
    const foregroundSrgb = neutralOklchToSrgb(foreground);
    const backgroundSrgb = neutralOklchToSrgb(background);
    const compositedSrgb =
      foregroundSrgb * alpha + backgroundSrgb * (1 - alpha);
    return contrast(srgbToLinear(compositedSrgb), srgbToLinear(backgroundSrgb));
  };

  const sidebar = tokenLightness("sidebar");
  const foreground = tokenLightness("sidebar-foreground");
  const popover = tokenLightness("popover");
  const accent = tokenLightness("sidebar-accent");
  const accentForeground = tokenLightness("sidebar-accent-foreground");

  assert.ok(compositeContrast(foreground, sidebar, 0.7) >= 4.5);
  assert.ok(compositeContrast(foreground, popover, 0.7) >= 4.5);
  assert.ok(compositeContrast(accentForeground, accent, 1) >= 4.5);
  assert.equal(userMenu.match(/text-sidebar-foreground\/70/g)?.length, 2);
  assert.doesNotMatch(userMenu, /text-muted-foreground/);
  assert.match(userMenu, /bg-sidebar-accent text-sidebar-accent-foreground/);
});
