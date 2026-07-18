import { useRef, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  createConnection,
  deleteConnection,
  getAliCloudOAuthFlow,
  listConnectionRegions,
  listConnections,
  listProviders,
  renameConnection,
  replaceConnectionCredential,
  startAliCloudOAuthFlow,
  validateConnection,
} from "@/api/client";
import type {
  CloudConnection,
  CredentialSchema,
  ProviderDescriptor,
} from "@/api/types";
import { ThemeProvider } from "@/app/ThemeProvider";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { ConnectionEditor, type EditorState } from "./ConnectionEditor";
import {
  connectionDeletionConfirmed,
  credentialInputFromValues,
} from "./CredentialFields";
import { SettingsNavigation } from "./SettingsNavigation";
import { SettingsRoute } from "./SettingsRoute";
import { SettingsView } from "./SettingsView";

vi.mock("@/api/client", () => ({
  addConnectionRegion: vi.fn(),
  createConnection: vi.fn(),
  deleteConnection: vi.fn(),
  excludeConnectionRegion: vi.fn(),
  getAliCloudOAuthFlow: vi.fn(),
  listConnectionRegions: vi.fn().mockResolvedValue({ items: [] }),
  listConnections: vi.fn(),
  listProviders: vi.fn(),
  refreshConnectionRegions: vi.fn(),
  renameConnection: vi.fn(),
  restoreConnectionRegion: vi.fn(),
  replaceConnectionCredential: vi.fn(),
  startAliCloudOAuthFlow: vi.fn(),
  updateConnectionRegion: vi.fn(),
  validateConnection: vi.fn(),
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    principal: { subject: "local-admin", roles: ["admin"] },
  }),
}));

const schema: CredentialSchema = {
  type: "access_key",
  label_key: "credentials.accessKey",
  fields: [
    {
      key: "access_key_id",
      label_key: "credentials.accessKeyId",
      input_type: "text",
      secret: false,
      required: true,
    },
    {
      key: "access_key_secret",
      label_key: "credentials.accessSecret",
      input_type: "password",
      secret: true,
      required: true,
    },
  ],
};

const focusProviders: ProviderDescriptor[] = [
  {
    provider: "aws",
    sites: [],
    inventory_sources: [],
    credential_schemas: [schema],
  },
];

const focusConnection: CloudConnection = {
  id: "connection-1",
  name: "Production",
  provider: "aws",
  partition: "aws",
  principal: "123456789012",
  status: "active",
  credential: { type: "access_key", updated_at: "2026-07-14T00:00:00Z" },
  created_at: "2026-07-14T00:00:00Z",
  updated_at: "2026-07-14T00:00:00Z",
};

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
    releasePointerCapture: { configurable: true, value: vi.fn() },
    setPointerCapture: { configurable: true, value: vi.fn() },
    scrollIntoView: { configurable: true, value: vi.fn() },
  });
  vi.mocked(createConnection).mockReset();
  vi.mocked(deleteConnection).mockReset();
  vi.mocked(getAliCloudOAuthFlow).mockReset();
  vi.mocked(listConnectionRegions).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(renameConnection).mockReset();
  vi.mocked(replaceConnectionCredential).mockReset();
  vi.mocked(startAliCloudOAuthFlow).mockReset();
  vi.mocked(validateConnection).mockReset();
  vi.mocked(listConnections)
    .mockReset()
    .mockResolvedValue({ items: [focusConnection] });
  vi.mocked(listProviders).mockReset().mockResolvedValue(focusProviders);
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  );
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => vi.unstubAllGlobals());

