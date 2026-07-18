import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { ConnectionGate } from "@/app/ConnectionGate";
import { RouteErrorSurface } from "@/app/RouteErrorBoundary";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { LoginForm } from "./LoginView";

const labels = {
  token: "Bearer token",
  placeholder: "Token",
  verifying: "Verifying…",
  signIn: "Sign in",
  failed: "Authentication failed",
};

describe("entry surfaces", () => {
  it("keeps login disabled for a blank token and shows a busy state", async () => {
    let finish: () => void = () => undefined;
    const login = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const user = userEvent.setup();
    render(
      <LoginForm
        labels={labels}
        login={login}
        formatError={() => labels.failed}
        onSuccess={() => undefined}
      />,
    );

    expect(screen.getByRole("main")).toHaveClass("bg-background");
    expect(screen.getByRole("heading", { name: labels.signIn })).toHaveClass(
      "text-xl",
    );
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
    await user.type(screen.getByLabelText("Bearer token"), "secret-value");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(screen.getByRole("button", { name: "Verifying…" })).toBeDisabled();
    finish();
  });

  it("formats authentication failures without rendering the submitted token", async () => {
    const user = userEvent.setup();
    render(
      <LoginForm
        labels={labels}
        login={() => Promise.reject(new Error("access denied"))}
        formatError={() => labels.failed}
        onSuccess={() => undefined}
      />,
    );
    await user.type(screen.getByLabelText("Bearer token"), "do-not-echo");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Authentication failed")).toBeVisible();
    expect(screen.queryByText("do-not-echo")).not.toBeInTheDocument();
  });

  it("links the empty connection state to cloud connection settings", () => {
    render(
      <MemoryRouter>
        <LocaleProvider>
          <ConnectionGate />
        </LocaleProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "/settings?section=connections",
    );
  });

  it("offers retry and home actions after a route failure", async () => {
    const retry = vi.fn();
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <LocaleProvider>
          <RouteErrorSurface
            error={new Error("render failed")}
            onRetry={retry}
          />
        </LocaleProvider>
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("button", { name: /try again|重试/i }));
    expect(retry).toHaveBeenCalledOnce();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/panorama");
  });

  it("reloads after a route module failure without exposing module details", async () => {
    const retry = vi.fn();
    const reload = vi.fn();
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <LocaleProvider>
          <RouteErrorSurface
            error={
              new TypeError(
                "Failed to fetch dynamically imported module: http://127.0.0.1:5858/src/features/panorama/PanoramaView.tsx",
              )
            }
            onRetry={retry}
            onReload={reload}
          />
        </LocaleProvider>
      </MemoryRouter>,
    );

    expect(
      screen.getByText(/page loading was interrupted|页面加载已中断/i),
    ).toBeVisible();
    expect(screen.queryByText(/PanoramaView\.tsx/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /try again|重试/i }));
    expect(reload).toHaveBeenCalledOnce();
    expect(retry).not.toHaveBeenCalled();
  });
});
