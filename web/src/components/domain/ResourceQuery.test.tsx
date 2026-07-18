import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { ResourceKind } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { ResourceQueryInput } from "./ResourceQuery";

const kinds: ResourceKind[] = Array.from({ length: 12 }, (_, index) => ({
  id: `kind-${index}`,
  provider: "alicloud",
  native_type: `ACS::TEST::Resource${String(index).padStart(2, "0")}`,
  capabilities: [],
  display_name: `Test resource ${index}`,
  bundle_revision: "revision-a",
}));

it("scrolls the active suggestion into view during keyboard navigation", async () => {
  const scrollIntoView = vi.fn();
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: scrollIntoView,
  });
  const user = userEvent.setup();

  render(
    <LocaleProvider>
      <ResourceQueryInput
        value=""
        resourceKinds={kinds}
        onApply={() => undefined}
      />
    </LocaleProvider>,
  );

  const editor = screen.getByRole("combobox", {
    name: "Resource query editor",
  });
  await user.type(editor, "type = ");
  await screen.findByRole("option", { name: /ACS::TEST::Resource11/ });
  scrollIntoView.mockClear();

  await user.keyboard("{ArrowDown}");

  await waitFor(() =>
    expect(scrollIntoView).toHaveBeenLastCalledWith({ block: "nearest" }),
  );
  const activeID = editor.getAttribute("aria-activedescendant");
  expect(activeID).toBeTruthy();
  expect(document.getElementById(activeID!)).toHaveTextContent(
    "ACS::TEST::Resource01",
  );
});
