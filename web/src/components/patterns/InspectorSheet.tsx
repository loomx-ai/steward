import { useWorkspace } from "@/app/WorkspaceContext";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";

export function InspectorSheet() {
  const { inspector, closeInspector } = useWorkspace();

  return (
    <Sheet
      open={Boolean(inspector)}
      onOpenChange={(open) => !open && closeInspector()}
    >
      <SheetContent
        side="right"
        className="w-screen gap-0 p-0 sm:max-w-md"
        {...(!inspector?.description ? { "aria-describedby": undefined } : {})}
        onCloseAutoFocus={(event) => event.preventDefault()}
      >
        {inspector && (
          <>
            <SheetHeader className="border-b px-5 py-4 pr-12">
              <SheetTitle className="text-base">{inspector.title}</SheetTitle>
              {inspector.description && (
                <SheetDescription>{inspector.description}</SheetDescription>
              )}
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="p-5">{inspector.body}</div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
