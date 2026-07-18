import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { cn } from "@/lib/utils";

describe("UI toolchain", () => {
  it("renders with the project alias and class merge helper", () => {
    render(<div className={cn("px-2", false && "hidden")}>ready</div>);
    expect(screen.getByText("ready")).toHaveClass("px-2");
  });
});
