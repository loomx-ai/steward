import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { CopyableId } from "./CopyableId";

const writeText = vi.fn();

beforeEach(() => {
  localStorage.setItem("steward.locale", "zh-CN");
  writeText.mockReset().mockResolvedValue(undefined);
});

it("shows a copy action on hover or keyboard focus and copies the ID", async () => {
  const user = userEvent.setup();
  vi.spyOn(navigator.clipboard, "writeText").mockImplementation(writeText);
  render(
    <TooltipProvider>
      <LocaleProvider>
        <CopyableId label="扫描 ID" value="scn-23456789abcdefgh" />
      </LocaleProvider>
    </TooltipProvider>,
  );

  expect(screen.getByText("scn-23456789abcdefgh")).toBeVisible();
  const copy = screen.getByRole("button", { name: "复制扫描 ID" });
  expect(copy).toHaveClass(
    "opacity-0",
    "group-hover:opacity-100",
    "focus-visible:opacity-100",
  );

  await user.click(copy);

  expect(writeText).toHaveBeenCalledWith("scn-23456789abcdefgh");
  expect(
    screen.getByRole("button", { name: "已复制扫描 ID" }),
  ).toBeInTheDocument();
});
