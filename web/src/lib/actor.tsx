import { createContext, useContext, useState, type ReactNode } from "react";

type ActorContextValue = {
  actor: string;
  setActor: (actor: string) => void;
};

const ActorContext = createContext<ActorContextValue | null>(null);

export function ActorProvider({ children }: { children: ReactNode }) {
  const [actor, setActor] = useState("local-user");
  return (
    <ActorContext.Provider value={{ actor, setActor }}>
      {children}
    </ActorContext.Provider>
  );
}

export function useActor(): ActorContextValue {
  const value = useContext(ActorContext);
  if (!value) throw new Error("useActor must be used within ActorProvider");
  return value;
}
