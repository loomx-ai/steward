import { Link, useLocation } from "react-router-dom";
import {
  PageHeaderActionsTarget,
  PageHeaderNavigationTarget,
} from "./PageHeaderActionsContext";
import { useResolvedPageTitle } from "./PageTitleContext";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { SidebarTrigger, useSidebar } from "@/components/ui/sidebar";

export function AppHeader() {
  const { t } = useLocale();
  const { isMobile } = useSidebar();
  const location = useLocation();
  const page = useResolvedPageTitle();
  const currentPath = `${location.pathname}${location.search}`;
  const panoramaTitleAction =
    !page.parent &&
    (location.pathname === "/panorama" ||
      location.pathname.startsWith("/panorama/"));
  const panoramaHasBreadcrumbs =
    panoramaTitleAction &&
    location.pathname !== "/panorama" &&
    location.pathname !== "/panorama/";

  return (
    <header className="sticky top-0 z-20 flex h-[52px] shrink-0 items-center gap-2 bg-background/95 px-3 backdrop-blur-md sm:px-4">
      {isMobile && <SidebarTrigger aria-label={t("shell.toggleSidebar")} />}
      <div className="flex min-w-0 flex-1 items-center gap-1">
        {page.parent ? (
          <Breadcrumb aria-label={t("shell.breadcrumb")} className="min-w-0">
            <BreadcrumbList className="flex-nowrap">
              <BreadcrumbItem className="shrink-0">
                <BreadcrumbLink asChild className="max-w-full truncate">
                  <Link to={page.parent.to} title={page.parent.label}>
                    {page.parent.label}
                  </Link>
                </BreadcrumbLink>
              </BreadcrumbItem>
              <BreadcrumbSeparator />
              <BreadcrumbItem className="min-w-0">
                <h1 className="min-w-0 truncate text-sm font-semibold">
                  <BreadcrumbLink asChild className="max-w-full truncate">
                    <Link
                      to={currentPath}
                      title={page.title}
                      aria-current="page"
                    >
                      {page.title}
                    </Link>
                  </BreadcrumbLink>
                </h1>
              </BreadcrumbItem>
            </BreadcrumbList>
          </Breadcrumb>
        ) : (
          <h1
            className={`shrink-0 truncate text-sm ${
              panoramaHasBreadcrumbs
                ? "font-normal text-muted-foreground"
                : "font-semibold"
            }`}
            title={page.title}
          >
            {panoramaTitleAction ? (
              <BreadcrumbLink
                asChild
                className={
                  panoramaHasBreadcrumbs
                    ? "font-normal"
                    : "font-semibold text-foreground"
                }
              >
                <button
                  type="button"
                  aria-current={panoramaHasBreadcrumbs ? undefined : "page"}
                  onClick={() =>
                    window.dispatchEvent(new Event("steward:panorama-root"))
                  }
                >
                  {page.title}
                </button>
              </BreadcrumbLink>
            ) : (
              page.title
            )}
          </h1>
        )}
        <PageHeaderNavigationTarget />
      </div>
      <PageHeaderActionsTarget ariaLabel={t("common.actions")} />
    </header>
  );
}
