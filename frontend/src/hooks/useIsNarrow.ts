import * as React from "react";

// Tailwind's `md` breakpoint — the line below which the hub detail screens
// switch to their phone layout.
const NARROW_BREAKPOINT = 768;

// Like the shadcn `useIsMobile`, but initialised from the real viewport width
// so the very first render already knows whether this is a phone. That matters
// when the value drives layout structure (which tabs exist, where the stats
// live) rather than a shadcn component's internal behaviour: the deprecated
// hook starts `undefined`/false and corrects a tick later, which would flash
// the desktop layout and race any effect that reads it on mount. SSR-safe:
// returns false when there is no window.
export function useIsNarrow() {
  const [isNarrow, setIsNarrow] = React.useState(() =>
    typeof window !== "undefined"
      ? window.innerWidth < NARROW_BREAKPOINT
      : false
  );

  React.useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${NARROW_BREAKPOINT - 1}px)`);
    const onChange = () => setIsNarrow(window.innerWidth < NARROW_BREAKPOINT);
    mql.addEventListener("change", onChange);
    onChange();
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return isNarrow;
}