it("selects Alibaba Cloud site independently from credentials and keeps it immutable on replacement", async () => {
  const providers: ProviderDescriptor[] = [
    {
      provider: "alicloud",
      inventory_sources: [],
      sites: [
        { value: "cn", label_key: "sites.alicloudCN" },
        { value: "intl", label_key: "sites.alicloudINTL" },
      ],
      credential_schemas: [
        {
          type: "access_key",
          label_key: "credentials.alicloudAccessKey",
          fields: [
            {
              key: "access_key_id",
              label_key: "credentials.accessKeyId",
              input_type: "text",
              secret: true,
              required: true,
            },
            {
              key: "access_key_secret",
              label_key: "credentials.accessSecret",
              input_type: "password",
              secret: true,
              required: true,
            },
          ],
        },
      ],
    },
    {
      provider: "aws",
      inventory_sources: [],
      sites: [],
      credential_schemas: [schema],
    },
  ];
  const onCreate = vi.fn();
  const onReplace = vi.fn();
  const returnFocusRef = { current: null };
  const props = {
    providers,
    busy: false,
    error: undefined,
    returnFocusRef,
    onClose: vi.fn(),
    onCreate,
    onRename: vi.fn(),
    onReplace,
    onDelete: vi.fn(),
  };
  const user = userEvent.setup();
  const { rerender } = render(
    <LocaleProvider>
      <ConnectionEditor {...props} editor={{ kind: "create" }} />
    </LocaleProvider>,
  );

  const siteSelect = screen.getByRole("combobox", { name: "Site" });
  expect(siteSelect).toHaveTextContent("China site");
  await user.click(siteSelect);
  await user.click(
    await screen.findByRole("option", { name: "International site" }),
  );
  await user.type(screen.getByLabelText("Name"), "International");
  await user.type(screen.getByLabelText("Access key ID"), "ak-id");
  await user.type(screen.getByLabelText("Access key secret"), "ak-secret");
  await user.click(screen.getByRole("button", { name: "Create connection" }));

  expect(onCreate).toHaveBeenLastCalledWith({
    name: "International",
    provider: "alicloud",
    site: "intl",
    credential: {
      type: "access_key",
      values: { access_key_id: "ak-id", access_key_secret: "ak-secret" },
    },
  });
  expect(onCreate.mock.calls[0]?.[0].credential.values).not.toHaveProperty(
    "site",
  );

  await user.click(screen.getByRole("combobox", { name: "Provider" }));
  await user.click(await screen.findByRole("option", { name: "AWS" }));
  expect(
    screen.queryByRole("combobox", { name: "Site" }),
  ).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Create connection" }));
  expect(onCreate).toHaveBeenLastCalledWith(
    expect.not.objectContaining({ site: expect.anything() }),
  );

  rerender(
    <LocaleProvider>
      <ConnectionEditor
        {...props}
        editor={{
          kind: "credential",
          connection: {
            ...focusConnection,
            provider: "alicloud",
            site: "intl",
          },
        }}
      />
    </LocaleProvider>,
  );
  expect(screen.getByText("Site: International site")).toBeVisible();
  expect(
    screen.queryByRole("combobox", { name: "Site" }),
  ).not.toBeInTheDocument();
});

it("defaults replacement to the connection's current credential type", async () => {
  const oauthSchema: CredentialSchema = {
    type: "oauth",
    label_key: "credentials.alicloudOAuth",
    flow: "browser_oauth",
    fields: [],
  };
  const providers: ProviderDescriptor[] = [
    {
      provider: "alicloud",
      inventory_sources: [],
      sites: [{ value: "cn", label_key: "sites.alicloudCN" }],
      credential_schemas: [schema, oauthSchema],
    },
  ];
  const props = {
    providers,
    busy: false,
    error: undefined,
    returnFocusRef: { current: null },
    onClose: vi.fn(),
    onCreate: vi.fn(),
    onRename: vi.fn(),
    onReplace: vi.fn(),
    onDelete: vi.fn(),
  };
  const { rerender } = render(
    <LocaleProvider>
      <ConnectionEditor
        {...props}
        editor={{
          kind: "credential",
          connection: {
            ...focusConnection,
            provider: "alicloud",
            site: "cn",
            credential: {
              ...focusConnection.credential,
              type: "oauth",
            },
          },
        }}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("combobox", { name: "Credential type" }),
  ).toHaveTextContent("OAuth");

  rerender(
    <LocaleProvider>
      <ConnectionEditor
        {...props}
        editor={{
          kind: "credential",
          connection: {
            ...focusConnection,
            provider: "alicloud",
            site: "cn",
          },
        }}
      />
    </LocaleProvider>,
  );

  await waitFor(() =>
    expect(
      screen.getByRole("combobox", { name: "Credential type" }),
    ).toHaveTextContent("Access key"),
  );
});

