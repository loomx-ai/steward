import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Probe() {
  const { resolvedTheme, setTheme, theme } = useTheme();
  return (
    <div>
      <span>{`${theme}:${resolvedTheme}`}</span>
      <button type="button" onClick={() => setTheme("dark")}>
        dark
      </button>
    </div>
  );
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.className = "";
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

it("defaults to the system theme and persists an explicit choice", async () => {
  render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );

  expect(screen.getByText("system:light")).toBeVisible();
  expect(document.documentElement).toHaveClass("light");

  await userEvent.click(screen.getByRole("button", { name: "dark" }));

  expect(screen.getByText("dark:dark")).toBeVisible();
  expect(document.documentElement).toHaveClass("dark");
  expect(localStorage.getItem("steward.theme")).toBe("dark");
});
