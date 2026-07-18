import { useEffect, useRef, useState } from "react";
import {
  Hand,
  Maximize2,
  Minus,
  MousePointer2,
  Plus,
  Waypoints,
  type LucideIcon,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import type { CanvasMode } from "./boxSelection";

interface ModeTool {
  mode: CanvasMode;
  label: MessageKey;
  shortcut: string;
  icon: LucideIcon;
}

const modeTools: readonly ModeTool[] = [
  {
    mode: "select",
    label: "panorama.toolSelect",
    shortcut: "V",
    icon: MousePointer2,
  },
  {
    mode: "pan",
    label: "panorama.toolPan",
    shortcut: "H",
    icon: Hand,
  },
];

export interface CanvasToolbarProps {
  mode: CanvasMode;
  zoom: number;
  onZoomChange: (zoom: number) => void;
  onModeChange: (mode: CanvasMode) => void;
  relationshipsVisible?: boolean;
  onRelationshipsVisibilityChange?: (visible: boolean) => void;
  onEscape: () => void;
  onZoomOut: () => void;
  onFitView: () => void;
  onZoomIn: () => void;
}

function isEditableElement(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (target instanceof HTMLElement) {
    if (target.isContentEditable) return true;
    if (
      target.contentEditable === "true" ||
      target.contentEditable === "plaintext-only"
    ) {
      return true;
    }
  }
  const editableAncestor = target.closest<HTMLElement>("[contenteditable]");
  return (
    editableAncestor?.isContentEditable === true ||
    editableAncestor?.contentEditable === "true" ||
    editableAncestor?.contentEditable === "plaintext-only" ||
    target.matches("input, textarea, select")
  );
}

export function CanvasToolbar({
  mode,
  zoom,
  onZoomChange,
  onModeChange,
  relationshipsVisible = false,
  onRelationshipsVisibilityChange,
  onEscape,
  onZoomOut,
  onFitView,
  onZoomIn,
}: CanvasToolbarProps) {
  const { t } = useLocale();
  const [editingZoom, setEditingZoom] = useState(false);
  const [zoomDraft, setZoomDraft] = useState(() => editableZoomValue(zoom));
  const cancelZoomEdit = useRef(false);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (isEditableElement(event.target)) return;
      const key = event.key.toLowerCase();
      if (key === "v") {
        event.preventDefault();
        onModeChange("select");
      } else if (key === "h") {
        event.preventDefault();
        onModeChange("pan");
      } else if (event.key === "Escape") {
        event.preventDefault();
        onEscape();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onEscape, onModeChange]);

  useEffect(() => {
    if (!editingZoom) setZoomDraft(editableZoomValue(zoom));
  }, [editingZoom, zoom]);

  const commitZoom = () => {
    setEditingZoom(false);
    if (cancelZoomEdit.current) {
      cancelZoomEdit.current = false;
      setZoomDraft(editableZoomValue(zoom));
      return;
    }
    const percentage = Number(zoomDraft.replace(",", "."));
    if (!Number.isFinite(percentage) || percentage <= 0) {
      setZoomDraft(editableZoomValue(zoom));
      return;
    }
    setZoomDraft(editableZoomValue(percentage / 100));
    onZoomChange(percentage / 100);
  };

  return (
    <div
      role="toolbar"
      aria-label={t("panorama.canvasTools")}
      className="absolute bottom-3 left-3 z-20 flex items-center gap-1 rounded-lg border bg-card p-1 shadow-sm"
    >
      {modeTools.map((item) => (
        <Tooltip key={item.mode}>
          <TooltipTrigger asChild>
            <Button
              type="button"
              size="icon-sm"
              variant={mode === item.mode ? "secondary" : "ghost"}
              aria-label={t(item.label)}
              aria-pressed={mode === item.mode}
              onClick={() => onModeChange(item.mode)}
            >
              <item.icon aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>
            {t(item.label)} <Kbd>{item.shortcut}</Kbd>
          </TooltipContent>
        </Tooltip>
      ))}
      <div
        role="separator"
        aria-orientation="vertical"
        className="mx-0.5 h-5 w-px bg-border"
      />
      {onRelationshipsVisibilityChange && (
        <>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                size="icon-sm"
                variant={relationshipsVisible ? "secondary" : "ghost"}
                aria-label={t(
                  relationshipsVisible
                    ? "panorama.hideRelationshipLines"
                    : "panorama.showRelationshipLines",
                )}
                aria-pressed={relationshipsVisible}
                onClick={() =>
                  onRelationshipsVisibilityChange(!relationshipsVisible)
                }
              >
                <Waypoints aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {t(
                relationshipsVisible
                  ? "panorama.hideRelationshipLines"
                  : "panorama.showRelationshipLines",
              )}
            </TooltipContent>
          </Tooltip>
          <div
            role="separator"
            aria-orientation="vertical"
            className="mx-0.5 h-5 w-px bg-border"
          />
        </>
      )}
      <div className="flex items-center gap-1" data-canvas-zoom-controls>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              className="size-6"
              aria-label={t("panorama.toolZoomOut")}
              onClick={onZoomOut}
            >
              <Minus aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>
            {t("panorama.toolZoomOut")} <Kbd>−</Kbd>
          </TooltipContent>
        </Tooltip>
        <label
          className="flex h-6 w-10 items-center justify-center rounded-md text-[11px] leading-none font-medium text-muted-foreground tabular-nums focus-within:bg-accent focus-within:text-accent-foreground"
          data-canvas-zoom
        >
          <span className="sr-only">{t("panorama.zoomLevel")}</span>
          <input
            type="text"
            inputMode="decimal"
            aria-label={t("panorama.zoomLevel")}
            className="shrink-0 bg-transparent text-right text-inherit outline-none"
            style={{
              width: `${Math.max(1, Math.min(6, zoomDraft.length))}ch`,
            }}
            value={zoomDraft}
            onFocus={(event) => {
              cancelZoomEdit.current = false;
              setEditingZoom(true);
              event.currentTarget.select();
            }}
            onChange={(event) => {
              const next = event.currentTarget.value.replaceAll("%", "").trim();
              if (/^\d{0,3}(?:[.,]\d{0,2})?$/.test(next)) {
                setZoomDraft(next);
              }
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                event.currentTarget.blur();
              } else if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                cancelZoomEdit.current = true;
                event.currentTarget.blur();
              }
            }}
            onBlur={commitZoom}
          />
          <span aria-hidden="true">%</span>
        </label>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              className="size-6"
              aria-label={t("panorama.toolZoomIn")}
              onClick={onZoomIn}
            >
              <Plus aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>
            {t("panorama.toolZoomIn")} <Kbd>+</Kbd>
          </TooltipContent>
        </Tooltip>
      </div>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            className="size-7"
            aria-label={t("panorama.toolFitView")}
            onClick={onFitView}
          >
            <Maximize2 aria-hidden="true" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>
          {t("panorama.toolFitView")} <Kbd>F</Kbd>
        </TooltipContent>
      </Tooltip>
    </div>
  );
}

function editableZoomValue(zoom: number): string {
  const percentage = Number.isFinite(zoom) ? Math.max(0, zoom * 100) : 100;
  if (percentage < 10) {
    return String(Number(percentage.toFixed(2)));
  }
  return String(Math.round(percentage));
}
