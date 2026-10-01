import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing, MoreHorizontal, Plus } from "lucide-react";
import { toast } from "sonner";
import {
  createNotificationChannel,
  deleteNotificationChannel,
  listNotificationChannels,
  testNotificationChannel,
  updateNotificationChannel,
} from "@/api/client";
import type {
  NotificationChannel,
  NotificationChannelInput,
  NotificationChannelType,
  NotificationEvent,
} from "@/api/types";
import { useHasRole } from "@/auth/roles";
import { SettingsGroup } from "@/components/patterns/SettingsList";
import { TaskCreationDialog } from "@/components/patterns/TaskCreationDialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useLocale } from "@/i18n/LocaleProvider";

const channelTypes: NotificationChannelType[] = [
  "feishu",
  "dingtalk",
  "wecom",
  "slack",
  "webhook",
];

const events: NotificationEvent[] = [
  "scan_failed",
  "scan_partial",
  "schedule_paused",
];

const signedTypes = new Set<NotificationChannelType>(["feishu", "dingtalk"]);

type Editing =
  { kind: "create" } | { kind: "edit"; channel: NotificationChannel };

export function NotificationSettings() {
  const queryClient = useQueryClient();
  const { formatError, formatRelative, t } = useLocale();
  const isAdmin = useHasRole("admin");
  const [editing, setEditing] = useState<Editing>();
  const [deleting, setDeleting] = useState<NotificationChannel>();
  const channels = useQuery({
    queryKey: ["notification-channels"],
    queryFn: listNotificationChannels,
  });
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["notification-channels"] });
  const toggle = useMutation({
    mutationFn: (channel: NotificationChannel) =>
      updateNotificationChannel(channel.id, {
        name: channel.name,
        type: channel.type,
        events: channel.events,
        language: channel.language,
        enabled: !channel.enabled,
      }),
    onSuccess: refresh,
    onError: (error) => toast.error(formatError(error)),
  });
  const test = useMutation({
    mutationFn: (channel: NotificationChannel) =>
      testNotificationChannel(channel.id),
    onSuccess: async (channel) => {
      await refresh();
      if (channel.last_delivery?.ok) toast.success(t("notifications.testSent"));
      else
        toast.error(
          t("notifications.testFailed", {
            error: channel.last_delivery?.error ?? "",
          }),
        );
    },
    onError: (error) => toast.error(formatError(error)),
  });
  const remove = useMutation({
    mutationFn: (channel: NotificationChannel) =>
      deleteNotificationChannel(channel.id),
    onSuccess: async () => {
      setDeleting(undefined);
      await refresh();
      toast.success(t("notifications.deleted"));
    },
    onError: (error) => toast.error(formatError(error)),
  });
  const rows = channels.data ?? [];
  return (
    <>
      <SettingsGroup
        title={t("notifications.title")}
        description={t("notifications.description")}
        action={
          isAdmin ? (
            <Button size="sm" onClick={() => setEditing({ kind: "create" })}>
              <Plus />
              {t("notifications.add")}
            </Button>
          ) : undefined
        }
      >
        {channels.error && (
          <Alert variant="destructive" className="rounded-none border-0">
            <AlertDescription>{formatError(channels.error)}</AlertDescription>
          </Alert>
        )}
        {!channels.isPending && rows.length === 0 && (
          <Empty className="py-10">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <BellRing />
              </EmptyMedia>
              <EmptyTitle className="text-sm font-normal text-muted-foreground">
                {isAdmin
                  ? t("notifications.empty")
                  : t("notifications.adminOnly")}
              </EmptyTitle>
            </EmptyHeader>
          </Empty>
        )}
        {rows.map((channel) => (
          <div
            key={channel.id}
            className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center"
          >
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">
                {channel.name}{" "}
                <span className="font-normal text-muted-foreground">
                  · {t(`notifications.type.${channel.type}`)}
                </span>
              </p>
              <p className="truncate font-mono text-xs text-muted-foreground">
                {channel.target}
              </p>
              <p className="text-xs text-muted-foreground">
                {channel.events
                  .map((event) => t(`notifications.event.${event}`))
                  .join("、")}
              </p>
              <p
                className={
                  channel.last_delivery && !channel.last_delivery.ok
                    ? "text-xs break-words text-destructive"
                    : "text-xs text-muted-foreground"
                }
              >
                {!channel.last_delivery
                  ? t("notifications.neverDelivered")
                  : channel.last_delivery.ok
                    ? t("notifications.delivered", {
                        time: formatRelative(channel.last_delivery.at),
                      })
                    : t("notifications.failed", {
                        time: formatRelative(channel.last_delivery.at),
                        error: channel.last_delivery.error ?? "",
                      })}
              </p>
            </div>
            <div className="flex items-center gap-2 self-end sm:self-center">
              <Switch
                checked={channel.enabled}
                disabled={!isAdmin || toggle.isPending}
                aria-label={`${t("notifications.enabled")}: ${channel.name}`}
                onCheckedChange={() => toggle.mutate(channel)}
              />
              {isAdmin && (
                <>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={test.isPending}
                    onClick={() => test.mutate(channel)}
                  >
                    {t("notifications.test")}
                  </Button>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={`${t("schedules.more")}: ${channel.name}`}
                      >
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem
                        onSelect={() => setEditing({ kind: "edit", channel })}
                      >
                        {t("schedules.editAction")}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        variant="destructive"
                        onSelect={() => setDeleting(channel)}
                      >
                        {t("schedules.delete")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </>
              )}
            </div>
          </div>
        ))}
      </SettingsGroup>
      {editing && (
        <ChannelDialog
          key={editing.kind === "edit" ? editing.channel.id : "create"}
          editing={editing}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await refresh();
            toast.success(t("notifications.saved"));
          }}
        />
      )}
      <AlertDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(undefined)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {deleting
                ? t("notifications.deleteTitle", { name: deleting.name })
                : ""}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("notifications.deleteDescription")}
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
}

