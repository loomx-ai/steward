import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PAGE_SIZE_OPTIONS, type PageSize } from "@/hooks/useCursorPagination";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

type PageItem = number | string;

function visiblePageItems(page: number, pageCount: number): PageItem[] {
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, index) => index + 1);
  }

  const pages = [...new Set([1, page - 1, page, page + 1, pageCount])]
    .filter((value) => value >= 1 && value <= pageCount)
    .sort((left, right) => left - right);

  return pages.flatMap((value, index) => {
    const previous = pages[index - 1];
    if (previous === undefined || value - previous === 1) return [value];
    if (value - previous === 2) return [previous + 1, value];
    return [`ellipsis-${previous}-${value}`, value];
  });
}

export function CursorPagination({
  page,
  pageCount,
  hasNextPage,
  pending,
  pageSize,
  onPrevious,
  onNext,
  onPageSelect,
  onPageSizeChange,
  labels,
}: {
  page: number;
  pageCount: number;
  hasNextPage: boolean;
  pending: boolean;
  pageSize: PageSize;
  onPrevious: () => void;
  onNext: () => void;
  onPageSelect: (page: number) => void;
  onPageSizeChange: (pageSize: PageSize) => void;
  labels: {
    page: (page: number) => string;
    pageSize: string;
    previous: string;
    next: string;
  };
}) {
  const hasPageControls = pageCount > 1 || hasNextPage;

  return (
    <div
      data-slot="cursor-pagination"
      className="flex w-full flex-wrap items-center justify-end gap-2"
    >
      <Select
        value={String(pageSize)}
        disabled={pending}
        onValueChange={(value) => {
          const nextPageSize = Number(value) as PageSize;
          if (PAGE_SIZE_OPTIONS.includes(nextPageSize)) {
            onPageSizeChange(nextPageSize);
          }
        }}
      >
        <SelectTrigger
          size="sm"
          className="w-20 tabular-nums"
          aria-label={labels.pageSize}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          {PAGE_SIZE_OPTIONS.map((option) => (
            <SelectItem key={option} value={String(option)}>
              {option}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {hasPageControls && (
        <div className="flex items-center gap-1" aria-label={labels.page(page)}>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={labels.previous}
            title={labels.previous}
            disabled={page === 1 || pending}
            onClick={onPrevious}
          >
            <ChevronLeft />
          </Button>
          {visiblePageItems(page, pageCount).map((item) =>
            typeof item === "number" ? (
              <Button
                key={item}
                variant={item === page ? "secondary" : "ghost"}
                size="icon-sm"
                className="text-xs tabular-nums"
                aria-label={labels.page(item)}
                aria-current={item === page ? "page" : undefined}
                disabled={pending}
                onClick={() => {
                  if (item !== page) onPageSelect(item);
                }}
              >
                {item}
              </Button>
            ) : (
              <span
                key={item}
                className="flex size-8 items-center justify-center text-xs text-muted-foreground"
                aria-hidden
              >
                …
              </span>
            ),
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={labels.next}
            title={labels.next}
            disabled={!hasNextPage || pending}
            onClick={onNext}
          >
            <ChevronRight />
          </Button>
        </div>
      )}
    </div>
  );
}
