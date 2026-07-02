import { useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  type Node,
  type Edge,
  type NodeMouseHandler,
} from "@xyflow/react";
import type { Copy } from "../../lib/i18n";
import type { Resource, ResourceEdge } from "../../lib/api";
import { resourceTypeLabel } from "../../lib/format";
import { EmptyState } from "../../components/EmptyState";

const COLS = 5;

export function TopologyGraph({
  resources,
  edges,
  candidateIds,
  copy,
  onSelect,
}: {
  resources: Resource[];
  edges: ResourceEdge[];
  candidateIds: Set<string>;
  copy: Copy;
  onSelect: (resource: Resource) => void;
}) {
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const dependents = useMemo(() => {
    if (!selectedId) return new Set<string>();
    return new Set(
      edges
        .filter((edge) => edge.target_resource_id === selectedId)
        .map((edge) => edge.source_resource_id),
    );
  }, [selectedId, edges]);

  const nodes: Node[] = useMemo(
    () =>
      resources.map((resource, index) => {
        const classes = ["topo-node", `topo-${resource.type}`];
        if (resource.protected) classes.push("topo-protected");
        if (candidateIds.has(resource.id)) classes.push("topo-candidate");
        if (resource.id === selectedId) classes.push("topo-selected");
        if (dependents.has(resource.id)) classes.push("topo-dependent");
        return {
          id: resource.id,
          position: {
            x: (index % COLS) * 220,
            y: Math.floor(index / COLS) * 120,
          },
          data: {
            label: `${resource.name}\n${resourceTypeLabel(copy, resource.type)}`,
          },
          className: classes.join(" "),
        };
      }),
    [resources, candidateIds, selectedId, dependents, copy],
  );

  const flowEdges: Edge[] = useMemo(
    () =>
      edges.map((edge) => ({
        id: edge.id,
        source: edge.source_resource_id,
        target: edge.target_resource_id,
        label: edge.type,
        animated: edge.target_resource_id === selectedId,
      })),
    [edges, selectedId],
  );

  const onNodeClick: NodeMouseHandler = (_event, node) => {
    setSelectedId(node.id);
    const resource = resources.find((r) => r.id === node.id);
    if (resource) onSelect(resource);
  };

  if (resources.length === 0) {
    return <EmptyState title={copy.empty.runAnalyze} />;
  }

  return (
    <div className="topo-canvas">
      <ReactFlow
        nodes={nodes}
        edges={flowEdges}
        onNodeClick={onNodeClick}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
