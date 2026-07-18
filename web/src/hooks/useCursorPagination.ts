import { useCallback, useEffect, useState } from "react";

export const PAGE_SIZE_OPTIONS = [20, 50, 100] as const;
export const DEFAULT_PAGE_SIZE = PAGE_SIZE_OPTIONS[0];
export type PageSize = (typeof PAGE_SIZE_OPTIONS)[number];

interface CursorPaginationState {
  resetKey: unknown;
  cursors: string[];
  currentIndex: number;
  pageSize: PageSize;
}

function initialState(
  resetKey: unknown,
  pageSize: PageSize = DEFAULT_PAGE_SIZE,
): CursorPaginationState {
  return { resetKey, cursors: [""], currentIndex: 0, pageSize };
}

export function useCursorPagination(resetKey: unknown) {
  const [state, setState] = useState(() => initialState(resetKey));
  const current =
    state.resetKey === resetKey
      ? state
      : initialState(resetKey, state.pageSize);

  useEffect(() => {
    setState((current) =>
      current.resetKey === resetKey
        ? current
        : initialState(resetKey, current.pageSize),
    );
  }, [resetKey]);

  const goNext = useCallback(
    (cursor: string) => {
      setState((current) => {
        const active =
          current.resetKey === resetKey
            ? current
            : initialState(resetKey, current.pageSize);
        if (active.currentIndex < active.cursors.length - 1) {
          return { ...active, currentIndex: active.currentIndex + 1 };
        }
        if (!cursor) return active;
        return {
          ...active,
          cursors: [...active.cursors, cursor],
          currentIndex: active.currentIndex + 1,
        };
      });
    },
    [resetKey],
  );

  const goPrevious = useCallback(() => {
    setState((current) => ({
      ...current,
      currentIndex: Math.max(0, current.currentIndex - 1),
    }));
  }, []);

  const goToPage = useCallback((page: number) => {
    setState((current) => {
      if (
        !Number.isInteger(page) ||
        page < 1 ||
        page > current.cursors.length
      ) {
        return current;
      }
      return { ...current, currentIndex: page - 1 };
    });
  }, []);

  const reset = useCallback(
    () => setState((current) => initialState(resetKey, current.pageSize)),
    [resetKey],
  );

  const setPageSize = useCallback(
    (pageSize: PageSize) => {
      if (!PAGE_SIZE_OPTIONS.includes(pageSize)) return;
      setState((current) => {
        const active =
          current.resetKey === resetKey
            ? current
            : initialState(resetKey, current.pageSize);
        return active.pageSize === pageSize
          ? active
          : initialState(resetKey, pageSize);
      });
    },
    [resetKey],
  );

  return {
    cursor: current.cursors[current.currentIndex] ?? "",
    page: current.currentIndex + 1,
    pageCount: current.cursors.length,
    pageSize: current.pageSize,
    canGoPrevious: current.currentIndex > 0,
    goNext,
    goPrevious,
    goToPage,
    reset,
    setPageSize,
  };
}
