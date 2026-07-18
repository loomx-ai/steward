import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { CanvasToolbar, type CanvasToolbarProps } from "./CanvasToolbar";

const propsWithoutCandidateClearing: Omit<CanvasToolbarProps, "onEscape"> = {
  mode: "select",
  zoom: 1,
  onZoomChange: () => {},
  onModeChange: () => {},
  onZoomOut: () => {},
  onFitView: () => {},
  onZoomIn: () => {},
};

// @ts-expect-error Escape must always have a single canvas-layer owner.
const propsRequiringEscapeOwner: CanvasToolbarProps =
  propsWithoutCandidateClearing;
void propsRequiringEscapeOwner;

beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

function renderToolbar(
  props: Partial<React.ComponentProps<typeof CanvasToolbar>> = {},
) {
  const onModeChange = vi.fn();
  const onEscape = vi.fn();
  render(
    <TooltipProvider>
      <LocaleProvider>
        <CanvasToolbar
          mode="select"
          zoom={1}
          onZoomChange={() => {}}
          onModeChange={onModeChange}
          onEscape={onEscape}
          onZoomOut={() => {}}
          onFitView={() => {}}
          onZoomIn={() => {}}
          {...props}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );
  return { onModeChange, onEscape };
}

it("renders select and pan modes before the three zoom controls", () => {
  localStorage.setItem("steward.locale", "en-US");
  renderToolbar();

  const toolbar = screen.getByRole("toolbar");
  expect(toolbar).toHaveAttribute("aria-label", "Canvas tools");
  expect(toolbar).toHaveClass("bottom-3", "left-3");
  expect(toolbar).not.toHaveClass("top-3", "right-3");
  expect(screen.getAllByRole("button")).toHaveLength(5);
  expect(screen.getAllByRole("separator")).toHaveLength(1);
  expect(toolbar).toHaveTextContent("%");
  expect(toolbar.querySelector("[data-canvas-zoom-controls]")).toHaveClass(
    "gap-1",
  );
  expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(screen.getByRole("button", { name: "Pan" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  expect(
    screen.queryByRole("button", { name: "Box select" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Zoom out" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Fit view" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Zoom in" })).toBeVisible();
  expect(screen.getByLabelText("Zoom level")).toHaveValue("100");
  expect(screen.getByLabelText("Zoom level")).toHaveStyle({ width: "3ch" });
});

it("formats the current zoom as a compact percentage", () => {
  localStorage.setItem("steward.locale", "en-US");
  const { rerender } = render(
    <TooltipProvider>
      <LocaleProvider>
        <CanvasToolbar
          mode="select"
          zoom={1.256}
          onZoomChange={() => {}}
          onModeChange={() => {}}
          onEscape={() => {}}
          onZoomOut={() => {}}
          onFitView={() => {}}
          onZoomIn={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );

  expect(screen.getByLabelText("Zoom level")).toHaveValue("126");
  rerender(
    <TooltipProvider>
      <LocaleProvider>
        <CanvasToolbar
          mode="select"
          zoom={0.045}
          onZoomChange={() => {}}
          onModeChange={() => {}}
          onEscape={() => {}}
          onZoomOut={() => {}}
          onFitView={() => {}}
          onZoomIn={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );
  expect(screen.getByLabelText("Zoom level")).toHaveValue("4.5");
});

it("applies an edited percentage on Enter and cancels with Escape", async () => {
  localStorage.setItem("steward.locale", "en-US");
  const user = userEvent.setup();
  const onZoomChange = vi.fn();
  renderToolbar({ zoom: 0.72, onZoomChange });
  const input = screen.getByLabelText("Zoom level");

  await user.clear(input);
  await user.type(input, "85{Enter}");
  expect(onZoomChange).toHaveBeenCalledWith(0.85);

  await user.click(input);
  await user.clear(input);
  await user.type(input, "40{Escape}");
  expect(onZoomChange).toHaveBeenCalledOnce();
  expect(input).toHaveValue("72");
});

it("shows localized tooltips on hover and keyboard focus", async () => {
  localStorage.setItem("steward.locale", "en-US");
  const user = userEvent.setup();
  renderToolbar();

  const select = screen.getByRole("button", { name: "Select" });
  await user.hover(select);
  expect(await screen.findByRole("tooltip")).toHaveTextContent("Select V");
  await user.unhover(select);
  select.focus();
  expect(await screen.findByRole("tooltip")).toHaveTextContent("Select V");
});

it("exposes relationship lines as an optional pressed-state control", async () => {
  localStorage.setItem("steward.locale", "en-US");
  const user = userEvent.setup();
  const onRelationshipsVisibilityChange = vi.fn();
  const { rerender } = render(
    <TooltipProvider>
      <LocaleProvider>
        <CanvasToolbar
          mode="select"
          zoom={1}
          onZoomChange={() => {}}
          onModeChange={() => {}}
          relationshipsVisible={false}
          onRelationshipsVisibilityChange={onRelationshipsVisibilityChange}
          onEscape={() => {}}
          onZoomOut={() => {}}
          onFitView={() => {}}
          onZoomIn={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );

  const show = screen.getByRole("button", {
    name: "Show relationship lines",
  });
  expect(show).toHaveAttribute("aria-pressed", "false");
  await user.click(show);
  expect(onRelationshipsVisibilityChange).toHaveBeenCalledWith(true);

  rerender(
    <TooltipProvider>
      <LocaleProvider>
        <CanvasToolbar
          mode="select"
          zoom={1}
          onZoomChange={() => {}}
          onModeChange={() => {}}
          relationshipsVisible
          onRelationshipsVisibilityChange={onRelationshipsVisibilityChange}
          onEscape={() => {}}
          onZoomOut={() => {}}
          onFitView={() => {}}
          onZoomIn={() => {}}
        />
      </LocaleProvider>
    </TooltipProvider>,
  );
  expect(
    screen.getByRole("button", { name: "Hide relationship lines" }),
  ).toHaveAttribute("aria-pressed", "true");
});

it("uses mode shortcuts and delegates Escape once", () => {
  localStorage.setItem("steward.locale", "en-US");
  const { onModeChange, onEscape } = renderToolbar();

  fireEvent.keyDown(window, { key: "v" });
  fireEvent.keyDown(window, { key: "B" });
  fireEvent.keyDown(window, { key: "h" });
  fireEvent.keyDown(window, { key: "Escape" });

  expect(onModeChange.mock.calls).toEqual([["select"], ["pan"]]);
  expect(onEscape).toHaveBeenCalledTimes(1);
});

it("does not use shortcuts while an editable element has focus", () => {
  localStorage.setItem("steward.locale", "en-US");
  const { onModeChange, onEscape } = renderToolbar();
  const input = document.createElement("input");
  const textarea = document.createElement("textarea");
  const select = document.createElement("select");
  const contentEditable = document.createElement("div");
  contentEditable.contentEditable = "true";
  document.body.append(input, textarea, select, contentEditable);

  for (const element of [input, textarea, select, contentEditable]) {
    element.focus();
    fireEvent.keyDown(element, { key: "b" });
    fireEvent.keyDown(element, { key: "Escape" });
  }

  expect(onModeChange).not.toHaveBeenCalled();
  expect(onEscape).not.toHaveBeenCalled();
  input.remove();
  textarea.remove();
  select.remove();
  contentEditable.remove();
});

it("allows shortcuts from an explicitly non-editable content element", () => {
  localStorage.setItem("steward.locale", "en-US");
  const { onModeChange, onEscape } = renderToolbar();
  const nonEditable = document.createElement("div");
  nonEditable.contentEditable = "false";
  document.body.append(nonEditable);

  nonEditable.focus();
  fireEvent.keyDown(nonEditable, { key: "h" });
  fireEvent.keyDown(nonEditable, { key: "Escape" });

  expect(onModeChange).toHaveBeenCalledWith("pan");
  expect(onEscape).toHaveBeenCalledOnce();
  nonEditable.remove();
});
