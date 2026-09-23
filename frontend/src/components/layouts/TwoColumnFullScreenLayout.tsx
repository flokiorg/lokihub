import React from "react";
import { Outlet, ScrollRestoration, useLocation } from "react-router-dom";
import { CompactLanguageSwitcher } from "src/components/CompactLanguageSwitcher";
import { useInfo } from "src/hooks/useInfo";
import { useLocale } from "src/hooks/useLocale";

export default function TwoColumnFullScreenLayout() {
  const { data: info } = useInfo();
  const { isRTL } = useLocale();
  const { pathname } = useLocation();
  const panelRef = React.useRef<HTMLDivElement>(null);

  // This layout's form panel scrolls ITSELF at lg: (lg:overflow-y-auto below),
  // not the window — so <ScrollRestoration/>, which only manages window
  // scroll, cannot reach it. Without this, stepping between setup screens
  // keeps the previous screen's offset and lands mid-form.
  React.useEffect(() => {
    panelRef.current?.scrollTo({ top: 0 });
  }, [pathname]);

  return (
    <>
      {/* Below lg: the window scrolls, so the built-in still applies here. */}
      {/* Below lg: the window is what scrolls, so the built-in applies here
          too; the panel effect above covers the lg: case it cannot reach. */}
      <ScrollRestoration />
      {/* dir="ltr" prevents the grid columns from reversing in RTL languages.
          The form panel re-applies the document direction for its content. */}
      <div
        dir="ltr"
        className="w-full lg:grid lg:h-[calc(100vh-var(--app-titlebar-height))] lg:grid-cols-2 lg:overflow-hidden items-stretch text-background"
      >
        <div className="hidden lg:flex flex-col justify-end p-10 pt-[calc(2.5rem+var(--app-titlebar-overlay-height))] relative overflow-hidden bg-white/100">
          <img
            src="/images/lokilight.svg"
            alt="Floki Sun Logo"
            className="absolute inset-0 w-full h-full object-cover object-center"
          />
          <div
            className="absolute inset-0"
            style={{
              background:
                "linear-gradient(to top, rgba(0,0,0,0.8), rgba(0,0,0,0.2), transparent)",
            }}
          />

          <div className="flex-1 w-full h-full flex flex-col relative z-10 pointer-events-none">
            <div className="flex flex-row justify-end items-center mt-5">
              {info?.version && (
                <p className="text-sm text-white/90 bg-black/40 backdrop-blur-md px-3 py-1.5 rounded-full font-mono border border-white/10 shadow-sm">
                  {info.version}
                </p>
              )}
            </div>
          </div>

          <div className="flex flex-col relative z-10 text-start">
            <h1 className="text-4xl font-black text-white tracking-tight mb-6 text-hero-heading">
              Your Hub, Your Rules
            </h1>
            <p className="text-white/90 text-xl font-medium leading-relaxed max-w-lg text-hero-subtitle">
              Manage your channels, connect apps, and make instant payments.
            </p>
          </div>
        </div>
        <div
          ref={panelRef}
          dir={isRTL ? "rtl" : "ltr"}
          className="flex justify-center py-12 pt-[calc(3rem+var(--app-titlebar-overlay-height))] text-foreground relative bg-background min-h-screen lg:min-h-0 lg:h-full lg:overflow-y-auto"
        >
          <Outlet />
          <div className="absolute top-[calc(1rem+var(--app-titlebar-overlay-height))] end-4 z-50">
            <CompactLanguageSwitcher showLabel />
          </div>
        </div>
      </div>
    </>
  );
}
