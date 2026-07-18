import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import test from "node:test";
import {
  developmentOrigin,
  isTrustedDevelopmentApiRequest,
} from "../dev-auth-boundary.mjs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("frontend uses only terminal domain resources and opaque pagination", () => {
  const types = read("../src/api/types.ts");
  const client = read("../src/api/client.ts");
  const routes = read("../src/routes.tsx");

  assert.match(types, /interface Page<T>/);
  assert.match(types, /next_cursor\?: string/);
  assert.match(types, /capabilities: string\[\]/);
  assert.doesNotMatch(
    types,
    /"ecs"\s*\|\s*"vpc"|ResourceType[A-Z][A-Za-z]+/,
    "resource kinds must come from provider catalog data",
  );
  assert.doesNotMatch(
    `${client}\n${routes}`,
    /\/api\/(?:resources|settings|regions)(?:["'/?`]|\$\{)/,
    "old API resources must not remain in the terminal client",
  );
  for (const endpoint of [
    "/api/providers",
    "/api/providers/catalog",
    "/api/connections",
    "/api/scopes",
    "/api/scans",
    "/api/scan-targets/vpcs",
    "/api/scan-targets/vswitches",
    "/api/assets",
    "/api/findings",
    "/api/cleanup",
    "/api/execution-attempts",
    "/api/audit-events",
    "/api/topology",
  ]) {
    assert.ok(
      client.includes(endpoint),
      `missing terminal endpoint ${endpoint}`,
    );
  }
  assert.doesNotMatch(
    client,
    /\bactor\s*:/,
    "client requests must not declare audit actor",
  );
  assert.match(client, /selectors:\s*CleanupSelector\[\]/);
  assert.doesNotMatch(client, /asset_ids\s*:/);

  for (const route of [
    "assets",
    "scans",
    "cleanup",
    "executions",
    "findings",
    "audits",
    "panorama",
    "settings",
  ]) {
    assert.match(routes, new RegExp(`path=["']${route}`));
  }
  assert.doesNotMatch(
    routes,
    /path=["']regions(?:["'/])/,
    "the removed top-level Region workspace route must not return",
  );
  assert.doesNotMatch(routes, /path=["']connections/);
  assert.match(routes, /Navigate to="\/panorama"/);
});

test("the default panorama workspace begins preloading at application startup", () => {
  const routes = read("../src/routes.tsx");

  assert.match(
    routes,
    /const panoramaViewModule = import\("\.\/features\/panorama\/PanoramaView"\)/,
  );
  assert.match(
    routes,
    /const PanoramaView = lazy\(\(\) =>\s*panoramaViewModule\.then/,
  );
});

test("authentication keeps bearer token session-scoped and has no editable actor", () => {
  const provider = read("../src/auth/AuthProvider.tsx");
  const login = read("../src/auth/LoginView.tsx");

  assert.match(provider, /sessionStorage/);
  assert.doesNotMatch(provider, /localStorage/);
  assert.match(provider, /setAccessTokenProvider/);
  assert.match(provider, /setPrincipalObserver/);
  assert.doesNotMatch(
    `${provider}\n${login}`,
    /setActor|actor-input|name=["']actor/,
  );
});

test("connection context is mandatory and all dropdowns use framework components", () => {
  const client = read("../src/api/client.ts");
  const shell = read("../src/app/AppShell.tsx");
  const sidebar = read("../src/app/AppSidebar.tsx");
  const select = read("../src/components/ui/select.tsx");
  const sourceRoot = new URL("../src/", import.meta.url);
  const files = readdirSync(sourceRoot, {
    recursive: true,
    withFileTypes: true,
  });
  const nativeSelectSources = files
    .filter((entry) => entry.isFile() && /\.tsx?$/.test(entry.name))
    .flatMap((entry) => {
      const content = readFileSync(`${entry.parentPath}/${entry.name}`, "utf8");
      return new RegExp("<" + "select(?:\\s|>)").test(content)
        ? [entry.name]
        : [];
    });

  assert.match(client, /params\.set\("connection_id", connectionID\)/);
  assert.match(shell, /activeConnection/);
  assert.match(sidebar, /connectionContext\.label/);
  assert.match(select, /from "radix-ui"/);
  assert.match(select, /data-slot="select"/);
  assert.match(select, /@\/lib\/utils/);
  assert.deepEqual(nativeSelectSources, []);
});

test("make dev auto-authenticates through the Vite proxy only", () => {
  const makefile = read("../../Makefile");
  const vite = read("../vite.config.ts");
  const boundary = read("../dev-auth-boundary.mjs");
  const provider = read("../src/auth/AuthProvider.tsx");
  const sidebar = read("../src/app/AppSidebar.tsx");

  assert.match(makefile, /dev_token=.*\/dev\/urandom/);
  assert.match(makefile, /mktemp -d/);
  assert.match(makefile, /go build -o/);
  assert.doesNotMatch(makefile, /go run/);
  assert.match(makefile, /rm -rf "\$\$dev_dir"/);
  assert.match(makefile, /STEWARD_AUTH_TOKEN=\$\$dev_token/);
  assert.match(makefile, /STEWARD_DEV_PROXY_TOKEN=\$\$dev_token/);
  assert.match(makefile, /VITE_STEWARD_DEV_AUTO_LOGIN=1/);
  assert.match(vite, /process\.env\.STEWARD_DEV_PROXY_TOKEN/);
  assert.match(vite, /Authorization:\s*`Bearer \$\{devProxyToken\}`/);
  assert.doesNotMatch(vite, /VITE_STEWARD_DEV_PROXY_TOKEN/);
  assert.match(vite, /isTrustedDevelopmentApiRequest\(request\.headers\)/);
  assert.match(boundary, /sec-fetch-site/);
  assert.match(boundary, /headers\.origin/);
  assert.match(vite, /statusCode = 403/);
  assert.match(vite, /"X-Frame-Options": "DENY"/);
  assert.match(vite, /"Content-Security-Policy": "frame-ancestors 'none'"/);
  assert.match(provider, /import\.meta\.env\.DEV/);
  assert.match(provider, /VITE_STEWARD_DEV_AUTO_LOGIN/);
  assert.doesNotMatch(provider, /STEWARD_DEV_PROXY_TOKEN/);
  assert.match(sidebar, /automaticDevelopmentSession/);
});

test("development proxy credential injection accepts only its own origin", () => {
  assert.equal(
    isTrustedDevelopmentApiRequest({
      host: "127.0.0.1:5858",
      origin: developmentOrigin,
      "sec-fetch-site": "same-origin",
    }),
    true,
  );
  assert.equal(
    isTrustedDevelopmentApiRequest({ host: "127.0.0.1:5858" }),
    true,
  );
  assert.equal(
    isTrustedDevelopmentApiRequest({
      host: "127.0.0.1:5858",
      origin: "https://attacker.example",
      "sec-fetch-site": "cross-site",
    }),
    false,
  );
  assert.equal(
    isTrustedDevelopmentApiRequest({
      host: "attacker.example:5858",
      origin: "http://attacker.example:5858",
      "sec-fetch-site": "same-origin",
    }),
    false,
  );
});
