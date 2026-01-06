import { Outlet } from "react-router-dom";
import { useInfo } from "src/hooks/useInfo";

export default function TwoColumnFullScreenLayout() {
  const { data: info } = useInfo();

  return (
    <div className="w-full lg:grid lg:h-screen lg:grid-cols-2 lg:overflow-hidden items-stretch text-background">
      <div className="hidden lg:flex flex-col bg-muted/20 justify-end p-10 relative overflow-hidden bg-white/100">
        <img
          src="/images/lokilight.svg"
          alt="Floki Sun Logo"
          className="absolute inset-0 w-full h-full object-cover object-center"
        />
        {/* Modern clean gradient overlay for readability */}
        <div className="absolute inset-0 bg-gradient-to-t from-black/80 via-black/20 to-transparent" />
        
        <div className="flex-1 w-full h-full flex flex-col relative z-10 pointer-events-none">
          <div className="flex flex-row justify-end items-center mt-5">
            {info?.version && (
              <p className="text-sm text-white/90 bg-black/40 backdrop-blur-md px-3 py-1.5 rounded-full font-mono border border-white/10 shadow-sm">{info.version}</p>
            )}
          </div>
        </div>
        
        <div className="flex flex-col relative z-10 text-left">
          <h1 className="text-5xl font-black text-white tracking-tight mb-6 drop-shadow-lg">
            Your Gateway to the<br /> Lightning Network
          </h1>
          <p className="text-white/90 text-xl font-medium leading-relaxed max-w-lg drop-shadow-md">
            Manage your channels, connect apps, and experience instant operations with the power of Lokichain.
          </p>
        </div>
      </div>
      <div className="flex justify-center py-12 text-foreground relative bg-background min-h-screen lg:min-h-0 lg:h-full lg:overflow-y-auto">
        <Outlet />
      </div>
    </div>
  );
}
