import { useRef, useState, type ReactNode } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  Sidebar,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { PageToolbar } from "@/components/patterns/PageToolbar";
import { navigationItems } from "./navigation";
import { AppSidebar } from "./AppSidebar";
import { AppShell } from "./AppShell";
import { ThemeProvider } from "./ThemeProvider";
import { WorkspaceProvider, useWorkspace } from "./WorkspaceContext";

const activeConnectionMock = vi.hoisted(() => ({
  connections: [] as Array<{
    id: string;
    name: string;
    provider: string;
    status: string;
  }>,
  activeConnectionID: "",
  setActiveConnectionID: vi.fn(),
  error: null as Error | null,
  retry: vi.fn(),
}));
const cleanupSelectionMock = vi.hoisted(() => ({
  requestConnectionChange: vi.fn(),
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    principal: { subject: "local-admin", roles: ["admin"] },
    automaticDevelopmentSession: false,
    logout: vi.fn(),
  }),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  ActiveConnectionProvider: ({ children }: { children: ReactNode }) => (
    <div data-testid="active-connection-provider">{children}</div>
  ),
  useActiveConnection: () => ({
    connections: activeConnectionMock.connections,
    activeConnectionID: activeConnectionMock.activeConnectionID,
    setActiveConnectionID: activeConnectionMock.setActiveConnectionID,
    loading: false,
    error: activeConnectionMock.error,
    retry: activeConnectionMock.retry,
  }),
}));

vi.mock("@/features/panorama/CleanupSelectionContext", () => ({
  CleanupSelectionProvider: ({ children }: { children: ReactNode }) => (
    <div data-testid="cleanup-selection-provider">{children}</div>
  ),
  useCleanupSelection: () => cleanupSelectionMock,
}));

vi.mock("@/i18n/LocaleProvider", () => ({
  useLocale: () => ({ t: (key: string) => key }),
}));

function LocationProbe() {
  return <span data-testid="location">{useLocation().pathname}</span>;
}

function setViewportWidth(width: number) {
  Object.defineProperty(window, "innerWidth", {
    configurable: true,
    writable: true,
    value: width,
  });
}

function WorkspaceProbe() {
  const workspace = useWorkspace();
  return (
    <div>
      <span data-testid="sidebar">
        {workspace.sidebarExpanded ? "expanded" : "collapsed"}
      </span>
      <span data-testid="command">
        {workspace.commandOpen ? "open" : "closed"}
      </span>
      <span data-testid="inspector">
        {workspace.inspector?.title ?? "empty"}
      </span>
      <button
        type="button"
        onClick={() => workspace.setSidebarExpanded(!workspace.sidebarExpanded)}
      >
        toggle sidebar
      </button>
      <button
        type="button"
        onClick={() =>
          workspace.openInspector({ title: "Resource", body: <p>Details</p> })
        }
      >
        open inspector
      </button>
      <button type="button" onClick={() => workspace.closeInspector()}>
        close inspector
      </button>
      <input aria-label="search" />
    </div>
  );
}

function FocusSafetyProbe() {
  const workspace = useWorkspace();
  const [showTrigger, setShowTrigger] = useState(true);
  const newInspectorFocus = useRef<HTMLButtonElement>(null);
  return (
    <div>
      {showTrigger && (
        <button
          type="button"
          onClick={() =>
            workspace.openInspector({ title: "First", body: <p>First</p> })
          }
        >
          first trigger
        </button>
      )}
      <button
        type="button"
        onClick={() => {
          setShowTrigger(false);
          workspace.closeInspector();
        }}
      >
        remove trigger
      </button>
      <button
        type="button"
        onClick={() => {
          workspace.closeInspector();
          workspace.openInspector({ title: "Second", body: <p>Second</p> });
          newInspectorFocus.current?.focus();
        }}
      >
        replace inspector
      </button>
      <button type="button" ref={newInspectorFocus}>
        new inspector focus
      </button>
      <span data-testid="focus-safety-inspector">
        {workspace.inspector?.title ?? "empty"}
      </span>
    </div>
  );
}

