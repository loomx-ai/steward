import { useCallback, useEffect, useRef, useState } from "react";
import { getAliCloudOAuthFlow, startAliCloudOAuthFlow } from "@/api/client";
import type { OAuthFlow, OAuthFlowStatus } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";

const oauthPollInterval = 500;

export function OAuthCredentialAuthorization({
  site,
  disabled,
  onAuthorized,
}: {
  site: string;
  disabled: boolean;
  onAuthorized: (flowID: string) => void | Promise<unknown>;
}) {
  const { t } = useLocale();
  const [flow, setFlow] = useState<OAuthFlow>();
  const [status, setStatus] = useState<OAuthFlowStatus | "idle" | "starting">(
    "idle",
  );
  const [popupBlocked, setPopupBlocked] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submissionFailed, setSubmissionFailed] = useState(false);
  const generation = useRef(0);
  const authorizedFlowID = useRef("");

  useEffect(() => {
    generation.current += 1;
    authorizedFlowID.current = "";
    setFlow(undefined);
    setStatus("idle");
    setPopupBlocked(false);
    setSubmitting(false);
    setSubmissionFailed(false);
    return () => {
      generation.current += 1;
    };
  }, [site]);

  const submitAuthorized = useCallback(
    async (flowID: string) => {
      const currentGeneration = generation.current;
      setSubmitting(true);
      setSubmissionFailed(false);
      try {
        await onAuthorized(flowID);
      } catch {
        if (generation.current !== currentGeneration) return;
        try {
          const latest = await getAliCloudOAuthFlow(flowID);
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
    [onAuthorized],
  );

  useEffect(() => {
    if (status !== "pending" || !flow) return;
    const currentGeneration = generation.current;
    const timer = window.setTimeout(async () => {
      try {
        const next = await getAliCloudOAuthFlow(flow.id);
        if (generation.current !== currentGeneration) return;
        setFlow(next);
        setStatus(next.status);
      } catch {
        if (generation.current === currentGeneration) setStatus("failed");
      }
    }, oauthPollInterval);
    return () => window.clearTimeout(timer);
  }, [flow, status]);

  useEffect(() => {
    if (
      status !== "authorized" ||
      !flow ||
      authorizedFlowID.current === flow.id
    ) {
      return;
    }
    authorizedFlowID.current = flow.id;
    void submitAuthorized(flow.id);
  }, [flow, status, submitAuthorized]);

  const start = async () => {
    const currentGeneration = generation.current + 1;
    generation.current = currentGeneration;
    authorizedFlowID.current = "";
    setFlow(undefined);
    setStatus("starting");
    setPopupBlocked(false);
    setSubmitting(false);
    setSubmissionFailed(false);
    try {
      const next = await startAliCloudOAuthFlow(site);
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
  const canRetrySubmission =
    status === "authorized" && submissionFailed && !!flow;
  return (
    <div className="space-y-2">
      <Button
        type="button"
        disabled={
          disabled ||
          pending ||
          submitting ||
          (status === "authorized" && !canRetrySubmission)
        }
        onClick={() => {
          if (canRetrySubmission && flow) {
            void submitAuthorized(flow.id);
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
      {status === "authorized" && (
        <p className="text-sm text-muted-foreground">
          {t("connections.oauthAuthorized")}
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
