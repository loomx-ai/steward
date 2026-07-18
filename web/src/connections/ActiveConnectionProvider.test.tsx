import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { listConnections } from "@/api/client";
import type { CloudConnection, ConnectionStatus } from "@/api/types";
import {
  ActiveConnectionProvider,
  useActiveConnection,
} from "./ActiveConnectionProvider";

vi.mock("@/api/client", () => ({
  listConnections: vi.fn(),
}));

const listConnectionsMock = vi.mocked(listConnections);

function connection(id: string, status: ConnectionStatus): CloudConnection {
  return {
    id,
    name: status,
    provider: "alicloud",
    partition: status === "active" ? "public" : "",
    principal: status === "active" ? "cloud-user" : "",
    status,
    credential: {
      type: "access_key",
      updated_at: "2026-07-27T12:00:00Z",
    },
    created_at: "2026-07-27T12:00:00Z",
    updated_at: "2026-07-27T12:00:00Z",
  };
}

function RetryProbe() {
  const { error, retry } = useActiveConnection();
  return (
    <div>
      <span>{error?.message ?? "connected"}</span>
      <button type="button" onClick={retry}>
        Retry connections
      </button>
    </div>
  );
}

function ContextProbe() {
  const {
    connections,
    activeConnection,
    activeConnectionID,
    setActiveConnectionID,
  } = useActiveConnection();
  return (
    <div>
      <span>connections:{connections.map((value) => value.id).join(",")}</span>
      <span>active:{activeConnection?.id ?? "none"}</span>
      <span>active-id:{activeConnectionID || "none"}</span>
      <button
        type="button"
        onClick={() => setActiveConnectionID("connection-unverified")}
      >
        Select unverified
      </button>
    </div>
  );
}

describe("ActiveConnectionProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    listConnectionsMock.mockReset();
  });

  it("refetches the same connection query through retry", async () => {
    listConnectionsMock
      .mockRejectedValueOnce(new Error("connection unavailable"))
      .mockResolvedValueOnce({ items: [] });
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <ActiveConnectionProvider>
          <RetryProbe />
        </ActiveConnectionProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("connection unavailable")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Retry connections" }));
    await waitFor(() => expect(listConnectionsMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("connected")).toBeVisible();
  });

  it("exposes only validated connections to the business context", async () => {
    localStorage.setItem("steward.active-connection", "connection-unverified");
    listConnectionsMock.mockResolvedValue({
      items: [
        connection("connection-unverified", "unverified"),
        connection("connection-active", "active"),
        connection("connection-invalid", "invalid"),
      ],
    });
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <ActiveConnectionProvider>
          <ContextProbe />
        </ActiveConnectionProvider>
      </QueryClientProvider>,
    );

    expect(
      await screen.findByText("connections:connection-active"),
    ).toBeVisible();
    expect(screen.getByText("active:connection-active")).toBeVisible();
    expect(screen.getByText("active-id:connection-active")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Select unverified" }));
    expect(screen.getByText("active:connection-active")).toBeVisible();
    expect(screen.getByText("active-id:connection-active")).toBeVisible();
  });
});
