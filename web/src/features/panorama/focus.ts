import type { TopologyResourceEdge } from "@/api/types";

export interface FocusState {
  selectedKey?: string;
  relatedKeys: ReadonlySet<string>;
  highlightedEdgeKeys: ReadonlySet<string>;
}

interface AdjacentEdge {
  edgeKey: string;
  otherKey: string;
}

export function connectedEdgeFocus(
  resourceKeys: readonly string[],
  edges: readonly TopologyResourceEdge[],
  selectedKey?: string,
): FocusState {
  const visibleKeys = new Set(resourceKeys);
  if (!selectedKey || !visibleKeys.has(selectedKey)) return emptyFocus();

  const adjacency = new Map<string, AdjacentEdge[]>();
  for (const edge of edges) {
    if (
      !visibleKeys.has(edge.source_key) ||
      !visibleKeys.has(edge.target_key)
    ) {
      continue;
    }
    addAdjacent(adjacency, edge.source_key, edge.key, edge.target_key);
    addAdjacent(adjacency, edge.target_key, edge.key, edge.source_key);
  }

  const relatedKeys = new Set<string>();
  const highlightedEdgeKeys = new Set<string>();
  for (const adjacent of adjacency.get(selectedKey) ?? []) {
    if (adjacent.otherKey !== selectedKey) {
      relatedKeys.add(adjacent.otherKey);
    }
  }

  const visitedKeys = new Set([selectedKey]);
  const pendingKeys = [selectedKey];
  while (pendingKeys.length > 0) {
    const currentKey = pendingKeys.pop();
    if (!currentKey) continue;
    for (const adjacent of adjacency.get(currentKey) ?? []) {
      highlightedEdgeKeys.add(adjacent.edgeKey);
      if (visitedKeys.has(adjacent.otherKey)) continue;
      visitedKeys.add(adjacent.otherKey);
      pendingKeys.push(adjacent.otherKey);
    }
  }

  return {
    selectedKey,
    relatedKeys,
    highlightedEdgeKeys,
  };
}

function addAdjacent(
  adjacency: Map<string, AdjacentEdge[]>,
  endpointKey: string,
  edgeKey: string,
  otherKey: string,
) {
  const values = adjacency.get(endpointKey);
  const value = { edgeKey, otherKey };
  if (values) {
    values.push(value);
  } else {
    adjacency.set(endpointKey, [value]);
  }
}

function emptyFocus(): FocusState {
  return {
    relatedKeys: new Set(),
    highlightedEdgeKeys: new Set(),
  };
}
