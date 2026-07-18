import { expect, it } from "vitest";
import {
  clearBoxSelection,
  completeBoxSelection,
  prioritizeBoxSelectionTargets,
} from "./boxSelection";

it("replaces candidates with the selected keys in stable unique order", () => {
  expect(completeBoxSelection(["current"], ["b", "a", "b"], false)).toEqual([
    "b",
    "a",
  ]);
});

it("appends selected keys without moving existing candidates", () => {
  expect(completeBoxSelection(["b", "a"], ["a", "c", "b", "d"], true)).toEqual([
    "b",
    "a",
    "c",
    "d",
  ]);
});

it("clears temporary candidates", () => {
  expect(clearBoxSelection({ candidateKeys: ["a", "b"] })).toEqual({
    candidateKeys: [],
  });
});

it("keeps a selected resource batch and removes its selected members", () => {
  expect(
    prioritizeBoxSelectionTargets([
      { key: "asset:a", kind: "resource" },
      {
        key: "batch:compute",
        kind: "resource_batch",
        memberKeys: ["asset:a", "asset:b"],
      },
      { key: "asset:b", kind: "resource" },
      { key: "asset:c", kind: "resource" },
    ]),
  ).toEqual([
    {
      key: "batch:compute",
      kind: "resource_batch",
      memberKeys: ["asset:a", "asset:b"],
    },
    { key: "asset:c", kind: "resource" },
  ]);
});
