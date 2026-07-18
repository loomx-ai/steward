import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { listConnections } from "../api/client";
import type { CloudConnection } from "../api/types";

const storageKey = "steward.active-connection";

interface ActiveConnectionContextValue {
  connections: CloudConnection[];
  activeConnection?: CloudConnection;
  activeConnectionID: string;
  setActiveConnectionID: (id: string) => void;
  loading: boolean;
  error: Error | null;
  retry: () => void;
}

const ActiveConnectionContext = createContext<
  ActiveConnectionContextValue | undefined
>(undefined);

export function ActiveConnectionProvider({
  children,
}: {
  children: ReactNode;
}) {
  const queryClient = useQueryClient();
  const [activeConnectionID, setActiveConnectionIDState] = useState(
    () => localStorage.getItem(storageKey) ?? "",
  );
  const query = useQuery({
    queryKey: ["connections", "active-context"],
    queryFn: () => listConnections({ limit: 500 }),
  });
  const connections = useMemo(
    () =>
      (query.data?.items ?? []).filter(
        (connection) => connection.status === "active",
      ),
    [query.data?.items],
  );

  useEffect(() => {
    if (query.isPending) return;
    if (connections.some((value) => value.id === activeConnectionID)) return;
    const next = connections[0]?.id ?? "";
    setActiveConnectionIDState(next);
    if (next) localStorage.setItem(storageKey, next);
    else localStorage.removeItem(storageKey);
  }, [activeConnectionID, connections, query.isPending]);

  const setActiveConnectionID = useCallback(
    (id: string) => {
      if (!connections.some((value) => value.id === id)) return;
      localStorage.setItem(storageKey, id);
      setActiveConnectionIDState(id);
      queryClient.removeQueries({
        predicate: (value) =>
          !["connections", "providers", "provider-catalog"].includes(
            String(value.queryKey[0]),
          ),
      });
    },
    [connections, queryClient],
  );
  const retry = useCallback(() => {
    void query.refetch();
  }, [query.refetch]);

  const value = useMemo<ActiveConnectionContextValue>(
    () => ({
      connections,
      activeConnection: connections.find(
        (connection) => connection.id === activeConnectionID,
      ),
      activeConnectionID,
      setActiveConnectionID,
      loading: query.isPending,
      error: query.error,
      retry,
    }),
    [
      activeConnectionID,
      connections,
      query.error,
      query.isPending,
      retry,
      setActiveConnectionID,
    ],
  );
  return (
    <ActiveConnectionContext.Provider value={value}>
      {children}
    </ActiveConnectionContext.Provider>
  );
}

export function useActiveConnection() {
  const value = useContext(ActiveConnectionContext);
  if (!value) {
    throw new Error("useActiveConnection requires ActiveConnectionProvider");
  }
  return value;
}

export function useRequiredConnection(): CloudConnection {
  const { activeConnection } = useActiveConnection();
  if (!activeConnection) {
    throw new Error("a cloud connection must be selected");
  }
  return activeConnection;
}
