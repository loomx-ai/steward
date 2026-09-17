import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getConnectionOIDCTrust } from "@/api/client";
import { useLocale } from "@/i18n/LocaleProvider";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function OIDCTrust({ connectionID }: { connectionID: string }) {
  const [open, setOpen] = useState(false);
  const { t, formatError } = useLocale();
  const trust = useQuery({
    queryKey: ["connections", connectionID, "oidc"],
    queryFn: () => getConnectionOIDCTrust(connectionID),
    enabled: open,
  });
  return (
    <details
      className="px-4 pb-3 text-sm"
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary className="cursor-pointer text-muted-foreground">
        {t("connections.oidcTrust")}
      </summary>
      <div className="mt-3 space-y-3">
        <p className="text-muted-foreground">
          {t("connections.oidcTrustHelp")}
        </p>
        {trust.isLoading && <p role="status">{t("common.loading")}</p>}
        {trust.error && <p role="alert">{formatError(trust.error)}</p>}
        {trust.data &&
          (
            [
              ["issuer", "connections.oidcIssuer"],
              ["jwks_uri", "connections.oidcJWKS"],
              ["audience", "credentials.oidcAudience"],
              ["read_subject", "connections.oidcReadSubject"],
              ["write_subject", "connections.oidcWriteSubject"],
            ] as const
          ).map(([key, label]) => (
            <div className="space-y-1" key={key}>
              <Label htmlFor={`${connectionID}-${key}`}>{t(label)}</Label>
              <Input
                id={`${connectionID}-${key}`}
                readOnly
                value={trust.data[key]}
                className="font-mono text-xs"
                onFocus={(event) => event.currentTarget.select()}
              />
            </div>
          ))}
      </div>
    </details>
  );
}
