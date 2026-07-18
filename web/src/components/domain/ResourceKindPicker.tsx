import { Check, ChevronsUpDown, Search, X } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import type { ComboboxOption } from "@/components/ui/combobox";
import { Badge } from "@/components/ui/badge";
import {
  Popover,
  PopoverAnchor,
  PopoverContent,
} from "@/components/ui/popover";
import { cn } from "@/lib/utils";

const ALL_KINDS_VALUE = "__all__";

interface ResourceKindPickerOption extends ComboboxOption {
  tag?: string;
}

export function ResourceKindPicker({
  label,
  values,
  options,
  allLabel,
  selectedCountLabel,
  selectedListLabel,
  removeLabel,
  searchPlaceholder,
  emptyLabel,
  onValuesChange,
}: {
  label: string;
  values: string[];
  options: ResourceKindPickerOption[];
  allLabel: string;
  selectedCountLabel: (count: number) => string;
  selectedListLabel: string;
  removeLabel: (name: string) => string;
  searchPlaceholder: string;
  emptyLabel: string;
  onValuesChange: (values: string[]) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const listboxID = useId();
  const anchorRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const tagsViewportRef = useRef<HTMLDivElement | null>(null);
  const normalizedQuery = normalizeSearch(query);
  const filteredOptions = useMemo(
    () =>
      normalizedQuery
        ? options.filter((option) =>
            [
              option.label,
              option.value,
              option.tag ?? "",
              ...(option.keywords ?? []),
            ].some((token) => normalizeSearch(token).includes(normalizedQuery)),
          )
        : [{ value: ALL_KINDS_VALUE, label: allLabel }, ...options],
    [allLabel, normalizedQuery, options],
  );
  const optionByValue = useMemo(
    () => new Map(options.map((option) => [option.value, option])),
    [options],
  );
  const selectedOptions = values.map((value) => ({
    value,
    label: optionByValue.get(value)?.label ?? value,
  }));
  const buttonLabel =
    selectedOptions.length === 0
      ? allLabel
      : selectedOptions.length === 1
        ? selectedOptions[0].label
        : selectedCountLabel(selectedOptions.length);

  const setPopoverOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      setQuery("");
      setActiveIndex(0);
      requestAnimationFrame(() => {
        if (tagsViewportRef.current) tagsViewportRef.current.scrollLeft = 0;
      });
    }
  };

  const startSearching = () => {
    if (!open) {
      setQuery("");
      const selectedIndex = options.findIndex((option) =>
        values.includes(option.value),
      );
      setActiveIndex(selectedIndex >= 0 ? selectedIndex + 1 : 0);
      setOpen(true);
    }
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      if (tagsViewportRef.current) {
        tagsViewportRef.current.scrollLeft =
          tagsViewportRef.current.scrollWidth;
      }
    });
  };

  const toggleOption = (option: ComboboxOption) => {
    if (option.value === ALL_KINDS_VALUE) {
      onValuesChange([]);
      return;
    }
    onValuesChange(
      values.includes(option.value)
        ? values.filter((value) => value !== option.value)
        : [...values, option.value],
    );
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      if (tagsViewportRef.current) {
        tagsViewportRef.current.scrollLeft =
          tagsViewportRef.current.scrollWidth;
      }
    });
  };

  const moveActive = (offset: number) => {
    if (filteredOptions.length === 0) return;
    setActiveIndex((current) => {
      const next =
        (current + offset + filteredOptions.length) % filteredOptions.length;
      requestAnimationFrame(() =>
        document
          .getElementById(`${listboxID}-option-${next}`)
          ?.scrollIntoView({ block: "nearest" }),
      );
      return next;
    });
  };

  return (
    <Popover open={open} onOpenChange={setPopoverOpen}>
      <PopoverAnchor asChild>
        <div
          ref={anchorRef}
          data-slot="resource-kind-picker-control"
          title={buttonLabel}
          className="relative flex h-11 w-full items-center rounded-lg border border-input bg-background pr-8 pl-1.5 text-sm shadow-none transition-[border-color,box-shadow] duration-[120ms] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50 sm:h-9"
          onMouseDown={(event) => {
            const target = event.target as HTMLElement;
            if (target.closest("button")) return;
            if (target !== inputRef.current) event.preventDefault();
            startSearching();
          }}
        >
          <div
            ref={tagsViewportRef}
            role={selectedOptions.length > 0 ? "list" : undefined}
            aria-label={
              selectedOptions.length > 0 ? selectedListLabel : undefined
            }
            className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto overflow-y-hidden whitespace-nowrap [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
          >
            {selectedOptions.map((option) => (
              <Badge
                key={option.value}
                role="listitem"
                variant="secondary"
                className="h-6 max-w-48 shrink-0 gap-1 pr-1"
                title={option.label}
              >
                <span className="truncate">{option.label}</span>
                <button
                  type="button"
                  aria-label={removeLabel(option.label)}
                  className="shrink-0 rounded-full p-0.5 text-muted-foreground outline-none hover:bg-background hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
                  onMouseDown={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                  }}
                  onClick={(event) => {
                    event.stopPropagation();
                    onValuesChange(
                      values.filter((value) => value !== option.value),
                    );
                  }}
                >
                  <X aria-hidden="true" className="size-3" />
                </button>
              </Badge>
            ))}
            {open && selectedOptions.length === 0 && (
              <Search
                aria-hidden="true"
                className="size-4 shrink-0 text-muted-foreground"
              />
            )}
            <input
              ref={inputRef}
              type="text"
              role="combobox"
              value={query}
              onFocus={startSearching}
              onChange={(event) => {
                if (!open) startSearching();
                setQuery(event.target.value);
                setActiveIndex(0);
              }}
              onKeyDown={(event) => {
                if (
                  event.key === "Backspace" &&
                  query.length === 0 &&
                  values.length > 0
                ) {
                  event.preventDefault();
                  onValuesChange(values.slice(0, -1));
                } else if (event.key === "ArrowDown") {
                  event.preventDefault();
                  if (open) moveActive(1);
                  else startSearching();
                } else if (event.key === "ArrowUp") {
                  event.preventDefault();
                  if (open) moveActive(-1);
                  else startSearching();
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  if (open) {
                    const option = filteredOptions[activeIndex];
                    if (option) toggleOption(option);
                  } else {
                    startSearching();
                  }
                } else if (event.key === "Escape" && open) {
                  event.preventDefault();
                  event.stopPropagation();
                  setPopoverOpen(false);
                }
              }}
              aria-label={label}
              aria-autocomplete="list"
              aria-controls={listboxID}
              aria-expanded={open}
              aria-activedescendant={
                open && filteredOptions[activeIndex]
                  ? `${listboxID}-option-${activeIndex}`
                  : undefined
              }
              autoComplete="off"
              placeholder={
                open
                  ? searchPlaceholder
                  : selectedOptions.length === 0
                    ? allLabel
                    : undefined
              }
              className={cn(
                "h-7 bg-transparent text-sm outline-none placeholder:text-muted-foreground",
                open
                  ? "min-w-28 flex-1 px-1"
                  : selectedOptions.length > 0
                    ? "w-px min-w-px flex-none p-0"
                    : "min-w-0 flex-1 px-1",
              )}
            />
          </div>
          <ChevronsUpDown
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground opacity-50"
          />
        </div>
      </PopoverAnchor>
      <PopoverContent
        align="start"
        collisionPadding={12}
        portalContainer={anchorRef.current?.closest<HTMLElement>(
          '[data-slot="dialog-content"], [data-slot="sheet-content"]',
        )}
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
          {filteredOptions.length === 0 ? (
            <div className="py-6 text-center text-sm">{emptyLabel}</div>
          ) : (
            filteredOptions.map((option, index) => {
              const checked =
                option.value === ALL_KINDS_VALUE
                  ? values.length === 0
                  : values.includes(option.value);
              return (
                <button
                  key={option.value}
                  id={`${listboxID}-option-${index}`}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  aria-label={
                    option.tag
                      ? `${option.label} · ${option.tag}`
                      : option.label
                  }
                  data-active={index === activeIndex ? "true" : undefined}
                  className="relative flex w-full cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none select-none hover:bg-accent hover:text-accent-foreground data-[active=true]:bg-accent data-[active=true]:text-accent-foreground"
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
                  <span className="min-w-0 flex-1 truncate">
                    {option.label}
                  </span>
                  {option.tag && (
                    <Badge
                      variant="outline"
                      className="h-5 shrink-0 px-1.5 font-mono text-[10px] font-normal text-muted-foreground"
                    >
                      {option.tag}
                    </Badge>
                  )}
                </button>
              );
            })
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function normalizeSearch(value: string): string {
  return value.trim().toLocaleLowerCase();
}
