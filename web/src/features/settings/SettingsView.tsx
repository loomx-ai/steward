import { useEffect, useRef, useState } from "react";
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  KeyRound,
  MapPinned,
  MoreHorizontal,
  Plus,
  ShieldCheck,
} from "lucide-react";
import {
  createConnection,
  deleteConnection,
  listConnections,
  listProviders,
  renameConnection,
  replaceConnectionCredential,
  validateConnection,
} from "@/api/client";
import type { CloudConnection, CreateConnectionInput } from "@/api/types";
import { useTheme, type Theme } from "@/app/ThemeProvider";
import { useAuth } from "@/auth/AuthProvider";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { SettingsGroup, SettingsRow } from "@/components/patterns/SettingsList";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import type { LocalePreference } from "@/i18n/locales";
import type { MessageKey } from "@/i18n/messages";
import {
  ConnectionEditor,
  providerName,
  type EditorState,
} from "./ConnectionEditor";
import { ConnectionValidationError } from "./ConnectionValidationError";
import { ConnectionRegions } from "./ConnectionRegions";
import { SettingsNavigation, type SettingsSection } from "./SettingsNavigation";

export function SettingsView({
  section: controlledSection,
  onSectionChange,
}: {
  section?: SettingsSection;
  onSectionChange?: (section: SettingsSection) => void;
} = {}) {
  const queryClient = useQueryClient();
  const auth = useAuth();
  const { formatError, label, locale, preference, setPreference, t } =
    useLocale();
  const { theme, setTheme } = useTheme();
  const [provider, setProvider] = useState("alicloud");
  const [localSection, setLocalSection] = useState<SettingsSection>("general");
  const section = controlledSection ?? localSection;
  const setSection = onSectionChange ?? setLocalSection;
  const [editor, setEditor] = useState<EditorState>({ kind: "closed" });
  const [regionConnectionID, setRegionConnectionID] = useState<string>();
  const editorReturnFocusRef = useRef<HTMLElement | null>(null);
  const regionTriggerRefs = useRef(new Map<string, HTMLButtonElement>());
  const pagination = useCursorPagination(provider);
  const connections = useQuery({
    queryKey: [
      "connections",
      "settings",
      provider,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listConnections({
        cursor: pagination.cursor || undefined,
        limit: pagination.pageSize,
        provider,
      }),
    placeholderData: keepPreviousData,
  });
  const providers = useQuery({
    queryKey: ["providers"],
    queryFn: listProviders,
  });
  useEffect(() => {
    if (
      providers.data?.length &&
      !providers.data.some((value) => value.provider === provider)
    ) {
      setProvider(providers.data[0].provider);
    }
  }, [provider, providers.data]);
  const refresh = async () => {
    setEditor({ kind: "closed" });
    await queryClient.invalidateQueries({ queryKey: ["connections"] });
  };
  const create = useMutation({
    mutationFn: createConnection,
    onSuccess: refresh,
  });
  const rename = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      renameConnection(id, name),
    onSuccess: refresh,
  });
  const replace = useMutation({
    mutationFn: ({
      id,
      credential,
    }: {
      id: string;
      credential: CreateConnectionInput["credential"];
    }) => replaceConnectionCredential(id, credential),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: ({ id, confirmation }: { id: string; confirmation: string }) =>
      deleteConnection(id, confirmation),
    onSuccess: refresh,
  });
  const validation = useMutation({
    mutationFn: (id: string) => validateConnection(id),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: ["connections"] }),
  });
  const mutationError =
    create.error ?? rename.error ?? replace.error ?? remove.error;
  const rows = connections.data?.items ?? [];
  useEffect(() => {
    if (
      !connections.isFetching &&
      pagination.page > 1 &&
      connections.data &&
      rows.length === 0
    ) {
      pagination.goPrevious();
    }
  }, [
    connections.data,
    connections.isFetching,
    pagination.goPrevious,
    pagination.page,
    rows.length,
  ]);
  useEffect(() => {
    if (
      regionConnectionID &&
      connections.data &&
      !connections.data.items.some(
        (connection) => connection.id === regionConnectionID,
      )
    ) {
      setRegionConnectionID(undefined);
    }
  }, [connections.data, regionConnectionID]);
  const toggleRegions = (connectionID: string) => {
    setRegionConnectionID((current) =>
      current === connectionID ? undefined : connectionID,
    );
  };
  const collapseRegions = (connectionID: string) => {
    setRegionConnectionID(undefined);
    regionTriggerRefs.current.get(connectionID)?.focus();
  };
  const openEditor = (next: EditorState) => {
    create.reset();
    rename.reset();
    replace.reset();
    remove.reset();
    setEditor(next);
  };
  const languageOptions: Array<{
    value: LocalePreference;
    label: string;
  }> = [
    {
      value: "auto",
      label: t("settings.auto"),
    },
    { value: "zh-CN", label: t("settings.zhCN") },
    { value: "en-US", label: t("settings.enUS") },
  ];
  const themeOptions: Array<{ value: Theme; label: string }> = [
    { value: "system", label: t("theme.system") },
    { value: "light", label: t("theme.light") },
    { value: "dark", label: t("theme.dark") },
  ];

  return (
    <PageLayout mode="list" className="max-w-none">
      <div className="grid gap-4 md:grid-cols-[max-content_minmax(0,1fr)]">
        <SettingsNavigation
          value={section}
          onValueChange={(next) => {
            setSection(next);
            if (next !== "connections") setRegionConnectionID(undefined);
          }}
          labels={{
            general: t("settings.general"),
            connections: t("settings.cloudConnections"),
            account: t("settings.account"),
          }}
        />
        <div className="min-w-0 space-y-8">
          {(connections.error || providers.error) && (
            <Alert variant="destructive">
              <AlertDescription>
                {formatError(connections.error ?? providers.error)}
              </AlertDescription>
            </Alert>
          )}

          {section === "general" && (
            <>
              <SettingsGroup title={t("theme.toggle")}>
                <SettingsRow
                  label={t("theme.toggle")}
                  control={
                    <Select
                      value={theme}
                      onValueChange={(value) => setTheme(value as Theme)}
                    >
                      <SelectTrigger aria-label={t("theme.toggle")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {themeOptions.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  }
                />
              </SettingsGroup>
              <SettingsGroup title={t("settings.language")}>
                <SettingsRow
                  label={t("settings.language")}
                  description={
                    preference === "auto"
                      ? t("settings.autoDetail", {
                          language:
                            locale === "zh-CN"
                              ? t("settings.zhCN")
                              : t("settings.enUS"),
                        })
                      : undefined
                  }
                  control={
                    <Select
                      value={preference}
                      onValueChange={(value) =>
                        setPreference(value as LocalePreference)
                      }
                    >
                      <SelectTrigger aria-label={t("settings.language")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {languageOptions.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  }
                />
              </SettingsGroup>
            </>
          )}

          {section === "connections" && (
            <SettingsGroup
              title={t("settings.cloudConnections")}
              contentClassName="overflow-visible rounded-none border-0 bg-transparent"
              action={
                <Button
                  size="sm"
                  onClick={(event) => {
                    editorReturnFocusRef.current = event.currentTarget;
                    openEditor({ kind: "create" });
                  }}
                >
                  <Plus />
                  {t("connections.add")}
                </Button>
              }
            >
              {providers.isPending || connections.isPending ? (
                <div className="space-y-3">
                  <Skeleton className="h-9 w-72" />
                  <Skeleton className="h-24 w-full" />
                  <Skeleton className="h-24 w-full" />
                </div>
              ) : (
                <Tabs
                  value={provider}
                  onValueChange={(next) => {
                    setProvider(next);
                    setRegionConnectionID(undefined);
                  }}
                >
                  <TabsList className="mb-4 h-auto flex-wrap justify-start">
                    {(providers.data ?? []).map((value) => (
                      <TabsTrigger key={value.provider} value={value.provider}>
                        {providerName(value.provider)}
                      </TabsTrigger>
                    ))}
                  </TabsList>
                  {(providers.data ?? []).map((value) => (
                    <TabsContent key={value.provider} value={value.provider}>
                      {rows.length === 0 ? (
                        <Empty className="min-h-52 border">
                          <EmptyHeader>
                            <EmptyMedia variant="icon">
                              <ShieldCheck />
                            </EmptyMedia>
                            <EmptyTitle>
                              {t("connections.emptyProvider")}
                            </EmptyTitle>
                          </EmptyHeader>
                        </Empty>
                      ) : (
                        <div>
                          <div className="divide-y">
                            {rows.map((connection) => (
                              <article
                                key={connection.id}
                                data-connection-id={connection.id}
                                className="group"
                              >
                                <div className="grid gap-3 px-4 py-3 transition-colors duration-[120ms] hover:bg-muted/45 lg:grid-cols-[minmax(12rem,0.75fr)_minmax(0,2fr)_auto] lg:items-center">
                                  <div className="min-w-0">
                                    <div className="flex items-center gap-2">
                                      <h3 className="truncate text-sm font-semibold">
                                        {connection.name}
                                      </h3>
                                      <StateBadge
                                        value={connection.status}
                                        label={label(connection.status)}
                                      />
                                    </div>
                                    <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                                      {connection.id}
                                    </p>
                                  </div>
                                  <dl className="grid min-w-0 gap-3 text-sm sm:grid-cols-2 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]">
                                    <ConnectionFact
                                      label={t("connections.principal")}
                                      value={connection.principal || "—"}
                                    />
                                    <ConnectionFact
                                      label={t("connections.credentialType")}
                                      value={credentialTypeName(
                                        connection.credential.type,
                                        t,
                                      )}
                                      icon={<KeyRound className="size-3.5" />}
                                    />
                                    {connection.status === "active" && (
                                      <ConnectionRegionFact
                                        connection={connection}
                                        expanded={
                                          regionConnectionID === connection.id
                                        }
                                        panelID={connectionRegionPanelID(
                                          connection.id,
                                        )}
                                        triggerRef={(node) => {
                                          if (node) {
                                            regionTriggerRefs.current.set(
                                              connection.id,
                                              node,
                                            );
                                          } else {
                                            regionTriggerRefs.current.delete(
                                              connection.id,
                                            );
                                          }
                                        }}
                                        onToggle={() =>
                                          toggleRegions(connection.id)
                                        }
                                        t={t}
                                      />
                                    )}
                                  </dl>
                                  <div className="flex items-center gap-1">
                                    <Button
                                      variant="outline"
                                      size="xs"
                                      aria-label={t(
                                        "connections.validateNamed",
                                        { name: connection.name },
                                      )}
                                      disabled={validation.isPending}
                                      onClick={() =>
                                        validation.mutate(connection.id)
                                      }
                                    >
                                      <ShieldCheck />
                                      {validation.isPending &&
                                      validation.variables === connection.id
                                        ? t("connections.validating")
                                        : t("connections.validate")}
                                    </Button>
                                    <DropdownMenu>
                                      <DropdownMenuTrigger asChild>
                                        <Button
                                          variant="ghost"
                                          size="icon"
                                          aria-label={t("common.actions")}
                                          onClick={(event) => {
                                            editorReturnFocusRef.current =
                                              event.currentTarget;
                                          }}
                                        >
                                          <MoreHorizontal />
                                        </Button>
                                      </DropdownMenuTrigger>
                                      <DropdownMenuContent align="end">
                                        {connection.status === "active" && (
                                          <>
                                            <DropdownMenuItem
                                              onSelect={() =>
                                                toggleRegions(connection.id)
                                              }
                                            >
                                              <MapPinned />
                                              {t("regions.manage")}
                                            </DropdownMenuItem>
                                            <DropdownMenuSeparator />
                                          </>
                                        )}
                                        <DropdownMenuItem
                                          onSelect={() =>
                                            openEditor({
                                              kind: "rename",
                                              connection,
                                            })
                                          }
                                        >
                                          {t("connections.rename")}
                                        </DropdownMenuItem>
                                        <DropdownMenuItem
                                          onSelect={() =>
                                            openEditor({
                                              kind: "credential",
                                              connection,
                                            })
                                          }
                                        >
                                          {t("connections.replaceCredential")}
                                        </DropdownMenuItem>
                                        <DropdownMenuSeparator />
                                        <DropdownMenuItem
                                          variant="destructive"
                                          onSelect={() =>
                                            openEditor({
                                              kind: "delete",
                                              connection,
                                            })
                                          }
                                        >
                                          {t("connections.delete")}
                                        </DropdownMenuItem>
                                      </DropdownMenuContent>
                                    </DropdownMenu>
                                  </div>
                                </div>
                                {validation.error &&
                                  validation.variables === connection.id && (
                                    <div className="px-4 pb-3">
                                      <Alert
                                        variant="destructive"
                                        className="rounded-none border-0 bg-transparent px-0 py-0"
                                      >
                                        <ConnectionValidationError
                                          error={validation.error}
                                        />
                                      </Alert>
                                    </div>
                                  )}
                                {connection.status === "active" &&
                                  regionConnectionID === connection.id && (
                                    <ConnectionRegions
                                      connection={connection}
                                      panelID={connectionRegionPanelID(
                                        connection.id,
                                      )}
                                      onCollapse={() =>
                                        collapseRegions(connection.id)
                                      }
                                    />
                                  )}
                              </article>
                            ))}
                          </div>
                          <div className="border-t px-3 py-2.5 empty:hidden">
                            {rows.length > 0 && (
                              <CursorPagination
                                page={pagination.page}
                                pageCount={pagination.pageCount}
                                hasNextPage={Boolean(
                                  connections.data?.next_cursor,
                                )}
                                pending={connections.isFetching}
                                pageSize={pagination.pageSize}
                                onPrevious={pagination.goPrevious}
                                onNext={() =>
                                  pagination.goNext(
                                    connections.data?.next_cursor ?? "",
                                  )
                                }
                                onPageSelect={pagination.goToPage}
                                onPageSizeChange={pagination.setPageSize}
                                labels={{
                                  page: (page) => t("common.page", { page }),
                                  pageSize: t("common.pageSize"),
                                  previous: t("common.previous"),
                                  next: t("common.next"),
                                }}
                              />
                            )}
                          </div>
                        </div>
                      )}
                    </TabsContent>
                  ))}
                </Tabs>
              )}
            </SettingsGroup>
          )}

          {section === "account" && (
            <SettingsGroup title={t("settings.account")}>
              <SettingsRow
                label={t("common.actor")}
                control={
                  <span className="text-sm">
                    {auth.principal?.subject ?? t("auth.identityPending")}
                  </span>
                }
              />
              <SettingsRow
                label={t("common.capabilities")}
                control={
                  <span className="text-sm">
                    {auth.principal?.roles.join(", ") || "—"}
                  </span>
                }
              />
            </SettingsGroup>
          )}
        </div>
      </div>
      <ConnectionEditor
        editor={editor}
        providers={providers.data ?? []}
        busy={
          create.isPending ||
          rename.isPending ||
          replace.isPending ||
          remove.isPending
        }
        error={mutationError}
        returnFocusRef={editorReturnFocusRef}
        onClose={() => setEditor({ kind: "closed" })}
        onCreate={(input) => create.mutateAsync(input)}
        onRename={(id, name) => rename.mutate({ id, name })}
        onReplace={(id, credential) => replace.mutateAsync({ id, credential })}
        onDelete={(id, confirmation) => remove.mutate({ id, confirmation })}
      />
    </PageLayout>
  );
}

function ConnectionRegionFact({
  connection,
  expanded,
  panelID,
  triggerRef,
  onToggle,
  t,
}: {
  connection: CloudConnection;
  expanded: boolean;
  panelID: string;
  triggerRef: (node: HTMLButtonElement | null) => void;
  onToggle: () => void;
  t: (key: MessageKey, values?: Record<string, string | number>) => string;
}) {
  const value = t("regions.countSummary", {
    active: connection.active_region_count ?? 0,
    inactive:
      (connection.retired_region_count ?? 0) +
      (connection.excluded_region_count ?? 0),
  });
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{t("regions.title")}</dt>
      <dd className="mt-1 min-w-0">
        <button
          ref={triggerRef}
          type="button"
          aria-label={`${t("regions.manage")} · ${connection.name}`}
          aria-expanded={expanded}
          aria-controls={panelID}
          className="flex min-w-0 max-w-full items-center gap-1.5 rounded-sm text-left outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/60 [&>svg]:shrink-0"
          onClick={onToggle}
        >
          <MapPinned className="size-3.5" />
          <span className="min-w-0 truncate" title={value}>
            {value}
          </span>
        </button>
      </dd>
    </div>
  );
}

function connectionRegionPanelID(connectionID: string) {
  return `connection-regions-${connectionID}`;
}

function ConnectionFact({
  label,
  value,
  icon,
}: {
  label: string;
  value: string;
  icon?: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 flex min-w-0 items-center gap-1.5 [&>svg]:shrink-0">
        {icon}
        <span className="min-w-0 truncate" title={value}>
          {value}
        </span>
      </dd>
    </div>
  );
}

function credentialTypeName(
  type: string,
  t: (key: MessageKey, values?: Record<string, string | number>) => string,
) {
  const key =
    {
      access_key: "credentials.accessKey",
      oauth: "credentials.alicloudOAuth",
      sts: "credentials.sts",
      session: "credentials.session",
      service_account: "credentials.gcpServiceAccount",
      service_principal: "credentials.azureServicePrincipal",
    }[type] ?? "credentials.unknown";
  return t(key as MessageKey);
}
