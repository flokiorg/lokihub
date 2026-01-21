import { ComponentProps, useState } from "react";

interface LokihubLogoProps extends ComponentProps<"div"> {
  invert?: boolean;
  iconClassName?: string;
  alias?: string;
}

import LokiLightHead from "src/assets/loki-light-head.svg?react";
import { cn } from "src/lib/utils";

export function LokihubLogo({
  invert = false,
  className,
  iconClassName,
  alias,
  ...props
}: LokihubLogoProps) {
  const [isHovered, setIsHovered] = useState(false);

  return (
    <div className={`flex items-center gap-2 ${className}`} {...props}>
      <div 
        onMouseEnter={() => setIsHovered(true)} 
        onMouseLeave={() => setIsHovered(false)}
        className="cursor-pointer"
      >
        <LokiLightHead
          className={cn(
            "h-10 w-10 object-contain drop-shadow-[0_0_15px_#da9526]",
            iconClassName
          )}
        />
      </div>
      <div className="relative min-w-40 max-w-64 [perspective:500px]">
        <div
          className={cn(
            "relative w-full transition-transform duration-1000 [transform-style:preserve-3d] [transform:rotateX(0deg)] grid",
            alias && isHovered && "[transform:rotateX(180deg)]"
          )}
        >
          {/* Front Face: "Lokihub" */}
          <span
            className={cn(
              "col-start-1 row-start-1 flex items-center font-bold text-2xl tracking-tighter [backface-visibility:hidden]",
              invert ? "text-white" : "text-primary"
            )}
          >
            Lokihub
          </span>

          {/* Back Face: Alias */}
          <span
            className={cn(
              "col-start-1 row-start-1 flex items-center font-bold text-2xl tracking-tighter [backface-visibility:hidden] [transform:rotateX(180deg)] overflow-hidden text-ellipsis whitespace-nowrap",
              invert ? "text-white" : "text-primary"
            )}
          >
            {alias || "Lokihub"}
          </span>
        </div>
      </div>
    </div>
  );
}
