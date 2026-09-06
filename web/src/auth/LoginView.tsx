import { useEffect, useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { ShieldCheck } from "lucide-react";
import { useAuth } from "./AuthProvider";
import { useLocale } from "../i18n/LocaleProvider";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { StewardBrand } from "@/components/StewardBrand";

interface LoginLabels {
  token: string;
  placeholder: string;
  verifying: string;
  signIn: string;
  failed: string;
}

export function LoginView() {
  const auth = useAuth();
  const { formatError, t } = useLocale();
  const navigate = useNavigate();
  const location = useLocation();

  useEffect(() => {
    if (auth.mode === "cloud" && !auth.authenticated)
      window.location.assign("/auth/login");
  }, [auth.mode, auth.authenticated]);

  if (auth.authenticated) return <Navigate to="/panorama" replace />;
  if (auth.mode === "cloud")
    return (
      <main className="grid min-h-svh place-items-center">
        <a href="/auth/login">{t("auth.signIn")}</a>
      </main>
    );

  return (
    <LoginForm
      labels={{
        token: t("auth.token"),
        placeholder: t("auth.tokenPlaceholder"),
        verifying: t("auth.verifying"),
        signIn: t("auth.signIn"),
        failed: t("auth.failed"),
      }}
      login={auth.login}
      formatError={formatError}
      onSuccess={() => {
        const from = (location.state as { from?: string } | null)?.from;
        navigate(from || "/panorama", { replace: true });
      }}
    />
  );
}

export function LoginForm({
  labels,
  login,
  formatError,
  onSuccess,
}: {
  labels: LoginLabels;
  login: (token: string) => Promise<void>;
  formatError: (error: unknown) => string;
  onSuccess: () => void;
}) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const candidate = token.trim();
    if (!candidate) return;
    setBusy(true);
    setError("");
    try {
      await login(candidate);
      onSuccess();
    } catch (reason) {
      const message = formatError(reason) || labels.failed;
      setError(message.replaceAll(candidate, "••••"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="grid min-h-svh place-items-center bg-background px-5 py-10">
      <div className="w-full max-w-sm">
        <StewardBrand className="mb-8" />
        <div className="space-y-6">
          <header>
            <div className="mb-4 flex size-8 items-center justify-center rounded-lg bg-muted text-muted-foreground">
              <ShieldCheck className="size-4" />
            </div>
            <h1 className="text-xl font-semibold tracking-tight">
              {labels.signIn}
            </h1>
          </header>
          <form className="space-y-4" onSubmit={submit}>
            <div className="space-y-2">
              <Label htmlFor="access-token">{labels.token}</Label>
              <Input
                id="access-token"
                type="password"
                autoComplete="current-password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                placeholder={labels.placeholder}
                disabled={busy}
              />
            </div>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <Button
              className="w-full"
              type="submit"
              disabled={busy || !token.trim()}
            >
              {busy ? labels.verifying : labels.signIn}
            </Button>
          </form>
        </div>
      </div>
    </main>
  );
}
