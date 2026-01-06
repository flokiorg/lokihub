import { ComponentProps } from "react";

interface LokihubLogoProps extends ComponentProps<"div"> {
  invert?: boolean;
}

import LokiLightHead from "src/assets/loki-light-head.svg?react";

export function LokihubLogo({ invert = false, className, ...props }: LokihubLogoProps) {
  return (
    <div className={`flex items-center gap-2 ${className}`} {...props}>
      <LokiLightHead
        className="h-10 w-10 object-contain drop-shadow-[0_0_15px_#da9526]" 
      />
      <span className={`font-bold text-2xl tracking-tighter ${invert ? "text-white" : "text-primary"}`}>
        Lokihub
      </span>
    </div>
  );
}
