import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import {
  getSession,
  setAccessTokenProvider,
  setPrincipalObserver,
  setUnauthorizedObserver,
  type Session,
} from "../api/client";
import type { Principal } from "../api/types";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";

const tokenKey = "steward.access-token";
const developmentProxy =
  import.meta.env.DEV && import.meta.env.VITE_STEWARD_DEV_AUTO_LOGIN === "1";

interface AuthContextValue {
  authenticated: boolean;
  mode: Session["mode"];
  automaticDevelopmentSession: boolean;
  principal: Principal | null;
  displayName: string | null;
  login: (token: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const token = useRef(sessionStorage.getItem(tokenKey) ?? "");
  const [session, setSession] = useState<Session | null>(null);
  const [principal, setPrincipal] = useState<Principal | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const queryClient = useQueryClient();
  const { t } = useLocale();

  useEffect(() => {
    let active = true;
    setAccessTokenProvider(() =>
      developmentProxy ? undefined : token.current || undefined,
    );
    setPrincipalObserver(setPrincipal);
    setUnauthorizedObserver(() => {
      token.current = "";
      sessionStorage.removeItem(tokenKey);
      queryClient.clear();
      setPrincipal(null);
      setSession(
        (current) =>
          current && { ...current, authenticated: false, principal: null },
      );
    });
    getSession()
      .then((next) => {
        if (!active) return;
        setSession(next);
        setPrincipal(next.principal);
      })
      .catch(() => {
        if (active) setFailed(true);
      });
    return () => {
      active = false;
      setPrincipalObserver(() => undefined);
      setUnauthorizedObserver(() => undefined);
    };
  }, [attempt, queryClient]);

  const login = useCallback(
    async (candidate: string) => {
      const normalized = candidate.trim();
      if (!normalized) throw new Error("Bearer token is required");
      token.current = normalized;
      setAccessTokenProvider(() => token.current || undefined);
      try {
        const next = await getSession();
        if (!next.authenticated) throw new Error("Authentication failed");
        queryClient.clear();
        sessionStorage.setItem(tokenKey, normalized);
        setSession(next);
        setPrincipal(next.principal);
      } catch (error) {
        token.current = "";
        sessionStorage.removeItem(tokenKey);
        setPrincipal(null);
        throw error;
      }
    },
    [queryClient],
  );

  const logout = useCallback(() => {
    if (session?.mode === "local" || developmentProxy) return;
    token.current = "";
    sessionStorage.removeItem(tokenKey);
    queryClient.clear();
    setAccessTokenProvider(() => undefined);
    setPrincipal(null);
    if (session?.mode === "cloud") {
      // The gateway owns cookie invalidation and returns the login page.
      const form = document.createElement("form");
      form.method = "post";
      form.action = "/auth/logout";
      document.body.appendChild(form);
      form.submit();
      return;
    }
    setSession({ mode: "token", authenticated: false, principal: null });
  }, [session?.mode, queryClient]);

  const value = useMemo<AuthContextValue>(
    () => ({
      authenticated: session?.authenticated ?? false,
      mode: session?.mode ?? "token",
      automaticDevelopmentSession:
        session?.mode === "local" || developmentProxy,
      principal,
      displayName: session?.display_name ?? null,
      login,
      logout,
    }),
    [session, principal, login, logout],
  );

  if (failed)
    return (
      <main className="grid min-h-svh place-items-center p-6">
        <div className="space-y-4 text-center">
          <p role="alert">{t("auth.sessionUnavailable")}</p>
          <Button
            onClick={() => {
              setFailed(false);
              setAttempt((value) => value + 1);
            }}
          >
            {t("shell.retry")}
          </Button>
        </div>
      </main>
    );
  if (!session)
    return (
      <main className="grid min-h-svh place-items-center" aria-busy="true">
        {t("common.loading")}
      </main>
    );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}

export function RequireAuth({ children }: { children: ReactNode }) {
  const auth = useAuth();
  const location = useLocation();
  if (!auth.authenticated)
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}
