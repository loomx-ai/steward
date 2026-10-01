import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { deleteSchedule, runSchedule, setScheduleEnabled } from "@/api/client";
import type { CloudConnection, ScanSchedule, ScheduleRun } from "@/api/types";
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
import { useLocale } from "@/i18n/LocaleProvider";
import { ScheduleDialog, type ScheduleDialogMode } from "./ScheduleDialog";
import { scheduleName } from "./scheduleFormat";

// useScheduleActions keeps the list and detail page in step: both run, edit,
// duplicate, toggle and delete schedules the same way.
export function useScheduleActions(
  connection: CloudConnection,
  options: { onDeleted?: () => void } = {},
) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { formatError, t } = useLocale();
  const [dialog, setDialog] = useState<ScheduleDialogMode>();
  const [deleting, setDeleting] = useState<ScanSchedule>();
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({
        queryKey: ["schedules", connection.id],
      }),
      queryClient.invalidateQueries({ queryKey: ["schedule-overview"] }),
      queryClient.invalidateQueries({ queryKey: ["scans", connection.id] }),
    ]);
  const announceRun = (run: ScheduleRun) => {
    if (run.outcome === "failed_to_start") {
      toast.error(t("schedules.notStarted", { error: run.error ?? "" }));
      return;
    }
    toast.success(t("schedules.started"));
    if (run.scan_task_id) {
      navigate(`/scans/${encodeURIComponent(run.scan_task_id)}`);
    }
  };
  const run = useMutation({
    mutationFn: (schedule: ScanSchedule) =>
      runSchedule(connection.id, schedule.id),
    onSuccess: async (value) => {
      await refresh();
      announceRun(value);
    },
    onError: (error) => toast.error(formatError(error)),
  });
  const toggle = useMutation({
    mutationFn: ({
      schedule,
      enabled,
    }: {
      schedule: ScanSchedule;
      enabled: boolean;
    }) => setScheduleEnabled(connection.id, schedule.id, enabled),
    onSuccess: refresh,
    onError: (error) => toast.error(formatError(error)),
  });
  const remove = useMutation({
    mutationFn: (schedule: ScanSchedule) =>
      deleteSchedule(connection.id, schedule.id),
    onSuccess: async () => {
      setDeleting(undefined);
      await refresh();
      toast.success(t("schedules.deleted"));
      options.onDeleted?.();
    },
    onError: (error) => toast.error(formatError(error)),
  });
  const dialogs = (
    <>
      <ScheduleDialog
        mode={dialog}
        connection={connection}
        onOpenChange={(open) => !open && setDialog(undefined)}
        onSaved={async (saved, value) => {
          setDialog(undefined);
          await refresh();
          void queryClient.invalidateQueries({
            queryKey: ["schedule", connection.id, saved.id],
          });
          if (value) announceRun(value);
          else toast.success(t("schedules.saved"));
        }}
      />
      <AlertDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(undefined)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {deleting
                ? t("schedules.deleteTitle", {
                    name: scheduleName(deleting, t),
                  })
                : ""}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("schedules.deleteDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => deleting && remove.mutate(deleting)}
            >
              {t("schedules.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
  return {
    dialogs,
    create: () => setDialog({ kind: "create" }),
    edit: (schedule: ScanSchedule) => setDialog({ kind: "edit", schedule }),
    duplicate: (schedule: ScanSchedule) =>
      setDialog({ kind: "duplicate", schedule }),
    confirmDelete: setDeleting,
    run: run.mutate,
    running: run.isPending,
    setEnabled: (schedule: ScanSchedule, enabled: boolean) =>
      toggle.mutate({ schedule, enabled }),
    toggling: toggle.isPending,
  };
}
