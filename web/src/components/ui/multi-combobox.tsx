import { Check, ChevronsUpDown, LoaderCircle, Search, X } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverAnchor,
  PopoverContent,
} from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export interface MultiComboboxOption {
  value: string;
  label: string;
  description?: string;
  disabled?: boolean;
}

export function MultiCombobox({
  label,
  placeholder,
  searchPlaceholder = placeholder,
  emptyLabel,
  selectedCountLabel,
  selectedListLabel,
  removeLabel,
  options,
  selected,
  onSelectedChange,
  query,
  onQueryChange,
  loading = false,
  hasMore = false,
  loadMoreLabel,
  onLoadMore,
}: {
  label: string;
  placeholder: string;
  searchPlaceholder?: string;
  emptyLabel: string;
  selectedCountLabel?: (count: number) => string;
  selectedListLabel: string;
  removeLabel: (name: string) => string;
  options: MultiComboboxOption[];
  selected: string[];
  onSelectedChange: (values: string[]) => void;
  query: string;
  onQueryChange: (value: string) => void;
  loading?: boolean;
  hasMore?: boolean;
  loadMoreLabel?: string;
  onLoadMore?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const listboxID = useId();
  const anchorRef = useRef<HTMLDivElement | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const byValue = useMemo(
    () => new Map(options.map((option) => [option.value, option])),
    [options],
  );
  const selectedOptions = selected.map((value) => ({
    value,
    label: byValue.get(value)?.label ?? value,
  }));
  const buttonLabel =
    selectedOptions.length === 0
      ? placeholder
      : selectedOptions.length === 1
        ? selectedOptions[0].label
        : (selectedCountLabel?.(selectedOptions.length) ??
          `${label} · ${selectedOptions.length}`);

  const setPopoverOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      onQueryChange("");
      setActiveIndex(0);
    }
  };

  const startSearching = () => {
    if (open) return;
    onQueryChange("");
    const selectedIndex = options.findIndex((option) =>
      selected.includes(option.value),
    );
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
    setOpen(true);
  };

  const toggleOption = (option: MultiComboboxOption) => {
    if (option.disabled) return;
    onSelectedChange(
      selected.includes(option.value)
        ? selected.filter((value) => value !== option.value)
        : [...selected, option.value],
    );
  };

  const moveActive = (offset: number) => {
    if (options.length === 0) return;
    setActiveIndex((current) => {
      let next = current;
      do {
        next = (next + offset + options.length) % options.length;
      } while (options[next]?.disabled && next !== current);
      requestAnimationFrame(() =>
        document
          .getElementById(`${listboxID}-option-${next}`)
          ?.scrollIntoView({ block: "nearest" }),
      );
      return next;
    });
  };

  return (
    <div className="space-y-2">
      <Popover open={open} onOpenChange={setPopoverOpen}>
        <PopoverAnchor asChild>
          <div ref={anchorRef} className="relative">
            {open ? (
              <>
                <Search
                  aria-hidden="true"
                  className="pointer-events-none absolute top-1/2 left-3 z-10 size-4 -translate-y-1/2 text-muted-foreground"
                />
                <input
                  autoFocus
                  type="text"
                  role="combobox"
                  value={query}
                  onChange={(event) => {
                    onQueryChange(event.target.value);
                    setActiveIndex(0);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      moveActive(1);
                    } else if (event.key === "ArrowUp") {
                      event.preventDefault();
                      moveActive(-1);
                    } else if (event.key === "Enter") {
                      event.preventDefault();
                      const option = options[activeIndex];
                      if (option) toggleOption(option);
                    } else if (event.key === "Escape") {
                      event.preventDefault();
                      event.stopPropagation();
                      setPopoverOpen(false);
                      requestAnimationFrame(() => buttonRef.current?.focus());
                    }
                  }}
                  aria-label={label}
                  aria-autocomplete="list"
                  aria-controls={listboxID}
                  aria-expanded="true"
                  aria-activedescendant={
                    options[activeIndex]
                      ? `${listboxID}-option-${activeIndex}`
                      : undefined
                  }
                  autoComplete="off"
                  placeholder={searchPlaceholder}
                  className="h-9 w-full rounded-lg border border-input bg-background pr-9 pl-9 text-sm shadow-none outline-none transition-[border-color,box-shadow] duration-[120ms] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                />
              </>
            ) : (
              <button
                ref={buttonRef}
                type="button"
                role="combobox"
                aria-label={label}
                aria-controls={listboxID}
                aria-expanded="false"
                className="h-9 w-full truncate rounded-lg border border-input bg-background pr-9 pl-3 text-left text-sm shadow-none outline-none transition-[border-color,box-shadow] duration-[120ms] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                onClick={startSearching}
              >
                {buttonLabel}
              </button>
            )}
            {loading ? (
              <LoaderCircle
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 animate-spin text-muted-foreground"
              />
            ) : (
              <ChevronsUpDown
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground opacity-50"
              />
            )}
          </div>
        </PopoverAnchor>
        <PopoverContent
          align="start"
          collisionPadding={12}
          className="w-[var(--radix-popover-trigger-width)] overflow-hidden p-0"
          onOpenAutoFocus={(event) => event.preventDefault()}
          onInteractOutside={(event) => {
            if (anchorRef.current?.contains(event.target as Node)) {
              event.preventDefault();
            }
          }}
        >
          <div
            id={listboxID}
            role="listbox"
            aria-label={label}
            aria-multiselectable="true"
            className="scroll-py-1 overflow-x-hidden overscroll-contain p-1"
            style={{
              maxHeight:
                "min(13rem, var(--radix-popover-content-available-height))",
              overflowY: "auto",
            }}
          >
            {!loading && options.length === 0 ? (
              <div className="py-6 text-center text-sm">{emptyLabel}</div>
            ) : (
              options.map((option, index) => {
                const checked = selected.includes(option.value);
                return (
                  <button
                    key={option.value}
                    id={`${listboxID}-option-${index}`}
                    type="button"
                    role="option"
                    aria-selected={checked}
                    aria-disabled={option.disabled || undefined}
                    disabled={option.disabled}
                    data-active={index === activeIndex ? "true" : undefined}
                    className="relative flex w-full cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none select-none hover:bg-accent hover:text-accent-foreground data-[active=true]:bg-accent data-[active=true]:text-accent-foreground disabled:pointer-events-none disabled:opacity-50"
                    onMouseEnter={() => setActiveIndex(index)}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => toggleOption(option)}
                  >
                    <Check
                      className={cn(
                        "size-4 shrink-0",
                        checked ? "opacity-100" : "opacity-0",
                      )}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate">{option.label}</span>
                      {option.description && (
                        <span className="block truncate font-mono text-xs text-muted-foreground">
                          {option.description}
                        </span>
                      )}
                    </span>
                  </button>
                );
              })
            )}
            {hasMore && onLoadMore && (
              <div className="border-t p-1.5">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="w-full"
                  disabled={loading}
                  onClick={onLoadMore}
                >
                  {loadMoreLabel}
                </Button>
              </div>
            )}
          </div>
        </PopoverContent>
      </Popover>
      {selectedOptions.length > 0 && (
        <div className="flex flex-wrap gap-1.5" aria-label={selectedListLabel}>
          {selectedOptions.map((option) => (
            <Badge
              key={option.value}
              variant="secondary"
              className="max-w-full gap-1 pr-1"
            >
              <span className="truncate">{option.label}</span>
              <button
                type="button"
                aria-label={removeLabel(option.label)}
                className="rounded-full p-0.5 text-muted-foreground outline-none hover:bg-background hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
                onClick={() =>
                  onSelectedChange(
                    selected.filter((value) => value !== option.value),
                  )
                }
              >
                <X aria-hidden="true" className="size-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}
