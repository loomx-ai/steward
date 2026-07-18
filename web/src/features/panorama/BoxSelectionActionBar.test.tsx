import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { BoxSelectionActionBar } from "./BoxSelectionActionBar";

function renderActionBar(
  props: Partial<React.ComponentProps<typeof BoxSelectionActionBar>> = {},
) {
  const onAdd = vi.fn();
  const onCancel = vi.fn();
  render(
    <LocaleProvider>
      <BoxSelectionActionBar
        candidateKeys={["asset:a", "asset:b"]}
        onAdd={onAdd}
        onCancel={onCancel}
        {...props}
      />
    </LocaleProvider>,
  );
  return { onAdd, onCancel };
}

it("does not render when there are no temporary candidates", () => {
  localStorage.setItem("steward.locale", "en-US");
  renderActionBar({ candidateKeys: [] });

  expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
});

it("adds candidates then clears them on confirmation", async () => {
  localStorage.setItem("steward.locale", "en-US");
  const user = userEvent.setup();
  const { onAdd, onCancel } = renderActionBar();

  const toolbar = screen.getByRole("toolbar", {
    name: "Box selection actions",
  });
  expect(toolbar).toHaveClass("h-12", "gap-1.5", "px-2");
  expect(toolbar).toHaveTextContent("2 selected");
  await user.click(screen.getByRole("button", { name: "Add selected" }));

  expect(onAdd).toHaveBeenCalledWith(["asset:a", "asset:b"]);
  expect(onCancel).toHaveBeenCalledTimes(1);
  expect(onAdd.mock.invocationCallOrder[0]).toBeLessThan(
    onCancel.mock.invocationCallOrder[0],
  );
});

it("only clears candidates when canceled", async () => {
  localStorage.setItem("steward.locale", "en-US");
  const user = userEvent.setup();
  const { onAdd, onCancel } = renderActionBar();

  await user.click(screen.getByRole("button", { name: "Cancel" }));

  expect(onAdd).not.toHaveBeenCalled();
  expect(onCancel).toHaveBeenCalledTimes(1);
});
