import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SidebarProvider } from "@/components/ui/sidebar";
import { SidebarUserMenu } from "./SidebarUserMenu";

beforeEach(() => {
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

afterEach(() => vi.unstubAllGlobals());

it("reveals settings and sign out from the account control", async () => {
  const signOut = vi.fn();
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <SidebarProvider>
        <SidebarUserMenu
          subject="local-admin"
          roles={["admin"]}
          automaticDevelopmentSession={false}
          onSignOut={signOut}
          labels={{
            settings: "Settings",
            signOut: "Sign out",
            identityPending: "Identity pending",
          }}
        />
      </SidebarProvider>
    </MemoryRouter>,
  );
  const trigger = screen.getByRole("button", { name: "local-admin" });
  await user.click(trigger);
  expect(trigger).toHaveAttribute("data-radix-popper-side", "top");
  expect(trigger).toHaveAttribute("data-radix-popper-align", "start");
  expect(screen.getByRole("menuitem", { name: "Settings" })).toBeVisible();
  await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
  expect(signOut).toHaveBeenCalledOnce();
});

it("keeps automatic development sessions signed in", async () => {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <SidebarProvider>
        <SidebarUserMenu
          subject="development"
          roles={[]}
          automaticDevelopmentSession
          onSignOut={vi.fn()}
          labels={{
            settings: "Settings",
            signOut: "Sign out",
            identityPending: "Identity pending",
          }}
        />
      </SidebarProvider>
    </MemoryRouter>,
  );
  await user.click(screen.getByRole("button", { name: "development" }));
  expect(screen.getAllByText("Identity pending")).toHaveLength(2);
  expect(
    screen.queryByRole("menuitem", { name: "Sign out" }),
  ).not.toBeInTheDocument();
});
