import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type RefObject,
} from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight, Network, Plus, X } from "lucide-react";
import {
  Link,
  useLocation,
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import {
  createCleanupTask,
  findAssets,
  listProviderCatalog,
} from "@/api/client";
import type { Asset, CleanupSelector } from "@/api/types";
import {
  compareResourceKindOptionsByProduct,
  resourceProductName,
  resourceTypeName,
} from "@/components/domain/resourceKindLabel";
import { ResourceKindPicker } from "@/components/domain/ResourceKindPicker";
import { TaskCreationDialog } from "@/components/patterns/TaskCreationDialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useCleanupSelection } from "@/features/panorama/CleanupSelectionContext";
import {
  expandCleanupSelectors,
  type CleanupTarget,
} from "@/features/panorama/cleanupSelection";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  clearCleanupSelectionHandoff,
  decodeSelector,
  dedupeSelectors,
  readCleanupSelectionHandoff,
  selectorKey,
} from "./selection";

interface CleanupTaskCreationIntent {
  connectionID: string;
  input: Parameters<typeof createCleanupTask>[1];
  cleanupTargets?: readonly CleanupTarget[];
}

export function CleanupTaskBuilder({
  open = true,
  onOpenChange = () => {},
  returnFocusRef,
  onCreated,
}: {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
  onCreated?: (id: string) => void;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const connection = useRequiredConnection();
  const cleanupSelection = useCleanupSelection();
  const { formatError, locale, t } = useLocale();
  const [params] = useSearchParams();
  const fromPanoramaCleanup = location.state?.fromPanoramaCleanup === true;
  const fromCleanupReview = location.state?.fromCleanupReview === true;
  const fromSelectionHandoff = fromPanoramaCleanup || fromCleanupReview;
  const encodedSelector = params.get("selector") ?? "";
  const decodedSelector = encodedSelector
    ? decodeSelector(encodedSelector)
    : null;
  const [selectors, setSelectors] = useState<CleanupSelector[]>(() => {
    const assetSelectors: CleanupSelector[] = (params.get("assets") ?? "")
      .split(",")
      .filter(Boolean)
      .map((asset_id) => ({ kind: "asset", asset_id }));
    return dedupeSelectors([
      ...(fromSelectionHandoff
        ? readCleanupSelectionHandoff(connection.id)
        : []),
      ...(fromCleanupReview && Array.isArray(location.state?.selectors)
        ? location.state.selectors
        : []),
      ...(fromPanoramaCleanup
        ? expandCleanupSelectors(cleanupSelection.targets)
        : []),
      ...(decodedSelector ? [decodedSelector] : []),
      ...assetSelectors,
    ]);
  });
  const [assetID, setAssetID] = useState("");
  const [resourceKindIDs, setResourceKindIDs] = useState<string[]>([]);
  const [selectionError, setSelectionError] = useState(
    encodedSelector && !decodedSelector ? t("cleanup.invalidSelector") : "",
  );
  const activeConnectionID = useRef(connection.id);
  activeConnectionID.current = connection.id;
  const draftConnectionID = useRef(connection.id);
  const initialConnectionID = useRef(connection.id);
  const initialInputsPending = useRef(
    fromPanoramaCleanup &&
      cleanupSelection.hydratedConnectionID !== connection.id,
  );
  const consumedCleanupConnectionID = useRef<string | null>(
    fromPanoramaCleanup &&
      cleanupSelection.hydratedConnectionID === connection.id
      ? connection.id
      : null,
  );
  const individualAssetIDs = selectors.flatMap((selector) =>
    selector.kind === "asset" ? [selector.asset_id] : [],
  );
  const assets = useQuery({
    queryKey: ["cln-assets", connection.id, individualAssetIDs],
    queryFn: () => findAssets(connection.id, individualAssetIDs),
    enabled: individualAssetIDs.length > 0,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
    enabled: open,
  });
  const assetsByID = useMemo(
    () => new Map((assets.data ?? []).map((asset) => [asset.id, asset])),
    [assets.data],
  );
  const kindOptions = useMemo(() => {
    const available = new Map(
      (catalog.data ?? [])
        .filter((bundle) => bundle.provider === connection.provider)
        .flatMap((bundle) => bundle.kinds)
        .map((kind) => [kind.id, kind] as const),
    );
    return [...available.values()]
      .map((kind) => {
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
  }, [catalog.data, connection.provider, locale]);
  const create = useMutation({
    mutationFn: (intent: CleanupTaskCreationIntent) =>
      createCleanupTask(intent.connectionID, intent.input),
    onSuccess: (aggregate, intent) => {
      if (activeConnectionID.current !== intent.connectionID) return;
      if (
        intent.cleanupTargets &&
        !cleanupSelection.consumeTargets(
          intent.connectionID,
          intent.cleanupTargets,
        )
      ) {
        return;
      }
      clearCleanupSelectionHandoff(intent.connectionID);
      if (onCreated) onCreated(aggregate.task.id);
      else navigate(`/cleanup/${aggregate.task.id}`);
    },
  });
  useEffect(() => {
    if (draftConnectionID.current !== connection.id) {
      draftConnectionID.current = connection.id;
      setSelectors([]);
      setAssetID("");
      setResourceKindIDs([]);
      setSelectionError("");
      create.reset();
    }
    if (
      !fromPanoramaCleanup ||
      cleanupSelection.hydratedConnectionID !== connection.id ||
      consumedCleanupConnectionID.current === connection.id
    ) {
      return;
    }
    consumedCleanupConnectionID.current = connection.id;
    const includeInitialInputs =
      initialInputsPending.current &&
      initialConnectionID.current === connection.id;
    if (includeInitialInputs) initialInputsPending.current = false;
    const assetSelectors: CleanupSelector[] = includeInitialInputs
      ? (params.get("assets") ?? "")
          .split(",")
          .filter(Boolean)
          .map((asset_id) => ({ kind: "asset", asset_id }))
      : [];
    setSelectors(
      dedupeSelectors([
        ...readCleanupSelectionHandoff(connection.id),
        ...expandCleanupSelectors(
          cleanupSelection.targets.filter(
            (target) => target.connectionId === connection.id,
          ),
        ),
        ...(includeInitialInputs && decodedSelector ? [decodedSelector] : []),
        ...assetSelectors,
      ]),
    );
  }, [
    cleanupSelection.hydratedConnectionID,
    cleanupSelection.targets,
    connection.id,
    create,
    decodedSelector,
    fromPanoramaCleanup,
    params,
  ]);
  const addAsset = () => {
    const normalized = assetID.trim();
    if (!normalized) return;
    setSelectors((current) =>
      dedupeSelectors([...current, { kind: "asset", asset_id: normalized }]),
    );
    setAssetID("");
  };
  const removeSelector = (selector: CleanupSelector) => {
    const key = selectorKey(selector);
    setSelectors((current) =>
      current.filter((candidate) => selectorKey(candidate) !== key),
    );
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (
      fromPanoramaCleanup &&
      cleanupSelection.hydratedConnectionID !== connection.id
    ) {
      return;
    }
    create.mutate({
      connectionID: connection.id,
      input: {
        selectors,
        request_options: {},
      },
      cleanupTargets: fromPanoramaCleanup
        ? cleanupSelection.targets
        : undefined,
    });
  };
  const createBelongsToConnection =
    create.variables?.connectionID === connection.id;
  const creationPending = create.isPending && createBelongsToConnection;
  const error =
    selectionError ||
    assets.error ||
    (createBelongsToConnection ? create.error : null);

  return (
    <TaskCreationDialog
      open={open}
      onOpenChange={onOpenChange}
      returnFocusRef={returnFocusRef}
      title={t("cleanup.buildTitle")}
      pending={creationPending}
      onSubmit={submit}
      bodyClassName="space-y-4"
      footer={
        <>
          <Button
            type="button"
            variant="ghost"
            disabled={creationPending}
            onClick={() => onOpenChange(false)}
          >
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            disabled={
              selectors.length === 0 ||
              creationPending ||
              (fromPanoramaCleanup &&
                cleanupSelection.hydratedConnectionID !== connection.id)
            }
          >
            {creationPending ? t("cleanup.building") : t("cleanup.buildReview")}
            {!creationPending && <ArrowRight />}
          </Button>
        </>
      }
    >
      {error && (
        <Alert variant="destructive">
          <AlertDescription>
            {typeof error === "string" ? error : formatError(error)}
          </AlertDescription>
        </Alert>
      )}
      <section className="min-w-0">
        <header className="flex flex-wrap items-center gap-2">
          <h2 className="text-base font-semibold">{t("cleanup.selectors")}</h2>
          <Badge variant="secondary">
            {t("cleanup.selected", { count: selectors.length })}
          </Badge>
        </header>

        {selectors.length === 0 ? (
          <Empty className="min-h-36 py-6">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Network />
              </EmptyMedia>
              <EmptyTitle>{t("cleanup.noSelectorsTitle")}</EmptyTitle>
              <EmptyDescription>{t("cleanup.noSelectors")}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button asChild variant="outline">
                <Link to="/panorama">
                  {t("cleanup.chooseFromPanorama")}
                  <ArrowRight />
                </Link>
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <ul className="mt-3 divide-y">
            {selectors.map((selector) => (
              <SelectorRow
                key={selectorKey(selector)}
                selector={selector}
                asset={
                  selector.kind === "asset"
                    ? assetsByID.get(selector.asset_id)
                    : undefined
                }
                onRemove={() => removeSelector(selector)}
              />
            ))}
          </ul>
        )}

        <div className="mt-4 border-t pt-4">
          <Label htmlFor="asset-id">{t("cleanup.addAsset")}</Label>
          <div className="mt-2 grid gap-2 sm:grid-cols-[minmax(12rem,15rem)_minmax(0,1fr)_auto]">
            <ResourceKindPicker
              label={t("common.resourceKind")}
              values={resourceKindIDs}
              options={kindOptions}
              allLabel={t("assets.allResourceKinds")}
              selectedCountLabel={(count) =>
                t("common.selectedResourceKindCount", { count })
              }
              selectedListLabel={t("common.selectedResourceKinds")}
              removeLabel={(name) => t("common.removeResourceKind", { name })}
              searchPlaceholder={t("assets.searchResourceKind")}
              emptyLabel={t("assets.noResourceKinds")}
              onValuesChange={setResourceKindIDs}
            />
            <Input
              id="asset-id"
              autoFocus
              className="h-11 min-w-0 font-mono sm:h-9"
              value={assetID}
              placeholder={t("cleanup.assetIdPlaceholder")}
              onChange={(event) => setAssetID(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  addAsset();
                }
              }}
            />
            <Button
              type="button"
              variant="outline"
              className="h-11 sm:h-9"
              disabled={!assetID.trim()}
              onClick={addAsset}
            >
              <Plus />
              {t("common.add")}
            </Button>
          </div>
        </div>
      </section>
    </TaskCreationDialog>
  );
}

function SelectorRow({
  selector,
  asset,
  onRemove,
}: {
  selector: CleanupSelector;
  asset?: Asset;
  onRemove?: () => void;
}) {
  const { t } = useLocale();
  const details = selectorDetails(selector, asset, t);
  return (
    <li className="flex min-w-0 items-center gap-3 py-3">
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-2">
          <Badge
            variant="secondary"
            className="h-5 rounded-md px-1.5 py-0 text-[10px] leading-none font-medium"
          >
            {details.kind}
          </Badge>
          <strong className="min-w-0 truncate text-sm font-medium">
            {details.name}
          </strong>
        </span>
        <span className="mt-0.5 block truncate font-mono text-xs text-muted-foreground">
          {details.identity}
        </span>
      </span>
      {onRemove && (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="size-11 shrink-0 sm:size-8"
          onClick={onRemove}
          aria-label={t("cleanup.removeSelector", { name: details.name })}
        >
          <X aria-hidden="true" />
        </Button>
      )}
    </li>
  );
}

function selectorDetails(
  selector: CleanupSelector,
  asset: Asset | undefined,
  t: ReturnType<typeof useLocale>["t"],
) {
  switch (selector.kind) {
    case "connection":
      return {
        kind: t("cleanup.selectorConnection"),
        name: selector.display_name || selector.connection_id,
        identity: selector.connection_id,
      };
    case "scope":
      return {
        kind: `${t("cleanup.selectorScope")} · ${selector.scope_kind || t("common.scope")}`,
        name: selector.display_name || selector.scope_id,
        identity: selector.scope_id,
      };
    case "group":
      return {
        kind: t("cleanup.selectorGroup"),
        name: selector.display_name || selector.group_key,
        identity: selector.group_key,
      };
    case "asset":
      return {
        kind: t("cleanup.selectorAsset"),
        name:
          selector.display_name ||
          asset?.name ||
          asset?.identity.native_id ||
          selector.asset_id,
        identity: selector.asset_id,
      };
  }
}
