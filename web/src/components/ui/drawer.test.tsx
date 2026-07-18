import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  Drawer,
  DrawerBody,
  DrawerClose,
  DrawerContent,
  DrawerDescription,
  DrawerFooter,
  DrawerHeader,
  DrawerTitle,
  DrawerTrigger,
} from "./drawer";

describe("DrawerContent", () => {
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
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      setPointerCapture: { configurable: true, value: vi.fn() },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Reflect.deleteProperty(HTMLElement.prototype, "hasPointerCapture");
    Reflect.deleteProperty(HTMLElement.prototype, "releasePointerCapture");
    Reflect.deleteProperty(HTMLElement.prototype, "setPointerCapture");
  });

  it("scrolls only the body and restores focus after dismissal", async () => {
    const user = userEvent.setup();
    render(
      <Drawer>
        <DrawerTrigger>Open resource details</DrawerTrigger>
        <DrawerContent>
          <DrawerHeader>
            <DrawerTitle>Resource details</DrawerTitle>
            <DrawerDescription>Review the selected resource.</DrawerDescription>
          </DrawerHeader>
          <DrawerBody>
            <p>Scrollable facts</p>
          </DrawerBody>
          <DrawerFooter>
            <DrawerClose>Done</DrawerClose>
          </DrawerFooter>
        </DrawerContent>
      </Drawer>,
    );

    const trigger = screen.getByRole("button", {
      name: "Open resource details",
    });
    await user.click(trigger);
    expect(screen.getByRole("dialog")).toHaveClass(
      "data-[vaul-drawer-direction=bottom]:max-h-[85svh]",
      "min-h-0",
      "overflow-hidden",
    );
    expect(screen.getByText("Scrollable facts")).toBeVisible();
    const scrollArea = document.querySelector<HTMLElement>(
      '[data-slot="drawer-scroll-area"]',
    );
    expect(scrollArea).toHaveClass("min-h-0", "flex-1");
    expect(within(scrollArea!).getByText("Scrollable facts")).toBeVisible();
    expect(scrollArea).not.toContainElement(
      screen.getByRole("heading", { name: "Resource details" }),
    );
    expect(scrollArea).not.toContainElement(
      screen.getByRole("button", { name: "Done" }),
    );
    expect(
      scrollArea?.querySelector('[data-slot="scroll-area-viewport"]'),
    ).toHaveClass("size-full");

    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(trigger).toHaveFocus();
  });
});
