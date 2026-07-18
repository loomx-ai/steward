import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";

export interface BoxSelectionActionBarProps {
  candidateKeys: readonly string[];
  onAdd: (candidateKeys: readonly string[]) => void;
  onCancel: () => void;
}

export function BoxSelectionActionBar({
  candidateKeys,
  onAdd,
  onCancel,
}: BoxSelectionActionBarProps) {
  const { t } = useLocale();
  if (candidateKeys.length === 0) return null;

  return (
    <div
      role="toolbar"
      aria-label={t("panorama.boxSelectionActions")}
      className="absolute bottom-3 left-1/2 z-20 flex h-12 -translate-x-1/2 items-center gap-1.5 rounded-lg border bg-card px-2 shadow-sm"
    >
      <span className="whitespace-nowrap text-sm">
        {t("panorama.selectedCount", { count: candidateKeys.length })}
      </span>
      <Button
        type="button"
        className="px-3"
        onClick={() => {
          onAdd(candidateKeys);
          onCancel();
        }}
      >
        {t("panorama.addSelected")}
      </Button>
      <Button
        type="button"
        variant="ghost"
        className="px-2.5"
        onClick={onCancel}
      >
        {t("common.cancel")}
      </Button>
    </div>
  );
}
