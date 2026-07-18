import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useIsMobile } from "./use-mobile";

function setViewportWidth(width: number) {
  Object.defineProperty(window, "innerWidth", {
    configurable: true,
    writable: true,
    value: width,
  });
}

describe("useIsMobile", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );
  });

  it("treats the 768px narrow-layout boundary as mobile navigation", () => {
    setViewportWidth(768);

    const { result } = renderHook(() => useIsMobile());

    expect(result.current).toBe(true);
  });

  it("keeps the 1024px tablet layout on desktop navigation", () => {
    setViewportWidth(1024);

    const { result } = renderHook(() => useIsMobile());

    expect(result.current).toBe(false);
  });

  it("returns to desktop navigation immediately above 768px", () => {
    setViewportWidth(769);

    const { result } = renderHook(() => useIsMobile());

    expect(result.current).toBe(false);
  });
});
