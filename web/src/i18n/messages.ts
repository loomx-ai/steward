import type { MessageKey, Messages } from "./en-US";
import type { Locale } from "./locales";

export type { MessageKey, Messages };

// Each locale's dictionary is its own chunk so the entry bundle only carries
// the one the user reads. loadMessages must resolve before a locale renders.
const loaders: Record<Locale, () => Promise<{ default: Messages }>> = {
  "en-US": () => import("./en-US"),
  "zh-CN": () => import("./zh-CN"),
};
const loaded: Partial<Record<Locale, Messages>> = {};

export async function loadMessages(locale: Locale): Promise<Messages> {
  return (loaded[locale] ??= (await loaders[locale]()).default);
}

export function loadedMessages(locale: Locale): Messages | undefined {
  return loaded[locale];
}

export function translate(
  dictionary: Messages,
  key: MessageKey,
  values: Record<string, string | number> = {},
): string {
  return Object.entries(values).reduce(
    (result, [name, value]) => result.replaceAll(`{${name}}`, String(value)),
    dictionary[key],
  );
}

export function translateCode(
  dictionary: Messages,
  code: string,
  fallback: string,
  values: Record<string, string | number> = {},
): string {
  const key = `error.${code}` as MessageKey;
  if (!Object.prototype.hasOwnProperty.call(dictionary, key)) {
    return fallback;
  }
  return translate(dictionary, key, values);
}
