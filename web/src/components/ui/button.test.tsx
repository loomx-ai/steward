import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Button } from "./button";

describe("Button", () => {
  it("keeps the default link variant dimensionless after class merging", () => {
    render(<Button variant="link">Documentation</Button>);

    const button = screen.getByRole("button", { name: "Documentation" });
    expect(button).toHaveClass("h-auto", "px-0", "py-0");
    expect(button).not.toHaveClass("h-9", "px-4");
  });
});
