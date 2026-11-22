import { useEffect } from "react";
import { useLocation } from "react-router";

// Must match the `scroll-mt-20` (5rem = 80px) offset the anchored sections use,
// so the section header clears the top of the viewport instead of butting
// against it.
const SCROLL_OFFSET = 80;

/**
 * Scrolls the element whose `id` matches the current URL hash into view.
 *
 * Settings pages render many sections on one route (e.g. Settings > Services),
 * so a link like `/settings/services#relays` should land the user on that
 * section rather than the top of the page. React Router does not scroll to hash
 * fragments on its own.
 *
 * Two things make a one-shot scroll unreliable here: the target section often
 * mounts a frame or two late (its data loads async), and sibling sections above
 * it keep growing as THEIR data arrives (relay dropdowns, the LSP catalogue),
 * which shifts the target down after we've already scrolled. So we re-align the
 * target to the top offset every frame for a short window, bailing out the
 * moment the user scrolls themselves.
 *
 * Mount once in a layout that wraps the hashed content (the section ids live in
 * the routed children). It re-runs whenever the hash changes, so navigating
 * between sections on the same page works too.
 */
export function useScrollToHash() {
  const { hash } = useLocation();

  useEffect(() => {
    if (!hash) {
      return;
    }
    // decodeURIComponent so ids with non-ascii/escaped chars still match.
    const id = decodeURIComponent(hash.slice(1));
    if (!id) {
      return;
    }

    let frame = 0;
    let cancelled = false;
    // Keep correcting for a window long enough to cover async section loads,
    // then stop so we never fight the user.
    const deadline = performance.now() + 1500;
    // The scrollY we last set ourselves; if the live scrollY diverges from it,
    // the user has taken over and we stand down.
    let lastSet = -1;

    const align = () => {
      if (cancelled) {
        return;
      }
      const el = document.getElementById(id);
      if (el) {
        if (lastSet >= 0 && Math.abs(window.scrollY - lastSet) > 2) {
          return; // user scrolled — leave them alone
        }
        const top = el.getBoundingClientRect().top;
        if (Math.abs(top - SCROLL_OFFSET) > 2) {
          const y = Math.max(0, window.scrollY + top - SCROLL_OFFSET);
          // Instant (not smooth) so scrollY settles synchronously — that keeps
          // the user-takeover check above reliable, and matches how native
          // `#anchor` jumps behave.
          window.scrollTo(0, y);
        }
        lastSet = Math.round(window.scrollY);
      }
      if (performance.now() < deadline) {
        frame = requestAnimationFrame(align);
      }
    };

    frame = requestAnimationFrame(align);
    return () => {
      cancelled = true;
      cancelAnimationFrame(frame);
    };
  }, [hash]);
}
