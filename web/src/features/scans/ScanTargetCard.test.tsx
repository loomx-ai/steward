import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import type { ScanTargetProgress } from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { ScanTargetCard } from "./ScanTargetCard";

const runningTarget = {
  key: "ap-northeast-1",
  kind: "region",
  region_id: "ap-northeast-1",
  region_name: "Japan (Tokyo)",
  status: "running",
  completed: 0,
  total: 1,
  resource_count: 0,
  error_count: 0,
  summary: "Completed 0/1",
} satisfies ScanTargetProgress;

it("shows a rotating status icon without a progress bar while running", () => {
  localStorage.setItem("steward.locale", "en-US");
  render(
    <TooltipProvider>
      <LocaleProvider>
        <ScanTargetCard
          target={runningTarget}
          selected={false}
          onSelect={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );

  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  const icon = screen
    .getByRole("img", { name: "Running" })
    .querySelector("svg");
  expect(icon).toHaveClass("animate-spin");
  expect(icon).not.toHaveClass("animate-pulse");
});

it.each([
  ["succeeded", "before:bg-success"],
  ["running", "before:bg-info"],
  ["waiting", "before:bg-warning"],
  ["failed", "before:bg-destructive"],
  ["unknown", "before:bg-muted-foreground"],
])("shows a %s state rail", (status, railClass) => {
  localStorage.setItem("steward.locale", "en-US");
  render(
    <TooltipProvider>
      <LocaleProvider>
        <ScanTargetCard
          target={{ ...runningTarget, status }}
          selected={false}
          onSelect={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );

  expect(screen.getByRole("button")).toHaveClass("before:w-[3px]", railClass);
});

it("shows the global target with the localized label", () => {
  localStorage.setItem("steward.locale", "zh-CN");
  render(
    <TooltipProvider>
      <LocaleProvider>
        <ScanTargetCard
          target={{
            ...runningTarget,
            key: "global",
            kind: "global",
            region_id: "global",
            region_name: "Global",
            name: "Global",
          }}
          selected={false}
          onSelect={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );

  expect(screen.getByText("全局")).toBeVisible();
  expect(screen.queryByText("Global")).not.toBeInTheDocument();
});