it("creates and replaces Alibaba Cloud OAuth credentials using only an authorized flow ID", async () => {
  const oauthSchema: CredentialSchema = {
    type: "oauth",
    label_key: "credentials.alicloudOAuth",
    flow: "browser_oauth",
    fields: [],
  };
  const providers: ProviderDescriptor[] = [
    {
      provider: "alicloud",
      inventory_sources: [],
      sites: [
        { value: "cn", label_key: "sites.alicloudCN" },
        { value: "intl", label_key: "sites.alicloudINTL" },
      ],
      credential_schemas: [oauthSchema],
    },
  ];
  vi.mocked(startAliCloudOAuthFlow)
    .mockResolvedValueOnce({
      id: "oauth-create",
      status: "authorized",
      expires_at: "2026-07-27T12:05:00Z",
    })
    .mockResolvedValueOnce({
      id: "oauth-replace",
      status: "authorized",
      expires_at: "2026-07-27T12:05:00Z",
    });
  const onCreate = vi.fn();
  const onReplace = vi.fn();
  const props = {
    providers,
    busy: false,
    error: undefined,
    returnFocusRef: { current: null },
    onClose: vi.fn(),
    onCreate,
    onRename: vi.fn(),
    onReplace,
    onDelete: vi.fn(),
  };
  const user = userEvent.setup();
  const { rerender } = render(
    <LocaleProvider>
      <ConnectionEditor {...props} editor={{ kind: "create" }} />
    </LocaleProvider>,
  );

  await user.type(screen.getByLabelText("Name"), "OAuth account");
  expect(
    screen.queryByRole("button", { name: "Create connection" }),
  ).not.toBeInTheDocument();
  expect(screen.queryAllByRole("textbox")).toHaveLength(1);
  await user.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  );
  await waitFor(() =>
    expect(onCreate).toHaveBeenCalledWith({
      name: "OAuth account",
      provider: "alicloud",
      site: "cn",
      credential: { type: "oauth", values: { flow_id: "oauth-create" } },
    }),
  );

  rerender(
    <LocaleProvider>
      <ConnectionEditor
        {...props}
        editor={{
          kind: "credential",
          connection: {
            ...focusConnection,
            provider: "alicloud",
            site: "intl",
          },
        }}
      />
    </LocaleProvider>,
  );
  await user.click(
    screen.getByRole("button", { name: "Log in to Alibaba Cloud" }),
  );
  await waitFor(() =>
    expect(onReplace).toHaveBeenCalledWith("connection-1", {
      type: "oauth",
      values: { flow_id: "oauth-replace" },
    }),
  );
  expect(startAliCloudOAuthFlow).toHaveBeenNthCalledWith(1, "cn");
  expect(startAliCloudOAuthFlow).toHaveBeenNthCalledWith(2, "intl");
  expect(document.body.textContent).not.toMatch(
    /access token|refresh token|access key secret|security token/i,
  );
});

it("paginates cloud connections within the selected provider", async () => {
  const secondConnection = {
    ...focusConnection,
    id: "connection-2",
    name: "Staging",
  };
  vi.mocked(listConnections).mockImplementation(async (options) =>
    options?.cursor === "cursor-2"
      ? { items: [secondConnection] }
      : { items: [focusConnection], next_cursor: "cursor-2" },
  );
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  expect(await screen.findByText("Production")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Next" }));
  expect(await screen.findByText("Staging")).toBeVisible();
  expect(screen.queryByText("Production")).not.toBeInTheDocument();
  expect(listConnections).toHaveBeenCalledWith({
    cursor: "cursor-2",
    limit: 20,
    provider: "aws",
  });
});

it("builds credentials from explicit fields and requires exact deletion text", () => {
  expect(
    credentialInputFromValues(schema, {
      access_key_id: "ak-id",
      access_key_secret: "secret",
      expires_at: "2026-07-14T10:00",
    }),
  ).toEqual({
    type: "access_key",
    values: { access_key_id: "ak-id", access_key_secret: "secret" },
    expires_at: new Date("2026-07-14T10:00").toISOString(),
  });
  expect(connectionDeletionConfirmed("Production", "production")).toBe(false);
  expect(connectionDeletionConfirmed("Production", "Production")).toBe(true);
});

it("switches the settings category through a compact secondary navigation", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <SettingsNavigation
      value="general"
      onValueChange={onChange}
      labels={{
        general: "General",
        connections: "Cloud connections",
        account: "Account",
      }}
    />,
  );
  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  expect(onChange).toHaveBeenCalledWith("connections");
  expect(screen.getByRole("navigation")).not.toHaveClass("md:w-44");
  expect(screen.getByRole("button", { name: "Cloud connections" })).toHaveClass(
    "whitespace-nowrap",
  );
});

