import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./select";

beforeEach(() => {
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
});

afterEach(() => vi.unstubAllGlobals());

it("uses the shared standard width by default", () => {
  render(
    <Select>
      <SelectTrigger aria-label="Filter">
        <SelectValue placeholder="All" />
      </SelectTrigger>
    </Select>,
  );

  expect(screen.getByRole("combobox", { name: "Filter" })).toHaveClass("w-40");
});

it("allows container-bound selects to override the standard width", () => {
  render(
    <Select>
      <SelectTrigger className="w-full" aria-label="Form field">
        <SelectValue placeholder="Choose" />
      </SelectTrigger>
    </Select>,
  );

  const trigger = screen.getByRole("combobox", { name: "Form field" });
  expect(trigger).toHaveClass("w-full");
  expect(trigger).not.toHaveClass("w-40");
});

it("opens below the trigger instead of around the selected item by default", async () => {
  const view = render(
    <Select defaultOpen defaultValue="middle">
      <SelectTrigger aria-label="Language">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="first">Automatic</SelectItem>
        <SelectItem value="middle">Chinese</SelectItem>
        <SelectItem value="last">English</SelectItem>
      </SelectContent>
    </Select>,
  );

  expect(await screen.findByRole("listbox")).toHaveAttribute(
    "data-side",
    "bottom",
  );
  await act(async () => {
    view.unmount();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
});
