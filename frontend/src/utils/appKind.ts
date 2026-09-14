import i18n from "src/i18n";

// appKindLabel names the user-facing noun for app.kind — internally every
// Cash Hub, Circle Hub, Cash Wallet, and Circle Wallet is stored as an App
// row (same DB table, same REST resource), but that's an implementation
// detail: shown to a user, "app" specifically means a third-party client
// connected via NWC (Zapf, a Discord bot, ...), and calling their own hub
// or wallet "an app" reads as if lokihub lost track of what it's showing
// them. Falls back to "App" for every kind that genuinely is one
// (undefined, "isolated", or a real connected app).
export function appKindLabel(kind: string | undefined): string {
  const t = i18n.getFixedT(null, "apps");
  switch (kind) {
    case "cash_hub":
      return t("kindLabel.cashHub", "Cash Hub");
    case "circle_hub":
      return t("kindLabel.circleHub", "Circle Hub");
    case "cash_wallet":
      return t("kindLabel.cashWallet", "Cash Wallet");
    case "circle_wallet":
      return t("kindLabel.circleWallet", "Circle Wallet");
    default:
      return t("kindLabel.app", "App");
  }
}
