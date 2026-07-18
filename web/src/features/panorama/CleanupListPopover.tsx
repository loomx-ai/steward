import { useState } from "react";
import { ChevronRight, ListChecks, LocateFixed, X } from "lucide-react";
import { useNavigate } from "react-router-dom";
import type { CleanupSelector } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Badge } from "@/components/ui/badge";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { writeCleanupSelectionHandoff } from "../cleanup/selection";
import { expandCleanupSelectors, type CleanupTarget } from "./cleanupSelection";
import { useCleanupSelection } from "./CleanupSelectionContext";

export function CleanupListPopover({
  onLocate,
  position = "absolute",
}: {
  onLocate: (target: CleanupTarget) => void;
  position?: "absolute" | "fixed";
}) {
  const connection = useRequiredConnection();
  const navigate = useNavigate();
  const { formatNumber, t } = useLocale();
  const { targets, clearTargets, removeBatchMember, removeTarget } =
    useCleanupSelection();
  const [clearConfirmationOpen, setClearConfirmationOpen] = useState(false);
  const [popoverOpen, setPopoverOpen] = useState(false);
  const count = targets.length;

  if (count === 0) return null;

  const createCleanupTask = () => {
    const selectors = expandCleanupSelectors(targets);
    writeCleanupSelectionHandoff(connection.id, selectors);
    navigate("/cleanup/new", {
      state: { fromPanoramaCleanup: true },
    });
  };

  return (
    <>
      <div
        className={`${position === "fixed" ? "fixed" : "absolute"} right-4 bottom-4 z-30`}
      >
        <Popover open={popoverOpen} onOpenChange={setPopoverOpen}>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              className="h-11 min-w-36 gap-2 bg-background/95 px-3 shadow-md backdrop-blur"
              aria-label={t("panorama.cleanupListCount", {
                count: formatNumber(count),
              })}
            >
              <ListChecks aria-hidden="true" />
              <span>{t("panorama.cleanupList")}</span>
              <span className="min-w-5 rounded-full bg-warning px-1.5 py-0.5 text-center text-[11px] font-semibold text-warning-foreground">
                {formatNumber(count)}
              </span>
            </Button>
          </PopoverTrigger>
          <PopoverContent
            side="top"
            align="end"
            sideOffset={8}
            className="w-[min(26rem,calc(100vw-2rem))] p-0"
            aria-label={t("panorama.cleanupList")}
          >
            <header className="border-b px-4 py-3">
              <h2 className="font-semibold">{t("panorama.cleanupList")}</h2>
            </header>

            <div className="max-h-[min(28rem,55vh)] overflow-y-auto">
              {targets.length === 0 ? (
                <p className="px-4 py-8 text-center text-sm text-muted-foreground">
                  {t("panorama.cleanupListEmpty")}
                </p>
              ) : (
                <ul className="divide-y">
                  {targets.map((target) => (
                    <CleanupTargetRow
                      key={`${target.connectionId}:${target.key}`}
                      target={target}
                      onLocate={(locatedTarget) => {
                        setPopoverOpen(false);
                        onLocate(locatedTarget);
                      }}
                      onRemove={() => removeTarget(target.key)}
                      onRemoveBatchMember={(assetID) =>
                        removeBatchMember(target.key, assetID)
                      }
                    />
                  ))}
                </ul>
              )}
            </div>

            <footer className="flex items-center justify-between gap-2 border-t p-3">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={targets.length === 0}
                onClick={() => setClearConfirmationOpen(true)}
              >
                {t("panorama.clearCleanupList")}
              </Button>
              <Button
                type="button"
                size="sm"
                disabled={targets.length === 0}
                onClick={createCleanupTask}
              >
                {t("panorama.createCleanupTask")}
              </Button>
            </footer>
          </PopoverContent>
        </Popover>
      </div>

      <AlertDialog
        open={clearConfirmationOpen}
        onOpenChange={setClearConfirmationOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("panorama.clearCleanupListTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("panorama.clearCleanupListDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                clearTargets();
                setClearConfirmationOpen(false);
              }}
            >
              {t("panorama.clearCleanupList")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function CleanupTargetRow({
  target,
  onLocate,
  onRemove,
  onRemoveBatchMember,
}: {
  target: CleanupTarget;
  onLocate: (target: CleanupTarget) => void;
  onRemove: () => void;
  onRemoveBatchMember: (assetID: string) => void;
}) {
  const { formatNumber, t } = useLocale();
  const kindLabel = t(cleanupTargetKindMessage(target.kind));
  const removeLabel = t("panorama.removeCleanupTarget", {
    name: target.displayName,
  });
  const locateLabel = t("panorama.locateCleanupTarget", {
    name: target.displayName,
  });
  const contextParts: string[] = [];
  if (target.kind === "vpc") {
    const region = target.locationContext?.region;
    if (region) {
      contextParts.push(
        t("panorama.cleanupListOwningRegion", {
          region: topologyContextLabel(region),
        }),
      );
    }
  } else if (target.kind === "resource" || target.kind === "resource_batch") {
    const region = target.locationContext?.region;
    const vpc = target.locationContext?.vpc;
    const scope = target.locationContext?.scope;
    if (region) {
      contextParts.push(
        t("panorama.cleanupListOwningRegion", {
          region: topologyContextLabel(region),
        }),
      );
    }
    if (vpc) {
      contextParts.push(
        t("panorama.cleanupListOwningVpc", {
          vpc: topologyContextLabel(vpc),
        }),
      );
    } else if (scope) {
      contextParts.push(
        t("panorama.cleanupListOwningScope", {
          scope: topologyContextLabel(scope),
        }),
      );
    }
  }
  if (target.resourceCount !== undefined && target.kind !== "resource_batch") {
    contextParts.push(
      t("panorama.cleanupListResourceCount", {
        count: formatNumber(target.resourceCount),
      }),
    );
  }
  const context = contextParts.join(" · ");
  const [batchOpen, setBatchOpen] = useState(false);

  if (target.kind !== "resource_batch") {
    return (
      <li className="flex items-center gap-3 px-4 py-3">
        <TargetDescription
          kind={kindLabel}
          name={target.displayName}
          context={context}
        />
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="ml-auto shrink-0 text-muted-foreground hover:text-foreground"
          aria-label={locateLabel}
          title={locateLabel}
          onClick={() => onLocate(target)}
        >
          <LocateFixed aria-hidden="true" />
        </Button>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="shrink-0"
          aria-label={removeLabel}
          onClick={onRemove}
        >
          <X aria-hidden="true" />
        </Button>
      </li>
    );
  }

  const members = batchMembers(target);
  const total = target.resourceCount ?? members.length;
  return (
    <li>
      <Collapsible open={batchOpen} onOpenChange={setBatchOpen}>
        <div className="flex items-center gap-2 px-4 py-3">
          <CollapsibleTrigger asChild>
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              className="group shrink-0"
              aria-label={t(
                batchOpen
                  ? "panorama.collapseCleanupBatch"
                  : "panorama.expandCleanupBatch",
                { name: target.displayName },
              )}
            >
              <ChevronRight
                aria-hidden="true"
                className="transition-transform group-data-[state=open]:rotate-90"
              />
            </Button>
          </CollapsibleTrigger>
          <TargetDescription
            kind={kindLabel}
            name={target.displayName}
            context={context}
          />
          <span className="ml-auto shrink-0 text-xs tabular-nums text-muted-foreground">
            {formatNumber(members.length)}/{formatNumber(total)}
          </span>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            className="shrink-0 text-muted-foreground hover:text-foreground"
            aria-label={locateLabel}
            title={locateLabel}
            onClick={() => onLocate(target)}
          >
            <LocateFixed aria-hidden="true" />
          </Button>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            className="shrink-0"
            aria-label={removeLabel}
            onClick={onRemove}
          >
            <X aria-hidden="true" />
          </Button>
        </div>
        <CollapsibleContent>
          <ul className="border-t bg-muted/20 py-1">
            {members.map((member) => (
              <li
                key={member.assetID}
                className="flex items-center gap-2 py-2 pr-4 pl-12"
              >
                <TargetDescription
                  kind={t("panorama.cleanupListResource")}
                  name={member.displayName}
                  context={context}
                />
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  className="shrink-0 text-muted-foreground hover:text-foreground"
                  aria-label={t("panorama.locateCleanupTarget", {
                    name: member.displayName,
                  })}
                  title={t("panorama.locateCleanupTarget", {
                    name: member.displayName,
                  })}
                  onClick={() => onLocate(batchMemberTarget(target, member))}
                >
                  <LocateFixed aria-hidden="true" />
                </Button>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  className="shrink-0"
                  aria-label={t("panorama.removeCleanupTarget", {
                    name: member.displayName,
                  })}
                  onClick={() => onRemoveBatchMember(member.assetID)}
                >
                  <X aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        </CollapsibleContent>
      </Collapsible>
    </li>
  );
}

function TargetDescription({
  kind,
  name,
  context,
}: {
  kind: string;
  name: string;
  context?: string;
}) {
  return (
    <span className="min-w-0 flex-1">
      <span className="flex min-w-0 items-center gap-2">
        <Badge
          variant="secondary"
          className="h-5 rounded-md px-1.5 py-0 text-[10px] leading-none font-medium"
        >
          {kind}
        </Badge>
        <strong className="min-w-0 truncate text-sm font-medium">{name}</strong>
      </span>
      <span
        className="mt-0.5 block min-h-4 truncate text-xs text-muted-foreground"
        title={context}
        aria-hidden={context ? undefined : "true"}
      >
        {context || "\u00a0"}
      </span>
    </span>
  );
}

function batchMemberTarget(
  batch: CleanupTarget,
  member: { assetID: string; displayName: string },
): CleanupTarget {
  const selector = (
    Array.isArray(batch.selector) ? batch.selector : [batch.selector]
  ).find(
    (candidate) =>
      candidate.kind === "asset" && candidate.asset_id === member.assetID,
  );
  const key = `asset:${member.assetID}`;
  return {
    ...batch,
    key,
    kind: "resource",
    displayName: member.displayName,
    selector:
      selector ??
      ({
        kind: "asset",
        asset_id: member.assetID,
        display_name: member.displayName,
      } satisfies CleanupSelector),
    ancestryKeys: [...batch.ancestryKeys.slice(0, -1), key],
    memberAssetIds: undefined,
    resourceCount: undefined,
  };
}

function cleanupTargetKindMessage(
  kind: CleanupTarget["kind"],
):
  | "panorama.cleanupListRegion"
  | "panorama.cleanupListVpc"
  | "panorama.cleanupListResource"
  | "panorama.cleanupListBatch" {
  switch (kind) {
    case "region":
      return "panorama.cleanupListRegion";
    case "vpc":
      return "panorama.cleanupListVpc";
    case "resource":
      return "panorama.cleanupListResource";
    case "resource_batch":
      return "panorama.cleanupListBatch";
  }
}

function topologyContextLabel(context: {
  key: string;
  name: string;
  native_id?: string;
}): string {
  return context.name.trim() || context.native_id?.trim() || context.key;
}

function batchMembers(target: CleanupTarget): Array<{
  assetID: string;
  displayName: string;
}> {
  const selectors: CleanupSelector[] = Array.isArray(target.selector)
    ? target.selector
    : [target.selector];
  const displayNames = new Map(
    selectors.flatMap((selector) =>
      selector.kind === "asset"
        ? [
            [
              selector.asset_id,
              selector.display_name?.trim() || selector.asset_id,
            ],
          ]
        : [],
    ),
  );
  return (target.memberAssetIds ?? []).map((assetID) => ({
    assetID,
    displayName: displayNames.get(assetID) ?? assetID,
  }));
}
