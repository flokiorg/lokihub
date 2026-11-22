import { cn } from "src/lib/utils";

// A numbered group heading for the redesigned multi-step create forms (New
// Circle, New Cash Hub): a small circular step badge + an uppercase, letter-
// spaced group label, optionally with a one-line description underneath.
//
// Matches the prototype's "1 THE ESSENTIALS / 2 MEMBERSHIP & IDENTITY /
// 3 ADVANCED" grouping, which turns one long stack into a few labelled,
// scannable sections. Colours come from theme tokens only (`--primary` for the
// badge, `--muted-foreground` for the label) so the Settings accent/theme
// picker keeps control.
export function NumberedGroupHeading({
  step,
  title,
  description,
  className,
}: {
  step: number;
  title: string;
  description?: string;
  className?: string;
}) {
  return (
    <div className={cn("grid gap-1", className)}>
      <div className="flex items-center gap-2.5">
        <span
          className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary"
          aria-hidden
        >
          {step}
        </span>
        <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </span>
      </div>
      {description && (
        <p className="text-sm text-muted-foreground">{description}</p>
      )}
    </div>
  );
}
