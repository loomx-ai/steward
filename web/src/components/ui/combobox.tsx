import { Check, ChevronsUpDown, Search } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import {
  Popover,
  PopoverAnchor,
  PopoverContent,
} from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export interface ComboboxOption {
  value: string;
  label: string;
  keywords?: string[];
}

export function Combobox({
  label,
  value,
  options,
  placeholder,
  searchPlaceholder,
  emptyLabel,
  onValueChange,
}: {
  label: string;
  value: string;
  options: ComboboxOption[];
  placeholder: string;
  searchPlaceholder: string;
  emptyLabel: string;
  onValueChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const listboxID = useId();
  const anchorRef = useRef<HTMLDivElement | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const selected = options.find((option) => option.value === value);
  const normalizedQuery = normalizeSearch(query);
  const filteredOptions = useMemo(
    () =>
      normalizedQuery
        ? options.filter((option) =>
            [option.label, option.value, ...(option.keywords ?? [])].some(
              (token) => normalizeSearch(token).includes(normalizedQuery),
            ),
          )
        : options,
    [normalizedQuery, options],
  );

  const setPopoverOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      setQuery("");
      setActiveIndex(0);
    }
  };

  const startSearching = () => {
    if (open) return;
    setQuery("");
    const selectedIndex = options.findIndex((option) => option.value === value);
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
    setOpen(true);
  };

  const selectOption = (option: ComboboxOption) => {
    onValueChange(option.value);
    setPopoverOpen(false);
    requestAnimationFrame(() => buttonRef.current?.focus());
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
                  setQuery(event.target.value);
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
                    const option = filteredOptions[activeIndex];
                    if (option) selectOption(option);
                  } else if (event.key === "Escape") {
                    event.preventDefault();
                    setPopoverOpen(false);
                  }
                }}
                aria-label={label}
                aria-autocomplete="list"
                aria-controls={listboxID}
                aria-expanded="true"
                aria-activedescendant={
                  filteredOptions[activeIndex]
                    ? `${listboxID}-option-${activeIndex}`
                    : undefined
                }
                autoComplete="off"
                placeholder={searchPlaceholder}
                className="h-9 w-full rounded-lg border border-input bg-background pr-9 pl-9 text-sm shadow-none outline-none transition-[border-color,box-shadow] duration-[120ms] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
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
              className="h-9 w-full truncate rounded-lg border border-input bg-background pr-9 pl-3 text-left text-sm shadow-none outline-none transition-[border-color,box-shadow] duration-[120ms] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
              onClick={startSearching}
            >
              {selected?.label ?? placeholder}
            </button>
          )}
          <ChevronsUpDown
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground opacity-50"
          />
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
              const checked = option.value === value;
              return (
                <button
                  key={option.value}
                  id={`${listboxID}-option-${index}`}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  data-active={index === activeIndex ? "true" : undefined}
                  className="relative flex w-full cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none select-none hover:bg-accent hover:text-accent-foreground data-[active=true]:bg-accent data-[active=true]:text-accent-foreground"
                  onMouseEnter={() => setActiveIndex(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => selectOption(option)}
                >
                  <Check
                    className={cn(
                      "size-4 shrink-0",
                      checked ? "opacity-100" : "opacity-0",
                    )}
                  />
                  <span className="truncate">{option.label}</span>
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
