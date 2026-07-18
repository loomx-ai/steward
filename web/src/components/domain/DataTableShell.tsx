import type { ReactNode } from "react";

export function DataTableShell({
  toolbar,
  children,
  pagination,
}: {
  toolbar?: ReactNode;
  children: ReactNode;
  pagination?: ReactNode;
}) {
  return (
    <section data-slot="data-table-shell" className="w-full">
      {toolbar && (
        <div className="flex flex-wrap items-center gap-2 border-b px-2 py-3">
          {toolbar}
        </div>
      )}
      {children}
      {pagination && (
        <div className="border-t px-3 py-2.5 empty:hidden">{pagination}</div>
      )}
    </section>
  );
}
