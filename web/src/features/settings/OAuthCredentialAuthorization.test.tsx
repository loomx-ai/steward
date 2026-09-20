import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  getOAuthFlow,
  listOAuthFlowTargets,
  startOAuthFlow,
} from "@/api/client";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { OAuthCredentialAuthorization } from "./OAuthCredentialAuthorization";

vi.mock("@/api/client", () => ({
  getOAuthFlow: vi.fn(),
  listOAuthFlowTargets: vi.fn(),
  startOAuthFlow: vi.fn(),
}));

const pendingFlow = {
  id: "oauth-flow-a",
  status: "pending" as const,
  authorization_url:
    "https://signin.alibabacloud.com/oauth2/v1/auth?state=opaque",
  expires_at: "2026-07-27T12:05:00Z",
};

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  // Radix Select scrolls the active option into view, which jsdom does not
  // implement.
  Object.defineProperties(Element.prototype, {
    scrollIntoView: { configurable: true, value: vi.fn() },
  });
  vi.useFakeTimers();
  vi.mocked(startOAuthFlow).mockReset().mockResolvedValue(pendingFlow);
  vi.mocked(getOAuthFlow).mockReset();
  // Alibaba Cloud authorizes exactly one site, so it offers no target choice.
  vi.mocked(listOAuthFlowTargets).mockReset().mockResolvedValue([]);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("opens Alibaba Cloud authorization and emits an authorized flow exactly once", async () => {
  const popup = {};
  const open = vi.fn(() => popup);
  vi.stubGlobal("open", open);
  vi.mocked(getOAuthFlow)
    .mockResolvedValueOnce({ ...pendingFlow })
    .mockResolvedValueOnce({
      id: pendingFlow.id,
      status: "authorized",
      expires_at: pendingFlow.expires_at,
    });
  const onAuthorized = vi.fn();
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="alicloud"
        params={{ site: "intl" }}
        disabled={false}
        onAuthorized={onAuthorized}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(startOAuthFlow).toHaveBeenCalledWith("alicloud", { site: "intl" });
  expect(open).toHaveBeenCalledWith(
    pendingFlow.authorization_url,
    "_blank",
    "noopener,noreferrer",
  );
  expect(screen.getByText("Waiting for browser authorization…")).toBeVisible();

  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });
  expect(getOAuthFlow).toHaveBeenCalledTimes(1);
  expect(onAuthorized).not.toHaveBeenCalled();

  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(1);
  expect(onAuthorized).toHaveBeenCalledWith("oauth-flow-a", "");
  expect(screen.getByText("Authorization completed.")).toBeVisible();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2_000);
  });
  expect(getOAuthFlow).toHaveBeenCalledTimes(2);
  expect(onAuthorized).toHaveBeenCalledTimes(1);
});

it("shows a fallback authorization link when the popup is blocked", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => null),
  );
  vi.mocked(getOAuthFlow).mockResolvedValue(pendingFlow);
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="alicloud"
        params={{ site: "cn" }}
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(
    screen.getByRole("link", { name: "Open authorization page" }),
  ).toHaveAttribute("href", pendingFlow.authorization_url);
  expect(
    screen.getByRole("link", { name: "Open authorization page" }),
  ).toHaveAttribute("rel", expect.stringContaining("noopener"));
});

it("stops polling on failure, parameter changes, and unmount without rendering secret fields", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => ({})),
  );
  vi.mocked(getOAuthFlow).mockResolvedValue({
    id: pendingFlow.id,
    status: "failed",
    expires_at: pendingFlow.expires_at,
    error_code: "oauth_token_exchange_failed",
  });
  const { rerender, unmount, container } = render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="alicloud"
        params={{ site: "cn" }}
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await Promise.resolve();
  });
  expect(screen.getByText("Authorization failed. Try again.")).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  ).toBeEnabled();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000);
  });
  expect(getOAuthFlow).toHaveBeenCalledTimes(1);

  rerender(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="alicloud"
        params={{ site: "intl" }}
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );
  expect(
    screen.queryByText("Authorization failed. Try again."),
  ).not.toBeInTheDocument();
  expect(container.querySelectorAll("input")).toHaveLength(0);
  expect(container.textContent).not.toMatch(
    /access token|refresh token|access key secret|security token/i,
  );
  unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000);
  });
  expect(getOAuthFlow).toHaveBeenCalledTimes(1);
});

