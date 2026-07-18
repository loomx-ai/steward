import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type RefObject,
} from "react";
import { useInfiniteQuery, useMutation } from "@tanstack/react-query";
import { ScanLine } from "lucide-react";
import { createScan, searchNetworkTargets } from "@/api/client";
import type {
  CloudConnection,
  ConnectionRegion,
  CreateScanInput,
  NetworkTargetOption,
  ProviderBundle,
  ScanScopeMode,
} from "@/api/types";
import {
  compareResourceKindOptionsByProduct,
  resourceProductName,
  resourceTypeName,
} from "@/components/domain/resourceKindLabel";
import { ResourceKindPicker } from "@/components/domain/ResourceKindPicker";
import { TaskCreationDialog } from "@/components/patterns/TaskCreationDialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Combobox } from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { MultiCombobox } from "@/components/ui/multi-combobox";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { useLocale } from "@/i18n/LocaleProvider";
import { compareRegionIDs } from "@/lib/regionOrder";

const globalRegionID = "global";

export function CreateScanDialog({
  open,
  onOpenChange,
  returnFocusRef,
  connection,
  regions,
  bundles,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  returnFocusRef: RefObject<HTMLButtonElement | null>;
  connection: CloudConnection;
  regions: ConnectionRegion[];
  bundles: ProviderBundle[];
  onCreated: (id: string) => void;
}) {
  const { formatError, locale, t } = useLocale();
  const [scopeMode, setScopeMode] =
    useState<ScanScopeMode>("all_active_regions");
  const [selectedRegionIDs, setSelectedRegionIDs] = useState<string[]>([
    globalRegionID,
  ]);
  const [queryRegionID, setQueryRegionID] = useState("");
  const [selectedKindIDs, setSelectedKindIDs] = useState<string[]>([]);
  const [vpcQuery, setVPCQuery] = useState("");
  const [vSwitchQuery, setVSwitchQuery] = useState("");
  const [selectedVPCs, setSelectedVPCs] = useState<NetworkTargetOption[]>([]);
  const [selectedVSwitches, setSelectedVSwitches] = useState<
    NetworkTargetOption[]
  >([]);
  const orderedRegions = useMemo(
    () =>
      [...regions].sort((left, right) =>
        compareRegionIDs(left.region_id, right.region_id),
      ),
    [regions],
  );
  const debouncedVPCQuery = useDebounced(vpcQuery, 300);
  const debouncedVSwitchQuery = useDebounced(vSwitchQuery, 300);
  useEffect(() => {
    if (!queryRegionID && orderedRegions.length)
      setQueryRegionID(orderedRegions[0].region_id);
  }, [orderedRegions, queryRegionID]);
  const vpcs = useInfiniteQuery({
    queryKey: [
      "scan-targets",
      connection.id,
      "vpc",
      queryRegionID,
      debouncedVPCQuery,
    ],
    queryFn: ({ pageParam, signal }) =>
      searchNetworkTargets(connection.id, "vpc", {
        regionID: queryRegionID,
        query: debouncedVPCQuery,
        cursor: pageParam || undefined,
        signal,
      }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled:
      open && scopeMode === "selected_networks" && Boolean(queryRegionID),
  });
  const vswitches = useInfiniteQuery({
    queryKey: [
      "scan-targets",
      connection.id,
      "vswitch",
      queryRegionID,
      debouncedVSwitchQuery,
    ],
    queryFn: ({ pageParam, signal }) =>
      searchNetworkTargets(connection.id, "vswitch", {
        regionID: queryRegionID,
        query: debouncedVSwitchQuery,
        cursor: pageParam || undefined,
        signal,
      }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled:
      open && scopeMode === "selected_networks" && Boolean(queryRegionID),
  });
  const specs = bundles
    .filter((bundle) => bundle.provider === connection.provider)
    .flatMap((bundle) => bundle.specs);
  const kindOptions = specs
    .map((spec) => {
      const kind = spec.resource_kind;
      const product = resourceProductName(kind.native_type);
      return {
        value: kind.id,
        label: resourceTypeName(
          kind.native_type,
          kind.display_name || kind.native_type,
          kind.display_names,
          locale,
        ),
        tag: product === "—" ? undefined : product,
        keywords: [
          product,
          kind.native_type,
          ...Object.values(kind.display_names ?? {}),
        ],
      };
    })
    .sort((left, right) =>
      compareResourceKindOptionsByProduct(left, right, locale),
    );
  const physicalRegionOptions = orderedRegions.map((region) => ({
    value: region.region_id,
    label: `${region.name} · ${region.region_id}`,
    keywords: [region.name, region.discovered_name, region.region_id].filter(
      (value): value is string => Boolean(value),
    ),
  }));
  const scanRegionOptions = [
    {
      value: globalRegionID,
      label: t("common.global"),
      keywords: ["global", "全局"],
    },
    ...physicalRegionOptions,
  ];
  const mutation = useMutation({
    mutationFn: (input: CreateScanInput) => createScan(connection.id, input),
    onSuccess: (task) => onCreated(task.id),
  });
  const networkTargets = useMemo(
    () => canonicalNetworkTargets(selectedVPCs, selectedVSwitches),
    [selectedVPCs, selectedVSwitches],
  );
  const networkRegionCount = new Set(
    networkTargets.map((target) => target.region_id),
  ).size;
  const canSubmit =
    regions.length > 0 &&
    (scopeMode === "all_active_regions" ||
      (scopeMode === "selected_regions" && selectedRegionIDs.length > 0) ||
      (scopeMode === "selected_networks" && networkTargets.length > 0));
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    mutation.mutate({
      scope_mode: scopeMode,
      region_ids:
        scopeMode === "selected_regions"
          ? [...selectedRegionIDs].sort()
          : undefined,
      network_targets:
        scopeMode === "selected_networks"
          ? networkTargets.map(
              ({ kind, region_id, native_id, name, parent_native_id }) => ({
                kind,
                region_id,
                native_id,
                name,
                parent_native_id,
              }),
            )
          : undefined,
      resource_kind_ids:
        scopeMode !== "selected_networks" && selectedKindIDs.length > 0
          ? [...selectedKindIDs].sort()
          : undefined,
    });
  };
  const queriedVPCs = vpcs.data?.pages.flatMap((page) => page.items) ?? [];
  const queriedVSwitches =
    vswitches.data?.pages.flatMap((page) => page.items) ?? [];
  const regionVPCItems = mergeNetworkOptions(
    selectedVPCs.filter((item) => item.region_id === queryRegionID),
    queriedVPCs,
  );
  const regionVSwitchItems = mergeNetworkOptions(
    selectedVSwitches.filter((item) => item.region_id === queryRegionID),
    queriedVSwitches,
  );
  const regionVPCs = regionVPCItems.map((item) => ({
    value: networkKey(item),
    label: item.name || item.native_id,
    description: `${item.region_id} · ${item.native_id}`,
  }));
  const regionVSwitches = regionVSwitchItems.map((item) => ({
    value: networkKey(item),
    label: item.name || item.native_id,
    description: `${item.region_id} · ${item.native_id}`,
    disabled: selectedVPCs.some(
      (vpc) =>
        vpc.region_id === item.region_id &&
        vpc.native_id === item.parent_native_id,
    ),
  }));
  const currentVPCValues = selectedVPCs
    .filter((item) => item.region_id === queryRegionID)
    .map(networkKey);
  const currentVSwitchValues = selectedVSwitches
    .filter((item) => item.region_id === queryRegionID)
    .map(networkKey);
  return (
    <TaskCreationDialog
      open={open}
      onOpenChange={onOpenChange}
      returnFocusRef={returnFocusRef}
      title={t("scans.start")}
      pending={mutation.isPending}
      onSubmit={submit}
      bodyClassName="space-y-5"
      footer={
        <>
          <Button
            type="button"
            variant="ghost"
            disabled={mutation.isPending}
            onClick={() => onOpenChange(false)}
          >
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={!canSubmit || mutation.isPending}>
            {mutation.isPending ? t("scans.scheduling") : t("scans.schedule")}
          </Button>
        </>
      }
    >
      {mutation.error && (
        <Alert variant="destructive">
          <AlertDescription>{formatError(mutation.error)}</AlertDescription>
        </Alert>
      )}
      <div className="space-y-3">
        <Label>{t("scans.scope")}</Label>
        <RadioGroup
          value={scopeMode}
          onValueChange={(value) => setScopeMode(value as ScanScopeMode)}
          className="grid gap-2"
        >
          {(
            [
              ["all_active_regions", t("scans.allActiveRegions")],
              ["selected_regions", t("scans.selectedRegions")],
              ["selected_networks", t("scans.selectedNetworks")],
            ] as const
          ).map(([value, title]) => (
            <label
              key={value}
              className="flex cursor-pointer items-center gap-3 rounded-lg border px-3 py-2.5 has-[[data-state=checked]]:border-foreground"
            >
              <RadioGroupItem value={value} aria-label={title} />
              <span className="text-sm font-medium">{title}</span>
            </label>
          ))}
        </RadioGroup>
      </div>
      {scopeMode === "selected_regions" && (
        <div className="space-y-2">
          <Label>{t("scans.regions")}</Label>
          <ResourceKindPicker
            label={t("scans.regions")}
            values={selectedRegionIDs}
            options={scanRegionOptions}
            allLabel={t("scans.chooseRegions")}
            selectedCountLabel={(count) =>
              t("scans.selectedRegionCount", { count })
            }
            selectedListLabel={t("scans.selectedRegionList")}
            removeLabel={(name) => t("scans.removeRegion", { name })}
            searchPlaceholder={t("scans.searchRegion")}
            emptyLabel={t("scans.noRegions")}
            onValuesChange={setSelectedRegionIDs}
          />
        </div>
      )}
      {scopeMode === "selected_networks" && (
        <div className="space-y-4 rounded-lg border p-3">
          <div className="space-y-2">
            <Label>{t("scans.queryRegion")}</Label>
            <Combobox
              label={t("scans.queryRegion")}
              value={queryRegionID}
              options={physicalRegionOptions}
              placeholder={t("scans.chooseRegion")}
              searchPlaceholder={t("scans.searchRegion")}
              emptyLabel={t("scans.noRegions")}
              onValueChange={setQueryRegionID}
            />
          </div>
          <MultiCombobox
            label="VPC"
            placeholder={t("scans.searchVPC")}
            searchPlaceholder={t("scans.searchVPC")}
            emptyLabel={t("scans.noNetworkTargets")}
            selectedListLabel={t("scans.selectedNetworkList")}
            removeLabel={(name) => t("scans.removeNetworkTarget", { name })}
            options={regionVPCs}
            selected={currentVPCValues}
            query={vpcQuery}
            onQueryChange={setVPCQuery}
            loading={vpcs.isFetching}
            hasMore={vpcs.hasNextPage}
            loadMoreLabel={t("common.loadMore")}
            onLoadMore={() => void vpcs.fetchNextPage()}
            onSelectedChange={(values) => {
              const available = new Map(
                regionVPCItems.map((item) => [networkKey(item), item]),
              );
              const next = [
                ...selectedVPCs.filter(
                  (item) => item.region_id !== queryRegionID,
                ),
                ...values.flatMap((value) =>
                  available.has(value) ? [available.get(value)!] : [],
                ),
              ];
              setSelectedVPCs(next);
              setSelectedVSwitches((current) =>
                current.filter(
                  (item) =>
                    !next.some(
                      (vpc) =>
                        vpc.region_id === item.region_id &&
                        vpc.native_id === item.parent_native_id,
                    ),
                ),
              );
            }}
          />
          <MultiCombobox
            label="vSwitch"
            placeholder={t("scans.searchVSwitch")}
            searchPlaceholder={t("scans.searchVSwitch")}
            emptyLabel={t("scans.noNetworkTargets")}
            selectedListLabel={t("scans.selectedNetworkList")}
            removeLabel={(name) => t("scans.removeNetworkTarget", { name })}
            options={regionVSwitches}
            selected={currentVSwitchValues}
            query={vSwitchQuery}
            onQueryChange={setVSwitchQuery}
            loading={vswitches.isFetching}
            hasMore={vswitches.hasNextPage}
            loadMoreLabel={t("common.loadMore")}
            onLoadMore={() => void vswitches.fetchNextPage()}
            onSelectedChange={(values) => {
              const available = new Map(
                regionVSwitchItems.map((item) => [networkKey(item), item]),
              );
              setSelectedVSwitches([
                ...selectedVSwitches.filter(
                  (item) => item.region_id !== queryRegionID,
                ),
                ...values.flatMap((value) =>
                  available.has(value) ? [available.get(value)!] : [],
                ),
              ]);
            }}
          />
          <p className="text-xs text-muted-foreground">
            {t("scans.networkHint", {
              count: networkTargets.length,
              regions: networkRegionCount,
            })}
          </p>
        </div>
      )}
      {scopeMode !== "selected_networks" && (
        <div className="space-y-2">
          <Label>{t("common.resourceKind")}</Label>
          <ResourceKindPicker
            label={t("common.resourceKind")}
            values={selectedKindIDs}
            options={kindOptions}
            allLabel={t("scans.allKinds")}
            selectedCountLabel={(count) =>
              t("common.selectedResourceKindCount", { count })
            }
            selectedListLabel={t("common.selectedResourceKinds")}
            removeLabel={(name) => t("common.removeResourceKind", { name })}
            searchPlaceholder={t("scans.searchKind")}
            emptyLabel={t("scans.noKinds")}
            onValuesChange={setSelectedKindIDs}
          />
        </div>
      )}
      {regions.length === 0 && (
        <Alert>
          <ScanLine />
          <AlertDescription>{t("scans.noActiveRegions")}</AlertDescription>
        </Alert>
      )}
    </TaskCreationDialog>
  );
}

function networkKey(
  item: Pick<NetworkTargetOption, "region_id" | "native_id">,
) {
  return `${item.region_id}\u0000${item.native_id}`;
}

function mergeNetworkOptions(
  selected: NetworkTargetOption[],
  queried: NetworkTargetOption[],
) {
  return [
    ...new Map(
      [...selected, ...queried].map((item) => [networkKey(item), item]),
    ).values(),
  ];
}

export function canonicalNetworkTargets(
  vpcs: NetworkTargetOption[],
  vswitches: NetworkTargetOption[],
) {
  const selectedVPCs = new Set(
    vpcs.map((item) => `${item.region_id}\u0000${item.native_id}`),
  );
  const values = [
    ...vpcs,
    ...vswitches.filter(
      (item) =>
        !selectedVPCs.has(`${item.region_id}\u0000${item.parent_native_id}`),
    ),
  ];
  return [
    ...new Map(
      values.map((item) => [`${item.kind}\u0000${networkKey(item)}`, item]),
    ).values(),
  ].sort(
    (left, right) =>
      compareRegionIDs(left.region_id, right.region_id) ||
      networkKey(left).localeCompare(networkKey(right)),
  );
}

function useDebounced(value: string, delay: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}
