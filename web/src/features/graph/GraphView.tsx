import { useMemo } from "react";
import {
  Background,
  Controls,
  ReactFlow,
  type Edge,
  type Node,
} from "@xyflow/react";
import type { Asset, LifecycleBinding, Relationship } from "@/api/types";
import { useLocale } from "@/i18n/LocaleProvider";

export function GraphView({
  focus,
  assets,
  relationships,
  lifecycleBindings = [],
}: {
  focus: Asset;
  assets: Asset[];
  relationships: Relationship[];
  lifecycleBindings?: LifecycleBinding[];
}) {
  const { label, t } = useLocale();
  const model = useMemo(() => {
    const byID = new Map([focus, ...assets].map((asset) => [asset.id, asset]));
    const ids = [
      ...new Set([
        focus.id,
        ...relationships.flatMap((edge) => [
          edge.source_asset_id,
          edge.target_asset_id,
        ]),
        ...lifecycleBindings.flatMap((binding) => [
          binding.controller_asset_id,
          binding.managed_asset_id,
        ]),
      ]),
    ];
    const relatedCount = Math.max(ids.length - 1, 1);
    const nodes: Node[] = ids.map((id, index) => {
      const asset = byID.get(id);
      const angle = ((index - 1) / relatedCount) * Math.PI * 2;
      return {
        id,
        position:
          index === 0
            ? { x: 340, y: 180 }
            : {
                x: 340 + Math.cos(angle) * 300,
                y: 180 + Math.sin(angle) * 150,
              },
        data: { label: asset?.name || asset?.identity.native_id || id },
        className:
          "!max-w-48 !rounded-lg !border !border-border !bg-card !px-3 !py-2 !text-xs !text-card-foreground !shadow-sm",
        style:
          id === focus.id
            ? {
                borderColor: "var(--ring)",
                boxShadow:
                  "0 0 0 3px color-mix(in oklch, var(--ring) 20%, transparent)",
              }
            : undefined,
      };
    });
    const relationshipEdges: Edge[] = relationships.map((relationship) => ({
      id: `relationship:${relationship.id}`,
      source: relationship.source_asset_id,
      target: relationship.target_asset_id,
      label: label(relationship.type),
      style: { stroke: "var(--graph-relationship)", strokeWidth: 1.5 },
      labelStyle: { fill: "var(--muted-foreground)", fontSize: 10 },
    }));
    const lifecycleEdges: Edge[] = lifecycleBindings.map((binding, index) => ({
      id: `lifecycle:${binding.id || `${binding.controller_asset_id}:${binding.managed_asset_id}:${index}`}`,
      source: binding.controller_asset_id,
      target: binding.managed_asset_id,
      label: t("asset.lifecycleRelation"),
      animated: false,
      style: {
        stroke: "var(--muted-foreground)",
        strokeWidth: 1.25,
        strokeDasharray: "5 4",
      },
      labelStyle: { fill: "var(--muted-foreground)", fontSize: 10 },
    }));
    return { nodes, edges: [...relationshipEdges, ...lifecycleEdges] };
  }, [focus, assets, label, lifecycleBindings, relationships, t]);
  if (model.edges.length === 0)
    return (
      <div className="grid min-h-48 place-items-center text-sm text-muted-foreground">
        {t("graph.empty")}
      </div>
    );
  return (
    <div className="h-[26rem] overflow-hidden rounded-xl border bg-muted/20">
      <ReactFlow
        nodes={model.nodes}
        edges={model.edges}
        fitView
        minZoom={0.35}
        maxZoom={1.5}
        zoomOnScroll={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="var(--border)" />
        <Controls className="!overflow-hidden !rounded-lg !border !border-border !bg-card !shadow-sm [&_button]:!border-border [&_button]:!bg-card [&_button]:!fill-foreground" />
      </ReactFlow>
    </div>
  );
}
