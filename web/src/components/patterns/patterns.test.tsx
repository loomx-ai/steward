import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { WorkspaceProvider, useWorkspace } from "@/app/WorkspaceContext";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { DetailSection } from "./DetailSection";
import { InspectorSheet } from "./InspectorSheet";
import { PageLayout } from "./PageLayout";
import { PageToolbar } from "./PageToolbar";
import { RowActions } from "./RowActions";
import { SettingsGroup, SettingsRow } from "./SettingsList";
import { Toolbar } from "./Toolbar";

describe("ChatGPT-style layout patterns", () => {
  it.each(["reading", "list", "canvas"] as const)(
    "lets %s pages fill the workspace width",
    (mode) => {
      render(<PageLayout mode={mode}>content</PageLayout>);
      const layout = screen.getByText("content");
      expect(layout).toHaveAttribute("data-layout-mode", mode);
      expect(layout).toHaveClass("w-full");
    },
  );

  it.each(["reading", "list"] as const)(
    "left aligns %s pages inside the workspace",
    (mode) => {
      render(<PageLayout mode={mode}>content</PageLayout>);
      const layout = screen.getByText("content");
      expect(layout).not.toHaveClass("mx-auto");
      expect(layout).not.toHaveClass("max-w-[920px]");
      expect(layout).not.toHaveClass("max-w-[1120px]");
    },
  );

  it("renders compact page toolbars and flat table shells", () => {
    render(
      <PageLayout mode="list">
        <PageToolbar
          label="Task actions"
          leading={<span>Ready</span>}
          trailing={<button>Execute</button>}
        />
        <DataTableShell toolbar={<span>Filters</span>}>Rows</DataTableShell>
      </PageLayout>,
    );
    expect(screen.getByRole("toolbar", { name: "Task actions" })).toBeVisible();
    expect(screen.getByText("Ready")).toBeVisible();
    expect(screen.getByRole("button", { name: "Execute" })).toBeVisible();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    expect(screen.getByText("Rows").closest("[data-slot='card']")).toBeNull();
    const shell = screen
      .getByText("Rows")
      .closest<HTMLElement>("[data-slot='data-table-shell']");
    expect(shell).not.toHaveClass("rounded-xl");
    expect(shell).not.toHaveClass("border");
    expect(shell).not.toHaveClass("bg-background");
    expect(screen.getByText("Filters").parentElement).toHaveClass(
      "border-b",
      "px-2",
      "py-3",
    );
  });

  it("keeps toolbar, sections, and row actions semantically reachable", () => {
    render(
      <>
        <Toolbar label="Filters">filter</Toolbar>
        <DetailSection title="Identity">facts</DetailSection>
        <div className="group">
          <RowActions>
            <button>More</button>
          </RowActions>
        </div>
      </>,
    );
    expect(screen.getByRole("toolbar", { name: "Filters" })).toBeVisible();
    expect(screen.getByRole("region", { name: "Identity" })).toBeVisible();
    const moreButton = screen.getByRole("button", { name: "More" });
    expect(moreButton).toBeVisible();
    expect(moreButton.closest("[data-slot='row-actions']")).toHaveClass(
      "opacity-100",
      "md:opacity-0",
      "md:group-hover:opacity-100",
      "md:group-focus-within:opacity-100",
    );
  });

  it("renders inspector content in a dismissible sheet", async () => {
    function Probe() {
      const { openInspector } = useWorkspace();
      return (
        <button
          onClick={() => openInspector({ title: "Asset", body: <p>Facts</p> })}
        >
          Inspect
        </button>
      );
    }
    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <Probe />
        <InspectorSheet />
      </WorkspaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Inspect" }));
    expect(screen.getByRole("dialog", { name: "Asset" })).not.toHaveAttribute(
      "aria-describedby",
    );
    expect(screen.getAllByText("Asset")).toHaveLength(1);
    expect(screen.getByRole("dialog")).toHaveTextContent("Facts");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Inspect" })).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Inspect" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Inspect" })).toHaveFocus();
  });

  it("restores inspector focus to a replacement of the original trigger", async () => {
    function Probe() {
      const { openInspector } = useWorkspace();
      const [generation, setGeneration] = useState(0);
      return (
        <button
          key={generation}
          data-inspector-trigger="asset-1"
          onClick={(event) => {
            event.currentTarget.focus();
            openInspector(
              { title: "Asset", body: <p>Facts</p> },
              {
                resolveReturnFocus: () =>
                  document.querySelector<HTMLButtonElement>(
                    '[data-inspector-trigger="asset-1"]',
                  ),
              },
            );
            setGeneration((current) => current + 1);
          }}
        >
          Inspect replacement {generation}
        </button>
      );
    }

    const user = userEvent.setup();
    render(
      <WorkspaceProvider>
        <Probe />
        <InspectorSheet />
      </WorkspaceProvider>,
    );

    await user.click(
      screen.getByRole("button", { name: "Inspect replacement 0" }),
    );
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(
      screen.getByRole("button", { name: "Inspect replacement 1" }),
    ).toHaveFocus();

    await user.click(
      screen.getByRole("button", { name: "Inspect replacement 1" }),
    );
    await user.keyboard("{Escape}");
    expect(
      screen.getByRole("button", { name: "Inspect replacement 2" }),
    ).toHaveFocus();
  });

  it("groups settings as rows instead of cards", () => {
    render(
      <SettingsGroup title="General">
        <SettingsRow
          label="Appearance"
          description="Use system theme"
          control={<button>System</button>}
        />
        <SettingsRow label="Language" control={<button>Auto</button>} />
      </SettingsGroup>,
    );
    expect(screen.getByRole("group", { name: "General" })).toBeVisible();
    expect(screen.getAllByTestId("settings-row")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "System" })).toBeVisible();
  });

  it("keeps long account values inside a narrow settings row", () => {
    const principal = "principal-without-breaks-".repeat(12);
    const role = "single-unbroken-role-".repeat(12);
    render(
      <>
        <SettingsRow label="Actor" control={<span>{principal}</span>} />
        <SettingsRow label="Capabilities" control={<span>{role}</span>} />
      </>,
    );

    const actorRow = screen
      .getByText("Actor")
      .closest("[data-testid='settings-row']");
    expect(actorRow).toHaveClass(
      "flex-col",
      "items-stretch",
      "sm:flex-row",
      "sm:items-center",
    );
    expect(screen.getByText(principal).parentElement).toHaveClass(
      "min-w-0",
      "max-w-full",
      "self-end",
      "break-words",
      "text-right",
      "sm:max-w-[60%]",
    );
    expect(screen.getByText(role)).toBeVisible();
  });
});
