import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  localePreferenceKey,
  normalizePreference,
  resolveLocale,
  type Locale,
  type LocalePreference,
} from "./locales";
import {
  messages,
  translate,
  translateCode,
  type MessageKey,
} from "./messages";

interface LocaleContextValue {
  locale: Locale;
  preference: LocalePreference;
  setPreference: (value: LocalePreference) => void;
  t: (key: MessageKey, values?: Record<string, string | number>) => string;
  label: (value: string) => string;
  messageForCode: (
    code: string,
    fallback: string,
    details?: Record<string, unknown>,
  ) => string;
  formatError: (error: unknown) => string;
  formatDate: (value: string | Date) => string;
  formatTime: (value: string | Date) => string;
  formatNumber: (value: number) => string;
}

const LocaleContext = createContext<LocaleContextValue | null>(null);

export function LocaleProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<LocalePreference>(() =>
    normalizePreference(localStorage.getItem(localePreferenceKey)),
  );
  const [languages, setLanguages] = useState<readonly string[]>(() =>
    browserLanguages(),
  );
  const locale = resolveLocale(preference, languages);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => {
    const update = () => setLanguages(browserLanguages());
    window.addEventListener("languagechange", update);
    return () => window.removeEventListener("languagechange", update);
  }, []);

  const setPreference = useCallback((value: LocalePreference) => {
    localStorage.setItem(localePreferenceKey, value);
    setPreferenceState(value);
  }, []);
  const t = useCallback(
    (key: MessageKey, values?: Record<string, string | number>) =>
      translate(messages[locale], key, values),
    [locale],
  );
  const label = useCallback(
    (value: string) => {
      const key = `domain.${value.toLowerCase()}` as MessageKey;
      return key in messages[locale]
        ? messages[locale][key]
        : value.replaceAll("_", " ");
    },
    [locale],
  );
  const messageForCode = useCallback(
    (code: string, fallback: string, details: Record<string, unknown> = {}) =>
      translateCode(locale, code, fallback, interpolationValues(details)),
    [locale],
  );
  const formatError = useCallback(
    (error: unknown) => {
      if (!error) return "";
      if (typeof error !== "object") return String(error);
      const value = error as {
        code?: unknown;
        message?: unknown;
        details?: unknown;
        requestID?: unknown;
        request_id?: unknown;
      };
      const fallback =
        typeof value.message === "string" ? value.message : String(error);
      const details =
        value.details &&
        typeof value.details === "object" &&
        !Array.isArray(value.details)
          ? (value.details as Record<string, unknown>)
          : undefined;
      const message =
        typeof value.code === "string"
          ? messageForCode(value.code, fallback, details)
          : fallback;
      const requestID =
        typeof value.requestID === "string"
          ? value.requestID
          : typeof value.request_id === "string"
            ? value.request_id
            : "";
      const parts = [message];
      if (
        value.code === "credential_validation_failed" &&
        details !== undefined
      ) {
        const providerMessage =
          typeof details.provider_message === "string"
            ? details.provider_message.trim()
            : "";
        const providerCode =
          typeof details.provider_code === "string"
            ? details.provider_code.trim()
            : "";
        const providerRequestID =
          typeof details.provider_request_id === "string"
            ? details.provider_request_id.trim()
            : "";
        if (providerMessage || providerCode) {
          const diagnostic = providerMessage || providerCode;
          const codeSuffix =
            providerMessage && providerCode ? ` (${providerCode})` : "";
          parts.push(
            `${translate(messages[locale], "common.providerError")}: ${diagnostic}${codeSuffix}`,
          );
        }
        if (providerRequestID) {
          parts.push(
            `${translate(messages[locale], "common.providerRequestId")}: ${providerRequestID}`,
          );
        }
      }
      if (requestID) {
        parts.push(
          `${translate(messages[locale], "common.requestId")}: ${requestID}`,
        );
      }
      return parts.join(" · ");
    },
    [locale, messageForCode],
  );
  const dateTimeFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(locale, {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hourCycle: "h23",
      }),
    [locale],
  );
  const numberFormatter = useMemo(
    () => new Intl.NumberFormat(locale),
    [locale],
  );
  const value = useMemo<LocaleContextValue>(
    () => ({
      locale,
      preference,
      setPreference,
      t,
      label,
      messageForCode,
      formatError,
      formatDate: (date) => {
        const parts = dateTimeParts(dateTimeFormatter, date);
        return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
      },
      formatTime: (date) => {
        const parts = dateTimeParts(dateTimeFormatter, date);
        return `${parts.hour}:${parts.minute}:${parts.second}`;
      },
      formatNumber: (number) => numberFormatter.format(number),
    }),
    [
      locale,
      preference,
      setPreference,
      t,
      label,
      messageForCode,
      formatError,
      dateTimeFormatter,
      numberFormatter,
    ],
  );
  return (
    <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>
  );
}

export function useLocale(): LocaleContextValue {
  const value = useContext(LocaleContext);
  if (!value) throw new Error("useLocale must be used inside LocaleProvider");
  return value;
}

function dateTimeParts(formatter: Intl.DateTimeFormat, value: string | Date) {
  return Object.fromEntries(
    formatter
      .formatToParts(new Date(value))
      .filter((part) => part.type !== "literal")
      .map((part) => [part.type, part.value]),
  );
}

function browserLanguages(): readonly string[] {
  if (navigator.languages?.length) return navigator.languages;
  return navigator.language ? [navigator.language] : [];
}

function interpolationValues(
  details: Record<string, unknown>,
): Record<string, string | number> {
  const result: Record<string, string | number> = {};
  for (const [key, value] of Object.entries(details)) {
    if (typeof value === "string" || typeof value === "number") {
      result[key] = value;
    }
  }
  return result;
}
