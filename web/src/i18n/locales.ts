export type Locale = "zh-CN" | "en-US";
export type LocalePreference = "auto" | Locale;

export const localePreferenceKey = "steward.locale";

export function normalizePreference(value: string | null): LocalePreference {
  return value === "zh-CN" || value === "en-US" || value === "auto"
    ? value
    : "auto";
}

export function resolveLocale(
  preference: LocalePreference,
  languages: readonly string[],
): Locale {
  if (preference !== "auto") return preference;
  for (const value of languages) {
    const normalized = value.replaceAll("_", "-").toLowerCase();
    if (
      normalized === "zh" ||
      normalized === "zh-cn" ||
      normalized.startsWith("zh-cn-") ||
      normalized === "zh-hans" ||
      normalized.startsWith("zh-hans-")
    ) {
      return "zh-CN";
    }
  }
  return "en-US";
}
