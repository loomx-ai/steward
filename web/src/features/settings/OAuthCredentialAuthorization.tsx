import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  getOAuthFlow,
  listOAuthFlowTargets,
  startOAuthFlow,
} from "@/api/client";
import type { OAuthFlow, OAuthFlowStatus, OAuthTarget } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useLocale } from "@/i18n/LocaleProvider";

const oauthPollInterval = 500;

export function OAuthCredentialAuthorization({
  provider,
  params,
  disabled,
  onAuthorized,
}: {
  provider: string;
  params: Record<string, string>;
  disabled: boolean;
  onAuthorized: (flowID: string, targetID: string) => void | Promise<unknown>;
}) {
  const { t } = useLocale();
  const [flow, setFlow] = useState<OAuthFlow>();
  const [status, setStatus] = useState<OAuthFlowStatus | "idle" | "starting">(
    "idle",
  );
  const [targets, setTargets] = useState<OAuthTarget[]>();
  const [targetsFailed, setTargetsFailed] = useState(false);
  const [selectedTarget, setSelectedTarget] = useState("");
  const [popupBlocked, setPopupBlocked] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submissionFailed, setSubmissionFailed] = useState(false);
  const generation = useRef(0);
  const resolvedFlowID = useRef("");

  // Re-authorizing is the only way to change what was authorized, so any change
  // to the provider or to the parameters the flow was started with discards it.
  const identity = useMemo(
    () => `${provider}:${JSON.stringify(params)}`,
    [provider, params],
  );

  useEffect(() => {
    generation.current += 1;
    resolvedFlowID.current = "";
    setFlow(undefined);
    setStatus("idle");
    setTargets(undefined);
    setTargetsFailed(false);
    setSelectedTarget("");
    setPopupBlocked(false);
    setSubmitting(false);
    setSubmissionFailed(false);
    return () => {
      generation.current += 1;
    };
  }, [identity]);

  const submitAuthorized = useCallback(
    async (flowID: string, targetID: string) => {
      const currentGeneration = generation.current;
      setSubmitting(true);
      setSubmissionFailed(false);
      try {
        await onAuthorized(flowID, targetID);
      } catch {
        if (generation.current !== currentGeneration) return;
        try {
          const latest = await getOAuthFlow(provider, flowID);
          if (generation.current !== currentGeneration) return;
          setFlow(latest);
          setStatus(latest.status);
          setSubmissionFailed(latest.status === "authorized");
        } catch {
          if (generation.current === currentGeneration) {
            setStatus("failed");
            setSubmissionFailed(false);
          }
        }
      } finally {
        if (generation.current === currentGeneration) {
          setSubmitting(false);
        }
      }
    },
    [onAuthorized, provider],
  );

  useEffect(() => {
    if (status !== "pending" || !flow) return;
    const currentGeneration = generation.current;
    const timer = window.setTimeout(async () => {
      try {
        const next = await getOAuthFlow(provider, flow.id);
        if (generation.current !== currentGeneration) return;
        setFlow(next);
        setStatus(next.status);
      } catch {
        if (generation.current === currentGeneration) setStatus("failed");
      }
    }, oauthPollInterval);
    return () => window.clearTimeout(timer);
  }, [flow, provider, status]);

  // An authorization is resolved exactly once: either it names a single cloud
  // scope and is submitted straight away, or its scopes are listed for the
  // operator to choose from.
  useEffect(() => {
    if (
      status !== "authorized" ||
      !flow ||
      resolvedFlowID.current === flow.id
    ) {
      return;
    }
    resolvedFlowID.current = flow.id;
    const currentGeneration = generation.current;
    void (async () => {
      let available: OAuthTarget[];
      try {
        available = await listOAuthFlowTargets(provider, flow.id);
      } catch {
        if (generation.current === currentGeneration) setTargetsFailed(true);
        return;
      }
      if (generation.current !== currentGeneration) return;
      setTargets(available);
      if (available.length === 0) {
        void submitAuthorized(flow.id, "");
      }
    })();
  }, [flow, provider, status, submitAuthorized]);

  const start = async () => {
    const currentGeneration = generation.current + 1;
    generation.current = currentGeneration;
    resolvedFlowID.current = "";
    setFlow(undefined);
    setStatus("starting");
    setTargets(undefined);
    setTargetsFailed(false);
    setSelectedTarget("");
    setPopupBlocked(false);
    setSubmitting(false);
    setSubmissionFailed(false);
    try {
      const next = await startOAuthFlow(provider, params);
      if (generation.current !== currentGeneration) return;
      setFlow(next);
      setStatus(next.status);
      if (next.authorization_url) {
        const popup = window.open(
          next.authorization_url,
          "_blank",
          "noopener,noreferrer",
        );
        setPopupBlocked(popup === null);
      }
    } catch {
      if (generation.current === currentGeneration) setStatus("failed");
    }
  };

  const pending = status === "starting" || status === "pending";
  const choosing =
    status === "authorized" && !!targets && targets.length > 0 && !submitting;
  const canRetrySubmission =
    status === "authorized" && submissionFailed && !!flow && !choosing;
  return (
    <div className="space-y-2">
      <Button
        type="button"
        disabled={
          disabled ||
          pending ||
          submitting ||
          (status === "authorized" && !canRetrySubmission && !choosing)
        }
        onClick={() => {
          if (canRetrySubmission && flow) {
            void submitAuthorized(flow.id, selectedTarget);
            return;
          }
          void start();
        }}
      >
        {canRetrySubmission
          ? t("connections.oauthRetrySave")
          : t("connections.oauthLogin")}
      </Button>
      {pending && (
        <p className="text-sm text-muted-foreground">
          {t("connections.oauthPending")}
        </p>
      )}
      {choosing && flow && (
        <div className="space-y-2">
          <Label htmlFor="oauth-target">
            {t("connections.oauthTargetLabel")}
          </Label>
          <Select value={selectedTarget} onValueChange={setSelectedTarget}>
            <SelectTrigger id="oauth-target">
              <SelectValue
                placeholder={t("connections.oauthTargetPlaceholder")}
              />
            </SelectTrigger>
            <SelectContent>
              {targets?.map((target) => (
                <SelectItem key={target.id} value={target.id}>
                  {target.description
                    ? `${target.name} · ${target.description}`
                    : target.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            disabled={disabled || !selectedTarget}
            onClick={() => void submitAuthorized(flow.id, selectedTarget)}
          >
            {t("connections.oauthTargetConfirm")}
          </Button>
          {submissionFailed && (
            <p className="text-sm text-destructive">
              {t("connections.oauthFailed")}
            </p>
          )}
        </div>
      )}
      {status === "authorized" && !choosing && !targetsFailed && (
        <p className="text-sm text-muted-foreground">
          {t("connections.oauthAuthorized")}
        </p>
      )}
      {targetsFailed && (
        <p className="text-sm text-destructive">
          {t("connections.oauthTargetsFailed")}
        </p>
      )}
      {(status === "failed" || status === "consumed") && (
        <p className="text-sm text-destructive">
          {t("connections.oauthFailed")}
        </p>
      )}
      {status === "expired" && (
        <p className="text-sm text-destructive">
          {t("connections.oauthExpired")}
        </p>
      )}
      {popupBlocked && flow?.authorization_url && status === "pending" && (
        <a
          className="text-sm text-primary underline-offset-4 hover:underline"
          href={flow.authorization_url}
          target="_blank"
          rel="noopener noreferrer"
        >
          {t("connections.oauthOpenAuthorization")}
        </a>
      )}
    </div>
  );
}
