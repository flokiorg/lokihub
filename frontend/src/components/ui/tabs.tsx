import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";

import { cn } from "src/lib/utils";

const Tabs = TabsPrimitive.Root;

// Two tab looks share one primitive: the default "segmented" control (a pill
// that slides inside a muted track) for sub-filters, and "underline" for a
// page's main section tabs, where a boxed control competes with the content
// below it. TabsList publishes its variant through context so every
// TabsTrigger styles itself to match without the caller repeating a prop.
type TabsVariant = "segmented" | "underline";
const TabsVariantContext = React.createContext<TabsVariant>("segmented");

const TabsList = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.List>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.List> & {
    variant?: TabsVariant;
  }
>(({ className, variant = "segmented", ...props }, ref) => (
  <TabsVariantContext.Provider value={variant}>
    <TabsPrimitive.List
      ref={ref}
      className={cn(
        "max-w-full overflow-x-auto text-muted-foreground",
        variant === "underline"
          ? "inline-flex h-auto items-center justify-start gap-1 border-b"
          : "inline-flex h-9 items-center justify-center rounded-lg bg-muted p-1",
        className
      )}
      {...props}
    />
  </TabsVariantContext.Provider>
));
TabsList.displayName = TabsPrimitive.List.displayName;

const TabsTrigger = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>
>(({ className, ...props }, ref) => {
  const variant = React.useContext(TabsVariantContext);
  return (
    <TabsPrimitive.Trigger
      ref={ref}
      className={cn(
        "inline-flex items-center justify-center gap-1.5 whitespace-nowrap text-sm font-medium ring-offset-background transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50",
        variant === "underline"
          ? "-mb-px border-b-2 border-transparent px-3 pb-2.5 pt-1 hover:text-foreground data-[state=active]:border-primary data-[state=active]:text-foreground"
          : "rounded-md px-3 py-1 data-[state=active]:bg-primary/15 data-[state=active]:text-foreground data-[state=active]:shadow-sm data-[state=active]:ring-1 data-[state=active]:ring-primary/30",
        className
      )}
      {...props}
    />
  );
});
TabsTrigger.displayName = TabsPrimitive.Trigger.displayName;

const TabsContent = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Content
    ref={ref}
    className={cn(
      "mt-2 ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
      className
    )}
    {...props}
  />
));
TabsContent.displayName = TabsPrimitive.Content.displayName;

export { Tabs, TabsList, TabsTrigger, TabsContent };