it("opens cloud connections from the settings section query parameter", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <MemoryRouter initialEntries={["/settings?section=connections"]}>
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <ThemeProvider>
            <SettingsRoute />
          </ThemeProvider>
        </LocaleProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );

  expect(
    screen.getByRole("button", { name: "Cloud connections" }),
  ).toHaveAttribute("aria-current", "page");
  expect(await screen.findByText("Add connection")).toBeVisible();
});

it("expands region management inside its connection row", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  const trigger = await screen.findByRole("button", {
    name: "Manage regions · Production",
  });
  expect(listConnectionRegions).not.toHaveBeenCalled();

  await user.click(trigger);

  const panel = await screen.findByRole("region", {
    name: "Regions · Production",
  });
  expect(panel).toBeVisible();
  expect(panel.closest('[data-connection-id="connection-1"]')).not.toBeNull();
  expect(trigger).toHaveAttribute("aria-expanded", "true");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  const regionOptions = vi.mocked(listConnectionRegions).mock.calls[0]?.[1];
  expect(regionOptions).toEqual(
    expect.objectContaining({
      lifecycle: undefined,
      query: undefined,
    }),
  );
  expect(regionOptions).not.toHaveProperty("cursor");
  expect(regionOptions).not.toHaveProperty("limit");
});