describe("Codex workspace shell", () => {
  beforeEach(() => {
    localStorage.clear();
    activeConnectionMock.connections = [];
    activeConnectionMock.activeConnectionID = "";
    activeConnectionMock.setActiveConnectionID.mockReset();
    activeConnectionMock.error = null;
    activeConnectionMock.retry.mockReset();
    cleanupSelectionMock.requestConnectionChange.mockReset();
    setViewportWidth(1024);
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
    Object.defineProperty(Element.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });
    Object.defineProperty(HTMLElement.prototype, "hasPointerCapture", {
      configurable: true,
      value: vi.fn(() => false),
    });
    Object.defineProperty(HTMLElement.prototype, "setPointerCapture", {
      configurable: true,
      value: vi.fn(),
    });
    Object.defineProperty(HTMLElement.prototype, "releasePointerCapture", {
      configurable: true,
      value: vi.fn(),
    });
  });

  afterEach(() => vi.unstubAllGlobals());

  it("contains every stable application route", () => {
    expect(navigationItems.map((item) => item.to)).toEqual([
      "/panorama",
      "/assets",
      "/scans",
      "/cleanup",
      "/findings",
      "/audits",
      "/settings",
    ]);
  });

  it("groups resources separately from operations and omits governance", () => {
    render(
      <MemoryRouter initialEntries={["/assets"]}>
        <SidebarProvider>
          <AppSidebar />
        </SidebarProvider>
      </MemoryRouter>,
    );

    expect(screen.queryByText("nav.group.workspace")).not.toBeInTheDocument();
    expect(screen.queryByText("nav.group.governance")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "nav.findings" }),
    ).not.toBeInTheDocument();
    const resourcesGroup = screen
      .getByText("nav.group.resources")
      .closest('[data-slot="sidebar-group"]');
    const operationsGroup = screen
      .getByText("nav.group.operations")
      .closest('[data-slot="sidebar-group"]');

    expect(resourcesGroup).toContainElement(
      screen.getByRole("link", { name: "nav.panorama" }),
    );
    expect(resourcesGroup).toContainElement(
      screen.getByRole("link", { name: "nav.assets" }),
    );
    expect(
      [...(resourcesGroup?.querySelectorAll("a") ?? [])].map((link) =>
        link.textContent?.trim(),
      ),
    ).toEqual(["nav.panorama", "nav.assets"]);
    expect(operationsGroup).toContainElement(
      screen.getByRole("link", { name: "nav.scans" }),
    );
    expect(operationsGroup).toContainElement(
      screen.getByRole("link", { name: "nav.cleanup" }),
    );
    expect(operationsGroup).toContainElement(
      screen.getByRole("link", { name: "nav.audit" }),
    );
    expect(
      [...(operationsGroup?.querySelectorAll("a") ?? [])].map((link) =>
        link.textContent?.trim(),
      ),
    ).toEqual(["nav.scans", "nav.cleanup", "nav.audit"]);
  });

  it("places cleanup selection inside the active-connection boundary", () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(screen.getByTestId("active-connection-provider")).toContainElement(
      screen.getByTestId("cleanup-selection-provider"),
    );
  });

  it("guards sidebar cloud-connection changes through cleanup selection", async () => {
    activeConnectionMock.connections = [
      {
        id: "connection-a",
        name: "Connection A",
        provider: "alicloud",
        status: "active",
      },
      {
        id: "connection-b",
        name: "Connection B",
        provider: "aws",
        status: "active",
      },
    ];
    activeConnectionMock.activeConnectionID = "connection-a";
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(
      screen.getByRole("combobox", { name: "connectionContext.label" }),
    );
    await user.click(
      screen.getByRole("option", { name: "Connection B · aws" }),
    );

    expect(cleanupSelectionMock.requestConnectionChange).toHaveBeenCalledWith(
      "connection-b",
    );
    expect(activeConnectionMock.setActiveConnectionID).not.toHaveBeenCalled();
  });

  it("guards command-menu cloud-connection changes through cleanup selection", async () => {
    activeConnectionMock.connections = [
      {
        id: "connection-a",
        name: "Connection A",
        provider: "alicloud",
        status: "active",
      },
      {
        id: "connection-b",
        name: "Connection B",
        provider: "aws",
        status: "active",
      },
    ];
    activeConnectionMock.activeConnectionID = "connection-a";
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("button", { name: "command.open" }));
    await user.click(screen.getByRole("option", { name: /Connection B/ }));

    expect(cleanupSelectionMock.requestConnectionChange).toHaveBeenCalledWith(
      "connection-b",
    );
    expect(activeConnectionMock.setActiveConnectionID).not.toHaveBeenCalled();
  });

  it("places the desktop sidebar toggle in the sidebar header", async () => {
    const view = render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    const desktopToggle = await screen.findByRole("button", {
      name: "shell.collapseSidebar",
    });
    expect(
      desktopToggle.closest('[data-slot="sidebar-header"]'),
    ).not.toBeNull();
    expect(desktopToggle.closest("header")).toBeNull();
    expect(document.querySelector('[data-slot="sidebar-rail"]')).toBeNull();

    view.unmount();
    setViewportWidth(768);
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      const mobileToggle = screen.getByRole("button", {
        name: "shell.toggleSidebar",
      });
      expect(mobileToggle.closest("header")).not.toBeNull();
      expect(
        document.querySelector(
          '[data-slot="sidebar-header"] [data-slot="sidebar-trigger"]',
        ),
      ).toBeNull();
    });
  });

  it("places page actions in the global header while keeping status in content", async () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route
                path="/settings"
                element={
                  <PageToolbar
                    label="Task actions"
                    leading={<span>Ready</span>}
                    trailing={<button>Execute</button>}
                  />
                }
              />
            </Route>
          </Routes>
        </ThemeProvider>
      </MemoryRouter>,
    );

    const action = await screen.findByRole("button", { name: "Execute" });
    expect(action.closest("header")).not.toBeNull();
    expect(
      screen.getByRole("toolbar", { name: "common.actions" }),
    ).toContainElement(action);
    expect(
      screen.getByText("Ready").closest('[data-slot="page-toolbar"]'),
    ).not.toBeNull();
  });

  it("keeps search in the sidebar and removes color controls", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    const search = await screen.findByRole("button", { name: "command.open" });
    expect(search.closest('[data-slot="sidebar-header"]')).not.toBeNull();
    expect(search.closest("header")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "theme.toggle" }),
    ).not.toBeInTheDocument();

    await user.click(search);
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(screen.queryByText("command.appearance")).not.toBeInTheDocument();
    expect(screen.queryByText("theme.light")).not.toBeInTheDocument();
    expect(screen.queryByText("theme.dark")).not.toBeInTheDocument();
    expect(screen.queryByText("theme.system")).not.toBeInTheDocument();
  });

  it("labels sidebar header icons on hover", async () => {
    const user = userEvent.setup();
    const view = render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    const search = await screen.findByRole("button", {
      name: "command.open",
    });
    await user.hover(search);
    expect(
      await screen.findByText("command.open", {
        selector: '[data-slot="tooltip-content"]',
      }),
    ).toBeVisible();

    view.unmount();
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    const toggle = await screen.findByRole("button", {
      name: "shell.collapseSidebar",
    });
    await user.hover(toggle);
    expect(
      await screen.findByText("shell.collapseSidebar", {
        selector: '[data-slot="tooltip-content"]',
      }),
    ).toBeVisible();
  });

  it("retries the active connection query from its error state", async () => {
    activeConnectionMock.error = new Error("connection unavailable");
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/assets"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(screen.getByText("connection unavailable")).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: "shell.connectionError",
      }),
    ).toBeVisible();
    await user.click(screen.getByRole("button", { name: "shell.retry" }));
    expect(activeConnectionMock.retry).toHaveBeenCalledOnce();
  });

  it("keeps the connection gate subordinate to the application title", () => {
    render(
      <MemoryRouter initialEntries={["/assets"]}>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "nav.assets" }),
    ).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: "connectionContext.requiredTitle",
      }),
    ).toBeVisible();
  });

  it("persists the desktop sidebar preference", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <WorkspaceProbe />
      </WorkspaceProvider>,
    );

    expect(screen.getByTestId("sidebar")).toHaveTextContent("expanded");
    await user.click(screen.getByRole("button", { name: "toggle sidebar" }));

    expect(screen.getByTestId("sidebar")).toHaveTextContent("collapsed");
    expect(localStorage.getItem("steward.sidebar-expanded")).toBe("false");
  });

  it("preserves the controlled sidebar provider contract", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(
      <SidebarProvider open onOpenChange={onOpenChange}>
        <Sidebar collapsible="icon">
          <SidebarTrigger aria-label="toggle controlled sidebar" />
        </Sidebar>
      </SidebarProvider>,
    );

    await user.click(
      screen.getByRole("button", { name: "toggle controlled sidebar" }),
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("closes the mobile sidebar after ordinary navigation", async () => {
    setViewportWidth(768);
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/assets"]}>
        <SidebarProvider>
          <AppSidebar />
          <SidebarTrigger aria-label="open mobile navigation" />
          <LocationProbe />
        </SidebarProvider>
      </MemoryRouter>,
    );

    await user.click(
      screen.getByRole("button", { name: "open mobile navigation" }),
    );
    expect(screen.getByRole("dialog")).toBeVisible();
    await user.click(screen.getByRole("link", { name: "nav.scans" }));

    expect(screen.getByTestId("location")).toHaveTextContent("/scans");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("closes the mobile sidebar after navigating to settings from the user menu", async () => {
    setViewportWidth(768);
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/assets"]}>
        <SidebarProvider>
          <AppSidebar />
          <SidebarTrigger aria-label="open mobile navigation" />
          <LocationProbe />
        </SidebarProvider>
      </MemoryRouter>,
    );

    await user.click(
      screen.getByRole("button", { name: "open mobile navigation" }),
    );
    expect(screen.getByRole("dialog")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "local-admin" }));
    await user.click(screen.getByRole("menuitem", { name: "nav.settings" }));

    expect(screen.getByTestId("location")).toHaveTextContent("/settings");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("opens with Ctrl+K but ignores shortcuts while typing", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <WorkspaceProbe />
      </WorkspaceProvider>,
    );

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(screen.getByTestId("command")).toHaveTextContent("open");

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.getByTestId("command")).toHaveTextContent("closed");

    await user.click(screen.getByRole("textbox", { name: "search" }));
    fireEvent.keyDown(screen.getByRole("textbox", { name: "search" }), {
      key: "k",
      ctrlKey: true,
    });
    expect(screen.getByTestId("command")).toHaveTextContent("closed");
  });

  it("opens an inspector and restores focus to the originating control", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <WorkspaceProbe />
      </WorkspaceProvider>,
    );

    const trigger = screen.getByRole("button", { name: "open inspector" });
    await user.click(trigger);
    expect(screen.getByTestId("inspector")).toHaveTextContent("Resource");
    await user.click(screen.getByRole("button", { name: "close inspector" }));
    expect(screen.getByTestId("inspector")).toHaveTextContent("empty");
    expect(trigger).toHaveFocus();
  });

  it("does not focus a disconnected inspector trigger", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <FocusSafetyProbe />
      </WorkspaceProvider>,
    );

    const trigger = screen.getByRole("button", { name: "first trigger" });
    await user.click(trigger);
    const focus = vi.spyOn(trigger, "focus");
    focus.mockClear();
    await user.click(screen.getByRole("button", { name: "remove trigger" }));
    await Promise.resolve();

    expect(trigger.isConnected).toBe(false);
    expect(focus).not.toHaveBeenCalled();
  });

  it("does not let an old close steal focus from a replacement inspector", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <FocusSafetyProbe />
      </WorkspaceProvider>,
    );

    await user.click(screen.getByRole("button", { name: "first trigger" }));
    const replacement = screen.getByRole("button", {
      name: "replace inspector",
    });
    replacement.focus();
    fireEvent.click(replacement);
    await Promise.resolve();

    expect(screen.getByTestId("focus-safety-inspector")).toHaveTextContent(
      "Second",
    );
    expect(
      screen.getByRole("button", { name: "new inspector focus" }),
    ).toHaveFocus();
    expect(replacement).not.toHaveFocus();
  });
});
