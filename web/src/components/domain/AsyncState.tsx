import { AlertCircle, Inbox } from "lucide-react";
import type { ReactNode } from "react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";

export function AsyncState({
  pending,
  error,
  empty,
  onRetry,
  labels,
  formatError = String,
  emptyAction,
  children,
}: {
  pending: boolean;
  error: unknown;
  empty: boolean;
  onRetry?: () => void;
  labels: { empty: string; retry: string; failed?: string; loading?: string };
  formatError?: (error: unknown) => string;
  emptyAction?: ReactNode;
  children: ReactNode;
}) {
  if (pending) {
    return (
      <div
        className="min-h-40 space-y-3 p-5"
        aria-busy="true"
        aria-label={labels.loading}
      >
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-4/5" />
      </div>
    );
  }
  if (error) {
    return (
      <Alert variant="destructive" className="m-5 w-auto">
        <AlertCircle />
        {labels.failed && <AlertTitle>{labels.failed}</AlertTitle>}
        <AlertDescription>
          <p>{formatError(error)}</p>
          {onRetry && (
            <Button variant="outline" size="sm" onClick={onRetry}>
              {labels.retry}
            </Button>
          )}
        </AlertDescription>
      </Alert>
    );
  }
  if (empty) {
    return (
      <Empty className="min-h-40">
        <EmptyMedia variant="icon">
          <Inbox />
        </EmptyMedia>
        <EmptyContent>
          <EmptyTitle>{labels.empty}</EmptyTitle>
          {emptyAction}
        </EmptyContent>
      </Empty>
    );
  }
  return <>{children}</>;
}
