import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { Braces, Play, Search, X } from "lucide-react";
import type { ResourceKind, ResourceProperty } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import { resourceTypeName } from "./resourceKindLabel";

export type ResourceSearchMode = "simple" | "advanced";

export interface ResourceSearchModeToggleProps {
  mode: ResourceSearchMode;
  onModeChange: (mode: ResourceSearchMode) => void;
  className?: string;
}

export function ResourceSearchModeToggle({
  mode,
  onModeChange,
  className,
}: ResourceSearchModeToggleProps) {
  const { t } = useLocale();
  const advanced = mode === "advanced";
  const label = t(
    advanced ? "query.mode.advancedTooltip" : "query.mode.simpleTooltip",
  );

  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className={cn(
              "size-7 text-muted-foreground hover:text-foreground",
              advanced && "bg-accent text-info hover:text-info",
              className,
            )}
            aria-label={label}
            aria-pressed={advanced}
            data-search-mode={mode}
            onClick={() => onModeChange(advanced ? "simple" : "advanced")}
          >
            {advanced ? (
              <Braces aria-hidden="true" />
            ) : (
              <Search aria-hidden="true" />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

export interface ResourceQueryInputProps {
  value: string;
  resourceKinds: readonly ResourceKind[];
  fieldValues?: ResourceQueryFieldValues;
  error?: string;
  onApply: (query: string) => void;
  className?: string;
  inputClassName?: string;
  autoFocus?: boolean;
}

export type ResourceQueryValue = string | number | boolean;

export interface ResourceQueryValueSuggestion {
  value: ResourceQueryValue;
  detail?: string;
}

export type ResourceQueryFieldValues = Readonly<
  Record<string, readonly ResourceQueryValueSuggestion[]>
>;

interface Suggestion {
  key: string;
  label: string;
  detail?: string;
  insert: string;
  replaceStart: number;
  replaceEnd: number;
}

const BUILTIN_FIELDS = [
  ["type", "query.field.type"],
  ["name", "query.field.name"],
  ["state", "query.field.state"],
  ["region", "query.field.region"],
  ["provider", "query.field.provider"],
  ["dirty", "query.field.dirty"],
  ["resourceId", "query.field.resourceId"],
  ["resourceKindId", "query.field.resourceKindId"],
] as const;

const DEFAULT_OPERATORS = [
  "=",
  "!=",
  "IN",
  "NOT IN",
  "CONTAINS",
  "IS NULL",
  "IS NOT NULL",
];

export function ResourceQueryInput({
  value,
  resourceKinds,
  fieldValues,
  error,
  onApply,
  className,
  inputClassName,
  autoFocus,
}: ResourceQueryInputProps) {
  const { locale, t } = useLocale();
  const inputID = useId();
  const listboxID = `${inputID}-suggestions`;
  const [focused, setFocused] = useState(false);
  const [draft, setDraft] = useState(value);
  const [cursor, setCursor] = useState(value.length);
  const [selectedSuggestion, setSelectedSuggestion] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const pendingCursorRef = useRef<number | undefined>(undefined);
  const applied = value.trim().length > 0;
  const nextValue = draft.trim();
  const dirty = nextValue !== value.trim();
  const suggestions = useMemo(
    () =>
      resourceQuerySuggestions(
        draft,
        cursor,
        resourceKinds,
        fieldValues,
        locale,
        t,
      ),
    [cursor, draft, fieldValues, locale, resourceKinds, t],
  );
  const open = Boolean(error) || (focused && suggestions.length > 0);

  useEffect(() => {
    setDraft(value);
    setCursor(value.length);
  }, [value]);

  useEffect(() => setSelectedSuggestion(0), [draft, cursor]);

  useLayoutEffect(() => {
    const nextCursor = pendingCursorRef.current;
    if (nextCursor === undefined) return;
    pendingCursorRef.current = undefined;
    inputRef.current?.focus();
    inputRef.current?.setSelectionRange(nextCursor, nextCursor);
  }, [draft]);

  useLayoutEffect(() => {
    if (!open) return;
    const active = document.getElementById(
      `${listboxID}-${selectedSuggestion}`,
    );
    if (active && typeof active.scrollIntoView === "function") {
      active.scrollIntoView({ block: "nearest" });
    }
  }, [listboxID, open, selectedSuggestion]);

  const apply = () => {
    onApply(nextValue);
    setDraft(nextValue);
    setCursor(nextValue.length);
    setFocused(false);
    inputRef.current?.blur();
  };

  const clear = () => {
    setDraft("");
    setCursor(0);
    onApply("");
    setFocused(false);
    inputRef.current?.blur();
  };

  const insertSuggestion = (suggestion: Suggestion) => {
    const next =
      draft.slice(0, suggestion.replaceStart) +
      suggestion.insert +
      draft.slice(suggestion.replaceEnd);
    const nextCursor = suggestion.replaceStart + suggestion.insert.length;
    pendingCursorRef.current = nextCursor;
    setDraft(next);
    setCursor(nextCursor);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      apply();
      return;
    }
    if (suggestions.length > 0 && event.key === "ArrowDown") {
      event.preventDefault();
      setSelectedSuggestion((current) => (current + 1) % suggestions.length);
    } else if (suggestions.length > 0 && event.key === "ArrowUp") {
      event.preventDefault();
      setSelectedSuggestion(
        (current) => (current - 1 + suggestions.length) % suggestions.length,
      );
    } else if (
      suggestions.length > 0 &&
      (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) &&
      suggestions[selectedSuggestion]
    ) {
      event.preventDefault();
      insertSuggestion(suggestions[selectedSuggestion]);
    } else if (event.key === "Enter") {
      event.preventDefault();
      apply();
    } else if (event.key === "Escape") {
      event.preventDefault();
      setFocused(false);
      event.currentTarget.blur();
    }
  };

  return (
    <TooltipProvider delayDuration={300}>
      <div
        ref={rootRef}
        className={cn("relative w-full", className)}
        onBlur={(event) => {
          if (!rootRef.current?.contains(event.relatedTarget)) {
            setFocused(false);
          }
        }}
      >
        <Input
          ref={inputRef}
          id={inputID}
          role="combobox"
          aria-autocomplete="list"
          aria-controls={open ? listboxID : undefined}
          aria-expanded={open}
          aria-activedescendant={
            open && suggestions[selectedSuggestion]
              ? `${listboxID}-${selectedSuggestion}`
              : undefined
          }
          aria-label={t("query.editor")}
          aria-invalid={Boolean(error)}
          autoComplete="off"
          autoFocus={autoFocus}
          spellCheck={false}
          className={cn(
            "pr-10 pl-10 font-mono text-xs",
            applied && !error && "border-info/50 bg-info/5",
            inputClassName,
          )}
          value={draft}
          placeholder={t("query.placeholder")}
          onFocus={() => setFocused(true)}
          onChange={(event) => {
            setDraft(event.target.value);
            setCursor(event.target.selectionStart ?? event.target.value.length);
            setFocused(true);
          }}
          onClick={(event) =>
            setCursor(
              event.currentTarget.selectionStart ??
                event.currentTarget.value.length,
            )
          }
          onKeyUp={(event) =>
            setCursor(
              event.currentTarget.selectionStart ??
                event.currentTarget.value.length,
            )
          }
          onKeyDown={handleKeyDown}
        />
        {(dirty && nextValue) || applied ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="absolute top-1/2 right-1.5 size-7 -translate-y-1/2"
                aria-label={
                  dirty && nextValue ? t("query.apply") : t("query.clear")
                }
                onClick={dirty && nextValue ? apply : clear}
              >
                {dirty && nextValue ? (
                  <Play aria-hidden="true" />
                ) : (
                  <X aria-hidden="true" />
                )}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {t(dirty && nextValue ? "query.apply" : "query.clear")}
            </TooltipContent>
          </Tooltip>
        ) : null}

        {open && (
          <div
            className="absolute top-[calc(100%+0.375rem)] z-50 w-full overflow-hidden rounded-md border border-border bg-popover/98 text-popover-foreground shadow-lg backdrop-blur"
            data-testid="resource-query-dropdown"
          >
            {error && (
              <p
                className="border-b px-3 py-2 text-xs text-destructive"
                role="alert"
              >
                {error}
              </p>
            )}
            <div
              id={listboxID}
              role="listbox"
              aria-label={t("query.suggestions")}
              className="max-h-60 overflow-y-auto p-1"
            >
              {suggestions.map((suggestion, index) => (
                <button
                  id={`${listboxID}-${index}`}
                  key={suggestion.key}
                  type="button"
                  role="option"
                  aria-selected={selectedSuggestion === index}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs",
                    selectedSuggestion === index &&
                      "bg-accent text-accent-foreground",
                  )}
                  onMouseEnter={() => setSelectedSuggestion(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => insertSuggestion(suggestion)}
                >
                  <span className="min-w-0 flex-1 truncate font-mono">
                    {suggestion.label}
                  </span>
                  {suggestion.detail && (
                    <span className="max-w-52 truncate text-muted-foreground">
                      {suggestion.detail}
                    </span>
                  )}
                </button>
              ))}
            </div>
            <div className="border-t px-3 py-1.5 text-[11px] text-muted-foreground">
              {t("query.shortcut")}
            </div>
          </div>
        )}
      </div>
    </TooltipProvider>
  );
}

