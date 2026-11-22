import { useTranslation } from "react-i18next";
import { NavLink, Outlet, useLocation } from "react-router";
import AppHeader from "src/components/AppHeader";
import { useLspSummary } from "src/hooks/useLsp";
import { cn } from "src/lib/utils";

// One home for the LSP operator section. Before this, /lsp was a bare redirect
// with no chrome, so Orders/Peers/Analytics/Config were only reachable by URL.
// This layout gives the section a single header plus an underline tab bar (the
// same look as the detail-page section tabs), and every child renders inside
// it — so the tabs are always present and the pages are reachable. Child
// screens therefore no longer render their own AppHeader.
const TABS = [
  { key: "overview", to: "/lsp", end: true },
  { key: "orders", to: "/lsp/orders", end: false },
  { key: "peers", to: "/lsp/peers", end: false },
  { key: "analytics", to: "/lsp/analytics", end: false },
  { key: "config", to: "/lsp/config", end: false },
] as const;

export function LspLayout() {
  const { t } = useTranslation("lsp");
  const location = useLocation();
  // The node's live state drives one header pill. Summary is the same request
  // the Overview makes (SWR dedupes it), so this adds no extra fetch.
  const { data: summary } = useLspSummary();
  const online = summary?.node_status === "online";
  const synced = summary?.synced_to_chain;

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("header.title")}
        description={t("header.subtitle")}
        contentRight={
          summary ? (
            <span
              className={cn(
                "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium",
                online
                  ? "border-positive-foreground/30 bg-positive text-positive-foreground"
                  : "border-border bg-muted text-muted-foreground"
              )}
            >
              <span className="size-1.5 rounded-full bg-current" />
              {online ? t("header.online") : t("header.offline")}
              {online && synced ? ` · ${t("header.synced")}` : ""}
            </span>
          ) : undefined
        }
      />

      {/* Underline tab bar — NavLinks (these are routes, not local tab state),
          styled to match the detail-page section tabs. Wraps instead of
          scrolling on narrow widths. */}
      <nav className="flex flex-wrap items-center gap-x-1 border-b">
        {TABS.map((tab) => {
          // Mark Overview active only on the exact /lsp path; the others match
          // their prefix so /lsp/orders/:id keeps "Orders" lit.
          const active = tab.end
            ? location.pathname === tab.to
            : location.pathname.startsWith(tab.to);
          return (
            <NavLink
              key={tab.key}
              to={tab.to}
              end={tab.end}
              className={cn(
                "-mb-px border-b-2 px-3 pb-2.5 pt-1 text-sm font-medium transition-colors",
                active
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              )}
            >
              {t(`tabs.${tab.key}`)}
            </NavLink>
          );
        })}
      </nav>

      <Outlet />
    </div>
  );
}
