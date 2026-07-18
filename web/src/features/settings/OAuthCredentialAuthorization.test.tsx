import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { getAliCloudOAuthFlow, startAliCloudOAuthFlow } from "@/api/client";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { OAuthCredentialAuthorization } from "./OAuthCredentialAuthorization";

vi.mock("@/api/client", () => ({
  getAliCloudOAuthFlow: vi.fn(),
  startAliCloudOAuthFlow: vi.fn(),
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
  vi.useFakeTimers();
  vi.mocked(startAliCloudOAuthFlow).mockReset().mockResolvedValue(pendingFlow);
  vi.mocked(getAliCloudOAuthFlow).mockReset();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("opens Alibaba Cloud authorization and emits an authorized flow exactly once", async () => {
  const popup = {};
  const open = vi.fn(() => popup);
  vi.stubGlobal("open", open);
  vi.mocked(getAliCloudOAuthFlow)
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
        site="intl"
        disabled={false}
        onAuthorized={onAuthorized}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(startAliCloudOAuthFlow).toHaveBeenCalledWith("intl");
  expect(open).toHaveBeenCalledWith(
    pendingFlow.authorization_url,
    "_blank",
    "noopener,noreferrer",
  );
  expect(
    screen.getByText("Waiting for Alibaba Cloud authorization…"),
  ).toBeVisible();

  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });
  expect(getAliCloudOAuthFlow).toHaveBeenCalledTimes(1);
  expect(onAuthorized).not.toHaveBeenCalled();

  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(1);
  expect(onAuthorized).toHaveBeenCalledWith("oauth-flow-a");
  expect(
    screen.getByText("Alibaba Cloud authorization completed."),
  ).toBeVisible();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2_000);
  });
  expect(getAliCloudOAuthFlow).toHaveBeenCalledTimes(2);
  expect(onAuthorized).toHaveBeenCalledTimes(1);
});

it("shows a fallback authorization link when the popup is blocked", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => null),
  );
  vi.mocked(getAliCloudOAuthFlow).mockResolvedValue(pendingFlow);
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        site="cn"
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
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

it("stops polling on failure, site changes, and unmount without rendering secret fields", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => ({})),
  );
  vi.mocked(getAliCloudOAuthFlow).mockResolvedValue({
    id: pendingFlow.id,
    status: "failed",
    expires_at: pendingFlow.expires_at,
    error_code: "oauth_token_exchange_failed",
  });
  const { rerender, unmount, container } = render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        site="cn"
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await Promise.resolve();
  });
  expect(
    screen.getByText("Alibaba Cloud authorization failed. Try again."),
  ).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  ).toBeEnabled();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000);
  });
  expect(getAliCloudOAuthFlow).toHaveBeenCalledTimes(1);

  rerender(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        site="intl"
        disabled={false}
        onAuthorized={vi.fn()}
      />
    </LocaleProvider>,
  );
  expect(
    screen.queryByText("Alibaba Cloud authorization failed. Try again."),
  ).not.toBeInTheDocument();
  expect(container.querySelectorAll("input")).toHaveLength(0);
  expect(container.textContent).not.toMatch(
    /access token|refresh token|access key secret|security token/i,
  );
  unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000);
  });
  expect(getAliCloudOAuthFlow).toHaveBeenCalledTimes(1);
});

it("retries connection persistence with the same authorized flow without logging in again", async () => {
  vi.stubGlobal(
    "open",
    vi.fn(() => ({})),
  );
  vi.mocked(startAliCloudOAuthFlow).mockResolvedValue({
    id: "oauth-retry",
    status: "authorized",
    expires_at: pendingFlow.expires_at,
  });
  const onAuthorized = vi
    .fn()
    .mockRejectedValueOnce(new Error("database unavailable"))
    .mockResolvedValueOnce(undefined);
  vi.mocked(getAliCloudOAuthFlow).mockResolvedValue({
    id: "oauth-retry",
    status: "authorized",
    expires_at: pendingFlow.expires_at,
  });
  render(
    <LocaleProvider>
      <OAuthCredentialAuthorization
        site="cn"
        disabled={false}
        onAuthorized={onAuthorized}
      />
    </LocaleProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(1);
  expect(onAuthorized).toHaveBeenLastCalledWith("oauth-retry");
  expect(startAliCloudOAuthFlow).toHaveBeenCalledTimes(1);

  fireEvent.click(
    screen.getByRole("button", { name: "Retry saving connection" }),
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(onAuthorized).toHaveBeenCalledTimes(2);
  expect(onAuthorized).toHaveBeenLastCalledWith("oauth-retry");
  expect(startAliCloudOAuthFlow).toHaveBeenCalledTimes(1);
});

it.each(["expired", "consumed"] as const)(
  "starts a new authorization when failed persistence reconciliation reports %s",
  async (terminalStatus) => {
    vi.stubGlobal(
      "open",
      vi.fn(() => ({})),
    );
    vi.mocked(startAliCloudOAuthFlow)
      .mockResolvedValueOnce({
        id: "oauth-terminal",
        status: "authorized",
        expires_at: pendingFlow.expires_at,
      })
      .mockResolvedValueOnce(pendingFlow);
    vi.mocked(getAliCloudOAuthFlow).mockResolvedValue({
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
          site="cn"
          disabled={false}
          onAuthorized={onAuthorized}
        />
      </LocaleProvider>,
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getAliCloudOAuthFlow).toHaveBeenCalledWith("oauth-terminal");
    const login = screen.getByRole("button", {
      name: "Log in to Alibaba Cloud",
    });
    expect(login).toBeEnabled();

    fireEvent.click(login);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(startAliCloudOAuthFlow).toHaveBeenCalledTimes(2);
  },
);