function resourceQuerySuggestions(
  query: string,
  cursor: number,
  resourceKinds: readonly ResourceKind[],
  fieldValues: ResourceQueryFieldValues | undefined,
  locale: string,
  t: (key: MessageKey, values?: Record<string, string | number>) => string,
): Suggestion[] {
  const before = query.slice(0, cursor);
  const selectedTypes = extractSelectedTypes(query);
  const availableKinds =
    selectedTypes.size > 0
      ? resourceKinds.filter((kind) => selectedTypes.has(kind.native_type))
      : [...resourceKinds];

  const typeValue = before.match(
    /(?:^|\bAND\b|\bOR\b|\()\s*(?:type|resourceType)\s*(?:=|!=)\s*(["']?[A-Za-z0-9:._-]*)$/i,
  );
  if (typeValue) {
    const fragment = typeValue[1];
    const partial = fragment.replace(/^["']/, "").toLowerCase();
    const replaceStart = cursor - fragment.length;
    return resourceKinds
      .filter((kind) =>
        [
          kind.native_type,
          kind.display_name,
          ...Object.values(kind.display_names ?? {}),
        ]
          .filter(Boolean)
          .some((value) => String(value).toLowerCase().includes(partial)),
      )
      .slice(0, 60)
      .map((kind) => ({
        key: `type:${kind.id}`,
        label: kind.native_type,
        detail: resourceTypeName(
          kind.native_type,
          kind.display_name || kind.native_type,
          kind.display_names,
          locale,
        ),
        insert: `"${kind.native_type}" `,
        replaceStart,
        replaceEnd: cursor,
      }));
  }

  const typeList = before.match(
    /(?:^|\bAND\b|\bOR\b|\()\s*(?:type|resourceType)\s+(?:NOT\s+)?IN\s*\(([^)]*)$/i,
  );
  if (typeList) {
    const fragment = listValueFragment(typeList[1], cursor);
    return resourceKinds
      .filter((kind) =>
        [
          kind.native_type,
          kind.display_name,
          ...Object.values(kind.display_names ?? {}),
        ]
          .filter(Boolean)
          .some((value) =>
            String(value).toLowerCase().includes(fragment.partial),
          ),
      )
      .slice(0, 60)
      .map((kind) => ({
        key: `type:${kind.id}`,
        label: kind.native_type,
        detail: resourceTypeName(
          kind.native_type,
          kind.display_name || kind.native_type,
          kind.display_names,
          locale,
        ),
        insert: `"${kind.native_type}"`,
        replaceStart: fragment.replaceStart,
        replaceEnd: cursor,
      }));
  }

  const listValueContext = before.match(
    /([A-Za-z_][A-Za-z0-9_.-]*)\s+(?:NOT\s+)?IN\s*\(([^)]*)$/i,
  );
  if (listValueContext) {
    const field = listValueContext[1];
    const property = propertyForField(availableKinds, field);
    const values = suggestedValues(
      property,
      field,
      resourceKinds,
      fieldValues,
      locale,
    );
    const fragment = listValueFragment(listValueContext[2], cursor);
    return values
      .filter(({ value }) =>
        String(value).toLowerCase().includes(fragment.partial),
      )
      .slice(0, 60)
      .map(({ value, detail }) => ({
        key: `value:${field}:${String(value)}`,
        label: String(value),
        detail,
        insert: queryValue(value),
        replaceStart: fragment.replaceStart,
        replaceEnd: cursor,
      }));
  }

  const valueContext = before.match(
    /([A-Za-z_][A-Za-z0-9_.-]*)\s*(?:=|!=|>=|<=|>|<|CONTAINS)\s*(["']?[A-Za-z0-9:._-]*)$/i,
  );
  if (valueContext) {
    const field = valueContext[1];
    const fragment = valueContext[2];
    const replaceStart = cursor - fragment.length;
    const property = propertyForField(availableKinds, field);
    const values = suggestedValues(
      property,
      field,
      resourceKinds,
      fieldValues,
      locale,
    );
    return values
      .filter(({ value }) =>
        String(value)
          .toLowerCase()
          .includes(fragment.replace(/^["']/, "").toLowerCase()),
      )
      .slice(0, 60)
      .map(({ value, detail }) => ({
        key: `value:${field}:${String(value)}`,
        label: String(value),
        detail,
        insert: `${queryValue(value)} `,
        replaceStart,
        replaceEnd: cursor,
      }));
  }

  const operatorContext = before.match(
    /([A-Za-z_][A-Za-z0-9_.-]*)\s+([A-Za-z_]*)$/,
  );
  if (operatorContext) {
    const field = operatorContext[1];
    const partial = operatorContext[2].toUpperCase();
    const property = propertyForField(availableKinds, field);
    const builtin = BUILTIN_FIELDS.some(
      ([candidate]) => candidate.toLowerCase() === field.toLowerCase(),
    );
    if (property || builtin) {
      const operators = property?.operators?.length
        ? property.operators.map(displayOperator)
        : DEFAULT_OPERATORS;
      const replaceStart = cursor - operatorContext[2].length;
      return [...new Set(operators)]
        .filter((operator) => operator.startsWith(partial))
        .map((operator) => ({
          key: `operator:${operator}`,
          label: operator,
          insert: operator + (operator.startsWith("IS ") ? "" : " "),
          replaceStart,
          replaceEnd: cursor,
        }));
    }
  }

  const clause =
    before
      .split(/\b(?:AND|OR)\b|\(/i)
      .at(-1)
      ?.trimStart() ?? "";
  if (!clause || /^[A-Za-z_][A-Za-z0-9_.-]*$/.test(clause)) {
    const fragment = clause.toLowerCase();
    const replaceStart = cursor - clause.length;
    const fields: Suggestion[] = BUILTIN_FIELDS.filter(([field]) =>
      field.toLowerCase().includes(fragment),
    ).map(([field, labelKey]) => ({
      key: `field:${field}`,
      label: field,
      detail: t(labelKey),
      insert: `${field} `,
      replaceStart,
      replaceEnd: cursor,
    }));
    const includeProperties =
      selectedTypes.size > 0 || fragment.startsWith("properties.");
    if (includeProperties) {
      fields.push(
        ...propertiesForKinds(availableKinds)
          .filter(({ field, property }) =>
            [field, ...Object.values(property.display_names ?? {})]
              .join(" ")
              .toLowerCase()
              .includes(fragment),
          )
          .slice(0, 60)
          .map(({ field, property }) => ({
            key: `field:${field}`,
            label: field,
            detail:
              property.display_names?.[locale] ??
              property.display_names?.["en-US"] ??
              property.type,
            insert: `${field} `,
            replaceStart,
            replaceEnd: cursor,
          })),
      );
    }
    return fields.slice(0, 60);
  }

  if (/(["')]|\d|\btrue|\bfalse|\))\s*$/i.test(before)) {
    return ["AND", "OR"].map((connector) => ({
      key: `connector:${connector}`,
      label: connector,
      insert: ` ${connector} `,
      replaceStart: cursor,
      replaceEnd: cursor,
    }));
  }
  return [];
}

function extractSelectedTypes(query: string): Set<string> {
  const result = new Set<string>();
  const equality = /\b(?:type|resourceType)\s*=\s*(["'])(.*?)\1/gi;
  for (const match of query.matchAll(equality)) result.add(match[2]);
  const list = /\b(?:type|resourceType)\s+IN\s*\(([^)]*)\)/gi;
  for (const match of query.matchAll(list)) {
    for (const value of match[1].matchAll(/(["'])(.*?)\1/g)) {
      result.add(value[2]);
    }
  }
  return result;
}

function listValueFragment(list: string, cursor: number) {
  const value = list.split(",").at(-1) ?? "";
  const leadingSpace = value.length - value.trimStart().length;
  const token = value.trimStart();
  const quoteOffset = token.startsWith('"') || token.startsWith("'") ? 1 : 0;
  return {
    partial: token.slice(quoteOffset).toLowerCase(),
    replaceStart: cursor - value.length + leadingSpace,
  };
}

function suggestedValues(
  property: ResourceProperty | undefined,
  field: string,
  resourceKinds: readonly ResourceKind[],
  fieldValues: ResourceQueryFieldValues | undefined,
  locale: string,
): ResourceQueryValueSuggestion[] {
  if (property?.enum?.length) {
    return property.enum
      .filter(isResourceQueryValue)
      .map((value) => ({ value }));
  }
  if (property?.type === "boolean" || field.toLowerCase() === "dirty") {
    return [{ value: true }, { value: false }];
  }
  const canonical = field.toLowerCase();
  const supplied = Object.entries(fieldValues ?? {}).find(
    ([candidate]) => candidate.toLowerCase() === canonical,
  )?.[1];
  if (supplied?.length) return uniqueValueSuggestions(supplied);
  if (canonical === "provider") {
    return uniqueValueSuggestions(
      resourceKinds.map((kind) => ({ value: kind.provider })),
    );
  }
  if (canonical === "resourcekindid") {
    return uniqueValueSuggestions(
      resourceKinds.map((kind) => ({
        value: kind.id,
        detail: resourceTypeName(
          kind.native_type,
          kind.display_name || kind.native_type,
          kind.display_names,
          locale,
        ),
      })),
    );
  }
  return [];
}

function uniqueValueSuggestions(
  values: readonly ResourceQueryValueSuggestion[],
): ResourceQueryValueSuggestion[] {
  const result = new Map<string, ResourceQueryValueSuggestion>();
  for (const suggestion of values) {
    const key = `${typeof suggestion.value}:${String(suggestion.value)}`;
    if (!result.has(key)) result.set(key, suggestion);
  }
  return [...result.values()].sort((left, right) =>
    String(left.value).localeCompare(String(right.value)),
  );
}

function isResourceQueryValue(value: unknown): value is ResourceQueryValue {
  return ["string", "number", "boolean"].includes(typeof value);
}

function queryValue(value: unknown) {
  return typeof value === "string"
    ? `"${escapeQueryString(value)}"`
    : String(value);
}

function propertiesForKinds(kinds: readonly ResourceKind[]) {
  const result = new Map<string, ResourceProperty>();
  for (const kind of kinds) {
    for (const property of kind.properties ?? []) {
      if (!result.has(property.path)) result.set(property.path, property);
    }
  }
  return [...result.entries()]
    .map(([path, property]) => ({ field: `properties.${path}`, property }))
    .sort((left, right) => left.field.localeCompare(right.field));
}

function propertyForField(
  kinds: readonly ResourceKind[],
  field: string,
): ResourceProperty | undefined {
  if (!field.toLowerCase().startsWith("properties.")) return undefined;
  const path = field.slice("properties.".length);
  return propertiesForKinds(kinds).find((item) => item.property.path === path)
    ?.property;
}

function displayOperator(operator: string) {
  return operator.replaceAll("_", " ").toUpperCase();
}

function escapeQueryString(value: unknown) {
  return String(value).replaceAll("\\", "\\\\").replaceAll('"', '\\"');
}
