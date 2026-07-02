import React from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ActorProvider } from "./lib/actor";
import { AppRoutes } from "./routes";
import "./styles.css";
import "@xyflow/react/dist/style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: 1,
    },
  },
});

createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <ActorProvider>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
      </ActorProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
