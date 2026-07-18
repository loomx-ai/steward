import { act, renderHook } from "@testing-library/react";
import { expect, it } from "vitest";
import { useCursorPagination } from "./useCursorPagination";

it("retains visited cursors and jumps directly between historical pages", () => {
  const { result } = renderHook(() => useCursorPagination("assets"));

  expect(result.current).toMatchObject({
    cursor: "",
    page: 1,
    pageCount: 1,
    pageSize: 20,
    canGoPrevious: false,
  });

  act(() => result.current.goNext("cursor-2"));
  act(() => result.current.goNext("cursor-3"));
  act(() => result.current.goNext("cursor-4"));
  act(() => result.current.goPrevious());
  expect(result.current).toMatchObject({
    cursor: "cursor-3",
    page: 3,
    pageCount: 4,
    pageSize: 20,
    canGoPrevious: true,
  });

  act(() => result.current.goToPage(2));
  expect(result.current).toMatchObject({
    cursor: "cursor-2",
    page: 2,
    pageCount: 4,
    pageSize: 20,
  });

  act(() => result.current.goToPage(4));
  expect(result.current).toMatchObject({
    cursor: "cursor-4",
    page: 4,
    pageCount: 4,
    pageSize: 20,
  });
});

it("returns to the first page when the reset key changes", () => {
  const { result, rerender } = renderHook(
    ({ resetKey }) => useCursorPagination(resetKey),
    { initialProps: { resetKey: "provider:alicloud" } },
  );

  act(() => result.current.goNext("cursor-2"));
  rerender({ resetKey: "provider:aws" });

  expect(result.current).toMatchObject({
    cursor: "",
    page: 1,
    pageCount: 1,
    pageSize: 20,
    canGoPrevious: false,
  });
});

it("resets to the first page when the page size changes", () => {
  const { result } = renderHook(() => useCursorPagination("assets"));

  act(() => result.current.goNext("cursor-2"));
  act(() => result.current.setPageSize(100));

  expect(result.current).toMatchObject({
    cursor: "",
    page: 1,
    pageCount: 1,
    pageSize: 100,
    canGoPrevious: false,
  });
});
