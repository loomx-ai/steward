import assert from "node:assert/strict";
import test from "node:test";

import {
  canSelectForCleanup,
  projectLifecycle,
} from "../src/features/lifecycle/projection.ts";

const assets = [
  { id: "ack", name: "cluster", capabilities: ["actionable"] },
  { id: "ecs", name: "node", capabilities: ["actionable"] },
  { id: "vpc", name: "network", capabilities: ["actionable"] },
  { id: "disk", name: "disk", capabilities: ["actionable"] },
];

const bindings = [
  {
    controller_asset_id: "ack",
    managed_asset_id: "ecs",
    authority: "authoritative",
    ownership: "exclusive",
    cleanup_policy: "delegate",
    confidence: 1,
    evidence: { provider_default: "delete", possible_billing_residual: false },
  },
  {
    controller_asset_id: "ack",
    managed_asset_id: "vpc",
    authority: "authoritative",
    ownership: "shared",
    cleanup_policy: "retain",
    confidence: 1,
    evidence: { provider_default: "retain", possible_billing_residual: false },
  },
  {
    controller_asset_id: "ack",
    managed_asset_id: "disk",
    authority: "inferred",
    ownership: "unknown",
    cleanup_policy: "unknown",
    confidence: 0.6,
    evidence: { possible_billing_residual: true },
  },
  {
    controller_asset_id: "ros",
    managed_asset_id: "ros-child",
    authority: "authoritative",
    ownership: "exclusive",
    cleanup_policy: "delegate",
    direct_cleanup_allowed: true,
    confidence: 1,
  },
];

test("ACK lifecycle projection groups delegate retain and unknown semantics", () => {
  const result = projectLifecycle("ack", assets, bindings);
  assert.deepEqual(
    result.groups.map((group) => [
      group.key,
      group.items.map((item) => item.asset.id),
    ]),
    [
      ["delegated", ["ecs"]],
      ["retained", ["vpc"]],
      ["unknown", ["disk"]],
    ],
  );
  assert.equal(result.groups[0].items[0].expected, "delegated_delete");
  assert.equal(result.groups[1].items[0].expected, "retain_shared");
  assert.equal(result.groups[2].items[0].possibleBillingResidual, true);
});

test("delegated managed children require controllers unless direct cleanup is explicitly allowed", () => {
  assert.equal(canSelectForCleanup("ack", bindings), true);
  assert.equal(canSelectForCleanup("ecs", bindings), false);
  assert.equal(canSelectForCleanup("vpc", bindings), true);
  assert.equal(canSelectForCleanup("disk", bindings), true);
  assert.equal(canSelectForCleanup("ros-child", bindings), true);
});
