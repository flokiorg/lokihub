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

// appKindSiblingsLabel names the chip in an app's title that counts the OTHER
// things of its sort — the other Cash Hubs, the other Circles, the other
// bills issued by one hub. "{{count}} Connections" is only right for a real
// third-party connection: a Cash Hub is not something you connected to, it is
// something you own, so counting it as a connection both misnames it and says
// nothing about what clicking the chip would switch between.
//
// No plural forms needed: the chip only renders when count > 1 (a lone hub
// shows the "Connected" badge instead), so each string here is already the
// plural one.
export function appKindSiblingsLabel(
  kind: string | undefined,
  count: number
): string {
  const t = i18n.getFixedT(null, "apps");
  switch (kind) {
    case "cash_hub":
      return t("connections.siblingCashHubs", { count });
    case "circle_hub":
      return t("connections.siblingCircles", { count });
    case "cash_wallet":
      return t("connections.siblingCashBills", { count });
    case "circle_wallet":
      return t("connections.siblingCircleWallets", { count });
    case "isolated":
      return t("connections.siblingSubwallets", { count });
    default:
      return t("connections.connections_count", { count });
  }
}
