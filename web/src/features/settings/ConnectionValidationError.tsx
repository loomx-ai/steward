import { useState } from "react";
import { ChevronDown } from "lucide-react";
import { CopyableId } from "@/components/domain/CopyableId";
import { AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import { cn } from "@/lib/utils";

interface ValidationDiagnostic {
  category: string;
  code: string;
  fallback: string;
  providerCode: string;
  providerMessage: string;
  providerRequestID: string;
  requestID: string;
}

export function ConnectionValidationError({ error }: { error: unknown }) {
  const { messageForCode, t } = useLocale();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const diagnostic = validationDiagnostic(error);
  const reason = validationReason(diagnostic, t, messageForCode);
  const hasTechnicalDetails = Boolean(
    diagnostic.providerCode ||
    diagnostic.providerMessage ||
    diagnostic.providerRequestID ||
    diagnostic.requestID,
  );

  return (
    <>
      <AlertTitle>{t("connections.validationFailed")}</AlertTitle>
      <AlertDescription className="w-full gap-2">
        <p>{reason}</p>
        {hasTechnicalDetails && (
          <Collapsible
            open={detailsOpen}
            onOpenChange={setDetailsOpen}
            className="w-full"
          >
            <CollapsibleTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="xs"
                className="-ml-2 text-muted-foreground"
              >
                {t(
                  detailsOpen
                    ? "connections.hideTechnicalDetails"
                    : "connections.showTechnicalDetails",
                )}
                <ChevronDown
                  className={cn(
                    "transition-transform",
                    detailsOpen && "rotate-180",
                  )}
                />
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <dl className="mt-2 grid w-full grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
                {diagnostic.providerCode && (
                  <TechnicalDetail
                    label={t("connections.providerErrorCode")}
                    value={diagnostic.providerCode}
                  />
                )}
                {diagnostic.providerMessage && (
                  <TechnicalDetail
                    label={t("connections.providerErrorMessage")}
                    value={diagnostic.providerMessage}
                  />
                )}
                {diagnostic.providerRequestID && (
                  <TechnicalDetail
                    label={t("common.providerRequestId")}
                    value={diagnostic.providerRequestID}
                    copyable
                  />
                )}
                {diagnostic.requestID && (
                  <TechnicalDetail
                    label={t("connections.applicationRequestId")}
                    value={diagnostic.requestID}
                    copyable
                  />
                )}
              </dl>
            </CollapsibleContent>
          </Collapsible>
        )}
      </AlertDescription>
    </>
  );
}

function TechnicalDetail({
  label,
  value,
  copyable = false,
}: {
  label: string;
  value: string;
  copyable?: boolean;
}) {
  return (
    <>
      <dt className="self-start leading-5 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 self-start font-mono leading-5 text-foreground">
        {copyable ? (
          <CopyableId label={label} value={value}>
            <code>{value}</code>
          </CopyableId>
        ) : (
          <code className="break-words">{value}</code>
        )}
      </dd>
    </>
  );
}

function validationDiagnostic(error: unknown): ValidationDiagnostic {
  if (!error || typeof error !== "object") {
    return emptyDiagnostic(String(error ?? ""));
  }
  const value = error as {
    code?: unknown;
    details?: unknown;
    message?: unknown;
    requestID?: unknown;
    request_id?: unknown;
  };
  const details =
    value.details &&
    typeof value.details === "object" &&
    !Array.isArray(value.details)
      ? (value.details as Record<string, unknown>)
      : {};
  return {
    category: stringValue(details.category),
    code: stringValue(value.code),
    fallback: typeof value.message === "string" ? value.message : String(error),
    providerCode: stringValue(details.provider_code),
    providerMessage: stringValue(details.provider_message),
    providerRequestID: stringValue(details.provider_request_id),
    requestID: stringValue(value.requestID) || stringValue(value.request_id),
  };
}

function emptyDiagnostic(fallback: string): ValidationDiagnostic {
  return {
    category: "",
    code: "",
    fallback,
    providerCode: "",
    providerMessage: "",
    providerRequestID: "",
    requestID: "",
  };
}

function stringValue(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function validationReason(
  diagnostic: ValidationDiagnostic,
  t: (key: MessageKey) => string,
  messageForCode: (
    code: string,
    fallback: string,
    details?: Record<string, unknown>,
  ) => string,
) {
  switch (diagnostic.providerCode.toLowerCase()) {
    case "invalidaccesskeyid.inactive":
      return t("connections.validationReasonCredentialInactive");
    case "invalidaccesskeyid.notfound":
    case "invalidclienttokenid":
      return t("connections.validationReasonCredentialNotFound");
    case "signaturedoesnotmatch":
    case "invalidsignatureexception":
      return t("connections.validationReasonSignatureMismatch");
    case "expiredtoken":
    case "expiredtokenexception":
      return t("connections.validationReasonExpired");
  }
  if (diagnostic.providerCode) {
    if (diagnostic.providerMessage) return diagnostic.providerMessage;
    if (diagnostic.code) {
      return messageForCode(diagnostic.code, diagnostic.fallback);
    }
    return diagnostic.fallback;
  }
  switch (diagnostic.category) {
    case "permission_denied":
      return t("connections.validationReasonPermissionDenied");
    case "throttled":
      return t("connections.validationReasonThrottled");
    case "provider_failure":
    case "retryable":
      return t("connections.validationReasonUnavailable");
  }
  if (diagnostic.providerMessage) return diagnostic.providerMessage;
  if (diagnostic.code) {
    return messageForCode(diagnostic.code, diagnostic.fallback);
  }
  return diagnostic.fallback;
}
