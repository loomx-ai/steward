import { useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { Dropdown } from "../../components/Dropdown";
import { Skeleton } from "../../components/Skeleton";
import { EmptyState } from "../../components/EmptyState";
import { useResources, useGraph, useCandidates } from "../../lib/queries";
import { resourceTypeLabel } from "../../lib/format";
import type { Resource } from "../../lib/api";
import { ResourceDetail } from "./ResourceDetail";
import { TopologyGraph } from "./TopologyGraph";

const resourceTypes = [
  "",
  "ecs_instance",
  "disk",
  "eip",
  "security_group",
  "snapshot",
  "vpc",
  "vswitch",
];

type Tab = "inventory" | "topology";

export function ResourcesView() {
  const { copy, activeScanId } = useShell();
  const [tab, setTab] = useState<Tab>("inventory");
  const [resourceType, setResourceType] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Resource | null>(null);

  useEffect(() => {
    const id = window.setTimeout(() => setQuery(queryInput), 300);
    return () => window.clearTimeout(id);
  }, [queryInput]);

  const resources = useResources(activeScanId, resourceType, query);
  const allResources = useResources(activeScanId, "", "");
  const graph = useGraph(activeScanId);
  const candidates = useCandidates(activeScanId);

  const candidateIds = useMemo(
    () => new Set((candidates.data ?? []).map((c) => c.resource_id)),
    [candidates.data],
  );

  return (
    <div className="grid gap-4">
      <div className="tabs">
        <button
          className={tab === "inventory" ? "tab active" : "tab"}
          onClick={() => setTab("inventory")}
        >
          {copy.sections.resources}
        </button>
        <button
          className={tab === "topology" ? "tab active" : "tab"}
          onClick={() => setTab("topology")}
        >
          {copy.sections.resourceGraph}
        </button>
      </div>

      {tab === "inventory" ? (
        <section className="panel">
          <div className="section-heading">
            <h2 className="panel-title">{copy.sections.resources}</h2>
            <div className="filters">
              <Dropdown
                label={copy.table.type}
                value={resourceType}
                options={resourceTypes.map((type) => ({
                  value: type,
                  label: type
                    ? resourceTypeLabel(copy, type)
                    : copy.fields.allTypes,
                }))}
                onValueChange={setResourceType}
              />
              <div className="search">
                <Search size={16} />
                <input
                  value={queryInput}
                  onChange={(event) => setQueryInput(event.target.value)}
                  placeholder={copy.fields.searchPlaceholder}
                />
              </div>
            </div>
          </div>
          <div className="grid gap-4 xl:grid-cols-[1fr_360px]">
            {resources.isPending ? (
              <Skeleton rows={6} />
            ) : resources.data && resources.data.length > 0 ? (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{copy.table.name}</th>
                      <th>{copy.table.type}</th>
                      <th>{copy.table.region}</th>
                      <th>{copy.table.state}</th>
                      <th>{copy.table.team}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {resources.data.map((resource) => (
                      <tr
                        key={resource.id}
                        className={
                          resource.id === selected?.id
                            ? "row-active clickable"
                            : "clickable"
                        }
                        onClick={() => setSelected(resource)}
                      >
                        <td>
                          <div className="font-medium">{resource.name}</div>
                          <div className="text-xs text-slate-500">
                            {resource.native_id}
                          </div>
                        </td>
                        <td>{resourceTypeLabel(copy, resource.type)}</td>
                        <td>{resource.region}</td>
                        <td>{resource.state || "-"}</td>
                        <td>{resource.ownership.team || "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyState title={copy.empty.noResources} />
            )}
            <ResourceDetail resource={selected} copy={copy} />
          </div>
        </section>
      ) : (
        <section className="panel">
          <div className="section-heading">
            <h2 className="panel-title">{copy.sections.resourceGraph}</h2>
            <span className="muted">
              {graph.data?.length ?? 0} {copy.labels.edges}
            </span>
          </div>
          <div className="grid gap-4 xl:grid-cols-[1fr_360px]">
            <TopologyGraph
              resources={allResources.data ?? []}
              edges={graph.data ?? []}
              candidateIds={candidateIds}
              copy={copy}
              onSelect={setSelected}
            />
            <ResourceDetail resource={selected} copy={copy} />
          </div>
        </section>
      )}
    </div>
  );
}