it("keeps one connection region panel open and toggles the current row", async () => {
  const secondConnection = {
    ...focusConnection,
    id: "connection-2",
    name: "Staging",
  };
  vi.mocked(listConnections).mockResolvedValue({
    items: [focusConnection, secondConnection],
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  const production = await screen.findByRole("button", {
    name: "Manage regions · Production",
  });
  const staging = screen.getByRole("button", {
    name: "Manage regions · Staging",
  });

  await user.click(production);
  expect(
    await screen.findByRole("region", { name: "Regions · Production" }),
  ).toBeVisible();

  await user.click(staging);
  expect(
    await screen.findByRole("region", { name: "Regions · Staging" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("region", { name: "Regions · Production" }),
  ).not.toBeInTheDocument();

  await user.click(staging);
  expect(
    screen.queryByRole("region", { name: "Regions · Staging" }),
  ).not.toBeInTheDocument();
});

it("returns focus to the region trigger when the inline panel collapses", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  const trigger = await screen.findByRole("button", {
    name: "Manage regions · Production",
  });
  await user.click(trigger);
  await user.click(
    await screen.findByRole("button", { name: "Collapse region management" }),
  );

  expect(
    screen.queryByRole("region", { name: "Regions · Production" }),
  ).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
});

it("opens inline region management from the connection actions menu", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  await user.click(await screen.findByRole("button", { name: "Actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Manage regions" }));

  expect(
    await screen.findByRole("region", { name: "Regions · Production" }),
  ).toBeVisible();
});

it("validates an unverified connection without exposing region management", async () => {
  const unverifiedConnection: CloudConnection = {
    ...focusConnection,
    partition: "",
    principal: "",
    status: "unverified",
  };
  const activeConnection: CloudConnection = {
    ...focusConnection,
    status: "active",
  };
  let validated = false;
  vi.mocked(listConnections).mockImplementation(async () => ({
    items: [validated ? activeConnection : unverifiedConnection],
  }));
  vi.mocked(validateConnection).mockImplementation(async () => {
    validated = true;
    return activeConnection;
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  const validate = await screen.findByRole("button", {
    name: "Validate Production",
  });
  expect(
    screen.queryByRole("button", { name: "Manage regions · Production" }),
  ).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Actions" }));
  expect(
    screen.queryByRole("menuitem", { name: "Manage regions" }),
  ).not.toBeInTheDocument();
  await user.keyboard("{Escape}");

  await user.click(validate);

  expect(validateConnection).toHaveBeenCalledWith("connection-1");
  expect(
    await screen.findByRole("button", {
      name: "Manage regions · Production",
    }),
  ).toBeVisible();
});

it("shows raw provider diagnostics in a minimal inline validation layout", async () => {
  const unverifiedConnection: CloudConnection = {
    ...focusConnection,
    partition: "",
    principal: "",
    status: "unverified",
  };
  vi.mocked(listConnections).mockResolvedValue({
    items: [unverifiedConnection],
  });
  vi.mocked(validateConnection).mockRejectedValue(
    Object.assign(new Error("cloud provider rejected the credential"), {
      code: "credential_validation_failed",
      details: {
        category: "invalid_request",
        provider_code: "InvalidAccessKeyId.NotFound",
        provider_message: "The specified access key is not found.",
        provider_request_id: "provider-request",
      },
      requestID: "server-request",
    }),
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  await user.click(
    await screen.findByRole("button", { name: "Validate Production" }),
  );

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("Credential validation failed");
  expect(alert).toHaveTextContent(
    "The Access Key ID does not exist or is no longer valid. Check it and validate again.",
  );
  expect(alert).not.toHaveTextContent("InvalidAccessKeyId.NotFound");
  expect(alert).not.toHaveTextContent("provider-request");
  expect(alert).not.toHaveTextContent("server-request");
  expect(alert).not.toHaveTextContent("code: 404");
  expect(alert).not.toHaveClass("border", "rounded-lg", "bg-card");

  const connectionGroup = screen.getByRole("group", {
    name: "Cloud connections",
  });
  expect(connectionGroup.lastElementChild).not.toHaveClass(
    "border",
    "rounded-xl",
    "bg-background",
  );
  const connectionArticle = connectionGroup.querySelector(
    'article[data-connection-id="connection-1"]',
  );
  expect(connectionArticle).not.toBeNull();
  expect(connectionArticle?.parentElement).toHaveClass("divide-y");
  expect(connectionArticle?.parentElement?.parentElement).not.toHaveClass(
    "border",
    "rounded-lg",
  );

  await user.click(
    within(alert).getByRole("button", { name: "Show error details" }),
  );

  expect(alert).toHaveTextContent("InvalidAccessKeyId.NotFound");
  expect(alert).toHaveTextContent("Error message");
  expect(alert).toHaveTextContent("The specified access key is not found.");
  expect(alert).toHaveTextContent("provider-request");
  expect(alert).toHaveTextContent("server-request");
  expect(alert).not.toHaveTextContent("access_key_secret");
});

it("collapses region management when the cloud provider changes", async () => {
  const alicloudProvider = {
    ...focusProviders[0],
    provider: "alicloud",
  };
  vi.mocked(listProviders).mockResolvedValue([
    alicloudProvider,
    focusProviders[0],
  ]);
  vi.mocked(listConnections).mockResolvedValue({
    items: [
      { ...focusConnection, provider: "alicloud", partition: "public" },
      {
        ...focusConnection,
        id: "connection-2",
        name: "Staging",
        provider: "aws",
      },
    ],
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  await user.click(
    await screen.findByRole("button", {
      name: "Manage regions · Production",
    }),
  );
  expect(
    await screen.findByRole("region", { name: "Regions · Production" }),
  ).toBeVisible();

  await user.click(screen.getByRole("tab", { name: /AWS/ }));

  expect(
    screen.queryByRole("region", { name: "Regions · Production" }),
  ).not.toBeInTheDocument();

  await user.click(screen.getByRole("tab", { name: /Alibaba Cloud/ }));
  expect(
    screen.queryByRole("region", { name: "Regions · Production" }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Manage regions · Production" }),
  ).toHaveAttribute("aria-expanded", "false");
});

it("clears typed deletion confirmation before another connection is opened", async () => {
  const connection: CloudConnection = {
    id: "connection-1",
    name: "Production",
    provider: "aws",
    partition: "aws",
    principal: "123456789012",
    status: "active",
    credential: { type: "access_key", updated_at: "2026-07-14T00:00:00Z" },
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
  };
  const props = {
    providers: [],
    busy: false,
    error: undefined,
    returnFocusRef: { current: null },
    onClose: vi.fn(),
    onCreate: vi.fn(),
    onRename: vi.fn(),
    onReplace: vi.fn(),
    onDelete: vi.fn(),
  };
  const { rerender } = render(
    <LocaleProvider>
      <ConnectionEditor {...props} editor={{ kind: "delete", connection }} />
    </LocaleProvider>,
  );
  const user = userEvent.setup();

  await user.type(
    screen.getByRole("textbox", {
      name: "Type Production to confirm deletion",
    }),
    "Production",
  );
  expect(
    screen.getByRole("button", { name: "Delete connection" }),
  ).toBeEnabled();

  rerender(
    <LocaleProvider>
      <ConnectionEditor {...props} editor={{ kind: "closed" }} />
    </LocaleProvider>,
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "Delete connection" }),
    ).not.toBeInTheDocument(),
  );
  rerender(
    <LocaleProvider>
      <ConnectionEditor
        {...props}
        editor={{
          kind: "delete",
          connection: { ...connection, id: "connection-2" },
        }}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: "Delete connection" }),
  ).toBeDisabled();
});

it("restores a form sheet to its persistent settings trigger", async () => {
  function FocusHarness() {
    const [editor, setEditor] = useState<EditorState>({ kind: "closed" });
    const returnFocusRef = useRef<HTMLButtonElement | null>(null);
    return (
      <>
        <button
          ref={returnFocusRef}
          type="button"
          onClick={() => setEditor({ kind: "create" })}
        >
          Add connection trigger
        </button>
        <ConnectionEditor
          editor={editor}
          providers={focusProviders}
          busy={false}
          error={undefined}
          returnFocusRef={returnFocusRef}
          onClose={() => setEditor({ kind: "closed" })}
          onCreate={vi.fn()}
          onRename={vi.fn()}
          onReplace={vi.fn()}
          onDelete={vi.fn()}
        />
      </>
    );
  }

  const user = userEvent.setup();
  render(
    <LocaleProvider>
      <FocusHarness />
    </LocaleProvider>,
  );
  const trigger = screen.getByRole("button", {
    name: "Add connection trigger",
  });

  await user.click(trigger);
  expect(screen.getByRole("dialog")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Close" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();

  await user.click(trigger);
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();
});

it("returns a row editor to the persistent dropdown trigger", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));
  const trigger = await screen.findByRole("button", { name: "Actions" });
  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "Rename connection" }));
  expect(screen.getByRole("dialog")).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Close" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();

  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "Delete connection" }));
  expect(screen.getByRole("alertdialog")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() =>
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();
});

it("uses the available settings width and contains long connection facts", async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <ThemeProvider>
          <SettingsView />
        </ThemeProvider>
      </LocaleProvider>
    </QueryClientProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Cloud connections" }));

  const layout = document.querySelector('[data-slot="page-layout"]');
  expect(layout).toHaveAttribute("data-layout-mode", "list");
  expect(layout).toHaveClass("max-w-none");
  expect(layout?.firstElementChild).toHaveClass(
    "gap-4",
    "md:grid-cols-[max-content_minmax(0,1fr)]",
  );

  const principalLabel = await screen.findByText("Cloud principal");
  const principalValue =
    principalLabel.nextElementSibling?.querySelector("span[title]");
  expect(principalValue).toHaveAttribute("title", focusConnection.principal);
  expect(principalValue).toHaveClass("truncate");
  expect(screen.queryByText("Partition / tenant")).not.toBeInTheDocument();
  expect(screen.getByText("Credential type")).toBeVisible();
  expect(screen.getByText("Access key")).toBeVisible();
});
