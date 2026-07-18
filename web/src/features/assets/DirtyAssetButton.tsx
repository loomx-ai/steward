import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { setAssetDirty } from "@/api/client";
import type { Asset } from "@/api/types";
import { DirtyDataIcon } from "@/components/domain/DirtyDataIcon";
import { Button } from "@/components/ui/button";
import { ContextMenuItem } from "@/components/ui/context-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useLocale } from "@/i18n/LocaleProvider";

export interface DirtyAssetTarget {
  connectionId: string;
  id: string;
  dirty: boolean;
}

function useDirtyAssetMutation(
  target: DirtyAssetTarget,
  targets: readonly DirtyAssetTarget[] = [target],
) {
  const queryClient = useQueryClient();
  const { formatError, t } = useLocale();
  const nextDirty = !target.dirty;
  const mutationTargets = [
    ...new Map(
      [target, ...targets].map((candidate) => [
        `${candidate.connectionId}\u0000${candidate.id}`,
        candidate,
      ]),
    ).values(),
  ].filter((candidate) => candidate.dirty !== nextDirty);
  return useMutation({
    mutationFn: () =>
      Promise.all(
        mutationTargets.map((candidate) =>
          setAssetDirty(candidate.connectionId, candidate.id, nextDirty),
        ),
      ),
    onSuccess: (updatedAssets) => {
      for (const updated of updatedAssets) {
        const connectionId = updated.identity.connection_id;
        queryClient.setQueryData(["asset", connectionId, updated.id], updated);
        queryClient.setQueryData(
          ["panorama-resource-detail", connectionId, updated.id],
          updated,
        );
        queryClient.setQueryData(
          ["panorama-network-resource-detail", connectionId, updated.id],
          updated,
        );
      }
      toast.success(t(nextDirty ? "asset.markedDirty" : "asset.unmarkedDirty"));
    },
    onError: (error) => toast.error(formatError(error)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["assets"] });
      void queryClient.invalidateQueries({ queryKey: ["topology"] });
      void queryClient.invalidateQueries({ queryKey: ["panorama-search"] });
    },
  });
}

export function DirtyAssetButton({
  asset,
  connectionId,
  iconOnly = false,
  size = "default",
}: {
  asset: Asset;
  connectionId: string;
  iconOnly?: boolean;
  size?: "default" | "sm";
}) {
  const { t } = useLocale();
  const dirty = Boolean(asset.dirty);
  const label = t(dirty ? "asset.unmarkDirty" : "asset.markDirty");
  const mutation = useDirtyAssetMutation({
    connectionId,
    id: asset.id,
    dirty,
  });
  const button = (
    <Button
      type="button"
      variant={dirty ? "secondary" : "outline"}
      size={iconOnly ? "icon-sm" : size}
      className={iconOnly ? "size-11 sm:size-8" : undefined}
      disabled={mutation.isPending}
      aria-label={
        iconOnly
          ? `${label}: ${asset.name || asset.identity.native_id}`
          : undefined
      }
      aria-pressed={dirty}
      onClick={() => mutation.mutate()}
    >
      <DirtyDataIcon aria-hidden="true" className="size-4" />
      {!iconOnly && label}
    </Button>
  );

  if (!iconOnly) return button;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{button}</TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function DirtyAssetContextMenuItem({
  target,
  targets,
}: {
  target: DirtyAssetTarget;
  targets?: readonly DirtyAssetTarget[];
}) {
  const { t } = useLocale();
  const mutation = useDirtyAssetMutation(target, targets);
  const label = t(target.dirty ? "asset.unmarkDirty" : "asset.markDirty");

  return (
    <ContextMenuItem
      disabled={mutation.isPending}
      onSelect={() => mutation.mutate()}
    >
      {label}
    </ContextMenuItem>
  );
}
