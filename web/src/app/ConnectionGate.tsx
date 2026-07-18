import { CloudCog } from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";

export function ConnectionGate() {
  const { t } = useLocale();
  return (
    <div className="mx-auto grid min-h-[60vh] max-w-md place-items-center px-6 py-12 text-center">
      <div className="space-y-4">
        <div className="mx-auto flex size-9 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          <CloudCog className="size-4" />
        </div>
        <h2 className="text-xl font-semibold tracking-tight">
          {t("connectionContext.requiredTitle")}
        </h2>
        <Button asChild>
          <Link to="/settings?section=connections">
            {t("connectionContext.configure")}
          </Link>
        </Button>
      </div>
    </div>
  );
}
