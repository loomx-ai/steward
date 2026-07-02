import type { ReactNode } from "react";

export function ConfirmDialog({
  open,
  title,
  confirmLabel,
  cancelLabel,
  canConfirm = true,
  onConfirm,
  onCancel,
  children,
}: {
  open: boolean;
  title: string;
  confirmLabel: string;
  cancelLabel: string;
  canConfirm?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  children: ReactNode;
}) {
  if (!open) return null;
  return (
    <div className="confirm-overlay" onClick={onCancel}>
      <div
        className="confirm-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(event) => event.stopPropagation()}
      >
        <h3 className="confirm-title">{title}</h3>
        <div className="confirm-body">{children}</div>
        <div className="confirm-actions">
          <button className="icon-button" onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            className="primary-button"
            onClick={onConfirm}
            disabled={!canConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
