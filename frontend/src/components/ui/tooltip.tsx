import {
  PopoverContentProps,
  PopoverProps,
  PopoverTriggerProps,
} from "@radix-ui/react-popover";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import {
  TooltipContentProps,
  TooltipProps,
  TooltipTriggerProps,
} from "@radix-ui/react-tooltip";
import * as React from "react";
import { PropsWithChildren, createContext, useContext } from "react";
import { Popover, PopoverContent, PopoverTrigger } from "./popover";

import { cn } from "src/lib/utils";

function TooltipProvider({
  delayDuration = 200,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider
      data-slot="tooltip-provider"
      delayDuration={delayDuration}
      {...props}
    />
  );
}

function Tooltip({
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  return (
    <TooltipProvider>
      <TooltipPrimitive.Root data-slot="tooltip" {...props} />
    </TooltipProvider>
  );
}

function TooltipTrigger({
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Trigger>) {
  return <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props} />;
}

// A tooltip is `bg-primary` by default — a solid brand-coloured chip, right
// for a one-line hint. "surface" instead gives it the popover surface the
// app's chart tooltips already use (see CashFlowChart's ChartTooltip), for
// tooltips holding real content: a list, a definition, anything that needs
// its own muted text or a Badge. Those inner elements take their colours
// from the page palette, which is unreadable on `bg-primary` — `muted-
// foreground` on `primary` is low-contrast grey on a saturated fill.
//
// The arrow is dropped in this variant on purpose: the primitive's arrow is
// a rotated square filled from the bubble's colour, and a bordered bubble
// would draw that border straight across it.
export type TooltipSurface = "default" | "surface";

const SURFACE_CLASSES: Record<TooltipSurface, string> = {
  default: "bg-primary text-primary-foreground",
  surface: "bg-popover text-popover-foreground border shadow-md",
};

function TooltipContent({
  className,
  sideOffset = 0,
  variant = "default",
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content> & {
  variant?: TooltipSurface;
}) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          SURFACE_CLASSES[variant],
          "animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 z-50 w-fit max-w-72 origin-(--radix-tooltip-content-transform-origin) rounded-md px-3 py-1.5 text-xs text-balance",
          className
        )}
        {...props}
      >
        {children}
        {variant === "default" && (
          <TooltipPrimitive.Arrow className="bg-primary fill-primary z-50 size-2.5 translate-y-[calc(-50%_-_2px)] rotate-45 rounded-[2px]" />
        )}
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  );
}

// Hybrid tooltip for touch devices

const TouchContext = createContext<boolean | undefined>(undefined);
const useTouch = () => useContext(TouchContext);

const TouchProvider = (props: PropsWithChildren) => {
  const isTouch = React.useMemo(
    () => window.matchMedia("(pointer: coarse)").matches,
    []
  );

  return <TouchContext.Provider value={isTouch} {...props} />;
};

const HybridTooltip = (props: TooltipProps & PopoverProps) => {
  const isTouch = useTouch();

  return isTouch ? <Popover {...props} /> : <Tooltip {...props} />;
};

const HybridTooltipTrigger = (
  props: TooltipTriggerProps & PopoverTriggerProps
) => {
  const isTouch = useTouch();

  return isTouch ? (
    <PopoverTrigger {...props} />
  ) : (
    <TooltipTrigger {...props} />
  );
};

const HybridTooltipContent = ({
  variant = "default",
  ...props
}: TooltipContentProps &
  PopoverContentProps & { variant?: TooltipSurface }) => {
  const isTouch = useTouch();

  // The touch path swaps in a Popover, which has to be told the same
  // colours — otherwise the variant would silently do nothing on a phone.
  return isTouch ? (
    <PopoverContent
      {...props}
      className={cn(SURFACE_CLASSES[variant], "text-sm", props.className)}
    />
  ) : (
    <TooltipContent variant={variant} {...props} />
  );
};

export {
  HybridTooltip as Tooltip,
  HybridTooltipContent as TooltipContent,
  TooltipProvider,
  HybridTooltipTrigger as TooltipTrigger,
  TouchProvider,
};
