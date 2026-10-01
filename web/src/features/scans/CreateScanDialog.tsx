import { type FormEvent, type RefObject } from "react";
import { useMutation } from "@tanstack/react-query";
import { createScan } from "@/api/client";
import type {
  CloudConnection,
  ConnectionRegion,
  CreateScanInput,
  ProviderBundle,
} from "@/api/types";
import { TaskCreationDialog } from "@/components/patterns/TaskCreationDialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";
import { useScanScope } from "./ScanScopeFields";

export { canonicalNetworkTargets } from "./ScanScopeFields";

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
  const { formatError, t } = useLocale();
  const { scope, valid, fields } = useScanScope({
    connection,
    regions,
    bundles,
    enabled: open,
  });
  const mutation = useMutation({
    mutationFn: (input: CreateScanInput) => createScan(connection.id, input),
    onSuccess: (task) => onCreated(task.id),
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid) return;
    mutation.mutate(scope);
  };
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
          <Button type="submit" disabled={!valid || mutation.isPending}>
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
      {fields}
    </TaskCreationDialog>
  );
}
