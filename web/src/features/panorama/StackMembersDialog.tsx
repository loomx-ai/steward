import type { TopologyResource } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useLocale } from "@/i18n/LocaleProvider";

export function StackMembersDialog({
  open,
  onOpenChange,
  displayName,
  resources,
  onOpenResource,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  displayName: string;
  resources: readonly TopologyResource[];
  onOpenResource: (resource: TopologyResource) => void;
}) {
  const { formatNumber, t } = useLocale();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[min(40rem,calc(100vh-2rem))] grid-rows-[auto_minmax(0,1fr)] sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{displayName}</DialogTitle>
          <DialogDescription>
            {t("panorama.stackMembers")} · {formatNumber(resources.length)}
          </DialogDescription>
        </DialogHeader>
        <ul className="min-h-0 space-y-2 overflow-y-auto pr-1">
          {resources.map((resource) => {
            const name =
              resource.name.trim() || resource.native_id.trim() || resource.key;
            return (
              <li key={resource.key}>
                <Button
                  type="button"
                  variant="outline"
                  className="h-auto w-full justify-start px-3 py-2 text-left whitespace-normal"
                  aria-label={`${t("panorama.viewDetails")}: ${name}`}
                  onClick={() => {
                    onOpenChange(false);
                    onOpenResource(resource);
                  }}
                >
                  <span className="min-w-0">
                    <strong className="block truncate text-sm">{name}</strong>
                    <span className="block truncate text-xs font-normal text-muted-foreground">
                      {resource.native_id}
                    </span>
                  </span>
                </Button>
              </li>
            );
          })}
        </ul>
      </DialogContent>
    </Dialog>
  );
}
