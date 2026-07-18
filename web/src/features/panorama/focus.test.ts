import { expect, it } from "vitest";
import type { TopologyResourceEdge } from "@/api/types";
import { connectedEdgeFocus } from "./focus";

const resourceKeys = [
  "security-group",
  "ecs-a",
  "ecs-b",
  "ecs-c",
  "database",
  "orphan",
  "orphan-peer",
];
const edges: TopologyResourceEdge[] = [
  {
    key: "ecs-a-uses-security",
    source_key: "ecs-a",
    target_key: "security-group",
    kind: "relationship",
    relation: "uses",
  },
  {
    key: "security-used-by-ecs-b",
    source_key: "security-group",
    target_key: "ecs-b",
    kind: "relationship",
    relation: "used_by",
  },
  {
    key: "security-controls-ecs-c",
    source_key: "security-group",
    target_key: "ecs-c",
    kind: "lifecycle",
    relation: "delegate",
  },
  {
    key: "ecs-b-uses-database",
    source_key: "ecs-b",
    target_key: "database",
    kind: "relationship",
    relation: "uses",
  },
  {
    key: "orphan-uses-peer",
    source_key: "orphan",
    target_key: "orphan-peer",
    kind: "relationship",
    relation: "uses",
  },
];

it("focuses direct resources while highlighting every connected edge symmetrically", () => {
  const ecsFocus = connectedEdgeFocus(resourceKeys, edges, "ecs-a");
  expect(ecsFocus).toEqual({
    selectedKey: "ecs-a",
    relatedKeys: new Set(["security-group"]),
    highlightedEdgeKeys: new Set([
      "ecs-a-uses-security",
      "security-used-by-ecs-b",
      "security-controls-ecs-c",
      "ecs-b-uses-database",
    ]),
  });

  const securityGroupFocus = connectedEdgeFocus(
    resourceKeys,
    edges,
    "security-group",
  );
  expect(securityGroupFocus).toEqual({
    selectedKey: "security-group",
    relatedKeys: new Set(["ecs-a", "ecs-b", "ecs-c"]),
    highlightedEdgeKeys: new Set([
      "ecs-a-uses-security",
      "security-used-by-ecs-b",
      "security-controls-ecs-c",
      "ecs-b-uses-database",
    ]),
  });
});

it("keeps resource badges direct while highlighting N-level connections", () => {
  const focus = connectedEdgeFocus(resourceKeys, edges, "security-group");

  expect(focus.relatedKeys.has("database")).toBe(false);
  expect(focus.highlightedEdgeKeys.has("ecs-b-uses-database")).toBe(true);
  expect(focus.relatedKeys.has("orphan")).toBe(false);
  expect(focus.highlightedEdgeKeys.has("orphan-uses-peer")).toBe(false);
});

it("returns an unfocused state for absent or unknown selection", () => {
  for (const selectedKey of [undefined, "missing"]) {
    const focus = connectedEdgeFocus(resourceKeys, edges, selectedKey);
    expect(focus.selectedKey).toBeUndefined();
    expect(focus.relatedKeys).toEqual(new Set());
    expect(focus.highlightedEdgeKeys).toEqual(new Set());
    expect(focus).toEqual({
      relatedKeys: new Set(),
      highlightedEdgeKeys: new Set(),
    });
  }
});