it("retries connection persistence with the same authorized flow without logging in again", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => ({})),
  );
  vi.mocked(startOAuthFlow).mockResolvedValue({
    id: "oauth-retry",
    status: "authorized",
    expires_at: pendingFlow.expires_at,
  });
  const onAuthorized = vi
    .fn()
    .mockRejectedValueOnce(new Error("database unavailable"))
    .mockResolvedValueOnce(undefined);
  vi.mocked(getOAuthFlow).mockResolvedValue({
    id: "oauth-retry",
    status: "authorized",
    expires_at: pendingFlow.expires_at,
  });
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="alicloud"
        params={{ site: "cn" }}
        disabled={false}
        onAuthorized={onAuthorized}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(1);
  expect(onAuthorized).toHaveBeenLastCalledWith("oauth-retry", "");
  expect(startOAuthFlow).toHaveBeenCalledTimes(1);

  fireEvent.click(
    screen.getByRole("button", { name: "Retry saving connection" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(2);
  expect(onAuthorized).toHaveBeenLastCalledWith("oauth-retry", "");
  expect(startOAuthFlow).toHaveBeenCalledTimes(1);
});

it.each(["expired", "consumed"] as const)(
  "starts a new authorization when failed persistence reconciliation reports %s",
  async (terminalStatus) => {
    vi.stubGlobal(
      "open",
      vi.fn(() => ({})),
    );
    vi.mocked(startOAuthFlow)
      .mockResolvedValueOnce({
        id: "oauth-terminal",
        status: "authorized",
        expires_at: pendingFlow.expires_at,
      })
      .mockResolvedValueOnce(pendingFlow);
    vi.mocked(getOAuthFlow).mockResolvedValue({
      id: "oauth-terminal",
      status: terminalStatus,
      expires_at: pendingFlow.expires_at,
    });
    const onAuthorized = vi
      .fn()
      .mockRejectedValueOnce(new Error("response unavailable"));
    render(
      <LocaleProvider>
        <OAuthCredentialAuthorization
          provider="alicloud"
          params={{ site: "cn" }}
          disabled={false}
          onAuthorized={onAuthorized}
        />
      </LocaleProvider>,
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Sign in with your browser" }),
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getOAuthFlow).toHaveBeenCalledWith("alicloud", "oauth-terminal");
    const login = screen.getByRole("button", {
      name: "Sign in with your browser",
    });
    expect(login).toBeEnabled();

    fireEvent.click(login);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(startOAuthFlow).toHaveBeenCalledTimes(2);
  },
);

it("lets the operator pick a cloud scope when the authorization reaches several", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => ({})),
  );
  vi.mocked(startOAuthFlow).mockResolvedValue({
    id: "oauth-scoped",
    status: "authorized",
    expires_at: pendingFlow.expires_at,
  });
  vi.mocked(listOAuthFlowTargets).mockResolvedValue([
    { id: "sub-a", name: "Production", description: "tenant-a" },
    { id: "sub-b", name: "Staging" },
  ]);
  const onAuthorized = vi.fn();
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        provider="azure"
        params={{}}
        disabled={false}
        onAuthorized={onAuthorized}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Sign in with your browser" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(listOAuthFlowTargets).toHaveBeenCalledWith("azure", "oauth-scoped");
  // Nothing is submitted until the operator names a scope.
  expect(onAuthorized).not.toHaveBeenCalled();
  expect(screen.getByText("Connect to")).toBeVisible();
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();

  fireEvent.click(screen.getByRole("combobox"));
  await act(async () => {
    await Promise.resolve();
  });
  fireEvent.click(screen.getByRole("option", { name: "Staging" }));
  await act(async () => {
    await Promise.resolve();
  });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(1);
  expect(onAuthorized).toHaveBeenCalledWith("oauth-scoped", "sub-b");
});
