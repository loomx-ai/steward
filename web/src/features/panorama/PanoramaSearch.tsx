import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CompositionEvent,
  type KeyboardEvent,
} from "react";
import { useQuery } from "@tanstack/react-query";
import { LoaderCircle, X } from "lucide-react";
import { listAssets, listConnectionRegions, listScopes } from "@/api/client";
import type { ResourceKind, Scope } from "@/api/types";
import {
  ResourceQueryInput,
  ResourceSearchModeToggle,
  type ResourceQueryFieldValues,
  type ResourceSearchMode,
} from "@/components/domain/ResourceQuery";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import {
  buildPanoramaSearchResults,
  type PanoramaSearchResult,
} from "./search";
import type { PanoramaRoute } from "./route";

const SEARCH_DELAY_MS = 160;

export function PanoramaSearch({
  connectionId,
  kinds,
  queryKinds,
  scope,
  resourceQuery,
  resourceQueryFieldValues,
  resourceQueryError,
  onResourceQueryApply,
  onHighlight,
  onSelect,
}: {
  connectionId: string;
  kinds: ReadonlyMap<string, ResourceKind>;
  queryKinds: readonly ResourceKind[];
  scope: PanoramaRoute;
  resourceQuery: string;
  resourceQueryFieldValues?: ResourceQueryFieldValues;
  resourceQueryError?: string;
  onResourceQueryApply: (query: string) => void;
  onHighlight: (result: PanoramaSearchResult | undefined) => void;
  onSelect: (result: PanoramaSearchResult) => void;
}) {
  const { formatError, locale, t } = useLocale();
  const inputID = useId();
  const listboxID = `${inputID}-results`;
  const [value, setValue] = useState("");
  const [term, setTerm] = useState("");
  const [mode, setMode] = useState<ResourceSearchMode>(() =>
    resourceQuery ? "advanced" : "simple",
  );
  const [advancedQuery, setAdvancedQuery] = useState(resourceQuery);
  const requestedResourceQuery = useRef(resourceQuery);
  const [focused, setFocused] = useState(false);
  const [composing, setComposing] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const rootRef = useRef<HTMLDivElement>(null);

  const scopes = useQuery({
    queryKey: ["panorama-search-scopes", connectionId],
    queryFn: () => loadAllScopes(connectionId),
    enabled: term.length > 0 || mode === "advanced",
    staleTime: 60_000,
  });
  const search = useQuery({
    queryKey: ["panorama-search", connectionId, scope.pathname, term],
    queryFn: async ({ signal }) => {
      const [assets, regions] = await Promise.all([
        listAssets(connectionId, {
          query: term,
          limit: 50,
          ...assetSearchScope(scope),
          searchOrder: "panorama",
          signal,
        }),
        scope.kind === "account"
          ? listConnectionRegions(connectionId, {
              lifecycle: "active",
              query: term,
              signal,
            })
          : Promise.resolve({ items: [] }),
      ]);
      return { assets: assets.items, regions: regions.items };
    },
    enabled: term.length > 0,
    staleTime: 30_000,
  });

  useEffect(() => {
    if (composing) return;
    const timeout = window.setTimeout(
      () => setTerm(value.trim()),
      SEARCH_DELAY_MS,
    );
    return () => window.clearTimeout(timeout);
  }, [composing, value]);

  const results = useMemo(
    () =>
      scopes.isSuccess
        ? buildPanoramaSearchResults({
            term,
            assets: search.data?.assets ?? [],
            regions: search.data?.regions ?? [],
            scopes: scopes.data,
            kinds,
            locale,
            regionTypeName: t("panorama.searchRegionType"),
            scope,
          })
        : [],
    [kinds, locale, scope, scopes.data, scopes.isSuccess, search.data, t, term],
  );
  const advancedFieldValues = useMemo<ResourceQueryFieldValues>(
    () => ({
      ...resourceQueryFieldValues,
      region: [
        ...(resourceQueryFieldValues?.region ?? []),
        ...(scopes.data ?? [])
          .filter((item) => item.kind === "region")
          .map((item) => ({ value: item.native_id, detail: item.name })),
      ],
    }),
    [resourceQueryFieldValues, scopes.data],
  );
  const open = focused && !composing && value.trim().length > 0;
  const pending = search.isFetching || scopes.isPending;
  const error = search.error ?? scopes.error;

  useEffect(() => {
    setActiveIndex((current) =>
      results.length === 0
        ? -1
        : Math.min(Math.max(current, 0), results.length - 1),
    );
  }, [results]);

  useEffect(() => {
    if (open) return;
    setActiveIndex(-1);
    onHighlight(undefined);
  }, [onHighlight, open]);

  useEffect(
    () => () => {
      onHighlight(undefined);
    },
    [onHighlight],
  );

  useEffect(() => {
    if (resourceQuery === requestedResourceQuery.current) return;
    requestedResourceQuery.current = resourceQuery;
    setAdvancedQuery(resourceQuery);
    setMode(resourceQuery ? "advanced" : "simple");
  }, [resourceQuery]);

  const activate = (result: PanoramaSearchResult) => {
    onHighlight(undefined);
    setFocused(false);
    onSelect(result);
  };
  const moveActive = (offset: number) => {
    if (results.length === 0) return;
    const next =
      activeIndex < 0
        ? offset > 0
          ? 0
          : results.length - 1
        : (activeIndex + offset + results.length) % results.length;
    setActiveIndex(next);
    onHighlight(results[next]);
  };
  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      moveActive(event.key === "ArrowDown" ? 1 : -1);
      return;
    }
    if (event.key === "Enter" && activeIndex >= 0 && results[activeIndex]) {
      event.preventDefault();
      activate(results[activeIndex]);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      setFocused(false);
      event.currentTarget.blur();
    }
  };
  const finishComposition = (event: CompositionEvent<HTMLInputElement>) => {
    const next = event.currentTarget.value;
    setComposing(false);
    setValue(next);
    setTerm(next.trim());
  };
  const applyAdvancedQuery = (query: string) => {
    requestedResourceQuery.current = query;
    setAdvancedQuery(query);
    onResourceQueryApply(query);
  };
  const changeMode = (nextMode: ResourceSearchMode) => {
    if (nextMode === mode) return;
    if (nextMode === "advanced") {
      setTerm("");
      setActiveIndex(-1);
      onHighlight(undefined);
      requestedResourceQuery.current = advancedQuery;
      onResourceQueryApply(advancedQuery);
    } else {
      setTerm(value.trim());
      requestedResourceQuery.current = "";
      onResourceQueryApply("");
    }
    setFocused(false);
    setMode(nextMode);
  };

  return (
    <div
      ref={rootRef}
      className="pointer-events-auto relative w-[min(26rem,calc(100vw-2rem))]"
      onBlur={(event) => {
        if (!rootRef.current?.contains(event.relatedTarget)) {
          setFocused(false);
        }
      }}
    >
      <div className="relative">
        <ResourceSearchModeToggle
          mode={mode}
          onModeChange={changeMode}
          className="absolute top-1/2 left-1.5 z-20 -translate-y-1/2"
        />
        {mode === "simple" ? (
          <>
            <Input
              id={inputID}
              role="combobox"
              aria-autocomplete="list"
              aria-controls={listboxID}
              aria-expanded={open}
              aria-activedescendant={
                activeIndex >= 0 ? `${listboxID}-${activeIndex}` : undefined
              }
              aria-label={t("panorama.searchLabel")}
              autoComplete="off"
              className="h-9 bg-background/95 pr-9 pl-10 shadow-md backdrop-blur"
              value={value}
              placeholder={t(
                scope.kind === "account"
                  ? "panorama.searchPlaceholder"
                  : "panorama.searchResourcePlaceholder",
              )}
              onFocus={() => setFocused(true)}
              onChange={(event) => {
                setValue(event.target.value);
                setFocused(true);
              }}
              onCompositionStart={() => setComposing(true)}
              onCompositionEnd={finishComposition}
              onKeyDown={handleKeyDown}
            />
            {pending && value.trim() ? (
              <LoaderCircle
                aria-label={t("panorama.searchLoading")}
                className="absolute top-1/2 right-3 size-4 -translate-y-1/2 animate-spin text-muted-foreground"
              />
            ) : value ? (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="absolute top-1/2 right-1.5 size-7 -translate-y-1/2"
                aria-label={t("panorama.searchClear")}
                onClick={() => {
                  setValue("");
                  setTerm("");
                  setActiveIndex(-1);
                  onHighlight(undefined);
                  document.getElementById(inputID)?.focus();
                }}
              >
                <X aria-hidden="true" />
              </Button>
            ) : null}
          </>
        ) : (
          <ResourceQueryInput
            value={advancedQuery}
            resourceKinds={queryKinds}
            fieldValues={advancedFieldValues}
            error={resourceQueryError}
            onApply={applyAdvancedQuery}
            autoFocus
            inputClassName="h-9 bg-background/95 shadow-md backdrop-blur"
          />
        )}
      </div>

      {mode === "simple" && open && (
        <div
          className="absolute top-[calc(100%+0.375rem)] z-50 w-full overflow-hidden rounded-md border border-border bg-popover/98 text-popover-foreground shadow-lg backdrop-blur"
          data-testid="panorama-search-dropdown"
        >
          <div
            id={listboxID}
            role="listbox"
            aria-label={t("panorama.searchResults")}
            className="max-h-[min(28rem,60vh)] overflow-y-auto p-1"
          >
            {error ? (
              <p role="alert" className="px-3 py-4 text-xs text-destructive">
                {t("panorama.searchFailed", {
                  error: formatError(error),
                })}
              </p>
            ) : !pending && term && results.length === 0 ? (
              <div className="px-3 py-4">
                <p className="text-sm font-medium">
                  {t("panorama.searchEmpty")}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t(
                    scope.kind === "account"
                      ? "panorama.searchEmptyHint"
                      : "panorama.searchResourceEmptyHint",
                  )}
                </p>
              </div>
            ) : (
              results.map((result, index) => (
                <div
                  key={result.key}
                  id={`${listboxID}-${index}`}
                  role="option"
                  aria-selected={activeIndex === index}
                  className={cn(
                    "grid cursor-pointer grid-cols-[minmax(0,1fr)_auto] gap-x-3 rounded-sm px-3 py-2 outline-none transition-colors",
                    activeIndex === index
                      ? "bg-accent text-accent-foreground"
                      : "hover:bg-accent/65",
                  )}
                  data-search-result-key={result.key}
                  onPointerMove={() => {
                    setActiveIndex(index);
                    onHighlight(result);
                  }}
                  onPointerLeave={() => onHighlight(undefined)}
                  onPointerDown={(event) => {
                    event.preventDefault();
                    activate(result);
                  }}
                >
                  <strong
                    className="min-w-0 truncate text-sm font-medium"
                    title={result.name}
                  >
                    {result.name}
                  </strong>
                  <span className="row-span-2 self-center rounded-sm bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {result.typeName}
                  </span>
                  <code
                    className="min-w-0 truncate text-[11px] text-muted-foreground"
                    title={result.resourceId}
                  >
                    {result.resourceId}
                  </code>
                </div>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function assetSearchScope(scope: PanoramaRoute): {
  canvas: "account" | "global" | "region" | "region-public" | "vpc";
  regionID?: string;
  vpcID?: string;
} {
  switch (scope.kind) {
    case "account":
      return { canvas: "account" };
    case "account-global":
      return { canvas: "global" };
    case "region":
      return { canvas: "region", regionID: scope.regionId };
    case "region-public":
      return { canvas: "region-public", regionID: scope.regionId };
    case "vpc":
      return {
        canvas: "vpc",
        regionID: scope.regionId,
        vpcID: scope.vpcId,
      };
  }
}

export async function loadAllScopes(connectionID: string): Promise<Scope[]> {
  const result: Scope[] = [];
  let cursor = "";
  do {
    const page = await listScopes(connectionID, cursor);
    result.push(...page.items);
    cursor = page.next_cursor ?? "";
  } while (cursor);
  return result;
}
