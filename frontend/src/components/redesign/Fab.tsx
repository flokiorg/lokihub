import { LucideIcon } from "lucide-react";
import { Link } from "react-router";
import { cn } from "src/lib/utils";

// Fab — a thumb-first floating action button, fixed bottom-right, shown ONLY on
// narrow viewports (the redesign's mobile pattern: "the primary action on
// list/detail screens becomes a floating button in the bottom-right, reachable
// one-handed"). Screens keep their normal header action for desktop and add
// this for mobile; `md:hidden` means the two never show at once.
//
// Colour comes from the theme's primary tokens, so the Settings accent picker
// re-tints it like every other primary surface.
export function Fab({
  to,
  onClick,
  icon: Icon,
  label,
  className,
}: {
  to?: string;
  onClick?: () => void;
  icon: LucideIcon;
  // Accessible name — the button is icon-only, so this is required for a11y.
  label: string;
  className?: string;
}) {
  const classes = cn(
    "fixed bottom-6 end-6 z-30 flex size-14 items-center justify-center rounded-full",
    "bg-primary text-primary-foreground shadow-lg transition-transform",
    "hover:scale-105 active:scale-95 md:hidden",
    className
  );

  if (to) {
    return (
      <Link to={to} aria-label={label} className={classes}>
        <Icon className="size-6" />
      </Link>
    );
  }
  return (
    <button type="button" onClick={onClick} aria-label={label} className={classes}>
      <Icon className="size-6" />
    </button>
  );
}
