import { useEffect, useState, type FormEvent, type RefObject } from "react";
import type {
  CloudConnection,
  CreateConnectionInput,
  CredentialSchema,
  ProviderDescriptor,
  ProviderSite,
} from "@/api/types";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import {
  connectionDeletionConfirmed,
  CredentialFields,
  credentialInputFromValues,
} from "./CredentialFields";
import { OAuthCredentialAuthorization } from "./OAuthCredentialAuthorization";

export type EditorState =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "rename"; connection: CloudConnection }
  | { kind: "credential"; connection: CloudConnection }
  | { kind: "delete"; connection: CloudConnection };

export function ConnectionEditor({
  editor,
  providers,
  busy,
  error,
  returnFocusRef,
  onClose,
  onCreate,
  onRename,
  onReplace,
  onDelete,
}: {
  editor: EditorState;
  providers: ProviderDescriptor[];
  busy: boolean;
  error: unknown;
  returnFocusRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  onCreate: (input: CreateConnectionInput) => void | Promise<unknown>;
  onRename: (id: string, name: string) => void;
  onReplace: (
    id: string,
    credential: CreateConnectionInput["credential"],
  ) => void | Promise<unknown>;
  onDelete: (id: string, confirmation: string) => void;
}) {
  const { formatError, t } = useLocale();
  const restoreFocus = () => {
    const target = returnFocusRef.current;
    queueMicrotask(() => {
      if (target?.isConnected) target.focus();
    });
  };
  const sheetOpen = ["create", "rename", "credential"].includes(editor.kind);
  const title =
    editor.kind === "create"
      ? t("connections.add")
      : editor.kind === "rename"
        ? t("connections.rename")
        : t("connections.replaceCredential");
  return (
    <>
      <Sheet open={sheetOpen} onOpenChange={(open) => !open && onClose()}>
        <SheetContent
          className="w-screen overflow-y-auto sm:max-w-lg"
          {...(editor.kind !== "credential"
            ? { "aria-describedby": undefined }
            : {})}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            restoreFocus();
          }}
        >
          <SheetHeader>
            <SheetTitle>{title}</SheetTitle>
            {editor.kind === "credential" && (
              <SheetDescription>
                {t("connections.identityMustMatch")}
              </SheetDescription>
            )}
          </SheetHeader>
          <div className="space-y-4 px-4 pb-4">
            {error !== undefined && error !== null && (
              <Alert variant="destructive">
                <AlertDescription>{formatError(error)}</AlertDescription>
              </Alert>
            )}
            {editor.kind === "create" && (
              <CreateConnectionForm
                providers={providers}
                busy={busy}
                onSubmit={onCreate}
              />
            )}
            {editor.kind === "rename" && (
              <RenameForm
                connection={editor.connection}
                busy={busy}
                onSubmit={onRename}
              />
            )}
            {editor.kind === "credential" && (
              <CredentialForm
                key={`${editor.connection.id}:${editor.connection.credential.type}`}
                connection={editor.connection}
                schemas={
                  providers.find(
                    (value) => value.provider === editor.connection.provider,
                  )?.credential_schemas ?? []
                }
                sites={
                  providers.find(
                    (value) => value.provider === editor.connection.provider,
                  )?.sites ?? []
                }
                busy={busy}
                onSubmit={onReplace}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
      <DeleteConnectionDialog
        editor={editor}
        busy={busy}
        error={error}
        returnFocusRef={returnFocusRef}
        onClose={onClose}
        onDelete={onDelete}
      />
    </>
  );
}