function ChannelDialog({
  editing,
  onClose,
  onSaved,
}: {
  editing: Editing;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { formatError, locale, t } = useLocale();
  const current = editing.kind === "edit" ? editing.channel : undefined;
  const [name, setName] = useState(current?.name ?? "");
  const [type, setType] = useState<NotificationChannelType>(
    current?.type ?? "feishu",
  );
  const [url, setURL] = useState("");
  const [secret, setSecret] = useState("");
  const [clearSecret, setClearSecret] = useState(false);
  const [selected, setSelected] = useState<NotificationEvent[]>(
    current?.events ?? events,
  );
  const [language, setLanguage] = useState<"zh" | "en">(
    current?.language ?? (locale.startsWith("zh") ? "zh" : "en"),
  );
  const save = useMutation({
    mutationFn: (input: NotificationChannelInput) =>
      current
        ? updateNotificationChannel(current.id, input)
        : createNotificationChannel(input),
    onSuccess: onSaved,
  });
  const canSubmit =
    name.trim() !== "" &&
    selected.length > 0 &&
    (current !== undefined || url.trim() !== "");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    save.mutate({
      name: name.trim(),
      type,
      events: selected,
      language,
      url: url.trim() || undefined,
      signing_secret: signedTypes.has(type)
        ? secret.trim() || undefined
        : undefined,
      clear_signing_secret:
        clearSecret || (!signedTypes.has(type) && current?.has_signing_secret)
          ? true
          : undefined,
    });
  };
  return (
    <TaskCreationDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={current ? t("notifications.edit") : t("notifications.add")}
      pending={save.isPending}
      onSubmit={submit}
      bodyClassName="space-y-4"
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={!canSubmit || save.isPending}>
            {save.isPending ? t("schedules.saving") : t("schedules.save")}
          </Button>
        </>
      }
    >
      {save.error && (
        <Alert variant="destructive">
          <AlertDescription>{formatError(save.error)}</AlertDescription>
        </Alert>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="channel-name">{t("notifications.name")}</Label>
          <Input
            id="channel-name"
            value={name}
            maxLength={100}
            onChange={(event) => setName(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label>{t("notifications.type")}</Label>
          <Select
            value={type}
            onValueChange={(value) => setType(value as NotificationChannelType)}
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("notifications.type")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {channelTypes.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`notifications.type.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="channel-url">{t("notifications.url")}</Label>
        <Input
          id="channel-url"
          type="url"
          autoComplete="off"
          className="font-mono"
          value={url}
          placeholder={
            current
              ? t("notifications.urlKeep", { target: current.target })
              : "https://"
          }
          onChange={(event) => setURL(event.target.value)}
        />
      </div>
      {signedTypes.has(type) && (
        <div className="space-y-1.5">
          <Label htmlFor="channel-secret">
            {t("notifications.signingSecret")}
          </Label>
          <Input
            id="channel-secret"
            type="password"
            autoComplete="new-password"
            value={secret}
            disabled={clearSecret}
            aria-describedby="channel-secret-hint"
            onChange={(event) => setSecret(event.target.value)}
          />
          <p id="channel-secret-hint" className="text-xs text-muted-foreground">
            {current?.has_signing_secret
              ? t("notifications.signingSecretKeep")
              : t("notifications.signingSecretHint")}
          </p>
          {current?.has_signing_secret && (
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={clearSecret}
                onCheckedChange={(checked) => setClearSecret(checked === true)}
              />
              {t("notifications.clearSigningSecret")}
            </label>
          )}
        </div>
      )}
      <fieldset className="space-y-2">
        <legend className="mb-2 text-sm font-medium">
          {t("notifications.events")}
        </legend>
        {events.map((event) => (
          <label key={event} className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={selected.includes(event)}
              onCheckedChange={(checked) =>
                setSelected((values) =>
                  checked === true
                    ? [...values, event]
                    : values.filter((value) => value !== event),
                )
              }
            />
            {t(`notifications.event.${event}`)}
          </label>
        ))}
      </fieldset>
      <div className="max-w-48 space-y-1.5">
        <Label>{t("notifications.language")}</Label>
        <Select
          value={language}
          onValueChange={(value) => setLanguage(value as "zh" | "en")}
        >
          <SelectTrigger
            className="w-full"
            aria-label={t("notifications.language")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="zh">{t("notifications.language.zh")}</SelectItem>
            <SelectItem value="en">{t("notifications.language.en")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </TaskCreationDialog>
  );
}
