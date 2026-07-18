import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Navigate, useLocation } from "react-router-dom";
import {
  listProviderCatalog,
  setAccessTokenProvider,
  setPrincipalObserver,
} from "../api/client";
import type { Principal } from "../api/types";

const tokenKey = "steward.access-token";
const automaticDevelopmentSession =
  import.meta.env.DEV && import.meta.env.VITE_STEWARD_DEV_AUTO_LOGIN === "1";

interface AuthContextValue {
  authenticated: boolean;
  automaticDevelopmentSession: boolean;
  principal: Principal | null;
  login: (token: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState(() =>
    automaticDevelopmentSession ? "" : (sessionStorage.getItem(tokenKey) ?? ""),
  );
  const [principal, setPrincipal] = useState<Principal | null>(null);

  useEffect(() => {
    setAccessTokenProvider(() => token || undefined);
  }, [token]);

  useEffect(() => {
    setPrincipalObserver(setPrincipal);
    return () => setPrincipalObserver(() => undefined);
  }, []);

  const login = useCallback(async (candidate: string) => {
    const normalized = candidate.trim();
    if (!normalized) throw new Error("Bearer token is required");
    setAccessTokenProvider(() => normalized);
    try {
      await listProviderCatalog();
      sessionStorage.setItem(tokenKey, normalized);
      setToken(normalized);
    } catch (error) {
      setAccessTokenProvider(() => undefined);
      sessionStorage.removeItem(tokenKey);
      setToken("");
      setPrincipal(null);
      throw error;
    }
  }, []);

  const logout = useCallback(() => {
    if (automaticDevelopmentSession) return;
    sessionStorage.removeItem(tokenKey);
    setAccessTokenProvider(() => undefined);
    setToken("");
    setPrincipal(null);
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      authenticated: automaticDevelopmentSession || token !== "",
      automaticDevelopmentSession,
      principal,
      login,
      logout,
    }),
    [token, principal, login, logout],
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
  if (!auth.authenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}
