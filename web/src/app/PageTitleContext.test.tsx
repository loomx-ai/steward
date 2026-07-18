import { useState, type ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SidebarProvider } from "@/components/ui/sidebar";
import { AppHeader } from "./AppHeader";
import { PageTitle, PageTitleProvider } from "./PageTitleContext";
import { ThemeProvider } from "./ThemeProvider";
import { WorkspaceProvider } from "./WorkspaceContext";

vi.mock("@/i18n/LocaleProvider", () => ({
  useLocale: () => ({ t: (key: string) => key }),
}));

function Providers({ path, children }: { path: string; children?: ReactNode }) {
  return (
    <MemoryRouter initialEntries={[path]}>
      <ThemeProvider>
        <WorkspaceProvider>
          <PageTitleProvider>
            <SidebarProvider>
              <AppHeader />
              {children}
            </SidebarProvider>
          </PageTitleProvider>
        </WorkspaceProvider>
      </ThemeProvider>
    </MemoryRouter>
  );
}

function DynamicTitleProbe() {
  const [visible, setVisible] = useState(true);
  return (
    <>
      {visible && (
        <PageTitle
          title="production-api"
          parent={{ label: "nav.assets", to: "/assets" }}
        />
      )}
      <button type="button" onClick={() => setVisible(false)}>
        clear title
      </button>
    </>
  );
}

describe("application page title", () => {
  beforeEach(() => {
    localStorage.clear();
    document.title = "Steward";
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
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.title = "Steward";
  });

  it("uses the static navigation title without a breadcrumb", async () => {
    render(<Providers path="/settings" />);

    const header = screen.getByRole("banner");
    expect(
      screen.getByRole("heading", { level: 1, name: "nav.settings" }),
    ).toBeVisible();
    expect(
      header.querySelector('[data-slot="page-header-navigation"]'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("navigation", { name: "shell.breadcrumb" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("nav.settings · Steward"));
  });

  it("makes the Resource Panorama title the account-root action", async () => {
    const user = userEvent.setup();
    const reset = vi.fn();
    window.addEventListener("steward:panorama-root", reset);

    render(<Providers path="/panorama" />);

    const heading = screen.getByRole("heading", {
      level: 1,
      name: "nav.panorama",
    });
    const titleButton = within(heading).getByRole("button", {
      name: "nav.panorama",
    });
    expect(heading).toHaveClass("font-semibold");
    expect(heading).not.toHaveClass("text-muted-foreground");
    expect(titleButton).toHaveAttribute("aria-current", "page");
    expect(titleButton).toHaveClass(
      "h-7",
      "px-0.5",
      "transition-colors",
      "hover:text-foreground",
      "focus-visible:ring-2",
    );
    await user.click(titleButton);
    expect(reset).toHaveBeenCalledOnce();

    window.removeEventListener("steward:panorama-root", reset);
  });

  it("keeps the Resource Panorama title as the root action on a deep panorama route", async () => {
    const user = userEvent.setup();
    const reset = vi.fn();
    window.addEventListener("steward:panorama-root", reset);

    render(
      <Providers path="/panorama/regions/cn-hangzhou/vpcs/vpc-production" />,
    );

    const heading = screen.getByRole("heading", {
      level: 1,
      name: "nav.panorama",
    });
    expect(heading).toHaveClass("font-normal", "text-muted-foreground");
    const titleButton = within(heading).getByRole("button", {
      name: "nav.panorama",
    });
    expect(titleButton).not.toHaveAttribute("aria-current");
    expect(titleButton).toHaveClass(
      "h-7",
      "px-0.5",
      "text-muted-foreground",
      "hover:text-foreground",
      "focus-visible:ring-2",
    );
    await user.click(titleButton);
    expect(reset).toHaveBeenCalledOnce();

    window.removeEventListener("steward:panorama-root", reset);
  });

  it("shows clickable parent and current levels in one detail breadcrumb", async () => {
    render(
      <Providers path="/assets/asset-1">
        <PageTitle
          title="production-api"
          parent={{ label: "nav.assets", to: "/assets" }}
        />
      </Providers>,
    );

    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "production-api",
      }),
    ).toBeVisible();
    const breadcrumb = screen.getByRole("navigation", {
      name: "shell.breadcrumb",
    });
    const parent = screen.getByRole("link", { name: "nav.assets" });
    expect(breadcrumb).toContainElement(parent);
    expect(parent).toHaveAttribute("href", "/assets");
    expect(parent).toHaveClass(
      "h-7",
      "px-0.5",
      "text-muted-foreground",
      "hover:text-foreground",
      "active:text-foreground",
      "focus-visible:ring-2",
    );
    const current = within(breadcrumb).getByRole("link", {
      name: "production-api",
    });
    expect(current).toHaveAttribute("href", "/assets/asset-1");
    expect(current).toHaveAttribute("aria-current", "page");
    expect(current).toHaveClass(
      "h-7",
      "px-0.5",
      "text-muted-foreground",
      "hover:text-foreground",
      "active:text-foreground",
      "focus-visible:ring-2",
    );
    const breadcrumbList = breadcrumb.querySelector(
      '[data-slot="breadcrumb-list"]',
    );
    expect(breadcrumbList).toHaveClass("gap-0");
    const separator = breadcrumb.querySelector(
      '[data-slot="breadcrumb-separator"]',
    );
    expect(separator).toHaveClass("shrink-0", "text-muted-foreground");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    await waitFor(() =>
      expect(document.title).toBe("production-api · Steward"),
    );
  });

  it("restores the route title when a dynamic registration unmounts", async () => {
    const user = userEvent.setup();
    render(
      <Providers path="/assets/asset-1">
        <DynamicTitleProbe />
      </Providers>,
    );

    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "production-api",
      }),
    ).toBeVisible();
    await user.click(screen.getByRole("button", { name: "clear title" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "nav.assets" }),
    ).toBeVisible();
    expect(
      screen.queryByRole("navigation", { name: "shell.breadcrumb" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("nav.assets · Steward"));
  });
});
