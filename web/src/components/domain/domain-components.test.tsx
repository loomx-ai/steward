import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ActivityFeed } from "./ActivityFeed";
import { AsyncState } from "./AsyncState";
import { CursorPagination } from "./CursorPagination";
import { JsonViewer } from "./JsonViewer";
import { StateBadge, StateIcon } from "./StateBadge";
import { Timeline } from "./Timeline";

describe("domain components", () => {
  beforeEach(() => {
    Object.defineProperties(Element.prototype, {
      hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      setPointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    });
  });

  it("renders compact historical page numbers with ellipses", async () => {
    const user = userEvent.setup();
    const onPageSelect = vi.fn();

    render(
      <CursorPagination
        page={9}
        pageCount={20}
        hasNextPage
        pending={false}
        pageSize={20}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPageSelect={onPageSelect}
        onPageSizeChange={vi.fn()}
        labels={{
          page: (page) => `Page ${page}`,
          pageSize: "Items per page",
          previous: "Previous",
          next: "Next",
        }}
      />,
    );

    expect(screen.getAllByText("…")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Page 1" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Page 8" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Page 9" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("button", { name: "Page 10" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Page 20" })).toBeVisible();
    expect(screen.queryByText("Previous")).not.toBeInTheDocument();
    expect(screen.queryByText("Next")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Page 8" }));
    expect(onPageSelect).toHaveBeenCalledWith(8);
  });

  it("shows every historical page when the page count is small", () => {
    render(
      <CursorPagination
        page={2}
        pageCount={4}
        hasNextPage
        pending
        pageSize={20}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPageSelect={vi.fn()}
        onPageSizeChange={vi.fn()}
        labels={{
          page: (page) => `Page ${page}`,
          pageSize: "Items per page",
          previous: "Previous",
          next: "Next",
        }}
      />,
    );

    expect(screen.queryByText("…")).not.toBeInTheDocument();
    for (const page of [1, 2, 3, 4]) {
      expect(
        screen.getByRole("button", { name: `Page ${page}` }),
      ).toBeDisabled();
    }
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("lets people select the page size", async () => {
    const user = userEvent.setup();
    const onPageSizeChange = vi.fn();

    render(
      <CursorPagination
        page={1}
        pageCount={1}
        hasNextPage
        pending={false}
        pageSize={20}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPageSelect={vi.fn()}
        onPageSizeChange={onPageSizeChange}
        labels={{
          page: (page) => `Page ${page}`,
          pageSize: "Items per page",
          previous: "Previous",
          next: "Next",
        }}
      />,
    );

    const selector = screen.getByRole("combobox", {
      name: "Items per page",
    });
    expect(selector).toHaveTextContent("20");
    await user.click(selector);
    expect(screen.getByRole("option", { name: "20" })).toBeVisible();
    expect(screen.getByRole("option", { name: "50" })).toBeVisible();
    expect(screen.getByRole("option", { name: "100" })).toBeVisible();
    await user.click(screen.getByRole("option", { name: "50" }));
    expect(onPageSizeChange).toHaveBeenCalledWith(50);
  });

  it("keeps the page size selector when there is only one page", () => {
    const { container } = render(
      <CursorPagination
        page={1}
        pageCount={1}
        hasNextPage={false}
        pending={false}
        pageSize={20}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPageSelect={vi.fn()}
        onPageSizeChange={vi.fn()}
        labels={{
          page: (page) => `Page ${page}`,
          pageSize: "Items per page",
          previous: "Previous",
          next: "Next",
        }}
      />,
    );

    const selector = screen.getByRole("combobox", {
      name: "Items per page",
    });
    expect(selector).toHaveTextContent("20");
    expect(selector).toHaveClass("w-20");
    expect(container).not.toHaveTextContent("Items per page");
    expect(
      container.querySelector('[data-slot="cursor-pagination"]'),
    ).toHaveClass("justify-end");
    expect(screen.queryByRole("button", { name: "Previous" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
  });

  it("renders semantic state text with a status icon", () => {
    render(<StateBadge value="running" label="Running" />);
    expect(screen.getByText("Running")).toBeVisible();
    expect(screen.getByTestId("state-icon")).toBeInTheDocument();
    const status = screen.getByText("Running");
    expect(status).toHaveAttribute("data-slot", "state-badge");
    expect(status).not.toHaveClass("rounded-md");
    expect(status).not.toHaveClass("border");
    expect(status).not.toHaveClass("bg-info/15");
    expect(status).not.toHaveClass("px-2");
    expect(status).toHaveClass("text-info-foreground");
    expect(screen.getByTestId("state-icon")).toHaveClass(
      "animate-spin",
      "text-info",
    );
    expect(screen.getByTestId("state-icon")).not.toHaveClass("animate-pulse");
  });

  it("matches icon-only states to the bare semantic task icon", () => {
    render(
      <TooltipProvider>
        <StateIcon value="succeeded" label="Succeeded" />
      </TooltipProvider>,
    );

    const status = screen.getByRole("img", { name: "Succeeded" });
    expect(status).toHaveClass("size-4", "text-success");
    expect(status).not.toHaveClass("rounded-full", "bg-success/15");
    expect(status.querySelector("svg")).toHaveClass("size-3.5");
  });

  it("exposes a retry action for failed async content", async () => {
    const retry = vi.fn();
    const user = userEvent.setup();
    render(
      <AsyncState
        pending={false}
        error={new Error("Network unavailable")}
        empty={false}
        labels={{ empty: "No data", retry: "Retry" }}
        formatError={(error) => (error as Error).message}
        onRetry={retry}
      >
        content
      </AsyncState>,
    );
    expect(screen.getByText("Network unavailable")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("discloses formatted JSON on demand", async () => {
    const user = userEvent.setup();
    render(
      <JsonViewer
        value={{ request_id: "req-1" }}
        labels={{ show: "Show JSON", hide: "Hide JSON" }}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Show JSON" }));
    expect(screen.getByText(/"request_id": "req-1"/)).toBeVisible();
  });

  it("marks the active timeline item and formats activity times", () => {
    render(
      <>
        <Timeline
          items={[
            { id: "queued", title: "Queued", state: "complete" },
            { id: "running", title: "Running", state: "active" },
          ]}
        />
        <ActivityFeed
          items={[
            {
              id: "1",
              time: "2026-07-14T04:00:00Z",
              title: "Scan started",
              detail: "Worker accepted the job",
              tone: "info",
            },
          ]}
          formatTime={() => "12:00:00"}
        />
      </>,
    );
    expect(screen.getByText("Running").closest("li")).toHaveAttribute(
      "data-state",
      "active",
    );
    expect(screen.getByText("12:00:00")).toBeVisible();
  });
});