function CreateConnectionForm({
  providers,
  busy,
  onSubmit,
}: {
  providers: ProviderDescriptor[];
  busy: boolean;
  onSubmit: (input: CreateConnectionInput) => void | Promise<unknown>;
}) {
  const { t } = useLocale();
  const [name, setName] = useState("");
  const [provider, setProvider] = useState(providers[0]?.provider ?? "");
  const descriptor = providers.find((value) => value.provider === provider);
  const schemas = descriptor?.credential_schemas ?? [];
  const sites = descriptor?.sites ?? [];
  const [type, setType] = useState(schemas[0]?.type ?? "");
  const [site, setSite] = useState(sites[0]?.value ?? "");
  const [values, setValues] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!provider && providers[0]) setProvider(providers[0].provider);
  }, [provider, providers]);
  useEffect(() => {
    if (!schemas.some((schema) => schema.type === type)) {
      setType(schemas[0]?.type ?? "");
      setValues({});
    }
  }, [schemas, type]);
  useEffect(() => {
    if (!sites.some((candidate) => candidate.value === site)) {
      setSite(sites[0]?.value ?? "");
    }
  }, [site, sites]);
  const schema = schemas.find((value) => value.type === type);
  const browserOAuth = schema?.flow === "browser_oauth";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (
      !schema ||
      browserOAuth ||
      !name.trim() ||
      (sites.length > 0 && !site)
    ) {
      return;
    }
    onSubmit({
      name: name.trim(),
      provider,
      ...(sites.length > 0 ? { site } : {}),
      credential: credentialInputFromValues(schema, values),
    });
  };
  return (
    <form className="space-y-4" onSubmit={submit}>
      <div className="space-y-2">
        <Label htmlFor="connection-name">{t("common.name")}</Label>
        <Input
          id="connection-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          required
        />
      </div>
      <div className="space-y-2">
        <Label>{t("common.provider")}</Label>
        <Select value={provider} onValueChange={setProvider}>
          <SelectTrigger className="w-full" aria-label={t("common.provider")}>
            <SelectValue placeholder={t("connections.selectProvider")} />
          </SelectTrigger>
          <SelectContent>
            {providers.map((value) => (
              <SelectItem key={value.provider} value={value.provider}>
                {providerName(value.provider)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {sites.length > 0 && (
        <div className="space-y-2">
          <Label>{t("connections.site")}</Label>
          <Select value={site} onValueChange={setSite}>
            <SelectTrigger
              className="w-full"
              aria-label={t("connections.site")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {sites.map((value) => (
                <SelectItem key={value.value} value={value.value}>
                  {t(value.label_key as MessageKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      <CredentialTypeSelect schemas={schemas} value={type} onChange={setType} />
      {browserOAuth ? (
        <OAuthCredentialAuthorization
          key={`${provider}:${site}:${type}`}
          site={site}
          disabled={busy || !name.trim() || !site}
          onAuthorized={async (flowID) =>
            await onSubmit({
              name: name.trim(),
              provider,
              site,
              credential: {
                type: schema.type,
                values: { flow_id: flowID },
              },
            })
          }
        />
      ) : (
        <CredentialFields
          schema={schema}
          values={values}
          onChange={setValues}
        />
      )}
      {!browserOAuth && (
        <SheetFooter className="px-0">
          <Button
            type="submit"
            disabled={
              busy || !name.trim() || !schema || (sites.length > 0 && !site)
            }
          >
            {busy ? t("common.loading") : t("connections.create")}
          </Button>
        </SheetFooter>
      )}
    </form>
  );
}

function RenameForm({
  connection,
  busy,
  onSubmit,
}: {
  connection: CloudConnection;
  busy: boolean;
  onSubmit: (id: string, name: string) => void;
}) {
  const { t } = useLocale();
  const [name, setName] = useState(connection.name);
  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(connection.id, name.trim());
      }}
    >
      <div className="space-y-2">
        <Label htmlFor="rename-connection">{t("common.name")}</Label>
        <Input
          id="rename-connection"
          value={name}
          onChange={(event) => setName(event.target.value)}
          required
        />
      </div>
      <SheetFooter className="px-0">
        <Button disabled={busy || !name.trim()}>
          {t("connections.rename")}
        </Button>
      </SheetFooter>
    </form>
  );
}

function CredentialForm({
  connection,
  schemas,
  sites,
  busy,
  onSubmit,
}: {
  connection: CloudConnection;
  schemas: CredentialSchema[];
  sites: ProviderSite[];
  busy: boolean;
  onSubmit: (
    id: string,
    credential: CreateConnectionInput["credential"],
  ) => void | Promise<unknown>;
}) {
  const { t } = useLocale();
  const [type, setType] = useState(() =>
    replacementCredentialType(connection, schemas),
  );
  const [values, setValues] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!schemas.some((schema) => schema.type === type)) {
      setType(replacementCredentialType(connection, schemas));
      setValues({});
    }
  }, [connection, schemas, type]);
  const schema = schemas.find((value) => value.type === type);
  const browserOAuth = schema?.flow === "browser_oauth";
  const site = sites.find((value) => value.value === connection.site);
  const siteLabel = site ? t(site.label_key as MessageKey) : connection.site;
  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (schema && !browserOAuth)
          onSubmit(connection.id, credentialInputFromValues(schema, values));
      }}
    >
      <div className="rounded-md border bg-muted/30 px-3 py-2 text-sm">
        {connection.name} · {providerName(connection.provider)}
      </div>
      {siteLabel && (
        <div className="text-sm text-muted-foreground">
          {t("connections.site")}: {siteLabel}
        </div>
      )}
      <CredentialTypeSelect schemas={schemas} value={type} onChange={setType} />
      {browserOAuth ? (
        <OAuthCredentialAuthorization
          key={`${connection.id}:${connection.site}:${type}`}
          site={connection.site ?? ""}
          disabled={busy || !connection.site}
          onAuthorized={async (flowID) =>
            await onSubmit(connection.id, {
              type: schema.type,
              values: { flow_id: flowID },
            })
          }
        />
      ) : (
        <>
          <CredentialFields
            schema={schema}
            values={values}
            onChange={setValues}
          />
          <SheetFooter className="px-0">
            <Button disabled={busy || !schema}>
              {busy ? t("common.loading") : t("connections.replaceCredential")}
            </Button>
          </SheetFooter>
        </>
      )}
    </form>
  );
}

function replacementCredentialType(
  connection: CloudConnection,
  schemas: CredentialSchema[],
) {
  return schemas.some((schema) => schema.type === connection.credential.type)
    ? connection.credential.type
    : (schemas[0]?.type ?? "");
}

function CredentialTypeSelect({
  schemas,
  value,
  onChange,
}: {
  schemas: CredentialSchema[];
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useLocale();
  return (
    <div className="space-y-2">
      <Label>{t("connections.credentialType")}</Label>
      <Select
        value={value}
        onValueChange={(next) => {
          onChange(next);
        }}
      >
        <SelectTrigger
          className="w-full"
          aria-label={t("connections.credentialType")}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {schemas.map((schema) => (
            <SelectItem key={schema.type} value={schema.type}>
              {t(schema.label_key as MessageKey)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function DeleteConnectionDialog({
  editor,
  busy,
  error,
  returnFocusRef,
  onClose,
  onDelete,
}: {
  editor: EditorState;
  busy: boolean;
  error: unknown;
  returnFocusRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  onDelete: (id: string, confirmation: string) => void;
}) {
  const { formatError, t } = useLocale();
  const [confirmation, setConfirmation] = useState("");
  const restoreFocus = () => {
    const target = returnFocusRef.current;
    queueMicrotask(() => {
      if (target?.isConnected) target.focus();
    });
  };
  const connection = editor.kind === "delete" ? editor.connection : undefined;
  useEffect(() => setConfirmation(""), [connection?.id]);
  return (
    <AlertDialog
      open={!!connection}
      onOpenChange={(open) => !open && onClose()}
    >
      <AlertDialogContent
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          restoreFocus();
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{t("connections.delete")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("connections.deleteWarning")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error !== undefined && error !== null && (
          <Alert variant="destructive">
            <AlertDescription>{formatError(error)}</AlertDescription>
          </Alert>
        )}
        {connection && (
          <div className="space-y-2">
            <Label htmlFor="delete-confirmation">
              {t("connections.typeNameToDelete", { name: connection.name })}
            </Label>
            <Input
              id="delete-confirmation"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
            />
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={
              busy ||
              !connection ||
              !connectionDeletionConfirmed(connection.name, confirmation)
            }
            onClick={() => connection && onDelete(connection.id, confirmation)}
          >
            {t("connections.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export function providerName(provider: string) {
  return (
    {
      alicloud: "Alibaba Cloud",
      aws: "AWS",
    }[provider] ?? provider
  );
}
