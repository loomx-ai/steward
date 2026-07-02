import { KeyboardEvent, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";

export type DropdownOption = {
  value: string;
  label: string;
};

export function Dropdown({
  label,
  value,
  options,
  onValueChange,
}: {
  label: string;
  value: string;
  options: DropdownOption[];
  onValueChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const listID = useRef(`dropdown-${Math.random().toString(36).slice(2)}`);
  const selectedIndex = Math.max(
    0,
    options.findIndex((option) => option.value === value),
  );
  const selected = options[selectedIndex] ?? options[0];

  function openMenu(nextIndex = selectedIndex) {
    setActiveIndex(nextIndex);
    setOpen(true);
  }

  function choose(option: DropdownOption) {
    onValueChange(option.value);
    setOpen(false);
  }

  function move(delta: number) {
    if (options.length === 0) return;
    setActiveIndex(
      (index) => (index + delta + options.length) % options.length,
    );
  }

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (!open) openMenu();
        else move(1);
        break;
      case "ArrowUp":
        event.preventDefault();
        if (!open) openMenu();
        else move(-1);
        break;
      case "Home":
        event.preventDefault();
        openMenu(0);
        break;
      case "End":
        event.preventDefault();
        openMenu(Math.max(0, options.length - 1));
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (!open) {
          openMenu();
          return;
        }
        if (options[activeIndex]) choose(options[activeIndex]);
        break;
      case "Escape":
        setOpen(false);
        break;
    }
  }

  return (
    <div
      className="dropdown"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          setOpen(false);
        }
      }}
    >
      <button
        type="button"
        className="dropdown-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listID.current}
        onClick={() => (open ? setOpen(false) : openMenu())}
        onKeyDown={handleKeyDown}
      >
        <span>{selected?.label ?? label}</span>
        <ChevronDown size={16} />
      </button>
      {open && (
        <div
          id={listID.current}
          className="dropdown-menu"
          role="listbox"
          aria-label={label}
        >
          {options.map((option, index) => {
            const selectedOption = option.value === value;
            return (
              <button
                type="button"
                role="option"
                aria-selected={selectedOption}
                key={option.value}
                className={
                  index === activeIndex
                    ? "dropdown-option active"
                    : "dropdown-option"
                }
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => choose(option)}
              >
                <span>{option.label}</span>
                {selectedOption && <Check size={15} />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
