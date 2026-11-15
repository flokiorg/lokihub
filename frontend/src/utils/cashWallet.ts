import i18n from "src/i18n";

// LOKICASH_GIFT_SEPARATOR joins a lokicash1... token to its
// cash_secret into one copy/QR-able string (NIP-CASH §Cash-Mode Slices →
// Presenting a Cash-Mode Slice as One String). "#" never appears in a
// bech32-encoded token (NIP-CASH §Wire Format's charset excludes it), so the
// join is always unambiguous to split back apart. This is a display-layer
// convenience only — the token's own wire format and the cash_secret
// request parameter are both unchanged; nothing ever decodes this combined
// string as a single value.
export const LOKICASH_GIFT_SEPARATOR = "#";

// buildLokicashCashGift packages a freshly-minted cash-mode slice's token and
// secret into one string a recipient can redeem from without needing a
// second, separately-conveyed value — matching how a physical cash bill
// works: hand over the one thing, it's spendable. Only ever call this with a
// cash_secret that just came back from mint_cash's response (it cannot be
// retrieved again afterward, so there's nothing to rebuild this from later).
export function buildLokicashCashGift(
  token: string,
  cashSecret: string
): string {
  return `${token}${LOKICASH_GIFT_SEPARATOR}${cashSecret}`;
}

// Countdown-style claim deadline: minutes/hours when close (where a bare
// calendar date would hide how urgent the window actually is), a calendar
// date once it's more than a day out. `title` always carries the exact
// timestamp as alt text regardless of which form the label takes.
export function formatClaimDeadline(expiresAtSecs: number): {
  label: string;
  title: string;
} {
  const t = i18n.getFixedT(null, "circles");
  const date = new Date(expiresAtSecs * 1000);
  const title = date.toLocaleString();
  const diffMs = date.getTime() - Date.now();
  if (diffMs <= 0) {
    return { label: t("claimDeadline.expired"), title };
  }
  const diffMins = Math.round(diffMs / 60_000);
  if (diffMins < 60) {
    return {
      label: t("claimDeadline.withinMinutes", { count: diffMins }),
      title,
    };
  }
  const diffHours = Math.round(diffMs / 3_600_000);
  if (diffHours < 24) {
    return {
      label: t("claimDeadline.withinHours", { count: diffHours }),
      title,
    };
  }
  return {
    label: t("claimDeadline.byDate", { date: date.toLocaleDateString() }),
    title,
  };
}
