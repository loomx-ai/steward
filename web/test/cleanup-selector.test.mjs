import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("cleanup client submits selectors instead of asset IDs", () => {
  const client = read("../src/api/client.ts");
  assert.match(client, /selectors:\s*CleanupSelector\[\]/);
  assert.doesNotMatch(client, /asset_ids\s*:/);
});

test("connection account and region selectors require typed-name confirmation", async () => {
  const { confirmationMode } =
    await import("../src/features/cleanup/selection.ts");
  assert.equal(confirmationMode({ kind: "connection" }), "type_name");
  assert.equal(
    confirmationMode({ kind: "scope", scope_kind: "account" }),
    "type_name",
  );
  assert.equal(
    confirmationMode({ kind: "scope", scope_kind: "region" }),
    "type_name",
  );
  assert.equal(
    confirmationMode({ kind: "scope", scope_kind: "vpc" }),
    "confirm",
  );
  assert.equal(confirmationMode({ kind: "group" }), "confirm");
});

test("encoded cleanup selectors preserve localized display names", async () => {
  const { decodeSelector, encodeSelector } =
    await import("../src/features/cleanup/selection.ts");
  const selector = {
    kind: "scope",
    connection_id: "connection-1",
    scope_id: "region-1",
    scope_kind: "region",
    descendants: true,
    display_name: "华东 1（杭州）",
  };
  assert.deepEqual(decodeSelector(encodeSelector(selector)), selector);
  assert.equal(decodeSelector("not valid"), null);
});

test("task builder and review keep selector input and confirmation explicit", () => {
  const client = read("../src/api/client.ts");
  const builder = read("../src/features/cleanup/CleanupTaskBuilder.tsx");
  const detail = read("../src/features/cleanup/CleanupTaskDetail.tsx");
  assert.match(builder, /decodeSelector/);
  assert.match(builder, /selectors,/);
  assert.doesNotMatch(builder, /<textarea[^>]+Asset IDs|asset_ids/);
  assert.match(detail, /typedConfirmationNames/);
  assert.match(detail, /explicitConfirmation/);
  assert.match(detail, /confirmationSatisfied/);
  assert.match(detail, /typed_names:\s*typedConfirmations/);
  assert.match(detail, /acknowledged:\s*explicitConfirmation/);
  assert.match(detail, /cleanupResourceRows/);
  assert.match(detail, /const allStatuses/);
  assert.match(detail, /statusFilter/);
  assert.match(detail, /useState<PageSize>\(DEFAULT_PAGE_SIZE\)/);
  assert.match(detail, /CleanupTaskEvents/);
  assert.match(client, /confirmation:\s*ExecutionConfirmation/);
  assert.match(
    client,
    /JSON\.stringify\(\{\s*idempotency_key:\s*idempotencyKey,\s*concurrency,\s*confirmation,/s,
  );
});

test("task review summary separates direct controller retained and billing counts", async () => {
  const { cleanupReviewSummary } =
    await import("../src/features/cleanup/selection.ts");
  assert.deepEqual(
    cleanupReviewSummary({
      task: {
        resolved_asset_ids: ["a", "b", "c"],
        blockers: [{ code: "protected", message: "protected" }],
      },
      steps: [{ kind: "controller" }, { kind: "direct" }, { kind: "direct" }],
      impact_items: [
        { expected: "delegated_delete", may_continue_billing: false },
        { expected: "retain_shared", may_continue_billing: true },
        { expected: "unknown", may_continue_billing: true },
      ],
    }),
    {
      resolved: 3,
      controllerSteps: 1,
      directSteps: 2,
      retained: 1,
      blockers: 1,
      possibleBilling: 2,
    },
  );
});
