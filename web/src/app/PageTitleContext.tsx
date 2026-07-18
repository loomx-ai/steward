import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useLocation } from "react-router-dom";
import { useLocale } from "@/i18n/LocaleProvider";
import { navigationItemForPath } from "./navigation";

export interface PageTitleParent {
  label: string;
  to: string;
}

export interface PageTitleDescriptor {
  title: string;
  parent?: PageTitleParent;
}

interface PageTitleRegistration extends PageTitleDescriptor {
  pathname: string;
  token: symbol;
}

interface PageTitleContextValue {
  page: PageTitleDescriptor;
  register: (pathname: string, descriptor: PageTitleDescriptor) => () => void;
}

const fallbackContext: PageTitleContextValue = {
  page: { title: "Steward" },
  register: () => () => undefined,
};

const PageTitleContext = createContext<PageTitleContextValue>(fallbackContext);

export function PageTitleProvider({ children }: { children: ReactNode }) {
  const location = useLocation();
  const { t } = useLocale();
  const [registration, setRegistration] =
    useState<PageTitleRegistration | null>(null);
  const route = navigationItemForPath(location.pathname);
  const fallbackTitle = location.pathname.startsWith("/panorama")
    ? t("nav.panorama")
    : t(route?.label ?? "nav.assets");
  const page = useMemo<PageTitleDescriptor>(() => {
    if (registration?.pathname === location.pathname) {
      return {
        title: registration.title,
        parent: registration.parent,
      };
    }
    return { title: fallbackTitle };
  }, [fallbackTitle, location.pathname, registration]);

  const register = useCallback(
    (pathname: string, descriptor: PageTitleDescriptor) => {
      const token = Symbol("page-title");
      setRegistration({ ...descriptor, pathname, token });
      return () => {
        setRegistration((current) =>
          current?.token === token ? null : current,
        );
      };
    },
    [],
  );

  useEffect(() => {
    document.title = `${page.title} · Steward`;
    return () => {
      document.title = "Steward";
    };
  }, [page.title]);

  const value = useMemo(() => ({ page, register }), [page, register]);

  return (
    <PageTitleContext.Provider value={value}>
      {children}
    </PageTitleContext.Provider>
  );
}

export function PageTitle({
  title,
  parent,
}: {
  title: string;
  parent?: PageTitleParent;
}) {
  const location = useLocation();
  const { register } = useContext(PageTitleContext);
  const parentLabel = parent?.label;
  const parentTo = parent?.to;

  useLayoutEffect(
    () =>
      register(location.pathname, {
        title,
        parent:
          parentLabel && parentTo
            ? { label: parentLabel, to: parentTo }
            : undefined,
      }),
    [location.pathname, parentLabel, parentTo, register, title],
  );

  return null;
}

export function useResolvedPageTitle() {
  return useContext(PageTitleContext).page;
}
