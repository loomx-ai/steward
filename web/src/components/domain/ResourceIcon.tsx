import type { CSSProperties } from "react";
import {
  Boxes,
  HardDrive,
  Layers3,
  Network,
  Shield,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";

const resourceIconTokens: Readonly<Record<string, LucideIcon>> = {
  "/icons/alicloud/acs-ecs-image.png": Layers3,
  cluster: Boxes,
  disk: HardDrive,
  network: Network,
  shield: Shield,
  stack: Layers3,
};

export function ResourceIcon({
  icon,
  className,
}: {
  icon?: string;
  className?: string;
}) {
  const TokenIcon = icon ? resourceIconTokens[icon] : undefined;
  if (TokenIcon) {
    return (
      <TokenIcon
        aria-hidden="true"
        className={cn(
          "mt-0.5 size-7 shrink-0 text-muted-foreground",
          className,
        )}
      />
    );
  }
  if (icon && isImageSource(icon)) {
    const maskImage = `url(${icon})`;
    const style: CSSProperties = {
      maskImage,
      maskPosition: "center",
      maskRepeat: "no-repeat",
      maskSize: "contain",
      WebkitMaskImage: maskImage,
      WebkitMaskPosition: "center",
      WebkitMaskRepeat: "no-repeat",
      WebkitMaskSize: "contain",
    };
    return (
      <span
        aria-hidden="true"
        data-resource-icon-mask
        className={cn(
          "mt-0.5 block size-7 shrink-0 bg-current text-foreground",
          className,
        )}
        style={style}
      />
    );
  }
  return (
    <Boxes
      aria-hidden="true"
      className={cn("mt-0.5 size-7 shrink-0 text-muted-foreground", className)}
    />
  );
}

function isImageSource(value: string): boolean {
  return /^(?:https?:|data:|\/|\.\/|\.\.\/)/.test(value);
}
