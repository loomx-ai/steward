import { describe, expect, it } from "vitest";
import { formatDuration } from "./formatDuration";

describe("formatDuration", () => {
  it("formats backend-provided active milliseconds", () => {
    expect(formatDuration(0)).toBe("0s");
    expect(formatDuration(90_000)).toBe("1m 30s");
  });

  it("does not reconstruct missing timing data from timestamps", () => {
    expect(formatDuration()).toBe("—");
  });
});
