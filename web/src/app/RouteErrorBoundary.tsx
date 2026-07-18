import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RotateCcw } from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";

export class RouteErrorBoundary extends Component<
  { children: ReactNode },
  { error: unknown }
> {
  state: { error: unknown } = { error: null };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(_error: unknown, _info: ErrorInfo) {
    // The route surface intentionally contains the failure. Server request
    // errors remain rendered by their page-level AsyncState components.
  }

  render() {
    if (this.state.error) {
      return (
        <RouteErrorSurface
          error={this.state.error}
          onRetry={() => this.setState({ error: null })}
        />
      );
    }
    return this.props.children;
  }
}

export function RouteErrorSurface({
  error,
  onRetry,
  onReload = () => window.location.reload(),
}: {
  error: unknown;
  onRetry: () => void;
  onReload?: () => void;
}) {
  const { formatError, t } = useLocale();
  const routeModuleLoadFailed = isRouteModuleLoadError(error);
  return (
    <div className="mx-auto grid min-h-[60vh] max-w-md place-items-center px-6 py-12 text-center">
      <div className="space-y-4">
        <div className="mx-auto flex size-9 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
          <AlertTriangle className="size-4" />
        </div>
        <h2 className="text-xl font-semibold tracking-tight">
          {t("shell.routeFailure")}
        </h2>
        <p className="text-sm leading-relaxed text-muted-foreground">
          {routeModuleLoadFailed
            ? t("shell.routeLoadFailure")
            : formatError(error)}
        </p>
        <div className="flex justify-center gap-2">
          <Button onClick={routeModuleLoadFailed ? onReload : onRetry}>
            <RotateCcw />
            {t("shell.retry")}
          </Button>
          <Button asChild variant="link">
            <Link to="/panorama">{t("nav.panorama")}</Link>
          </Button>
        </div>
      </div>
    </div>
  );
}

function isRouteModuleLoadError(error: unknown) {
  const message =
    error instanceof Error
      ? error.message
      : typeof error === "string"
        ? error
        : "";
  return (
    [
      "Failed to fetch dynamically imported module",
      "Importing a module script failed",
      "error loading dynamically imported module",
    ].some((marker) => message.includes(marker)) ||
    /Loading chunk .+ failed/i.test(message)
  );
}
