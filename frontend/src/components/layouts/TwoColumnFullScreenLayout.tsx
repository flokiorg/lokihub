import { Outlet } from "react-router-dom";
import { CompactLanguageSwitcher } from "src/components/CompactLanguageSwitcher";
import { useInfo } from "src/hooks/useInfo";
import { useLocale } from "src/hooks/useLocale";

export default function TwoColumnFullScreenLayout() {
  const { data: info } = useInfo();
  const { isRTL } = useLocale();

  return (
    // dir="ltr" prevents the grid columns from reversing in RTL languages.
    // The form panel re-applies the document direction for its content.
    <div dir="ltr" className="w-full lg:grid lg:h-screen lg:grid-cols-2 lg:overflow-hidden items-stretch text-background">
      <div className="hidden lg:flex flex-col bg-muted/20 justify-end p-10 relative overflow-hidden bg-white/100">
        <img
          src="/images/lokilight.svg"
          alt="Floki Sun Logo"
          className="absolute inset-0 w-full h-full object-cover object-center"
        />
        <div className="absolute inset-0" style={{ background: "linear-gradient(to top, rgba(0,0,0,0.8), rgba(0,0,0,0.2), transparent)" }} />

        <div className="flex-1 w-full h-full flex flex-col relative z-10 pointer-events-none">
          <div className="flex flex-row justify-end items-center mt-5">
            {info?.version && (
              <p className="text-sm text-white/90 bg-black/40 backdrop-blur-md px-3 py-1.5 rounded-full font-mono border border-white/10 shadow-sm">{info.version}</p>
            )}
          </div>
        </div>

        <div className="flex flex-col relative z-10 text-start">
          <h1
            className="text-4xl font-black text-white tracking-tight mb-6"
            style={{ textShadow: "0 4px 12px rgba(0,0,0,0.6)" }}
          >
            Your Gateway to the<br /> Lightning Network
          </h1>
          <p
            className="text-white/90 text-xl font-medium leading-relaxed max-w-lg"
            style={{ textShadow: "0 2px 4px rgba(0,0,0,0.5)" }}
          >
            Manage your channels, connect apps, and make instant payments.
          </p>
        </div>
      </div>
      <div
        dir={isRTL ? "rtl" : "ltr"}
        className="flex justify-center py-12 text-foreground relative bg-background min-h-screen lg:min-h-0 lg:h-full lg:overflow-y-auto"
      >
        <Outlet />
        <div className="absolute top-4 end-4 z-50">
          <CompactLanguageSwitcher showLabel />
        </div>
      </div>
    </div>
  );
}
