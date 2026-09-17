import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { getConnectionOIDCTrust } from "@/api/client";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { OIDCTrust } from "./OIDCTrust";

vi.mock("@/api/client", () => ({ getConnectionOIDCTrust: vi.fn() }));

it("loads exact server-issued trust information only when expanded", async () => {
  localStorage.setItem("steward.locale", "en");
  const trust = {
    issuer: "https://identity.example/workspace",
    jwks_uri: "https://identity.example/workspace/.well-known/jwks",
    audience: "sts.amazonaws.com",
    read_subject: "workspace:one:connection:con_test:run_phase:read",
    write_subject: "workspace:one:connection:con_test:run_phase:write",
  };
  vi.mocked(getConnectionOIDCTrust).mockResolvedValue(trust);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <OIDCTrust connectionID="con_test" />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  expect(getConnectionOIDCTrust).not.toHaveBeenCalled();
  await userEvent.click(screen.getByText("OIDC trust configuration"));
  await waitFor(() =>
    expect(getConnectionOIDCTrust).toHaveBeenCalledWith("con_test"),
  );
  expect(await screen.findByLabelText("Issuer URL")).toHaveValue(trust.issuer);
  expect(screen.getByLabelText("Read subject")).toHaveValue(trust.read_subject);
  expect(screen.getByLabelText("Write subject")).toHaveValue(
    trust.write_subject,
  );
  expect(screen.getByLabelText("Read subject")).toHaveAttribute("readonly");
});
