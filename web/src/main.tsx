import React from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "./app/ThemeProvider";
import { AuthProvider, preloadSession } from "./auth/AuthProvider";
import { Toaster } from "./components/ui/sonner";
import { TooltipProvider } from "./components/ui/tooltip";
import { LocaleProvider, preloadActiveLocale } from "./i18n/LocaleProvider";
import { AppRoutes } from "./routes";
import "./styles/globals.css";
import "@xyflow/react/dist/style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 10_000, retry: 1, refetchOnWindowFocus: true },
  },
});
// The provider catalog ships with the server binary and only changes on upgrade.
queryClient.setQueryDefaults(["catalog"], { staleTime: Infinity });

// Render only once the active locale's dictionary is in, so the first paint is
// already translated. The session request runs meanwhile.
preloadSession();
void preloadActiveLocale().then(() =>
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
  ),
);
