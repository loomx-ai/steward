import React from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "./app/ThemeProvider";
import { AuthProvider } from "./auth/AuthProvider";
import { Toaster } from "./components/ui/sonner";
import { TooltipProvider } from "./components/ui/tooltip";
import { LocaleProvider } from "./i18n/LocaleProvider";
import { AppRoutes } from "./routes";
import "./styles/globals.css";
import "@xyflow/react/dist/style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 10_000, retry: 1, refetchOnWindowFocus: true },
  },
});

createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <TooltipProvider>
          <LocaleProvider>
            <BrowserRouter>
              <AuthProvider>
                <AppRoutes />
                <Toaster position="bottom-right" richColors />
              </AuthProvider>
            </BrowserRouter>
          </LocaleProvider>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
