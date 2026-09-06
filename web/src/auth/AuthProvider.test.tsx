import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { AuthProvider, useAuth } from "./AuthProvider";
import { listConnections } from "@/api/client";

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

function SessionProbe() {
  const auth = useAuth();
  return (
    <>
      <p>{auth.authenticated ? "Authenticated" : "Anonymous"}</p>
      <p>{auth.mode}</p>
      <p>{auth.principal?.subject}</p>
      <p>
        {auth.automaticDevelopmentSession
          ? "Automatic session"
          : "Manual session"}
      </p>
      <button onClick={() => void auth.login("test-token")}>Sign in</button>
      <button onClick={auth.logout}>Sign out</button>
      <button onClick={() => void listConnections().catch(() => undefined)}>Load resources</button>
    </>
  );
}

function renderSession() {
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={cache}>
      <LocaleProvider>
        <MemoryRouter>
          <AuthProvider>
            <SessionProbe />
          </AuthProvider>
        </MemoryRouter>
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return cache;
}

function response(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

it("enters local mode only after the backend verifies the session", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        response({
          mode: "local",
          authenticated: true,
          principal: { subject: "local-admin", roles: ["admin"] },
        }),
      ),
  );
  renderSession();
  expect(screen.queryByText("Authenticated")).not.toBeInTheDocument();
  expect(await screen.findByText("Authenticated")).toBeVisible();
  expect(screen.getByText("local-admin")).toBeVisible();
  expect(screen.getByText("Automatic session")).toBeVisible();
});

it("does not treat a saved but revoked bearer token as authenticated", async () => {
  sessionStorage.setItem("steward.access-token", "revoked-token");
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      response({ mode: "token", authenticated: false, principal: null }),
    );
  vi.stubGlobal("fetch", fetchMock);
  renderSession();
  expect(await screen.findByText("Anonymous")).toBeVisible();
  expect(fetchMock.mock.calls[0][0]).toBe("/api/session");
  expect(fetchMock.mock.calls[0][1].headers.get("Authorization")).toBe(
    "Bearer revoked-token",
  );
});

it("clears cached resource data when signing in and signing out", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      response({ mode: "token", authenticated: false, principal: null }),
    )
    .mockResolvedValueOnce(
      response({
        mode: "token",
        authenticated: true,
        principal: { subject: "alice", roles: ["viewer"] },
      }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const cache = renderSession();
  await screen.findByText("Anonymous");
  cache.setQueryData(["resources"], ["old-resource"]);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  expect(await screen.findByText("alice")).toBeVisible();
  expect(cache.getQueryData(["resources"])).toBeUndefined();
  expect(sessionStorage.getItem("steward.access-token")).toBe("test-token");
  cache.setQueryData(["resources"], ["new-resource"]);
  await user.click(screen.getByRole("button", { name: "Sign out" }));
  expect(screen.getByText("Anonymous")).toBeVisible();
  expect(cache.getQueryData(["resources"])).toBeUndefined();
  expect(sessionStorage.getItem("steward.access-token")).toBeNull();
});

it("fails closed and can retry after a session service error", async () => {
  const fetchMock = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce(
      response({
        mode: "local",
        authenticated: true,
        principal: { subject: "local-admin", roles: ["admin"] },
      }),
    );
  vi.stubGlobal("fetch", fetchMock);
  renderSession();
  expect(await screen.findByRole("alert")).toBeVisible();
  expect(screen.queryByText("Authenticated")).not.toBeInTheDocument();
  await userEvent
    .setup()
    .click(screen.getByRole("button", { name: /try again|重试/i }));
  await waitFor(() => expect(screen.getByText("Authenticated")).toBeVisible());
});

it("shows cloud identity without a browser bearer token", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response({
    mode: "cloud", authenticated: true, display_name: "alice@example.test",
    principal: { subject: "user-alice", roles: ["viewer"] },
  }));
  vi.stubGlobal("fetch", fetchMock);
  renderSession();
  expect(await screen.findByText("cloud")).toBeVisible();
  expect(screen.getByText("user-alice")).toBeVisible();
  expect(fetchMock.mock.calls[0][1].headers.has("Authorization")).toBe(false);
});

it("drops cached data and returns to login when an API session expires", async () => {
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(response({ mode: "cloud", authenticated: true, principal: { subject: "alice", roles: ["viewer"] } }))
    .mockResolvedValueOnce(new Response("Unauthorized", { status: 401 })));
  const cache = renderSession();
  await screen.findByText("Authenticated");
  cache.setQueryData(["resources"], ["private-resource"]);
  await userEvent.setup().click(screen.getByRole("button", { name: "Load resources" }));
  expect(await screen.findByText("Anonymous")).toBeVisible();
  expect(cache.getQueryData(["resources"])).toBeUndefined();
});
