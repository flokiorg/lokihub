import i18n from "src/i18n";

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
